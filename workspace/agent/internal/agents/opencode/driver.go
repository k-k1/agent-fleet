package opencode

// The managed OpenCode driver (docs/log/27 P2) — the first full implementation of the
// Driver type (agents.Driver/ThreadHandle). It maps the turn state machine (§4),
// Interaction (§5) and reconciliation (§6) onto the shared `opencode serve` HTTP + SSE
// surface.
//
// API choices, from measurement (1.17.18, docs/log/27 §12.2):
//   - Turns are driven by the v1 blocking POST /session/{id}/message, the only entry
//     point that writes the message/part records the read layer treats as canonical; a
//     goroutine wraps it to make it asynchronous. prompt_async only writes the user
//     message and never starts a turn (measured). On v2 the turn is /api/session/{id}/prompt
//     and the read layer goes through the API too (v2state.go), because v2 never writes the
//     v1 tables.
//   - v1 has no mid-turn steer entry point (v2's delivery=steer is not injected into the
//     running v1 turn, it starts its own — measured). Steer is held in a driver-side
//     queue and submitted as the next turn once the running one finishes, which is
//     exactly §4's queued state.
//   - interrupt = POST /session/{id}/abort (the blocking call returns 200 with partial
//     results and the assistant message is marked completed, so resume is safe).
//   - question = the question.asked/replied/rejected events plus GET /question and
//     POST /question/{id}/reply {answers: [[label,…]]} (answers are label strings).
//   - attachments = the v1 file part {type:"file", mime, url:"file://…"} (measured).
//
// ClientMessageID (§4) idempotency happens only in the driver's ledger (accept()); the
// messageID is never handed to serve. Measured (1.17.18): the turn loop depends on the
// lexical order of message ids, so a turn whose client-assigned id sorts below an
// existing one is never picked up and /message never returns (the same root cause behind
// prompt_async looking inert).
//
// serve's session/question/status/event surfaces are scoped to a project (directory)
// (measured), so the session's dir always travels along as a directory query, and SSE
// subscribes to the cross-project /global/event.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// turnClient runs the blocking /message calls: no timeout — a turn legitimately
// runs for minutes/hours. Interrupt (abort) or daemon death unblocks it.
var turnClient = &http.Client{Transport: authTransport{}}

// ledger is the persistent ClientMessageID ledger (§9.5, persisted across processes): an
// in-memory ledger does not deduplicate a re-send that spans an Agent restart.
var ledger = agents.NewMsgLedger("opencode-msgledger")

// NewDriver returns the managed opencode Driver (driverOf looks it up from /turn and
// /respond). The read layer is preserved by embedding agentImpl as is.
func NewDriver() agents.Driver { return managedDriver{} }

type managedDriver struct{ agentImpl }

// Capabilities (§3.1). Steer here means "accepts a follow-up aimed at the running turn",
// implemented as a driver-side queue (see the file comment). TUIAttach is an opencode
// strength: `opencode attach` joins a running serve without stopping it (measured), given the
// serve password (Basic auth, user `opencode`); nothing in this package attaches today.
func (managedDriver) Capabilities() agents.Capabilities {
	return agents.Capabilities{
		ProcessModel:  "shared-daemon",
		Steer:         true,
		Fork:          true,
		DynamicModel:  true,
		DynamicEffort: true,
		DynamicMode:   true,
		Questions:     true,
		TUIAttach:     true,
	}
}

// Resume returns the session's ThreadHandle, creating the runtime session when
// none exists yet (the Driver interface: start a new one if absent). It doubles as §6's
// shared reconciliation procedure: ensure the runtime, resolve the session, check the
// snapshot; the live subscription is held permanently by the supervisor, per generation.
func (managedDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	h, err := managedDriver{}.resume(m)
	if err != nil {
		return nil, err
	}
	// Peer messages a halt, a shutdown or a crash left held become the first turns (#1255).
	agents.DeliverHeld(m.Name, h)
	return h, nil
}

// resume is Resume without the held peer messages.
func (managedDriver) resume(m session.Meta) (agents.ThreadHandle, error) {
	if m.Kind != session.KindOpencode {
		return nil, errors.New("opencode driver は opencode セッション専用です")
	}
	if !session.DirExists(m.Dir) {
		return nil, agents.DirGoneErr(m.Dir)
	}
	addr, gen, err := Serve().Ensure()
	if err != nil {
		return nil, err
	}
	ocSid := session.UUID(m.Dir, m.Name) // identity: the working copy, never the subdir
	cwd := m.CWD()                       // where the session runs (Dir, or a chosen subdir)
	handlesMu.Lock()
	h := handles[m.Name]
	if h == nil {
		h = &threadHandle{
			name:   m.Name,
			dir:    cwd,
			ocSid:  ocSid,
			events: make(chan agents.Event, 64),
		}
		handles[m.Name] = h
	}
	handlesMu.Unlock()

	h.resumeMu.Lock()
	defer h.resumeMu.Unlock()

	h.mu.Lock()
	if h.alive && h.gen == gen && h.ses != "" {
		h.mu.Unlock()
		return h, nil
	}
	// The model defaults come from the meta at startup; a dynamic change overwrites them
	// through UpdateSettings (§9.4-3).
	if h.settings.Model == "" {
		h.settings.Model = m.Model
	}
	if h.settings.Effort == "" {
		h.settings.Effort = m.Effort
	}
	if h.settings.Mode == "" {
		h.settings.Mode = m.Mode
	}
	ses := h.ses
	h.mu.Unlock()

	if ses == "" {
		ses = sids.Read(ocSid)
	}
	ses, err = managedSession(addr, m, cwd, ocSid, ses)
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	h.addr, h.gen, h.ses, h.alive = addr, gen, ses, true
	// Revive a queue left behind when a daemon death ended the pump (§31): without this
	// the entries stall forever.
	if h.tq().Len() > 0 && !h.pumping {
		h.pumping = true
		go h.pump()
	}
	h.mu.Unlock()

	h.reconcile()
	// Baseline for exit recording (the same role startSessionTmux plays for tui): clear
	// the previous death record so later OOM attribution is bounded to this session.
	base, _ := status.OOMKillCount()
	status.PersistExit(m.Name, status.ExitInfo{OOMBase: base})
	return h, nil
}

// managedSession returns the opencode session m runs in: ses when the daemon still has it, else a
// fork of m.ForkFrom or a new one, recorded under ocSid. Either way it is stamped with m's name
// for the shell's AF_WORK_DIR / AF_SESSION_NAME (agent-fleet-shared.js), and it fails when it
// cannot: a fork carries its source's name, so a session left unstamped would run its shell as
// that other session. The next Resume finds the recorded session and stamps it again.
func managedSession(addr string, m session.Meta, cwd, ocSid, ses string) (string, error) {
	var err error
	if ses != "" && !serveSessionExists(addr, ses, cwd) {
		ses = "" // pruned/imported-away conversation — start fresh
	}
	if ses == "" {
		if m.ForkFrom != "" {
			ses, err = serveForkSession(addr, m.ForkFrom, cwd, m.ForkAt)
		} else {
			ses, err = serveCreateSession(addr, cwd, session.Display(m))
		}
		if err != nil {
			return "", err
		}
		// The same per-slot mapping claude/codex use: the read layer's activeSession
		// consults it first, and with no plugin in managed mode the driver is the writer.
		sids.Write(ocSid, ses)
	}
	if isV2() {
		if err := tagManagedSession(addr, ses, m.Name); err != nil {
			return "", fmt.Errorf("opencode session %s could not be named for its shell environment: %w", ses, err)
		}
	}
	return ses, nil
}

