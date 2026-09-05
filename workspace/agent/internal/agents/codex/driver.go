package codex

// The managed codex driver (docs/log/27 P3): the second implementation of the Driver
// types (agents.Driver/ThreadHandle). It maps the turn state machine (§4), Interaction
// (§5) and reconciliation (§6) onto the shared app-server's WS JSON-RPC.
//
// API choices, from measurements (0.144.4, docs/log/27 §12.3):
//   - A turn is driven by `turn/start`. The response returns the turn id immediately
//     (status=inProgress); completion arrives as a `turn/completed` notification
//     (status: completed | interrupted | failed).
//   - `turn/steer` is real mid-turn injection, keyed on expectedTurnId (measured: the
//     user message joins the turn that is already running and shows up in the rest of
//     its answer). Unlike opencode's "queue it until the turn finishes", codex Steer
//     maps natively; it degrades to the queue only when no turn is running.
//   - interrupt = `turn/interrupt {threadId, turnId}` → turn/completed(interrupted).
//   - A settings change = `thread/settings/update` (requires experimentalApi, §12.1-4).
//     Model and effort are plain fields; the mode (plan/normal) folds into
//     collaborationMode {mode: plan|default, settings:{model, reasoning_effort}}.
//   - A question = the server-initiated request `item/tool/requestUserInput` (the
//     per-thread config `features.default_mode_request_user_input=true` has to be asked
//     for on both thread/start and thread/resume — by default it answers "unavailable in
//     this mode", measured). The reply is the JSON-RPC response
//     {answers: {qid: {answers: [label…]}}}. An unanswered request survives a dropped
//     connection: thread/resume redelivers it to the new connection (measured), which is
//     what makes reconciliation of a waiting question work by itself.
//   - After thread/resume a thread's approvalPolicy / sandboxPolicy can have fallen back
//     to the config defaults (measured: a thread created with dangerFullAccess looked
//     readOnly after resume). Re-assert never + dangerFullAccess with
//     thread/settings/update right after resume, to keep bypass operation (§5).
//
// ClientMessageID (§4) idempotency uses the kind-agnostic persistent ledger
// (agents.MsgLedger — the cross-process persistence of §9.5), and the id is also sent to
// codex as clientUserMessageId (measured: it round-trips in the item's clientId, for
// diagnostics and future matching).

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// ledger is the persistent ClientMessageID ledger: cross-process idempotency for resends (§9.5).
var ledger = agents.NewMsgLedger("codex-msgledger")

// NewDriver returns the managed codex Driver (driverOf looks it up from /turn and
// /respond). The read layer is kept as is by embedding agentImpl.
func NewDriver() agents.Driver { return managedDriver{} }

type managedDriver struct{ agentImpl }

// Capabilities (§3.1). Steer maps natively onto turn/steer. TUIAttach is false: codex's
// CLI route is an exclusive switch (stop→drain→resume, §2), not a parallel channel.
func (managedDriver) Capabilities() agents.Capabilities {
	return agents.Capabilities{
		ProcessModel:  "shared-daemon",
		Steer:         true,
		Fork:          true,
		DynamicModel:  true,
		DynamicEffort: true,
		DynamicMode:   true,
		Questions:     true,
	}
}

// threadFeatures is the per-thread config passed on every thread/start and thread/resume.
// The request_user_input tool is disabled by default on app-server threads (measured:
// "unavailable in this mode"), and the TUI does not enable it either (measured 0.144.3 /
// 0.144.5: in Default mode the TUI is refused the same way, only Plan mode turns it on
// automatically). The CLI route stays symmetric: buildProgram passes the same feature
// with -c.
var threadFeatures = map[string]any{"default_mode_request_user_input": true}

// threadConfig is the per-thread config: the features above, plus the MCP servers
// scoped to THIS session (docs/log/27 §9.3.1).
//
// A managed session's MCP child is spawned by the one shared app-server, so the
// process environment cannot carry a per-session AF_SESSION_NAME the way tmux does
// for the TUI route. The thread config is the only channel that varies per session,
// and it was measured to reach the spawned child — so the session name is stamped
// there and `propose_session_handoff` stops guessing from cwd.
//
// A thread map MERGES with the file config layers — $CODEX_HOME/config.toml and a
// trusted project's own .codex/config.toml both still apply — and for a shared name
// the thread definition wins (measured 0.147.0, docs/log/27 §9.3.1). So mcpreg restates
// ONLY the af entry: everything else, including servers af does not manage at all,
// comes through untouched. When there is nothing to override the key is omitted and
// the thread inherits everything, which is also the safe failure for an unreadable
// registry — the session name degrades to the cwd fallback and nothing else changes.
func threadConfig(slot string) map[string]any {
	cfg := map[string]any{"features": threadFeatures}
	if servers, ok := threadMCPServers(slot); ok {
		cfg["mcp_servers"] = servers
	}
	return cfg
}

// sessionMCPDefs is a seam: the real registry reads the user's encrypted store, which
// a unit test must not touch.
var sessionMCPDefs = func() ([]mcpreg.ServerDef, error) { return mcpreg.ForSession(session.KindCodex) }

