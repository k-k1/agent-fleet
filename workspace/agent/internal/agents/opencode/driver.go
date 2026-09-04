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
//     message and never starts a turn (measured), and v2 /api/session/{id}/prompt writes
//     to a different store (session_message) that the read layer cannot see.
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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// turnClient runs the blocking /message calls: no timeout — a turn legitimately
// runs for minutes/hours. Interrupt (abort) or daemon death unblocks it.
var turnClient = &http.Client{}

// ledger is the persistent ClientMessageID ledger (§9.5, persisted across processes): an
// in-memory ledger does not deduplicate a re-send that spans an Agent restart.
var ledger = agents.NewMsgLedger("opencode-msgledger")

// NewDriver returns the managed opencode Driver (driverOf looks it up from /turn and
// /respond). The read layer is preserved by embedding agentImpl as is.
func NewDriver() agents.Driver { return managedDriver{} }

type managedDriver struct{ agentImpl }

// Capabilities (§3.1). Steer here means "accepts a follow-up aimed at the running turn",
// implemented as a driver-side queue (see the file comment). TUIAttach is an opencode
// strength: `opencode attach` joins a running serve without stopping it (measured).
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
	if m.Kind != session.KindOpencode {
		return nil, errors.New("opencode driver は opencode セッション専用です")
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
			return nil, err
		}
		// The same per-slot mapping claude/codex use: the read layer's activeSession
		// consults it first, and with no plugin in managed mode the driver is the writer.
		sids.Write(ocSid, ses)
	}

	h.mu.Lock()
	h.addr, h.gen, h.ses, h.alive = addr, gen, ses, true
	// Revive a queue left behind when a daemon death ended the pump (§31): without this
	// the entries stall forever.
	if len(h.queue) > 0 && !h.pumping {
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
		return
	}
	h.mu.Lock()
	addr, ses, dir, running := h.addr, h.ses, h.dir, h.running
	h.alive = false
	h.queue = nil
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
	return h.running || len(h.queue) > 0
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
			_ = h.Interrupt()
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
}

// reconcileAll is the supervisor-facing wrapper (serve.go, after a daemon death or restart).
func reconcileAll(reason string) { ReconcileManaged(reason) }

// --- thread handle -----------------------------------------------------------

type threadHandle struct {
	name  string
	dir   string
	ocSid string

	mu sync.Mutex
	// resumeMu serializes Resume end-to-end (same §32 competition as codex: two
	// concurrent Resumes would create two native sessions and orphan one).
	resumeMu sync.Mutex

	addr     string
	gen      int
	ses      string
	alive    bool
	state    agents.TurnState
	running  bool // a turn goroutine is in flight (pump busy)
	pumping  bool
	queue    []agents.TurnInput
	settings agents.ThreadSettings
	inter    *agents.Interaction // pending question (the payload of waiting_interaction)
	events   chan agents.Event
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
	h.mu.Lock()
	h.alive = false
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
func (h *threadHandle) Send(in agents.TurnInput) error { return h.accept(in) }

// Steer is a follow-up input to the running turn (§4 queued). opencode v1 has no entry
// point for mid-turn injection (see the file comment), so the semantics are "submit as
// the next turn once the running one finishes".
func (h *threadHandle) Steer(in agents.TurnInput) error { return h.accept(in) }

func (h *threadHandle) accept(in agents.TurnInput) error {
	if strings.TrimSpace(in.Prompt) == "" && len(in.Attachments) == 0 {
		return errors.New("empty prompt")
	}
	in.ClientMessageID = normalizeMsgID(in.ClientMessageID)
	h.mu.Lock()
	if !h.alive {
		h.mu.Unlock()
		return errors.New("runtime が停止しています（再開してください）")
	}
	if h.inter != nil {
		// Free text sent while a question is pending invites a mis-answer (the same
		// call /input's question_pending guard makes): steer the caller to the
		// structured reply (Respond).
		h.mu.Unlock()
		return errQuestionPending
	}
	if ledger.SeenOrRecord(h.name, in.ClientMessageID) {
		h.mu.Unlock()
		return nil // re-send; the persistent cross-process ledger makes it idempotent (§4)
	}
	h.queue = append(h.queue, in)
	start := !h.pumping
	if start {
		h.pumping = true
	}
	if len(h.queue) > 0 && (h.running || len(h.queue) > 1) {
		h.state = agents.TurnQueued
	}
	h.mu.Unlock()
	if start {
		go h.pump()
	}
	return nil
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
		h.mu.Lock()
		if len(h.queue) == 0 || !h.alive {
			// Settle the stop inside the same lock accept() takes. A gap between the
			// empty check and clearing the flag strands input that arrives in it: it
			// sees pumping=true and starts no pump.
			h.pumping = false
			h.mu.Unlock()
			return
		}
		in := h.queue[0]
		h.queue = h.queue[1:]
		addr, ses, dir := h.addr, h.ses, h.dir
		h.running = true
		h.mu.Unlock()

		// Guard for TUI co-use: wait while another client's turn is running (the same
		// 60s as the maximum drain), then send anyway — serve handles /message serially
		// even when busy.
		waitIdle(addr, ses, dir, 60*time.Second)
		h.runTurn(in)

		h.mu.Lock()
		h.running = false
		h.mu.Unlock()
	}
}