// LiveHandle returns the session's handle without starting anything (agents.LiveHandles): the
// /turn queue edits must not bring a runtime up only to find nothing queued. The handle stays
// registered from Resume to DropHandle, a runtime death in between included, and the queue and
// the kept discards live on it for that whole time, so an entry the messages payload still
// shows can be removed without a respawn.
func (managedDriver) LiveHandle(m session.Meta) (agents.ThreadHandle, bool) {
	h := handleFor(m.Name)
	if h == nil {
		return nil, false
	}
	return h, true
}

var _ agents.LiveHandles = managedDriver{}

var _ agents.TerminalResponder = managedDriver{}

// RespondTerminal answers a form of a Terminal slot attached to af's serve through the form API;
// the TUI follows (measured, docs/log/27). Terminal has no handle, so the daemon's own list is the
// source: the form must be one it still holds for the running session, and respondForm reads it
// again before answering. A settled, forgotten or unknown form is errFormSettled, so an answer made
// in the TUI at the same moment is never doubled. Anything not on the serve keeps keys.
func (managedDriver) RespondTerminal(m session.Meta, reply agents.InteractionReply) error {
	t, ok := v2TargetFor(m)
	if !ok || m.DriverKind() != session.DriverTUI {
		return agents.ErrTerminalRespond
	}
	// A write is aimed at the conversation af recorded for this slot, never at the directory guess
	// the readers fall back to (another slot in the same directory could be that conversation), and
	// the conversation must still carry this slot's tags (the invariant putEnvironmentFor holds).
	ses := sids.Read(session.UUID(m.Dir, m.Name))
	if ses == "" {
		return errFormSettled
	}
	info, err := getSharedSession(t.Addr, ses)
	if err != nil {
		if errors.Is(err, statusError(http.StatusNotFound)) {
			return errFormSettled
		}
		return err
	}
	if info.Metadata[metaName] != m.Name || info.Metadata[metaSID] != session.UUID(m.Dir, m.Name) {
		return fmt.Errorf("session %s does not belong to %s", ses, m.Name)
	}
	var waiting bool
	for _, f := range pendingFormsV2(t, m, info.ID) {
		waiting = waiting || f.ID == reply.ID
	}
	if !waiting {
		return fmt.Errorf("interaction %s is not pending: %w", reply.ID, errFormSettled)
	}
	return respondForm(t.Addr, ses, reply)
}

// --- handle registry ---------------------------------------------------------

var handlesMu sync.Mutex
var handles = map[string]*threadHandle{}

// handleFor returns the live handle for a session name, or nil.
func handleFor(name string) *threadHandle {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	return handles[name]
}

// liveHandles snapshots the currently-alive handles.
func liveHandles() []*threadHandle {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	var out []*threadHandle
	for _, h := range handles {
		h.mu.Lock()
		alive := h.alive
		h.mu.Unlock()
		if alive {
			out = append(out, h)
		}
	}
	return out
}

// DropHandle detaches a managed session from its runtime handle (stop/halt/
// archive): abort any running turn, clear the queue, forget the handle. The
// conversation stays in the SQLite store — a later Resume reattaches.
func DropHandle(name string) {
	handlesMu.Lock()
	h := handles[name]
	delete(handles, name)
	handlesMu.Unlock()
	if h == nil {
		// A Terminal slot has no handle; its turn runs on the shared serve all the same, and
		// closing the pane does not stop it.
		if m, ok := session.ReadMeta(name); ok {
			interruptTerminal(m)
		}
		return
	}
	h.mu.Lock()
	addr, ses, dir, running := h.addr, h.ses, h.dir, h.running
	h.alive = false
	h.compacting = false
	// Teardown discards the queue (ADR 0105 decision 8), the input the pump holds included. An
	// input already committed is left a pending stop, so the pump does not send it.
	h.tq().DropAll()
	h.tq().Interrupt(agents.InterruptOpts{DiscardQueue: true}, running)
	if running {
		h.abortAsked = true // stop / halt / archive: the abort below is ours
	}
	h.mu.Unlock()
	if running && ses != "" {
		abortSession(addr, ses, dir)
	}
}

// RemoveLedger drops a session's ClientMessageID ledger. Only for /stop, which discards
// the slot's identity as well; halt/archive keep it because the session can resume.
func RemoveLedger(name string) { ledger.Remove(name) }

// ManagedAlive reports whether the session has a live runtime handle — the
// managed counterpart of tmuxx.HasSession for the sessions list.
func ManagedAlive(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.alive
}

// ManagedBusy reports a turn is running or queued (graceful shutdown waits on this).
func ManagedBusy(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running || h.tq().Len() > 0
}

// ManagedInFlight reports that the handle holds an entry it took from the queue and has not
// settled. The pump gives its move-gate lease back once the runtime has the input, which can be
// before the runtime's own record (and so DriveState) shows the turn; a move asks this, under the
// exclusive gate, so it never starts in that window (ADR 0115 decision 11.2). Entries still
// queued do not count: the move carries them.
func ManagedInFlight(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running || h.tq().Head() != nil
}

// AbortManaged interrupts every running managed turn — the managed counterpart of
// graceful shutdown's per-pane Ctrl-C (docs/log/27 §10.2-8). It takes the same path as
// ThreadHandle.Interrupt, so once the abort lands the turn goroutine records cancelled
// and the status store returns to idle, releasing anySessionWorking's wait.
func AbortManaged() {
	for _, h := range liveHandles() {
		h.mu.Lock()
		running := h.running
		h.mu.Unlock()
		if running {
			_ = h.interruptAll()
		}
	}
}

// ReconcileManaged re-attaches managed opencode sessions after an Agent boot,
// daemon restart or daemon death (§6). It covers every managed meta not already treated
// as stopped, so the experience matches tui's tmux surviving an Agent restart. On failure
// the session stays stopped and the user's resume click (/start) retries it.
func ReconcileManaged(reason string) {
	d := managedDriver{}
	for _, m := range session.ListMetas() {
		if m.Kind != session.KindOpencode || m.DriverKind() != session.DriverManaged || m.Archived {
			continue
		}
		if m.StoppedAt != "" && handleFor(m.Name) == nil {
			continue // deliberately stopped — resume only on user action
		}
		if _, err := d.Resume(m); err != nil {
			log.Printf("opencode managed: reconcile %s (%s): %v", m.Name, reason, err)
		}
	}
	// Terminal panes that survived an Agent restart are attached to the daemon too: adopt it (or
	// start a replacement and put their environments back) rather than wait for a Console poll,
	// which never comes for a session that has no managed handle.
	if sharedTerminals() > 0 {
		ensureForTerminals(reason)
	}
}