func threadMCPServers(slot string) (map[string]any, bool) {
	if slot == "" {
		return nil, false
	}
	defs, err := sessionMCPDefs()
	if err != nil {
		// Inherit config.toml rather than fail the thread: an unreadable registry
		// must not stop a session from starting.
		log.Printf("codex managed: cannot read the MCP registry, omitting the per-thread config (%s): %v", slot, err)
		return nil, false
	}
	return mcpreg.CodexThreadServers(defs, mcpreg.CodexThreadOpts{SessionName: slot})
}

// bypassPolicies is AF's unattended-operation policy: the same meaning as the TUI route's
// --dangerously-bypass-… flags, with the container as the sandbox.
func bypassPolicies() map[string]any {
	return map[string]any{
		"approvalPolicy": "never",
		"sandboxPolicy":  map[string]any{"type": "dangerFullAccess"},
	}
}

// Resume returns the session's ThreadHandle, creating the runtime thread when
// none exists yet (Driver interface: start a new one when there is none). It doubles as
// the shared reconciliation procedure of §6: secure the runtime and writer connection →
// resolve the thread (resume/fork/start) → re-assert the policies → apply the snapshot.
// Live subscription is permanent per generation on the writer connection.
func (managedDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	if m.Kind != session.KindCodex {
		return nil, errors.New("codex driver は codex セッション専用です")
	}
	cl, gen, err := Serve().Ensure()
	if err != nil {
		return nil, err
	}
	slotSid := session.UUID(m.Dir, m.Name) // identity: the working copy, never the subdir
	cwd := m.CWD()                         // where the thread runs (Dir, or a chosen subdir)
	handlesMu.Lock()
	h := handles[m.Name]
	if h == nil {
		h = &threadHandle{
			name:    m.Name,
			dir:     cwd,
			slotSid: slotSid,
			events:  make(chan agents.Event, 64),
		}
		handles[m.Name] = h
	}
	handlesMu.Unlock()

	h.resumeMu.Lock()
	defer h.resumeMu.Unlock()

	h.mu.Lock()
	if h.alive && h.gen == gen && h.tid != "" {
		h.mu.Unlock()
		return h, nil
	}
	if h.settings.Model == "" {
		h.settings.Model = m.Model // launch-time default (UpdateSettings overwrites it on a change)
	}
	if h.settings.Effort == "" {
		h.settings.Effort = m.Effort
	}
	if h.settings.Mode == "" {
		h.settings.Mode = m.Mode
	}
	tid := h.tid
	h.mu.Unlock()

	if tid == "" {
		tid = sids.Read(slotSid)
	}
	var st threadSnapshotWire
	if tid != "" {
		st, err = threadResume(cl, tid, cwd, m.Name)
		if err != nil {
			// A thread whose daemon restarted before its first turn has no rollout and
			// cannot be resumed (§12.1-5); the conversation is still empty, so replace it
			// with a fresh start. On any other error, minting a new thread would repoint
			// sids and hide the conversation, so fail honestly instead.
			if !strings.Contains(err.Error(), "no rollout found") {
				return nil, err
			}
			tid = ""
		}
	}
	if tid == "" {
		if m.ForkFrom != "" {
			st, err = threadFork(cl, m.ForkFrom, cwd, m.ForkAt, m.Name)
		} else {
			st, err = threadStart(cl, cwd, firstNonEmpty(h.settings.Model, m.Model), m.Name)
		}
		if err != nil {
			return nil, err
		}
		tid = st.threadID
		// The same slot mapping as the TUI route's hook capture (RememberSid): the read
		// layer's rolloutPath (sids.Read) and the exclusive switch (one conversation kept
		// across cli⇄managed) both ride on it.
		sids.Write(slotSid, tid)
	}
	// After resume the policies can have fallen back to the config defaults (measured), so
	// re-assert them. A failure is not fatal: the turn still runs, only on the readOnly side.
	if _, err := cl.call("thread/settings/update", mergeMaps(map[string]any{"threadId": tid}, bypassPolicies()), 10*time.Second); err != nil {
		log.Printf("codex managed: policy re-assert %s: %v", m.Name, err)
	}

	h.mu.Lock()
	h.client, h.gen, h.tid, h.alive = cl, gen, tid, true
	if st.model != "" {
		h.curModel = st.model
		if h.settings.Model == "" {
			h.settings.Model = st.model // so GET /settings returns the effective value
		}
	}
	// reconcile (§6-4): the server state resume returned is the truth. A turn still running
	// (started by the previous Agent process) has its turnId taken over here and is settled
	// by the turn/completed notification. If a question is pending, the request is
	// redelivered right after (measured).
	switch {
	case st.active && st.waitingInput:
		h.state = agents.TurnWaitingInteraction
	case st.active:
		h.state = agents.TurnRunning
	default:
		h.state = agents.TurnCompleted
	}
	h.running = st.active
	h.turnID = st.inProgressTurn
	if !st.waitingInput {
		h.inter = nil
	}
	h.mu.Unlock()

	// thread/start only accepts model; effort and collaboration mode belong to
	// thread/settings/update. Re-apply the value chosen in the creation UI and the dynamic
	// settings persisted in Meta to the native thread before the first turn (and after a
	// daemon reconnect or a fork). Making this best-effort would let the UI selection and
	// the real inference settings diverge silently, so when the user specified something a
	// failure is returned as a Resume failure.
	if m.Model != "" || m.Effort != "" || m.Mode != "" {
		if err := h.UpdateSettings(agents.ThreadSettings{Model: m.Model, Effort: m.Effort, Mode: m.Mode}); err != nil {
			h.mu.Lock()
			h.alive = false // retry applying the settings on the next Resume
			h.mu.Unlock()
			return nil, fmt.Errorf("codex thread の実行設定を反映できませんでした: %w", err)
		}
	}

	// After a daemon death ends the pump, already-sent input can still sit in the queue
	// (§31). Unless Resume restarts it here, submitted prompts stay stranded.
	h.mu.Lock()
	if len(h.queue) > 0 && !h.pumping && !h.running {
		h.pumping = true
		go h.pump()
	}
	h.mu.Unlock()

	// Baseline for exit recording (the same role as in the opencode driver and tui startSessionTmux).
	base, _ := status.OOMKillCount()
	status.PersistExit(m.Name, status.ExitInfo{OOMBase: base})
	return h, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func mergeMaps(dst map[string]any, src map[string]any) map[string]any {
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// --- thread RPC helpers --------------------------------------------------------

// threadSnapshotWire is what Resume needs back from thread/start, resume and fork.
type threadSnapshotWire struct {
	threadID       string
	model          string
	active         bool
	waitingInput   bool
	inProgressTurn string
}

func parseThreadResult(res json.RawMessage) (threadSnapshotWire, error) {
	var p struct {
		Model  string `json:"model"`
		Thread struct {
			ID     string `json:"id"`
			Status struct {
				Type        string   `json:"type"`
				ActiveFlags []string `json:"activeFlags"`
			} `json:"status"`
			Turns []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(res, &p); err != nil || p.Thread.ID == "" {
		return threadSnapshotWire{}, errors.New("thread 応答を解釈できません")
	}
	st := threadSnapshotWire{threadID: p.Thread.ID, model: p.Model}
	st.active = p.Thread.Status.Type == "active"
	for _, f := range p.Thread.Status.ActiveFlags {
		if f == "waitingOnUserInput" {
			st.waitingInput = true
		}
	}
	for _, t := range p.Thread.Turns {
		if t.Status == "inProgress" {
			st.inProgressTurn = t.ID
		}
	}
	return st, nil
}

func threadStart(cl *appClient, cwd, model, slot string) (threadSnapshotWire, error) {
	params := mergeMaps(map[string]any{"cwd": cwd, "config": threadConfig(slot)}, bypassPolicies())
	if model != "" {
		params["model"] = model
	}
	res, err := cl.call("thread/start", params, 30*time.Second)
	if err != nil {
		return threadSnapshotWire{}, fmt.Errorf("codex thread の作成に失敗しました: %w", err)
	}
	return parseThreadResult(res)
}

func threadResume(cl *appClient, tid, cwd, slot string) (threadSnapshotWire, error) {
	params := map[string]any{"threadId": tid, "cwd": cwd, "config": threadConfig(slot)}
	res, err := cl.call("thread/resume", params, 30*time.Second)
	if err != nil {
		return threadSnapshotWire{}, err
	}
	return parseThreadResult(res)
}

// threadFork copies src into a NEW thread. lastTurnId, when non-empty, is the last turn
// the fork keeps — **inclusive**: codex omits every turn after it (docs/log/55 §55.2). The
// translation from the Console's exclusive anchor happened in ResolveForkAt; by the time
// it reaches here it is already "the turn to fork through". Empty = the whole thread.
func threadFork(cl *appClient, src, cwd, lastTurnID, slot string) (threadSnapshotWire, error) {
	params := mergeMaps(map[string]any{"threadId": src, "cwd": cwd, "config": threadConfig(slot)}, bypassPolicies())
	if lastTurnID != "" {
		params["lastTurnId"] = lastTurnID
	}
	res, err := cl.call("thread/fork", params, 30*time.Second)
	if err != nil {
		if lastTurnID != "" {
			// codex refuses a turn that is still in progress; that is about the anchor we
			// sent, not about the daemon, so don't report it as a generic fork failure.
			return threadSnapshotWire{}, fmt.Errorf("codex が分岐点を受け付けませんでした: %w", err)
		}
		return threadSnapshotWire{}, fmt.Errorf("codex thread の分岐に失敗しました: %w", err)
	}
	return parseThreadResult(res)
}

// --- handle registry ---------------------------------------------------------

var handlesMu sync.Mutex
var handles = map[string]*threadHandle{}

func handleFor(name string) *threadHandle {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	return handles[name]
}

// handleByTid finds the live handle owning a codex thread id.
func handleByTid(tid string) *threadHandle {
	if tid == "" {
		return nil
	}
	handlesMu.Lock()
	defer handlesMu.Unlock()
	for _, h := range handles {
		h.mu.Lock()
		match := h.tid == tid
		h.mu.Unlock()
		if match {
			return h
		}
	}
	return nil
}

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

// DropHandle detaches a managed session from its runtime handle (stop / halt /
// archive / exclusive switch): interrupt any running turn, unsubscribe the writer
// connection from the thread, forget the handle. The conversation's source of truth
// (the rollout) stays, and a later Resume (or the TUI's `codex resume`) reattaches.
func DropHandle(name string) {
	handlesMu.Lock()
	h := handles[name]
	delete(handles, name)
	handlesMu.Unlock()
	if h == nil {
		return
	}
	h.mu.Lock()
	cl, tid, turnID, running := h.client, h.tid, h.turnID, h.running
	h.alive = false
	h.queue = nil
	h.mu.Unlock()
	if cl == nil {
		return
	}
	if running && turnID != "" {
		_, _ = cl.call("turn/interrupt", map[string]any{"threadId": tid, "turnId": turnID}, 10*time.Second)
	}
	if tid != "" {
		// Do not leave a thread on a connection nobody watches (saves daemon memory and notifications).
		_, _ = cl.call("thread/unsubscribe", map[string]any{"threadId": tid}, 10*time.Second)
	}
}

// RemoveLedger drops a session's ClientMessageID ledger (/stop only, where the slot's
// identity is discarded with it; halt/archive keep it because the session can resume).
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

// ManagedBusy reports a turn is running or queued (the wait/refuse condition for a
// graceful shutdown and for the exclusive switch).
func ManagedBusy(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running || h.turnID != "" || len(h.queue) > 0
}

// AbortManaged interrupts every running managed turn (the equivalent of graceful
// shutdown's per-pane Ctrl-C, §10.2-8).
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

// ReconcileManaged re-attaches managed codex sessions after an Agent boot,
// daemon restart or writer loss (§6). It covers every managed meta that is not treated as
// stopped. On failure the session stays stopped and is retried when the user clicks resume
// (/start).
func ReconcileManaged(reason string) {
	d := managedDriver{}
	for _, m := range session.ListMetas() {
		if m.Kind != session.KindCodex || m.DriverKind() != session.DriverManaged || m.Archived {
			continue
		}
		if m.StoppedAt != "" && handleFor(m.Name) == nil {
			continue // deliberately stopped — resume only on user action
		}
		if _, err := d.Resume(m); err != nil {
			log.Printf("codex managed: reconcile %s (%s): %v", m.Name, reason, err)
		}
	}
}

// reconcileAll is the supervisor-facing wrapper (serve.go, after a daemon death or restart).
func reconcileAll(reason string) { ReconcileManaged(reason) }

// --- thread handle -----------------------------------------------------------

type threadHandle struct {
	name    string
	dir     string
	slotSid string

	// resumeMu serializes Resume end-to-end: its check-then-act (no tid → start a
	// new native thread + sids.Write) spans network calls, and two concurrent
	// Resumes (waitDaemon + writerLost reconcile, or a user /start) would mint two
	// threads and orphan one (§32).
	resumeMu sync.Mutex

	mu       sync.Mutex
	client   *appClient
	gen      int
	tid      string
	alive    bool
	state    agents.TurnState
	running  bool // the runtime has an active turn (pump-driven, or taken over by resume)
	pumping  bool
	turnID   string // the active turn (the target of turn/steer and turn/interrupt)
	turnEnd  chan agents.TurnState
	queue    []agents.TurnInput
	settings agents.ThreadSettings
	curModel string              // latest effective model (from settings/updated and thread responses; required as collaborationMode.settings.model on a mode switch)
	inter    *agents.Interaction // pending question (the contents of waiting_interaction)
	interQ   []string            // question ids (keys of the answer map, same order as Interaction.Questions)
	interReq json.RawMessage     // JSON-RPC id of the server request (scoped to the interCl connection)
	interCl  *appClient
	events   chan agents.Event
	lastErr  *codexError // failure detail of the last turn (errors.go); managedEnrich appends
	// it as a synthetic trailing error turn until clearLastError runs at the next turn start.
}

// setLastError / clearLastError / turnError manage the failure detail managedEnrich
// injects as a synthetic transcript turn (§10.2-5). In-memory only — matches h.inter's
// lifetime, since neither survives a process restart and both are re-derived by
// reconciliation instead of persisted.
func (h *threadHandle) setLastError(e codexError) {
	h.mu.Lock()
	ce := e
	h.lastErr = &ce
	h.mu.Unlock()
}

func (h *threadHandle) clearLastError() {
	h.mu.Lock()
	h.lastErr = nil
	h.mu.Unlock()
}

func (h *threadHandle) turnError() *codexError {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastErr
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

// runtimeLost drops the handle to unknown (§6-1: the honest state on disconnect). The pump's
// wait (if any) unblocks via turnEnd and keeps unknown until reconcile resolves it.
func (h *threadHandle) runtimeLost() {
	h.mu.Lock()
	h.alive = false
	h.state = agents.TurnUnknown
	end := h.turnEnd
	h.mu.Unlock()
	if end != nil {
		select {
		case end <- agents.TurnUnknown:
		default:
		}
	}
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnUnknown})
}

// --- ThreadHandle interface ---------------------------------------------------

// Send starts a turn (turn/start), queueing behind a running one.
func (h *threadHandle) Send(in agents.TurnInput) error { return h.accept(in) }

// Steer injects a follow-up into the RUNNING turn via native turn/steer (measured: keyed
// on expectedTurnId, it joins the same turn). When no turn is running (a race just after
// one completed, say) it degrades to the queue and goes in as the next turn.
func (h *threadHandle) Steer(in agents.TurnInput) error {
	if strings.TrimSpace(in.Prompt) == "" && len(in.Attachments) == 0 {
		return errors.New("empty prompt")
	}
	in.ClientMessageID = agents.NormalizeMsgID(in.ClientMessageID)
	h.mu.Lock()
	if !h.alive {
		h.mu.Unlock()
		return errors.New("runtime が停止しています（再開してください）")
	}
	if h.inter != nil {
		h.mu.Unlock()
		return agents.ErrQuestionPending
	}
	cl, tid, turnID, running := h.client, h.tid, h.turnID, h.running
	h.mu.Unlock()
	if !running || turnID == "" {
		return h.accept(in)
	}
	if ledger.SeenOrRecord(h.name, in.ClientMessageID) {
		return nil // resend: the ledger makes it idempotent (§4)
	}
	_, err := cl.call("turn/steer", map[string]any{
		"threadId":            tid,
		"expectedTurnId":      turnID,
		"input":               buildInput(in),
		"clientUserMessageId": in.ClientMessageID,
	}, 15*time.Second)
	if err != nil {
		// The turn just ended, or similar: keep the intent (the follow-up input) as the next turn.
		h.mu.Lock()
		h.queue = append(h.queue, in)
		start := !h.pumping
		if start {
			h.pumping = true
		}
		h.mu.Unlock()
		if start {
			go h.pump()
		}
	}
	return nil
}

func (h *threadHandle) accept(in agents.TurnInput) error {
	if strings.TrimSpace(in.Prompt) == "" && len(in.Attachments) == 0 {
		return errors.New("empty prompt")
	}
	in.ClientMessageID = agents.NormalizeMsgID(in.ClientMessageID)
	h.mu.Lock()
	if !h.alive {
		h.mu.Unlock()
		return errors.New("runtime が停止しています（再開してください）")
	}
	if h.inter != nil {
		// Free text sent while a question is pending invites a wrong answer (the same
		// judgement as /input's question_pending guard); steer to the structured
		// answer (Respond).
		h.mu.Unlock()
		return agents.ErrQuestionPending
	}
	if ledger.SeenOrRecord(h.name, in.ClientMessageID) {
		h.mu.Unlock()
		return nil // resend: the ledger makes it idempotent (§4)
	}
	h.queue = append(h.queue, in)
	// An externally running turn taken over by Resume has no pump goroutine. In that case
	// wait until the turn/completed dispatcher wakes the queue, so no second turn/start is
	// issued.
	start := !h.pumping && !h.running
	if start {
		h.pumping = true
	}
	if h.running || len(h.queue) > 1 {
		h.state = agents.TurnQueued
	}
	h.mu.Unlock()
	if start {
		go h.pump()
	}
	return nil
}

// pump processes the queue serially: run one turn/start, wait for its
// turn/completed (the dispatcher delivers it through turnEnd), repeat.
func (h *threadHandle) pump() {
	for {
		h.mu.Lock()
		if len(h.queue) == 0 || !h.alive || h.running {
			// Settle the stop inside the same lock accept takes. A gap between the empty
			// check and a defer would strand input arriving in it: it sees pumping=true
			// and never starts a pump.
			h.pumping = false
			h.mu.Unlock()
			return
		}
		in := h.queue[0]
		h.queue = h.queue[1:]
		h.running = true
		gen := h.gen
		h.mu.Unlock()

		h.runTurn(in, gen)

		h.mu.Lock()
		if h.gen == gen {
			h.running = false
		}
		h.mu.Unlock()
	}
}

// runTurn executes ONE turn: turn/start → wait for the dispatcher-delivered
// terminal state. The dispatcher (turn/started, turn/completed) owns the status store
// (the stand-in for hooks, read by WireLive's fallback and by anySessionWorking); here
// only the optimistic working mark is written ahead of it.
func (h *threadHandle) runTurn(in agents.TurnInput, gen int) {
	agents.MarkTurnStart(h.slotSid)
	h.clearLastError() // a new turn starting means the previous turn's synthetic error is done
	h.setState(agents.TurnStarting)
	h.mu.Lock()
	cl, tid := h.client, h.tid
	end := make(chan agents.TurnState, 1)
	h.turnEnd = end
	h.mu.Unlock()

	res, err := cl.call("turn/start", map[string]any{
		"threadId":            tid,
		"input":               buildInput(in),
		"clientUserMessageId": in.ClientMessageID,
	}, 30*time.Second)
	if err != nil {
		h.mu.Lock()
		sameGen := h.gen == gen
		if h.turnEnd == end {
			h.turnEnd = nil
		}
		alive := h.alive
		h.mu.Unlock()
		if !sameGen {
			return // reconciliation already installed a new-generation snapshot
		}
		st := agents.TurnUnknown // disconnected: left to §6, and not reported
		failure := ""
		if alive {
			log.Printf("codex managed: turn/start %s: %v", h.name, err)
			st = agents.TurnFailed
			// When turn/start is refused outright with a JSON-RPC error (measured: sending
			// with the usage limit exhausted), no turn is created and nothing is written to
			// the rollout, not even the user's own message. Without picking up the reason,
			// the notification and the report carry an empty string and can only say
			// "failed", and the mirror's pending echo never finds anything to match and
			// never clears (pendingEcho.echoLanded). The error turn synthesized here stands
			// in for both, through managedEnrich.
			if ce, ok := codexErrorFromRPC(err); ok {
				failure = ce.summary()
				h.setLastError(ce)
			} else {
				failure = "[error] " + err.Error()
			}
		}
		agents.MarkTurnEndErr(h.slotSid, st, failure)
		h.setState(st)
		return
	}
	var tr struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(res, &tr)
	h.mu.Lock()
	if h.gen != gen {
		if h.turnEnd == end {
			h.turnEnd = nil
		}
		h.mu.Unlock()
		return // do not let an old generation's response overwrite a new-generation handle
	}
	h.turnID = tr.Turn.ID
	h.mu.Unlock()
	h.setState(agents.TurnRunning)

	final := <-end // turn/completed (dispatcher) or runtimeLost always delivers one
	h.mu.Lock()
	sameGen := h.gen == gen
	if h.turnEnd == end {
		h.turnEnd = nil
	}
	if sameGen {
		h.turnID = ""
		h.inter = nil // the turn ended, so no question is pending any more
	}
	h.mu.Unlock()
	if sameGen {
		h.setState(final)
	}
}

// Interrupt aborts the running turn and clears the queued follow-ups: the intent to stop
// reaches the queue too.
func (h *threadHandle) Interrupt() error {
	h.mu.Lock()
	cl, tid, turnID := h.client, h.tid, h.turnID
	running := h.running || turnID != "" // turnID only: a running turn taken over after an agent restart
	h.queue = nil
	if running {
		h.state = agents.TurnInterrupting
	}
	h.mu.Unlock()
	if !running || turnID == "" {
		return nil
	}
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnInterrupting})
	_, err := cl.call("turn/interrupt", map[string]any{"threadId": tid, "turnId": turnID}, 15*time.Second)
	return err
}

// UpdateSettings applies dynamic thread settings via thread/settings/update (§9.4-3:
// changing model / effort / mode on a running session).
func (h *threadHandle) UpdateSettings(s agents.ThreadSettings) error {
	h.mu.Lock()
	cl, tid := h.client, h.tid
	next := h.settings
	if s.ClearModel {
		next.Model = ""
	} else if s.Model != "" {
		next.Model = s.Model
	}
	if s.ClearEffort {
		next.Effort = ""
	} else if s.Effort != "" {
		next.Effort = s.Effort
	}
	if s.Mode != "" {
		next.Mode = s.Mode
	}
	model := firstNonEmpty(next.Model, h.curModel)
	alive := h.alive
	h.mu.Unlock()
	if !alive {
		return errors.New("runtime が停止しています（再開してください）")
	}
	params := map[string]any{"threadId": tid}
	if s.ClearModel {
		params["model"] = nil
	} else if s.Model != "" {
		params["model"] = s.Model
	}
	if s.ClearEffort {
		params["effort"] = nil
	} else if s.Effort != "" {
		params["effort"] = s.Effort
	}
	if s.Mode != "" {
		// The mode is a collaborationMode preset (ModeKind: plan | default). settings.model
		// is required, so the effective model is sent with it (reasoning_effort may be null).
		kind := "default"
		if s.Mode == "plan" {
			kind = "plan"
		}
		if model == "" {
			return errors.New("モデルが未確定のためモードを切り替えられません")
		}
		cm := map[string]any{"mode": kind, "settings": map[string]any{"model": model}}
		if e := next.Effort; e != "" {
			cm["settings"].(map[string]any)["reasoning_effort"] = e
		}
		params["collaborationMode"] = cm
	}
	if _, err := cl.call("thread/settings/update", params, 15*time.Second); err != nil {
		return err
	}
	// Advance the local snapshot only after the RPC succeeds, so a failure cannot leave the
	// mode chip ahead of the real runtime.
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

// Respond answers the pending Interaction (§5), questions only. answer/allow becomes the
// JSON-RPC response to the server request (answers[qid] = the list of labels); cancel/deny
// means "stop the turn without answering" and maps to turn/interrupt, because codex has no
// reject entry point.
func (h *threadHandle) Respond(reply agents.InteractionReply) error {
	h.mu.Lock()
	inter, qids, reqID, cl := h.inter, h.interQ, h.interReq, h.interCl
	h.mu.Unlock()
	if inter == nil || inter.ID != reply.ID {
		return fmt.Errorf("interaction %s は待機中ではありません", reply.ID)
	}
	switch reply.Decision {
	case agents.DecisionCancel, agents.DecisionDeny:
		return h.Interrupt()
	case agents.DecisionAnswer, agents.DecisionAllow:
		if len(reply.Answers) != len(inter.Questions) {
			return fmt.Errorf("回答数が質問数と一致しません (%d != %d)", len(reply.Answers), len(inter.Questions))
		}
		answers := map[string]any{}
		for i, a := range reply.Answers {
			var labels []string
			for _, oi := range a.Options {
				if oi < 0 || oi >= len(inter.Questions[i].Options) {
					return fmt.Errorf("質問 %d の選択肢 %d は範囲外です", i+1, oi)
				}
				labels = append(labels, inter.Questions[i].Options[oi].Label)
			}
			if len(labels) == 0 && strings.TrimSpace(a.Text) != "" {
				labels = []string{strings.TrimSpace(a.Text)}
			}
			if len(labels) == 0 {
				return fmt.Errorf("質問 %d に回答がありません", i+1)
			}
			answers[qids[i]] = map[string]any{"answers": labels}
		}
		if cl == nil || !cl.alive() {
			return errors.New("質問の要求元接続が失われています（再開で再配送されます）")
		}
		if err := cl.respond(reqID, map[string]any{"answers": answers}); err != nil {
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

// Snapshot (§6-3) reports the current position, for reconciliation.
func (h *threadHandle) Snapshot() (agents.ThreadSnapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return agents.ThreadSnapshot{
		TurnState:   h.state,
		Interaction: h.inter,
		Settings:    h.settings,
	}, nil
}

// queuedPrompts surfaces the driver-held queue for the mirror's queued badge.
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

// hasQuestion reports a pending Interaction (read by WireLive's question projection: for
// managed sessions the handle is more accurate and cheaper than a rollout tail probe).
func (h *threadHandle) hasQuestion() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.inter != nil
}

// PendingInteraction returns the marshaled []transcript.Question a managed codex
// session is currently blocked on, read straight from the live handle WITHOUT
// resuming the runtime (a notification-poll caller must stay cheap). ok=false when no
// live handle / no pending question. Used to enrich the codex-question notification
// with P2b option buttons (docs/log/37, managed option buttons). The bytes are the SAME shape
// bridge_answer fingerprints from Snapshot at answer time (identical []transcript.
// Question marshaled the same way), so the send-side fingerprint matches the
// answer-side one for an unchanged question.
func PendingInteraction(name string) (json.RawMessage, bool) {
	h := handleFor(name)
	if h == nil {
		return nil, false
	}
	h.mu.Lock()
	inter := h.inter
	h.mu.Unlock()
	if inter == nil || len(inter.Questions) == 0 {
		return nil, false
	}
	b, err := json.Marshal(inter.Questions)
	if err != nil {
		return nil, false
	}
	return b, true
}

// managedEnrich folds the driver-side state into the read layer's TranscriptData (called
// from readTranscript, same shape as opencode): it stamps the Interaction id onto the
// pending question, merges the driver-held queue into the queued list, and makes the mode
// chip update immediately.
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
	if modeSet != "" {
		td.Mode = modeSet // right after a switch the rollout is stale; the driver setting is the truth for the next turn
	}
	// A turn rejected at turn/start (errors.go — usage limit exhausted, in the field) never
	// creates a Turn, so nothing — not even the user's own prompt — reaches the rollout.
	// Without this, the mirror's optimistic echo has no real turn to reconcile against and
	// sits pending forever (pendingEcho.echoLanded requires a landed turn). Appending a
	// synthetic trailing turn gives both the echo (any post-cutoff error turn resolves it,
	// console/src/features/mirror/pendingEcho.ts) and the Console (ErrorBlock, same Kind
	// opencode's errors.go targets) something to show. Cleared by clearLastError the moment
	// the next turn starts (runTurn), so it never lingers past a successful retry.
	if ce := h.turnError(); ce != nil {
		idx := 0
		if n := len(td.Turns); n > 0 {
			idx = td.Turns[n-1].Idx + 1
		}
		td.Turns = append(td.Turns, transcript.Turn{
			Role: "assistant", Parts: []transcript.Part{ce.part()}, Text: ce.summary(),
			Idx: idx, TS: time.Now().Format(time.RFC3339),
		})
	}
}

// buildInput assembles the turn/start and turn/steer input items: prompt text plus
// attachments (§10.2-3: managed attaches through the API instead of pasting into tmux).
// Images become localImage items; other files have their paths written into the text,
// because mentioning a path is enough to trigger view_image / reading in codex, in the TUI
// too — the same treatment as the existing imagePaste route.
func buildInput(in agents.TurnInput) []map[string]any {
	var items []map[string]any
	text := strings.TrimSpace(in.Prompt)
	var nonImage []string
	for _, p := range in.Attachments {
		if strings.TrimSpace(p) == "" {
			continue
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
			items = append(items, map[string]any{"type": "localImage", "path": p})
		default:
			nonImage = append(nonImage, p)
		}
	}
	if len(nonImage) > 0 {
		if text != "" {
			text += "\n\n"
		}
		text += "添付ファイル:\n" + strings.Join(nonImage, "\n")
	}
	if text != "" {
		// Put the text first in input: "text → image" is the order the TUI's attachment
		// uses, not "image → text".
		items = append([]map[string]any{{"type": "text", "text": text}}, items...)
	}
	return items
}

// --- notification / server-request dispatch ------------------------------------

// userInputRequest is the wire params of item/tool/requestUserInput (measured, §12.3:
// questions[{id, header, question, isOther, options[{label, description}]}]).
type userInputRequest struct {
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
	ItemID    string `json:"itemId"`
	Questions []struct {
		ID       string `json:"id"`
		Header   string `json:"header"`
		Question string `json:"question"`
		IsOther  bool   `json:"isOther"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
}

// deliverUserInputRequest lands a server question on its handle (from appclient.go's
// handleServerRequest). ItemID becomes the Interaction id: it is stable across a
// redelivery on another connection (measured), and the Console's /respond answers with it.
func deliverUserInputRequest(c *appClient, reqID json.RawMessage, p userInputRequest) {
	h := handleByTid(p.ThreadID)
	if h == nil {
		log.Printf("codex managed: requestUserInput for unknown thread %s", p.ThreadID)
		return
	}
	inter := &agents.Interaction{ID: p.ItemID, Kind: "question"}
	var qids []string
	for _, q := range p.Questions {
		tq := transcript.Question{ID: p.ItemID, Question: q.Question, Header: q.Header}
		for _, o := range q.Options {
			tq.Options = append(tq.Options, transcript.Option{Label: o.Label, Description: o.Description})
		}
		inter.Questions = append(inter.Questions, tq)
		qids = append(qids, q.ID)
	}
	h.mu.Lock()
	h.inter = inter
	h.interQ = qids
	h.interReq = append(json.RawMessage(nil), reqID...)
	h.interCl = c
	h.state = agents.TurnWaitingInteraction
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "interaction", TurnState: agents.TurnWaitingInteraction, Interaction: inter})
}

// dispatchNotification routes a server notification to the owning handle (from
// appclient.go's readLoop). This is where the turn state machine (§4) is decided; the
// pump receives the terminal state through turnEnd.
func dispatchNotification(msg rpcMsg) {
	switch msg.Method {
	case "turn/started":
		var p struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(msg.Params, &p) != nil {
			return
		}
		if h := handleByTid(p.ThreadID); h != nil {
			h.mu.Lock()
			h.turnID = p.Turn.ID
			h.mu.Unlock()
			agents.MarkTurnStart(h.slotSid)
		}
	case "turn/completed":
		var p struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string          `json:"id"`
				Status string          `json:"status"`
				Error  json.RawMessage `json:"error"` // TurnError; populated only when Status=="failed"
			} `json:"turn"`
		}
		if json.Unmarshal(msg.Params, &p) != nil {
			return
		}
		if h := handleByTid(p.ThreadID); h != nil {
			st := agents.TurnCompleted
			failure := ""
			switch p.Turn.Status {
			case "failed":
				st = agents.TurnFailed
				if ce, ok := decodeCodexError(p.Turn.Error); ok {
					failure = ce.summary()
					h.setLastError(ce)
					if ce.retryable() {
						st = agents.TurnAborted
					}
				}
			case "interrupted":
				st = agents.TurnCancelled
			}
			// Do not end the running turn because a different turn completed (late
			// delivery, multiple connections). Reject only when both ids are known and
			// differ: a taken-over turn that never saw turn/started (h.turnID == "") still
			// counts as terminal.
			h.mu.Lock()
			mismatch := h.turnID != "" && p.Turn.ID != "" && h.turnID != p.Turn.ID
			h.mu.Unlock()
			if mismatch {
				return
			}
			agents.MarkTurnEndErr(h.slotSid, st, failure)
			h.mu.Lock()
			end := h.turnEnd
			if h.turnID == p.Turn.ID {
				h.turnID = ""
			}
			h.inter = nil
			pumpDriven := end != nil
			startQueued := false
			if !pumpDriven {
				h.running = false
				startQueued = len(h.queue) > 0 && !h.pumping && h.alive
				if startQueued {
					h.pumping = true
				}
			}
			h.mu.Unlock()
			if pumpDriven {
				select {
				case end <- st:
				default:
				}
			} else {
				// A completion no pump is waiting for, e.g. a turn taken over across an Agent restart.
				h.setState(st)
			}
			if startQueued {
				go h.pump()
			}
		}
	case "thread/settings/updated":
		var p struct {
			ThreadID       string `json:"threadId"`
			ThreadSettings struct {
				Model             string `json:"model"`
				Effort            string `json:"effort"`
				CollaborationMode struct {
					Mode string `json:"mode"`
				} `json:"collaborationMode"`
			} `json:"threadSettings"`
		}
		if json.Unmarshal(msg.Params, &p) != nil {
			return
		}
		if h := handleByTid(p.ThreadID); h != nil {
			h.mu.Lock()
			if p.ThreadSettings.Model != "" {
				h.curModel = p.ThreadSettings.Model
				h.settings.Model = p.ThreadSettings.Model
			}
			if p.ThreadSettings.Effort != "" {
				h.settings.Effort = p.ThreadSettings.Effort
			}
			if p.ThreadSettings.CollaborationMode.Mode != "" {
				h.settings.Mode = p.ThreadSettings.CollaborationMode.Mode
				if h.settings.Mode == "default" {
					h.settings.Mode = "normal"
				}
			}
			cur := h.settings
			h.mu.Unlock()
			h.emit(agents.Event{Kind: "settings", Settings: &cur})
		}
	case "serverRequest/resolved":
		// The question was resolved elsewhere (another connection, an automatic resolution).
		// The id is connection-scoped, so match by thread: close the pending Interaction and
		// go back to the turn running.
		var p struct {
			ThreadID string `json:"threadId"`
		}
		if json.Unmarshal(msg.Params, &p) != nil {
			return
		}
		if h := handleByTid(p.ThreadID); h != nil {
			h.mu.Lock()
			had := h.inter != nil
			h.inter = nil
			if had && h.running {
				h.state = agents.TurnRunning
			}
			h.mu.Unlock()
			if had {
				h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
			}
		}
	case "item/started", "item/completed":
		// Compaction detection (the same projection as the P1 observer; the writer
		// connection receives it too, for redundancy).
		var p struct {
			ThreadID string `json:"threadId"`
			Item     struct {
				Type string `json:"type"`
			} `json:"item"`
		}
		if json.Unmarshal(msg.Params, &p) == nil && p.ThreadID != "" && p.Item.Type == "contextCompaction" {
			SetCompacting(p.ThreadID, msg.Method == "item/started")
		}
	}
}