// runTurn executes ONE blocking v1 /message turn and lands the terminal state.
// It also updates the status store, standing in for hooks: WireLive prefers the
// db-derived LiveState, but the fallback and anySessionWorking (graceful shutdown's wait
// condition) read this. Without returning to idle at the end of a turn, the session
// sticks on "in progress".
func (h *threadHandle) runTurn(in agents.TurnInput) {
	agents.MarkTurnStart(h.ocSid)
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
	body := map[string]any{"parts": buildParts(in)}
	if ag := agentForMode(st.Mode); ag != "" {
		body["agent"] = ag
	}
	if prov, model, ok := splitModel(st.Model); ok {
		body["model"] = map[string]string{"providerID": prov, "modelID": model}
	}
	if st.Effort != "" {
		body["variant"] = st.Effort
	}
	buf, err := json.Marshal(body)
	if err != nil {
		h.setState(agents.TurnFailed)
		return
	}
	h.setState(agents.TurnRunning)
	req, err := http.NewRequest("POST", dirQ(addr+"/session/"+url.PathEscape(ses)+"/message", h.dir), bytes.NewReader(buf))
	if err != nil {
		h.setState(agents.TurnFailed)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := turnClient.Do(req)
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
	turnErr, failed := decodeTurnError(res.Body)
	h.mu.Lock()
	interrupted := h.state == agents.TurnInterrupting
	h.inter = nil // the turn ended, so no question is pending any more
	h.mu.Unlock()
	switch {
	case interrupted:
		h.setState(agents.TurnCancelled)
	case res.StatusCode >= 400:
		failure = fmt.Sprintf("[error] HTTP %d", res.StatusCode)
		log.Printf("opencode managed: turn failed name=%s status=%d", h.name, res.StatusCode)
		h.setState(agents.TurnFailed)
	case failed:
		failure = turnErr.summary()
		log.Printf("opencode managed: turn failed name=%s model=%s %s", h.name, st.Model, turnErr.summary())
		if turnErr.retryable() {
			h.setState(agents.TurnAborted)
		} else {
			h.setState(agents.TurnFailed)
		}
	default:
		h.setState(agents.TurnCompleted)
	}
}

// Interrupt aborts the running turn and clears the queued follow-ups: the intent to stop
// reaches the queue too, since nothing surprises a user more than an old follow-up
// starting on its own once the turn finishes.
func (h *threadHandle) Interrupt() error {
	h.mu.Lock()
	addr, ses, dir := h.addr, h.ses, h.dir
	running := h.running
	h.queue = nil
	if running {
		h.state = agents.TurnInterrupting
	}
	h.mu.Unlock()
	if !running {
		return nil
	}
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnInterrupting})
	return serveAbort(addr, ses, dir)
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
	h.mu.Unlock()
	if inter == nil || inter.ID != reply.ID {
		return fmt.Errorf("interaction %s は待機中ではありません", reply.ID)
	}
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
	for _, in := range h.queue {
		if t := strings.TrimSpace(in.Prompt); t != "" {
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
	res, err := serveClient.Get(dirQ(addr+"/session/"+url.PathEscape(ses), dir))
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode == http.StatusOK
}

func serveSessionBusy(addr, ses, dir string) bool {
	res, err := serveClient.Get(dirQ(addr+"/session/status", dir))
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var m map[string]struct {
		Type string `json:"type"`
	}
	if json.NewDecoder(res.Body).Decode(&m) != nil {
		return false
	}
	st, ok := m[ses] // an idle session is not listed (measured)
	return ok && st.Type != "idle"
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
	body, _ := json.Marshal(map[string]string{"title": title})
	u := addr + "/session?directory=" + url.QueryEscape(dir)
	res, err := serveClient.Post(u, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("opencode session の作成に失敗しました: %w", err)
	}
	defer res.Body.Close()
	var s struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil || s.ID == "" {
		return "", errors.New("opencode session の作成応答を解釈できません")
	}
	return s.ID, nil
}

// serveForkSession copies src into a NEW opencode session. at, when non-empty, is the
// message the copy stops BEFORE: opencode's fork loop breaks at the first message whose
// id sorts >= it, so the anchored turn and everything after it stay out of the fork
// (measured 1.18.14 — docs/log/55 §55.2). Empty at = the whole conversation.
func serveForkSession(addr, src, dir, at string) (string, error) {
	body := "{}"
	if at != "" {
		b, err := json.Marshal(map[string]string{"messageID": at})
		if err != nil {
			return "", fmt.Errorf("opencode session の分岐点を組み立てられません: %w", err)
		}
		body = string(b)
	}
	res, err := serveClient.Post(dirQ(addr+"/session/"+url.PathEscape(src)+"/fork", dir), "application/json", strings.NewReader(body))
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
	var s struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil || s.ID == "" {
		return "", errors.New("opencode session の分岐応答を解釈できません")
	}
	return s.ID, nil
}

func serveAbort(addr, ses, dir string) error {
	res, err := serveClient.Post(dirQ(addr+"/session/"+url.PathEscape(ses)+"/abort", dir), "application/json", strings.NewReader("{}"))
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
	}
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	if ev.Type == "" && len(ev.Payload) > 0 {
		inner := ev
		if json.Unmarshal(ev.Payload, &inner) != nil {
			return
		}
		ev.Type, ev.Properties = inner.Type, inner.Properties
	}
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
	case "permission.asked":
		var p struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(ev.Properties, &p) != nil || p.ID == "" {
			return
		}
		if h := handleBySes(p.SessionID); h != nil {
			h.mu.Lock()
			addr, dir := h.addr, h.dir
			h.mu.Unlock()
			body := strings.NewReader(`{"reply":"always"}`)
			if res, err := serveClient.Post(dirQ(addr+"/permission/"+url.PathEscape(p.ID)+"/reply", dir), "application/json", body); err == nil {
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