// terminalEnsureBackoff is the wait before each further attempt ensureForTerminals makes, and
// ensureTerminalServe, when set, replaces the attempt; vars so tests need no daemon. (A function
// default for the latter would be an initialization cycle: Ensure can reach this reconcile.)
var (
	terminalEnsureBackoff = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}
	ensureTerminalServe   func() error
)

// ensureForTerminals brings the shared serve up for the Terminal panes attached to it, retrying a
// failed start. Nothing else retries for them: they have no managed handle, so no Console poll
// calls Resume, and a single failed start would strand every pane until another one is launched
// or the Agent restarts.
// Seen on CI: a replacement serve stayed silent past serveStartTimeout after a restart, and the
// same job passed when rerun.
func ensureForTerminals(reason string) {
	for i := 0; ; i++ {
		var err error
		if ensureTerminalServe != nil {
			err = ensureTerminalServe()
		} else {
			_, _, err = Serve().Ensure()
		}
		if err == nil {
			return
		}
		// Not connected or disabled is a decision, not a hiccup: another attempt cannot change it.
		if i == len(terminalEnsureBackoff) || errors.Is(err, ErrNotConnected) || Serve().Disabled() {
			log.Printf("opencode: terminal sessions need the shared serve (%s): %v", reason, err)
			return
		}
		log.Printf("opencode: terminal sessions need the shared serve (%s), retrying in %v: %v", reason, terminalEnsureBackoff[i], err)
		time.Sleep(terminalEnsureBackoff[i])
		if sharedTerminals() == 0 {
			return
		}
	}
}

// reconcileAll is the supervisor-facing wrapper (serve.go, after a daemon death or restart).
func reconcileAll(reason string) { ReconcileManaged(reason) }

// reconcileInflight counts the reconciles started by goReconcile. Tests wait on it so a
// reconcile that outlives the code that triggered it cannot read a package var after the test
// has restored it; production never waits.
var reconcileInflight sync.WaitGroup

// goReconcile runs reconcileAll in the background, tracked by reconcileInflight.
func goReconcile(reason string) {
	reconcileInflight.Add(1)
	go func() {
		defer reconcileInflight.Done()
		reconcileAll(reason)
	}()
}

// --- thread handle -----------------------------------------------------------

type threadHandle struct {
	name  string
	dir   string
	ocSid string

	mu sync.Mutex
	// resumeMu serializes Resume end-to-end (same §32 competition as codex: two
	// concurrent Resumes would create two native sessions and orphan one).
	resumeMu sync.Mutex

	addr  string
	gen   int
	ses   string
	alive bool
	state agents.TurnState
	// abortAsked: this side asked the runtime to cut the running turn short (Interrupt,
	// DropHandle, or the drain before a daemon swap). opencode records our abort and an
	// abort that merely happened to us as the same MessageAbortedError, so without this
	// flag runTurn cannot tell "the user pressed stop" from "the daemon went away under a
	// running turn". Cleared when an input is committed: an abort asked before that point was
	// aimed at another client's turn, not at this input.
	abortAsked bool
	running    bool // a turn goroutine is in flight (pump busy)
	pumping    bool
	// q is the input queue and the whole of ADR 0105's stop rules (agents.TurnQueue); every
	// call holds mu. Read it through tq.
	q        *agents.TurnQueue
	settings agents.ThreadSettings
	inter    *agents.Interaction // pending question (the payload of waiting_interaction)
	// forms: every form a v2 session waits on, oldest first; inter is forms[0] (v2form.go). v1 asks
	// one question at a time and leaves it nil.
	forms []*agents.Interaction
	// compacting: serve said a compaction summary is being written for this session (an
	// in-flight compaction message.updated). Only half of the "compacting" state — the store
	// has to agree (sessionCompacting) — and cleared by every terminal turn state, a stop, the
	// daemon going away and teardown, so a missed end event cannot keep it on.
	compacting bool
	// retry: the runtime is retrying a failing provider call (retry.go). Cleared by output, by
	// every terminal turn state and by teardown. retryFailed is the retry that ended the last
	// turn, kept for the mirror until the next input is committed; retryStop is its failure
	// text, which runTurn consumes.
	retry        *agents.RetryInfo
	retryFailed  *agents.RetryInfo
	retryStop    string
	retryAbort   chan struct{} // closed when the abort noteRetry sent has been answered or given up on
	retryAbortOK bool          // whether the daemon acknowledged it
	// turnSent: this handle's own /message is out (set by runTurn, cleared when the pump
	// finishes the input). running alone is also true while the pump waits behind another
	// client's turn, which a retry status must never be attributed to.
	turnSent bool
	events   chan agents.Event

	// act stamps every message the runtime sends for this handle, for the busy-row progress bound
	// (ManagedProgress).
	act agents.Activity
	// tools holds the tool calls the runtime reported started and not finished: a silent tool
	// sends nothing while it runs, so this is the only trace of it (progress_managed.go).
	tools agents.InFlight
}

// tq returns the handle's queue, creating it on first use. Caller holds h.mu.
func (h *threadHandle) tq() *agents.TurnQueue {
	if h.q == nil {
		h.q = agents.NewTurnQueue(h.name, ledger, agents.LedgerAtAccept)
	}
	return h.q
}

func (h *threadHandle) isCompacting() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.compacting
}

func (h *threadHandle) setCompacting(on bool) {
	h.mu.Lock()
	h.compacting = on
	h.mu.Unlock()
}

func (h *threadHandle) sessionID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ses
}

// emit pushes an event without ever blocking a state transition (drop on overflow —
// events are advisory; the source of truth is Snapshot + the native store, §6).
func (h *threadHandle) emit(e agents.Event) {
	select {
	case h.events <- e:
	default:
	}
}

func (h *threadHandle) setState(st agents.TurnState) {
	h.mu.Lock()
	h.state = st
	if st != agents.TurnRunning && st != agents.TurnWaitingInteraction {
		h.compacting = false // the turn ended, or is being stopped
		h.retry = nil
	}
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: st})
}

func (h *threadHandle) currentState() agents.TurnState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// runtimeLost drops the handle to unknown (§6-1: the honest state on a disconnect). The
// blocked turn call (if any) unblocks with a transport error and keeps unknown until
// reconcile resolves it.
func (h *threadHandle) runtimeLost() {
	h.tools.Unknown()
	h.mu.Lock()
	h.alive = false
	h.compacting = false
	h.retry = nil
	h.state = agents.TurnUnknown
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnUnknown})
}

// reconcile is steps 3-4 of §6: settle the turn state from the runtime's session state
// plus any pending question. busy=running / question=waiting_interaction / else completed.
func (h *threadHandle) reconcile() {
	h.mu.Lock()
	addr, ses, dir := h.addr, h.ses, h.dir
	h.mu.Unlock()
	st := agents.TurnCompleted
	if busy := serveSessionBusy(addr, ses, dir); busy {
		st = agents.TurnRunning
	}
	var inter *agents.Interaction
	if isV2() {
		if forms, err := (apiTarget{Addr: addr, Client: serveClient}).pendingForms(ses); err == nil && len(forms) > 0 {
			inter = forms[0].interaction()
			st = agents.TurnWaitingInteraction
			h.mu.Lock()
			h.setForms(forms)
			h.mu.Unlock()
		}
	}
	for _, q := range serveListQuestions(addr, dir) {
		if q.SessionID == ses {
			inter = q.toInteraction()
			st = agents.TurnWaitingInteraction
			break
		}
	}
	h.mu.Lock()
	h.state = st
	h.inter = inter
	h.mu.Unlock()
}

// --- ThreadHandle interface ---------------------------------------------------

// Send starts a turn (the turn/start equivalent), queueing behind a running one.
func (h *threadHandle) Send(in agents.TurnInput) error {
	_, err := h.accept(in)
	return err
}

// SendQueued is Send reporting whether the input was held behind a running turn. That includes a
// turn this handle did not start — an attached TUI's, or one a previous Agent process left
// running: the pump waits for it in waitIdle before this input goes out.
func (h *threadHandle) SendQueued(in agents.TurnInput) (bool, error) {
	h.mu.Lock()
	addr, ses, dir, ours := h.addr, h.ses, h.dir, h.pumping || h.running
	h.mu.Unlock()
	// Asked before accept starts the pump: asked after, the session is busy with this very input.
	foreign := !ours && ses != "" && serveSessionBusy(addr, ses, dir)
	queued, err := h.accept(in)
	return queued || (err == nil && foreign), err
}

// Steer is a follow-up input to the running turn (§4 queued). opencode v1 has no entry
// point for mid-turn injection (see the file comment), so the semantics are "submit as
// the next turn once the running one finishes".
func (h *threadHandle) Steer(in agents.TurnInput) error {
	_, err := h.accept(in)
	return err
}

// accept queues the input and starts the pump when none runs. queued reports that the input
// waits behind a running turn (or behind earlier queued input) rather than starting now.
func (h *threadHandle) accept(in agents.TurnInput) (queued bool, err error) {
	if strings.TrimSpace(in.Prompt) == "" && len(in.Attachments) == 0 {
		return false, errors.New("empty prompt")
	}
	in.ClientMessageID = normalizeMsgID(in.ClientMessageID)
	h.mu.Lock()
	if !h.alive {
		h.mu.Unlock()
		return false, errors.New("runtime が停止しています（再開してください）")
	}
	if h.inter != nil {
		// Free text sent while a question is pending invites a mis-answer (the same
		// call /input's question_pending guard makes): steer the caller to the
		// structured reply (Respond).
		h.mu.Unlock()
		return false, errQuestionPending
	}
	if _, dup := h.tq().Accept(in); dup {
		h.mu.Unlock()
		return false, nil // re-send; the persistent cross-process ledger makes it idempotent (§4)
	}
	start := !h.pumping
	if start {
		h.pumping = true
	}
	queued = h.running || h.tq().Len() > 1
	if queued {
		h.state = agents.TurnQueued
	}
	h.mu.Unlock()
	if start {
		go h.pump()
	}
	return queued, nil
}

// errQuestionPending is matched by the /turn handler to return the same
// question_pending wire error the tui route uses. The sentinel is kind-independent, so
// the codex driver returns the same value.
var errQuestionPending error = agents.ErrQuestionPending

// ErrQuestionPending reports whether err is the "answer the question first" guard.
func ErrQuestionPending(err error) bool { return errors.Is(err, errQuestionPending) }

// pump processes the queue serially: wait for the runtime to be idle (a TUI-attached
// user may be running their own turn, so wait here rather than betting on serve to
// serialize), run the blocking turn, repeat.
func (h *threadHandle) pump() {
	for {
		// Each entry gets a lease of its own on the session's lifecycle gate (ADR 0115 decision
		// 11.1), taken before it leaves the queue and held until /message is written to serve.
		// The input path that queued it released its lease at the accept, so a move finds every
		// accepted entry either with serve or still queued, where its snapshot reads it. The hold
		// spans waitIdle: a move waits out another client's turn (at most its 60s) rather than
		// snapshot an entry the pump is about to send. Taken without h.mu: a move reads the
		// queue under h.mu while it holds the gate.
		lease, err := lifegate.Enter(context.Background(), h.name)
		if err != nil {
			h.parkForMove(err)
			return
		}
		h.mu.Lock()
		var t *agents.Taken
		if h.alive {
			t = h.tq().Take()
		}
		if t == nil {
			// Settle the stop inside the same lock accept() takes. A gap between the
			// empty check and clearing the flag strands input that arrives in it: it
			// sees pumping=true and starts no pump.
			h.pumping = false
			h.mu.Unlock()
			lease.Release()
			return
		}
		addr, ses, dir := h.addr, h.ses, h.dir
		h.running = true
		h.mu.Unlock()

		// Guard for TUI co-use: wait while another client's turn is running (the same
		// 60s as the maximum drain), then send anyway — serve handles /message serially
		// even when busy. Waiting there, the input is queued behind that turn (ADR 0105
		// decision 1): a first stop lets it continue, a second one discards it. It counts as
		// held from before the status call, so a first stop that lands during that HTTP round
		// trip cannot take it for the turn being started while serve is still running
		// another client's.
		h.mu.Lock()
		h.tq().Hold(t, true)
		h.mu.Unlock()
		if serveSessionBusy(addr, ses, dir) {
			waitIdle(addr, ses, dir, 60*time.Second)
		}
		if h.commit(t) {
			h.runTurn(t, lease)
		} else {
			h.setState(agents.TurnCancelled) // stopped before it was sent: it never becomes a turn
		}
		lease.Release()
		if !h.awaitRetryAbort() {
			// The abort may still be executed later and would cut the next input's turn short:
			// send nothing more until a resume re-reads the session (queued inputs stay queued).
			log.Printf("opencode managed: retry abort not acknowledged name=%s: holding the queue", h.name)
			h.runtimeLost()
		}

		h.mu.Lock()
		h.tq().Settle(t)
		h.running = false
		h.turnSent = false
		h.retry = nil
		h.mu.Unlock()
	}
}

// parkForMove stops a pump that a move refused, leaving its input queued for the move's
// snapshot. If the move rolls back, the gate reopens and the pump starts again. The second look
// covers a reopen that ran before the callback was registered or while pumping was still set,
// when resumePump would have found nothing to do.
//
// A meta the gate could not read is not a move: nothing will reopen it, so the pump looks again
// after a pause instead.
func (h *threadHandle) parkForMove(err error) {
	if errors.Is(err, lifegate.ErrMetaUnreadable) {
		h.mu.Lock()
		h.pumping = false
		h.mu.Unlock()
		time.AfterFunc(time.Second, h.resumePump)
		return
	}
	lifegate.WhenReopened(h.name, h.resumePump)
	h.mu.Lock()
	h.pumping = false
	h.mu.Unlock()
	if !lifegate.Closed(h.name) {
		h.resumePump()
	}
}

// resumePump starts the pump for what is queued, unless one runs.
func (h *threadHandle) resumePump() {
	h.mu.Lock()
	start := !h.pumping && h.alive && h.tq().Len() > 0
	if start {
		h.pumping = true
	}
	h.mu.Unlock()
	if start {
		go h.pump()
	}
}

// commit is the pump's last act under the lock before it builds and sends /message: the line
// a discard or a removal cannot cross (ADR 0105 decision 3). false means a stop or a removal
// took the input back and it must not be sent. It also starts the input's own abort record:
// an abort asked before this point was aimed at the other client's turn, not at this input.
func (h *threadHandle) commit(t *agents.Taken) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tq().Hold(t, false)
	if !h.tq().Commit(t) {
		return false
	}
	h.abortAsked = false
	h.retry, h.retryFailed, h.retryStop = nil, nil, ""
	return true
}

// runTurn executes ONE blocking v1 /message turn and lands the terminal state.
// It also updates the status store, standing in for hooks: WireLive prefers the
// db-derived LiveState, but the fallback and anySessionWorking (graceful shutdown's wait
// condition) read this. Without returning to idle at the end of a turn, the session
// sticks on "in progress".
//
// lease is the entry's hold on the lifecycle gate (pump), released once the request carrying the
// input is written to serve; a turn can run for hours and must not hold a move off.
func (h *threadHandle) runTurn(t *agents.Taken, lease *lifegate.Lease) {
	defer lease.Release()
	in := t.In
	agents.MarkTurnStartRun(h.ocSid, in)
	// Stamp idle with the terminal turn state (and emit the docs/log/30 report on
	// completion). Every return path below has already called setState, so the state at
	// defer time is the turn's terminal one. failure is the reason it failed (errors.go),
	// carried to the end so the operator report and the chat bridge body can say the turn
	// ended in an error.
	failure := ""
	defer func() { agents.MarkTurnEndErr(h.ocSid, h.currentState(), failure) }()
	h.setState(agents.TurnStarting)
	h.mu.Lock()
	addr, ses := h.addr, h.ses
	st := h.settings
	h.mu.Unlock()

	// Let serve assign the messageID (measured 1.17.18: the turn loop depends on the
	// lexical order of message ids, so a turn whose client-assigned id sorts below an
	// existing one is never picked up and /message never returns — the same root cause
	// behind prompt_async looking inert). ClientMessageID idempotency happens only in the
	// driver's ledger (accept()).
	req, err := newTurnRequest(addr, ses, h.dir, in, st)
	if err != nil {
		h.setState(agents.TurnFailed)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	// Issuing /message is the only point opencode offers at which serve holds this input, and
	// it is best effort (ADR 0105 decision 3): serve's status names no input, so a stop from
	// here on aborts whatever runs, and an abort that reaches serve before /message finds
	// nothing. A stop that arrived while the input was committed but not yet out lands here,
	// before anything is sent.
	h.mu.Lock()
	stopped := h.tq().Received(t)
	if !stopped {
		h.state = agents.TurnRunning
		h.turnSent = true
	}
	h.mu.Unlock()
	if stopped {
		h.setState(agents.TurnCancelled)
		return
	}
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
	// /message blocks for the whole turn, so the request's write is the moment serve has the
	// input; a send that fails before it keeps the lease until runTurn returns.
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				lease.Release()
			}
		},
	}))
	res, err := sendTurn(addr, ses, h.dir, req, st)
	if err != nil {
		// A transport break means the daemon is gone or the turn was interrupted: fall
		// to unknown honestly and leave it to §6.
		h.mu.Lock()
		interrupted := h.state == agents.TurnInterrupting
		h.mu.Unlock()
		if interrupted {
			h.setState(agents.TurnCancelled)
		} else {
			h.setState(agents.TurnUnknown)
		}
		return
	}
	defer res.Body.Close()
	// A 200 can still be a failure: opencode reports a provider-side failure in the
	// assistant message's error field, not in the HTTP status (measured, errors.go).
	// Judging by the status alone lets an exhausted balance or an auth error return to
	// idle as a normal completion, leaving nothing in the transcript either.
	turnErr, hasErr := decodeTurnError(res.Body)
	h.mu.Lock()
	interrupted := h.state == agents.TurnInterrupting
	asked := h.abortAsked
	retryStop := h.retryStop
	h.inter = nil // the turn ended, so no question is pending any more
	h.mu.Unlock()
	switch {
	case interrupted:
		h.setState(agents.TurnCancelled)
	case retryStop != "":
		// We cut a retry loop short because its message can never succeed (retry.go); the
		// abort leaves no error on the message, so the reason is carried here.
		failure = retryStop
		log.Printf("opencode managed: turn failed name=%s model=%s %s", h.name, st.Model, retryStop)
		h.setState(agents.TurnFailed)
	case res.StatusCode >= 400:
		failure = fmt.Sprintf("[error] HTTP %d", res.StatusCode)
		log.Printf("opencode managed: turn failed name=%s status=%d", h.name, res.StatusCode)
		h.setState(agents.TurnFailed)
	case hasErr && turnErr.ok():
		failure = turnErr.summary()
		log.Printf("opencode managed: turn failed name=%s model=%s %s", h.name, st.Model, turnErr.summary())
		if turnErr.retryable() {
			h.setState(agents.TurnAborted)
		} else {
			h.setState(agents.TurnFailed)
		}
	case hasErr && turnErr.isAbort() && asked:
		// We asked: a stop / halt / archive, or the drain before the daemon is swapped.
		// The partial answer is in the parts and the user already knows why.
		h.setState(agents.TurnCancelled)
	case hasErr && turnErr.isAbort():
		// An abort nobody on this side asked for: the runtime went away under the turn
		// (measured: the shared serve daemon replaced mid-turn kills it exactly like this).
		// The work is unfinished and resending carries on from where it stopped, which is
		// what TurnAborted means; calling it a completion leaves the session looking like
		// it simply stopped talking.
		failure = abortedWithoutRequest
		log.Printf("opencode managed: turn aborted without a request name=%s model=%s", h.name, st.Model)
		h.setState(agents.TurnAborted)
	default:
		h.setState(agents.TurnCompleted)
	}
}

// abortedWithoutRequest is the operator-facing reason for a turn the runtime cut short on
// its own. Tagged like the other failures so a reader can tell it from the agent's prose.
const abortedWithoutRequest = "[error] ターンが中断されました（このセッションは中断を要求していません）。" +
	"opencode serve が入れ替わった可能性があります — もう一度送ると続きから進みます"

// Interrupt is the Console's stop (ADR 0105): the queue decides what the stop is and what
// happens to the queued input; this delivers it to serve. A first stop ends the running turn
// and the queue continues; a second one (or DiscardQueue) also discards what is still queued.
func (h *threadHandle) Interrupt(opts agents.InterruptOpts) (agents.InterruptResult, error) {
	return h.interrupt(opts, false)
}

// RemoveQueued takes a queued entry out while it is cancellable (decision 5), the input the
// pump holds behind another client's turn included.
func (h *threadHandle) RemoveQueued(id string) (agents.QueueItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tq().Remove(id)
}

// DismissDiscard drops a kept discard once the member restored or dismissed it (decision 4).
func (h *threadHandle) DismissDiscard(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tq().DismissDiscard(id)
}

// interruptAll is the stop for Agent shutdown: the whole queue goes and nothing is kept for
// return (decision 8), because anything left queued would be started on the way down.
func (h *threadHandle) interruptAll() error {
	_, err := h.interrupt(agents.InterruptOpts{DiscardQueue: true}, true)
	return err
}

func (h *threadHandle) interrupt(opts agents.InterruptOpts, teardown bool) (agents.InterruptResult, error) {
	h.mu.Lock()
	addr, ses, dir, pumpRunning := h.addr, h.ses, h.dir, h.running
	h.mu.Unlock()
	// Input the pump has not taken yet is the turn being started only when serve runs nothing:
	// behind another client's turn (an attached TUI's) it is queued, and a first stop lets it
	// continue. Asked outside the lock because it is an HTTP call.
	foreign := !pumpRunning && ses != "" && serveSessionBusy(addr, ses, dir)

	h.mu.Lock()
	if teardown {
		h.tq().DropAll()
	}
	// running covers the whole pump cycle, the wait behind another client's turn included. An
	// input committed but not yet sent (HeadStopPending) has nothing on serve to abort: runTurn
	// finds the stop before /message goes out. Every other case aborts what serve runs, as the
	// stop always has: this input's turn, or the other client's turn it waits behind.
	running := h.running
	out := h.tq().Interrupt(opts, running || foreign)
	abort := running && out.Head != agents.HeadStopPending
	h.compacting = false // a stop ends any compaction with the turn (measured: the abort completes it)
	if running {
		// The member's stop settles the turn as cancelled: no retry note, live or failed, may outlive it.
		h.retry, h.retryFailed, h.retryStop = nil, nil, ""
	}
	switch {
	case running:
		h.state = agents.TurnInterrupting
	case out.Head == agents.HeadCancelled:
		// Accepted into an idle session but not yet taken by the pump: the input was the turn
		// being started, and it never becomes one.
		h.state = agents.TurnCancelled
	}
	st := h.state
	if abort {
		h.abortAsked = true
	}
	h.mu.Unlock()
	if running || out.Head == agents.HeadCancelled {
		h.emit(agents.Event{Kind: "turn_state", TurnState: st})
	}
	if !abort {
		return out.Result, nil
	}
	return out.Result, serveAbort(addr, ses, dir)
}

// UpdateSettings merges the dynamic thread settings (§9.4-3). They take effect as the
// next turn's /message parameters (agent / model / variant): the v1 flow has no RPC that
// persists a settings update on the thread, so the driver owns the settings.
func (h *threadHandle) UpdateSettings(s agents.ThreadSettings) error {
	h.mu.Lock()
	if s.ClearModel {
		h.settings.Model = ""
	} else if s.Model != "" {
		h.settings.Model = s.Model
	}
	if s.ClearEffort {
		h.settings.Effort = ""
	} else if s.Effort != "" {
		h.settings.Effort = s.Effort
	}
	if s.Mode != "" {
		h.settings.Mode = s.Mode
	}
	cur := h.settings
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "settings", Settings: &cur})
	return nil
}

// Respond answers the pending Interaction (§5): questions only, since all three kinds run
// with approvals bypassed. answer is converted to the per-question list of selected labels
// and sent to /question/{id}/reply; cancel/deny goes to /reject.
func (h *threadHandle) Respond(reply agents.InteractionReply) error {
	h.mu.Lock()
	inter := h.inter
	addr := h.addr
	if isV2() {
		inter = h.formByID(reply.ID)
	}
	h.mu.Unlock()
	if inter == nil || inter.ID != reply.ID {
		return fmt.Errorf("interaction %s is not pending: %w", reply.ID, errFormSettled)
	}
	if isV2() {
		// A form, not a question: it is answered by field key and read again first (v2form.go).
		err := respondForm(addr, h.sessionID(), reply)
		if err == nil || errors.Is(err, errFormSettled) {
			// Only this form goes: a later one may have arrived while the request was out.
			h.settleForm(reply.ID)
		}
		return err
	} else {
		switch reply.Decision {
		case agents.DecisionCancel, agents.DecisionDeny:
			if err := serveQuestionReject(addr, reply.ID, h.dir); err != nil {
				return err
			}
		case agents.DecisionAnswer, agents.DecisionAllow:
			answers, err := answersToLabels(inter.Questions, reply.Answers)
			if err != nil {
				return err
			}
			if err := serveQuestionReply(addr, reply.ID, h.dir, answers); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported decision: %s", reply.Decision)
		}
	}
	h.mu.Lock()
	h.inter = nil
	if h.running {
		h.state = agents.TurnRunning
	}
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
	return nil
}

func (h *threadHandle) Events() <-chan agents.Event { return h.events }

// Snapshot (§6-3) is the current position, for reconciliation.
func (h *threadHandle) Snapshot() (agents.ThreadSnapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return agents.ThreadSnapshot{
		TurnState:   h.state,
		Interaction: h.inter,
		Settings:    h.settings,
	}, nil
}

// queuedPrompts surfaces the driver-held queue for the mirror's queued badge (§10.2-10:
// the ClientMessageID ledger and the turn state machine are canonical).
func (h *threadHandle) queuedPrompts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, p := range h.tq().Texts() {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// managedEnrich folds the driver-side state into the read layer's TranscriptData (called
// from readTranscript): it puts the Interaction id on the pending question (the address
// the Console's /respond path needs) and merges the driver-side queue into Queued. A tui
// session, which has no handle, is left alone.
func managedEnrich(m session.Meta, td *agents.TranscriptData) {
	if m.DriverKind() != session.DriverManaged {
		return
	}
	h := handleFor(m.Name)
	if h == nil {
		return
	}
	h.mu.Lock()
	inter := h.inter
	modeSet := h.settings.Mode
	td.QueuedItems = h.tq().Items()
	td.Discards = h.tq().Discards()
	td.Retry = h.retryInfo()
	h.mu.Unlock()
	if inter != nil {
		qs := make([]transcript.Question, len(inter.Questions))
		copy(qs, inter.Questions)
		for i := range qs {
			qs[i].ID = inter.ID
		}
		td.Pending = qs
	}
	td.Queued = append(td.Queued, h.queuedPrompts()...)
	// mode chip (§10.2-5): the db-derived mode is "the last turn's agent" and is stale
	// right after a switch. The driver setting, the value the next turn will use, is the
	// truth whenever it is set.
	if modeSet != "" {
		td.Mode = modeSet
	}
}

// --- serve API helpers ---------------------------------------------------------

// dirQ appends the session's project directory to a serve URL. serve's session/question/
// status surfaces are scoped to a project (measured 1.17.18: without directory, a session
// living outside serve's cwd appears in neither /question nor /session/status). Calling
// the session APIs with directory is the safe choice even when an id is given.
func dirQ(base, dir string) string {
	if dir == "" {
		return base
	}
	sep := "?"
	if strings.ContainsRune(base, '?') {
		sep = "&"
	}
	return base + sep + "directory=" + url.QueryEscape(dir)
}

func serveSessionExists(addr, ses, dir string) bool {
	res, err := serveClient.Get(sessionURL(addr, ses, dir))
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode == http.StatusOK
}

func serveSessionBusy(addr, ses, dir string) bool {
	m, err := activeSessions(addr, dir)
	if err != nil {
		return false
	}
	st, ok := m[ses] // an idle session is not listed (measured)
	return ok && st != "idle"
}

func waitIdle(addr, ses, dir string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !serveSessionBusy(addr, ses, dir) {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func serveCreateSession(addr, dir, title string) (string, error) {
	u, body := createSessionRequest(addr, dir, title)
	res, err := serveClient.Post(u, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("opencode session の作成に失敗しました: %w", err)
	}
	defer res.Body.Close()
	id := sessionID(res.Body)
	if id == "" {
		return "", errors.New("opencode session の作成応答を解釈できません")
	}
	return id, nil
}

// serveForkSession copies src into a NEW opencode session. at, when non-empty, is the
// message the copy stops BEFORE: opencode's fork loop breaks at the first message whose
// id sorts >= it, so the anchored turn and everything after it stay out of the fork
// (measured 1.18.14 — docs/log/55 §55.2). Empty at = the whole conversation.
func serveForkSession(addr, src, dir, at string) (string, error) {
	body, err := forkBody(at)
	if err != nil {
		return "", fmt.Errorf("opencode session の分岐点を組み立てられません: %w", err)
	}
	res, err := serveClient.Post(forkURL(addr, src, dir), "application/json", strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("opencode session の分岐に失敗しました: %w", err)
	}
	defer res.Body.Close()
	// The anchor is client-supplied, so this is the one serve call that can be rejected
	// for what we asked rather than for how the daemon is doing. Say so instead of
	// letting it fall through to the generic "cannot parse the response" error.
	if res.StatusCode >= 400 {
		if at != "" {
			return "", fmt.Errorf("opencode が分岐点を受け付けませんでした (HTTP %d)", res.StatusCode)
		}
		return "", fmt.Errorf("opencode session の分岐に失敗しました (HTTP %d)", res.StatusCode)
	}
	id := sessionID(res.Body)
	if id == "" {
		return "", errors.New("opencode session の分岐応答を解釈できません")
	}
	return id, nil
}

func serveAbort(addr, ses, dir string) error {
	res, err := serveClient.Post(abortURL(addr, ses, dir), "application/json", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

// serveQuestion is the wire QuestionRequest (GET /question, question.asked event).
type serveQuestion struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Questions []struct {
		Question string `json:"question"`
		Header   string `json:"header"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
		Multiple bool `json:"multiple"`
		Custom   bool `json:"custom"`
	} `json:"questions"`
}

func (q *serveQuestion) toInteraction() *agents.Interaction {
	inter := &agents.Interaction{ID: q.ID, Kind: "question"}
	for _, sq := range q.Questions {
		tq := transcript.Question{
			ID:          q.ID,
			Question:    sq.Question,
			Header:      sq.Header,
			MultiSelect: sq.Multiple,
		}
		for _, o := range sq.Options {
			tq.Options = append(tq.Options, transcript.Option{Label: o.Label, Description: o.Description})
		}
		inter.Questions = append(inter.Questions, tq)
	}
	return inter
}

func serveListQuestions(addr, dir string) []*serveQuestion {
	if isV2() {
		return nil // v2 has no /question: they became forms (v2form.go)
	}
	res, err := serveClient.Get(dirQ(addr+"/question", dir))
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	var qs []*serveQuestion
	if json.NewDecoder(res.Body).Decode(&qs) != nil {
		return nil
	}
	return qs
}

func serveQuestionReply(addr, id, dir string, answers [][]string) error {
	body, _ := json.Marshal(map[string]any{"answers": answers})
	res, err := serveClient.Post(dirQ(addr+"/question/"+url.PathEscape(id)+"/reply", dir), "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("質問への回答が拒否されました (HTTP %d)", res.StatusCode)
	}
	return nil
}

func serveQuestionReject(addr, id, dir string) error {
	res, err := serveClient.Post(dirQ(addr+"/question/"+url.PathEscape(id)+"/reject", dir), "application/json", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("質問の却下が拒否されました (HTTP %d)", res.StatusCode)
	}
	return nil
}

// --- pure mapping helpers (unit-tested) ---------------------------------------

// normalizeMsgID makes a ClientMessageID acceptable as opencode's v1 messageID (the ^msg
// prefix is required — measured). An empty id is assigned here, by AF (§4).
func normalizeMsgID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		b := make([]byte, 12)
		_, _ = rand.Read(b)
		return "msg_af" + hex.EncodeToString(b)
	}
	if strings.HasPrefix(id, "msg") {
		return id
	}
	return "msg_af_" + id
}

// agentForMode maps the ThreadSettings.Mode vocabulary (the same vocabulary as
// TranscriptData.Mode) onto opencode's agent name.
func agentForMode(mode string) string {
	switch mode {
	case "plan":
		return "plan"
	case "normal":
		return "build"
	}
	return ""
}

// splitModel parses the launch-model string "provider/model" (the same shape buildProgram
// passes to --model) into a v1 model ref. A value with no "/" has no identifiable
// provider, so nothing is sent and serve's default applies.
func splitModel(s string) (provider, model string, ok bool) {
	s = strings.TrimSpace(s)
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// buildParts assembles the v1 message parts: prompt text + one file part per
// attachment (§10.2-3: managed attaches through the API, not by pasting into tmux).
func buildParts(in agents.TurnInput) []map[string]any {
	var parts []map[string]any
	if t := strings.TrimSpace(in.Prompt); t != "" {
		parts = append(parts, map[string]any{"type": "text", "text": t})
	}
	for _, p := range in.Attachments {
		if strings.TrimSpace(p) == "" {
			continue
		}
		mt := mime.TypeByExtension(filepath.Ext(p))
		if mt == "" {
			mt = "application/octet-stream"
		}
		parts = append(parts, map[string]any{
			"type": "file", "mime": mt,
			"filename": filepath.Base(p),
			"url":      "file://" + p,
		})
	}
	return parts
}

// answersToLabels converts the structured reply (per-question Text / Options indexes)
// into opencode's answers (per-question lists of selected labels). An index resolves to
// that question's option; Text is passed through as a single label (a custom answer).
func answersToLabels(questions []transcript.Question, answers []agents.InteractionAnswer) ([][]string, error) {
	if len(answers) != len(questions) {
		return nil, fmt.Errorf("回答数が質問数と一致しません (%d != %d)", len(answers), len(questions))
	}
	out := make([][]string, len(answers))
	for i, a := range answers {
		var labels []string
		for _, oi := range a.Options {
			if oi < 0 || oi >= len(questions[i].Options) {
				return nil, fmt.Errorf("質問 %d の選択肢 %d は範囲外です", i+1, oi)
			}
			labels = append(labels, questions[i].Options[oi].Label)
		}
		if len(labels) == 0 && strings.TrimSpace(a.Text) != "" {
			labels = []string{strings.TrimSpace(a.Text)}
		}
		if len(labels) == 0 {
			return nil, fmt.Errorf("質問 %d に回答がありません", i+1)
		}
		out[i] = labels
	}
	return out, nil
}

// --- SSE dispatch ---------------------------------------------------------------

// handleServeEvent routes one SSE event to the owning handle (called from the
// supervisor's monitorEvents). Questions are the point (§5); permission.asked is
// auto-allowed as insurance for bypass operation (approvals are automated for all three
// kinds, §5 — serve's default was measured to pass them through, but a user config that
// adds ask must not silently wedge a managed session).
func handleServeEvent(data []byte) {
	var ev struct {
		// /global/event wraps it as {"payload": {type, properties}} (measured). The bare
		// /event shape ({type, properties} inline) is accepted too, for tests and future
		// compatibility.
		Payload    json.RawMessage `json:"payload"`
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
		// v2 (/api/event) carries the body in "data" instead (measured 2.0.28).
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	if len(ev.Properties) == 0 && len(ev.Data) > 0 {
		ev.Properties = ev.Data
	}
	if ev.Type == "" && len(ev.Payload) > 0 {
		inner := ev
		if json.Unmarshal(ev.Payload, &inner) != nil {
			return
		}
		ev.Type, ev.Properties = inner.Type, inner.Properties
	}
	noteProgress(ev.Type, ev.Properties)
	switch ev.Type {
	case "question.asked":
		var q serveQuestion
		if json.Unmarshal(ev.Properties, &q) != nil || q.SessionID == "" {
			return
		}
		if h := handleBySes(q.SessionID); h != nil {
			inter := q.toInteraction()
			h.mu.Lock()
			h.inter = inter
			h.state = agents.TurnWaitingInteraction
			h.mu.Unlock()
			h.emit(agents.Event{Kind: "interaction", TurnState: agents.TurnWaitingInteraction, Interaction: inter})
		}
	case "question.replied", "question.rejected":
		var p struct {
			SessionID string `json:"sessionID"`
			RequestID string `json:"requestID"`
		}
		if json.Unmarshal(ev.Properties, &p) != nil {
			return
		}
		if h := handleBySes(p.SessionID); h != nil {
			// Clear it even when the answer came from the attached TUI (co-use, §2).
			h.mu.Lock()
			if h.inter != nil && h.inter.ID == p.RequestID {
				h.inter = nil
				if h.running {
					h.state = agents.TurnRunning
				}
			}
			h.mu.Unlock()
			h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
		}
	case "form.created":
		// v2: the question tool asks through a form (v2form.go); it is the question.asked of v1.
		var p struct {
			Form v2Form `json:"form"`
		}
		if json.Unmarshal(ev.Properties, &p) != nil || p.Form.ID == "" {
			return
		}
		if h := handleBySes(p.Form.SessionID); h != nil {
			h.askForm(p.Form.interaction())
		}
	case "form.replied", "form.cancelled":
		var p struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(ev.Properties, &p) != nil {
			return
		}
		if h := handleBySes(p.SessionID); h != nil {
			// Whoever settled it: the Console, an attached TUI, or an interrupt.
			h.settleForm(p.ID)
		}
	case "message.updated":
		// Compaction (measured 1.18.34): the v1 loop announces neither
		// session.next.compaction.started nor .ended — those belong to the v2 event store and
		// never reach /global/event. What it does send is the compaction summary's own
		// message.updated: an assistant message with mode "compaction", without time.completed
		// while it is written and with it once it ends or is aborted. Any other assistant
		// message means the summary is over and the turn has moved on.
		var p struct {
			Info struct {
				compactionProbe
				SessionID string `json:"sessionID"`
			} `json:"info"`
		}
		if json.Unmarshal(ev.Properties, &p) != nil || p.Info.Role != "assistant" {
			return
		}
		if h := handleBySes(p.Info.SessionID); h != nil {
			h.setCompacting(p.Info.inFlight())
		}
	case "session.status":
		// v1: busy | retry | idle per session. The status goes busy between two attempts, so
		// only output (message.part.updated below), idle and the turn's end clear a retry.
		var p struct {
			SessionID string `json:"sessionID"`
			Status    struct {
				Type    string `json:"type"`
				Attempt int    `json:"attempt"`
				Message string `json:"message"`
				Next    int64  `json:"next"`
			} `json:"status"`
		}
		if json.Unmarshal(ev.Properties, &p) != nil {
			return
		}
		if h := handleBySes(p.SessionID); h != nil {
			switch p.Status.Type {
			case "retry":
				h.noteRetry(p.Status.Attempt, p.Status.Message, p.Status.Next)
			case "idle":
				h.clearRetry()
			}
		}
	case "session.retry.scheduled":
		// v2: the next attempt of a failing step is due at "at".
		var p struct {
			SessionID string `json:"sessionID"`
			Attempt   int    `json:"attempt"`
			At        int64  `json:"at"`
			Error     v2Err  `json:"error"`
		}
		if json.Unmarshal(ev.Properties, &p) != nil {
			return
		}
		if h := handleBySes(p.SessionID); h != nil {
			h.noteRetry(p.Attempt, p.Error.Message, p.At)
		}
	case "message.part.updated", "session.text.started", "session.step.ended":
		// The retried call produced output (a failing attempt emits no part, measured).
		var p struct {
			SessionID string `json:"sessionID"`
			Part      struct {
				SessionID string `json:"sessionID"`
			} `json:"part"`
		}
		if json.Unmarshal(ev.Properties, &p) != nil {
			return
		}
		if p.SessionID == "" {
			p.SessionID = p.Part.SessionID
		}
		if h := handleBySes(p.SessionID); h != nil {
			h.clearRetry()
		}
	case "session.compaction.started":
		// v2: the summary is being written. Cleared by .ended / .failed, and by every
		// terminal turn state (setState), so a missed end event cannot keep it on.
		var p struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(ev.Properties, &p) == nil {
			if h := handleBySes(p.SessionID); h != nil {
				h.setCompacting(true)
			}
		}
	case "session.compaction.ended", "session.compaction.failed":
		var p struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(ev.Properties, &p) == nil {
			if h := handleBySes(p.SessionID); h != nil {
				h.setCompacting(false)
			}
		}
	case "session.execution.started", "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted":
		// v2: a turn started or ended (testdata/v2/). The payload names its session, so a subagent's
		// execution events (its own id) never reach the parent's turn.
		var p struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(ev.Properties, &p) != nil || p.SessionID == "" {
			return
		}
		wakeExec(p.SessionID)
		if ev.Type != "session.execution.started" {
			if h := handleBySes(p.SessionID); h != nil {
				h.setCompacting(false)
			}
		}
	case "session.compacted", "session.idle":
		var p struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(ev.Properties, &p) != nil {
			return
		}
		if h := handleBySes(p.SessionID); h != nil {
			h.setCompacting(false)
		}
	case "permission.asked":
		var p struct {
			ID        string `json:"id"`
			RequestID string `json:"requestID"` // v2 may name it so (the spec's path parameter)
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(ev.Properties, &p) != nil {
			return
		}
		if p.ID == "" {
			p.ID = p.RequestID
		}
		if p.ID == "" {
			return
		}
		if h := handleBySes(p.SessionID); h != nil {
			h.mu.Lock()
			addr, dir := h.addr, h.dir
			h.mu.Unlock()
			u, payload := permissionReplyRequest(addr, p.SessionID, p.ID, dir)
			if res, err := serveClient.Post(u, "application/json", strings.NewReader(payload)); err == nil {
				res.Body.Close()
				log.Printf("opencode managed: auto-allowed permission %s (session %s)", p.ID, h.name)
			}
		}
	}
}

// handleBySes finds the live handle owning an opencode session id.
func handleBySes(ses string) *threadHandle {
	if ses == "" {
		return nil
	}
	for _, h := range liveHandles() {
		if h.sessionID() == ses {
			return h
		}
	}
	return nil
}
