package copilot

// The managed copilot driver (docs/log/36 Track A2) — a per-session child. Each session holds
// one `copilot --acp` (Agent Client Protocol, stdio JSON-RPC) child process, and session/new,
// session/load (cross-process resume, measured), session/prompt (blocking), session/cancel and
// session/set_mode are mapped onto the turn state machine (§4), Interaction (§5) and
// reconciliation (§6).
//
// Why a per-session child (the docs/log/36 contract): ACP has no per-session model selection
// (configOptions carries only mode/allow_all — measured), so pinning it with the --model /
// --effort flags of each child process is the only reliable path. Memory use matches a TUI
// pane, and the child's cmd.Wait() makes the exit/OOM record accurate per session.
//
// Permission requests (session/request_permission) never arrive while running with
// --allow-all, but a plan-mode launch drops allow-all, so they are reachable. Do not trust "it
// does not show in the UI, so it cannot happen": they are always mapped to an
// Interaction(question) and answered through the Console's question card (/respond).

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// ledger is the persistent ClientMessageID ledger (§9.5): it makes a resend or a reconnect's
// double submission idempotent across processes.
var ledger = agents.NewMsgLedger("copilot-msgledger")

// ACP session-mode ids (measured on v1.0.73), converted to and from the AF vocabulary
// "plan"/"normal".
const (
	acpModeAgent     = "https://agentclientprotocol.com/protocol/session-modes#agent"
	acpModePlan      = "https://agentclientprotocol.com/protocol/session-modes#plan"
	acpModeAutopilot = "https://agentclientprotocol.com/protocol/session-modes#autopilot"
)

func acpModeID(mode string) string {
	if mode == "plan" {
		return acpModePlan
	}
	return acpModeAgent
}

func modeFromACP(id string) string {
	switch id {
	case acpModePlan:
		return "plan"
	case "":
		return ""
	default:
		return "normal"
	}
}

// NewDriver returns the managed copilot Driver, which driverOf looks up from /turn and
// /respond. The read layer is preserved by embedding agentImpl as is.
func NewDriver() agents.Driver { return managedDriver{} }

type managedDriver struct{ agentImpl }

// Capabilities (§3.1, the docs/log/36 contract). Steer is a driver-held queue because ACP has
// no opening for mid-turn injection (the same semantics as opencode). DynamicModel/Effort are
// false: they are pinned by the child's launch flags, and changing them means re-creating the
// session. Mode is native, through session/set_mode.
func (managedDriver) Capabilities() agents.Capabilities {
	return agents.Capabilities{
		ProcessModel: "per-session-child",
		Steer:        true,
		DynamicMode:  true,
		Questions:    true,
	}
}

// Resume returns the session's ThreadHandle, spawning the child runtime and
// creating/loading the copilot session when needed (the Driver interface: start a new one when
// there is none). It doubles as the shared §6 reconciliation procedure.
func (managedDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	if m.Kind != session.KindCopilot {
		return nil, errors.New("copilot driver は copilot セッション専用です")
	}
	if !session.DirExists(m.Dir) {
		return nil, agents.DirGoneErr(m.Dir)
	}
	slotSid := session.UUID(m.Dir, m.Name) // identity: the working copy, never the subdir
	handlesMu.Lock()
	h := handles[m.Name]
	if h == nil {
		h = &threadHandle{
			name:    m.Name,
			dir:     m.CWD(), // Dir, or the subdir chosen at launch
			slotSid: slotSid,
			q:       agents.NewTurnQueue(m.Name, ledger, agents.LedgerAtTake),
			events:  make(chan agents.Event, 64),
		}
		handles[m.Name] = h
	}
	handlesMu.Unlock()

	// Serialize spawns per handle (the same shape as kiro A2-4): when boot's ReconcileManaged
	// and a /turn right after it Resume concurrently, the check-then-spawn is not serial, two
	// children are spawned and the earlier one is orphaned. Re-check liveness after taking the
	// lock.
	h.spawnMu.Lock()
	defer h.spawnMu.Unlock()

	h.mu.Lock()
	if h.alive && h.cl != nil && !h.cl.dead() {
		h.mu.Unlock()
		return h, nil
	}
	// Launch-time settings default to meta (a dynamic mode change is overwritten by
	// UpdateSettings).
	if h.settings.Model == "" {
		h.settings.Model = m.Model
	}
	if h.settings.Effort == "" {
		h.settings.Effort = m.Effort
	}
	if h.settings.Mode == "" {
		h.settings.Mode = m.Mode
	}
	// Whether to skip permission prompts (docs/log/76) is resolved from meta and ui-prefs on
	// every Resume. It is not carried on ThreadSettings because that is for dynamic updates
	// where "empty = leave unchanged", and a bool cannot be three-valued — a re-spawn after a
	// settings change picks up the value resolved here.
	h.bypass = agents.SkipPermissions(m)
	st := h.settings
	h.mu.Unlock()

	// First Resume of a forked slot (docs/log/55): mint this slot's session id and build its
	// session-state directory from the source before spawning, so the spawn below takes
	// the ordinary session/load path. Without this the slot has no sid, spawn falls to
	// session/new, and the branch quietly opens as an empty conversation.
	if m.ForkFrom != "" && sids.Read(slotSid) == "" {
		sid, err := newSessionID()
		if err != nil {
			return nil, fmt.Errorf("セッション ID を採番できません: %w", err)
		}
		if err := MaterializeForkAt(m.ForkFrom, sid, m.ForkAt); err != nil {
			return nil, fmt.Errorf("分岐を作成できませんでした: %w", err)
		}
		sids.Write(slotSid, sid)
	}

	if err := h.spawn(st); err != nil {
		return nil, err
	}

	// The baseline for exit recording (the same role as tui's startSessionTmux).
	base, _ := status.OOMKillCount()
	status.PersistExit(m.Name, status.ExitInfo{OOMBase: base})
	return h, nil
}

// LiveHandle returns the session's handle without starting anything (agents.LiveHandles): the
// /turn queue edits use it. A handle whose runtime died is still returned, because its queue
// survives for the respawn and can still be edited.
func (managedDriver) LiveHandle(m session.Meta) (agents.ThreadHandle, bool) {
	h := handleFor(m.Name)
	if h == nil {
		return nil, false
	}
	return h, true
}

// --- handle registry ---------------------------------------------------------

var handlesMu sync.Mutex
var handles = map[string]*threadHandle{}

func handleFor(name string) *threadHandle {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	return handles[name]
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

// DropHandle detaches a managed session from its runtime (stop/halt/archive):
// interrupt any running turn, terminate the child, forget the handle. The
// conversation stays in $COPILOT_HOME/session-state — a later Resume re-spawns
// and session/load reattaches (measured: history replay plus context retention).
func DropHandle(name string) {
	handlesMu.Lock()
	h := handles[name]
	delete(handles, name)
	handlesMu.Unlock()
	if h == nil {
		return
	}
	h.mu.Lock()
	h.alive = false
	h.q.DropAll()
	cmd, cl, sid, running := h.cmd, h.cl, h.sid, h.running
	h.mu.Unlock()
	if running && cl != nil && sid != "" {
		_ = cl.notifyPeer("session/cancel", map[string]any{"sessionId": sid})
	}
	stopChild(cmd)
}

// RemoveLedger drops the ClientMessageID ledger. Only for /stop, which discards the slot's
// identity along with it; halt/archive keep it because they can be resumed.
func RemoveLedger(name string) { ledger.Remove(name) }

// ManagedAlive reports whether the session has a live runtime handle.
func ManagedAlive(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.alive
}

// ManagedBusy reports a turn is running or queued (the wait condition of a graceful shutdown).
func ManagedBusy(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running || h.q.Len() > 0
}

// AbortManaged interrupts every running managed turn (the equivalent of a graceful shutdown's
// per-pane Ctrl-C).
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

// Shutdown terminates every managed child, at agent exit. The conversation of record stays in
// copilot's own store and the next boot's ReconcileManaged reconnects to it.
func Shutdown() {
	handlesMu.Lock()
	var cmds []*exec.Cmd
	for _, h := range handles {
		h.mu.Lock()
		h.alive = false
		cmds = append(cmds, h.cmd)
		h.mu.Unlock()
	}
	handlesMu.Unlock()
	for _, c := range cmds {
		stopChild(c)
	}
}

// ReconcileManaged re-attaches managed copilot sessions after an Agent boot or
// child death (§6). It covers every managed meta not counted as stopped. On failure the
// session simply stays stopped, and the user's Resume click retries it.
func ReconcileManaged(reason string) {
	d := managedDriver{}
	for _, m := range session.ListMetas() {
		if m.Kind != session.KindCopilot || m.DriverKind() != session.DriverManaged || m.Archived {
			continue
		}
		if m.StoppedAt != "" && handleFor(m.Name) == nil {
			continue // deliberately stopped — resume only on user action
		}
		if _, err := d.Resume(m); err != nil {
			log.Printf("copilot managed: reconcile %s (%s): %v", m.Name, reason, err)
		}
	}
}

// stopChild terminates a child process: SIGTERM (copilot gracefully records session.shutdown),
// then SIGKILL after a grace period. Reaping is done by the watch goroutine started at spawn
// (cmd.Wait) — every custom spawn path reaps its children (dev/04 §4.3).
func stopChild(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	p := cmd.Process
	if p.Signal(syscall.SIGTERM) != nil {
		return // already gone
	}
	time.AfterFunc(3*time.Second, func() { _ = p.Kill() })
}

// --- thread handle -----------------------------------------------------------

type threadHandle struct {
	name    string
	dir     string
	slotSid string

	spawnMu sync.Mutex // serializes spawns for this handle (no double spawn from a concurrent Resume; same shape as kiro A2-4)

	// bypass is the "skip permission prompts" choice (docs/log/76). Resume resolves it from meta
	// and puts it here because spawn has no meta of its own. plan is read from spawn's st.Mode
	// rather than at Resume time, since a mode change while running triggers a re-spawn.
	bypass bool

	mu      sync.Mutex
	cmd     *exec.Cmd
	cl      *acpClient
	sid     string // copilot session UUID
	alive   bool
	state   agents.TurnState
	running bool
	pumping bool
	q       *agents.TurnQueue // every method under mu (agents.TurnQueue's contract)
	// stopping marks the turn in flight as one a stop was aimed at, so it lands as cancelled.
	// The displayed state cannot carry this: accept overwrites it with queued.
	stopping bool
	// beforeCommit, when set, runs between the pump's Take and its Commit with mu released: the
	// window in which a stop or a removal can still cancel the taken entry. Tests only.
	beforeCommit func()
	settings     agents.ThreadSettings
	inter        *agents.Interaction
	permID       json.RawMessage // JSON-RPC id of the pending session/request_permission
	permOpts     []string        // Interaction choice index → ACP optionId
	events       chan agents.Event
}

// spawn starts the child runtime, initializes ACP and loads/creates the copilot
// session. Caller must NOT hold h.mu.
// bypassNow reports the resolved "skip permission prompts" choice (docs/log/76). Resume writes
// it under h.mu; spawn runs without the lock, so read it through here.
func (h *threadHandle) bypassNow() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.bypass
}

func (h *threadHandle) spawn(st agents.ThreadSettings) error {
	args := []string{"--acp", "--no-remote", "--no-remote-export"}
	if h.bypassNow() && st.Mode != "plan" {
		// The fleet default is bypass. It is dropped for a plan launch and when the user chose
		// permission prompts (docs/log/76), so approvals surface as an Interaction.
		args = append(args, "--allow-all")
	}
	concreteModel := st.Model != "" && st.Model != "auto"
	if concreteModel {
		args = append(args, "--model", st.Model)
	}
	// Auto (copilot's default / the only Free model) rejects --effort ("Model \"auto\"
	// does not support reasoning effort configuration") — only pass it with an explicit
	// non-auto model, else the child errors on startup.
	if st.Effort != "" && concreteModel {
		args = append(args, "--effort", st.Effort)
	}
	bin := os.Getenv("AGENT_COPILOT_BIN")
	if bin == "" {
		bin = "copilot"
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = h.dir
	env := append(os.Environ(), "COPILOT_AUTO_UPDATE=false")
	if tok := Token(); tok != "" {
		// Inject the gh transparent-auth token explicitly. The ambient fallback does work
		// (measured) but is undocumented, and a child process's env can be made deterministic,
		// so this is the path of record.
		env = append(env, "COPILOT_GITHUB_TOKEN="+tok)
	}
	// One child per session, and copilot hands its own environment to MCP children
	// (measured), so this is how the af server learns which session it serves.
	cmd.Env = agents.WithSessionName(env, h.name)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	tail, err := agents.StartWithStderrTail(cmd)
	if err != nil {
		return fmt.Errorf("copilot runtime を起動できません: %w", err)
	}
	defer tail.Settle() // after any failure snapshot; see StderrTail.Release
	// Closed by watch once the exit is recorded; a failed start waits on it (awaitExitRecord).
	exited := make(chan struct{})
	// Snapshot the tail before stopChild: the stop sequence can make the CLI print noise
	// that pushes the real cause out of the budget.
	fail := func(err error) error {
		err = tail.Wrap(err)
		stopChild(cmd)
		awaitExitRecord(exited)
		return err
	}
	cl := newACPClient(stdin, stdout)
	// Capture this cl in the closure: during the first spawn the readLoop can run while h.cl is
	// still unassigned, so reading h.cl would panic on a nil dereference when replying to an
	// unknown method.
	cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		h.onServerRequest(cl, id, method, params)
	}
	go h.watch(cmd, tail, cl, exited)

	if _, err := cl.call("initialize", map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{},
	}, 30*time.Second); err != nil {
		return fail(fmt.Errorf("copilot runtime の initialize に失敗しました: %w", err))
	}

	sid := h.sid
	if sid == "" {
		sid = sids.Read(h.slotSid)
	}
	mode := ""
	if sid != "" {
		// Cross-process resume (measured: history replay plus context retention). The replay is
		// proportional to the conversation's length, so allow plenty of time.
		res, err := cl.call("session/load", map[string]any{
			"sessionId": sid, "cwd": h.dir, "mcpServers": []any{},
		}, 180*time.Second)
		if err != nil {
			// Falling back to session/new is allowed only when sid's local store
			// (session-state/<sid>) is actually gone, i.e. the conversation was deleted (the
			// same shape as kiro A2-1). Doing it on a transient failure with the store intact
			// would silently detach a live conversation and overwrite its sid.
			if _, statErr := os.Stat(sessionStateDir(sid)); statErr != nil {
				log.Printf("copilot managed: session/load %s: store gone (%v) — restarting with a new session", h.name, err)
				sid = ""
			} else {
				return fail(fmt.Errorf("copilot セッションを読み込めませんでした（時間をおいて再開してください）: %w", err))
			}
		} else {
			mode = currentModeOf(res)
		}
	}
	if sid == "" {
		res, err := cl.call("session/new", map[string]any{
			"cwd": h.dir, "mcpServers": []any{},
		}, 60*time.Second)
		if err != nil {
			return fail(fmt.Errorf("copilot セッションを作成できません: %w", err))
		}
		var out struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(res, &out) != nil || out.SessionID == "" {
			return fail(errors.New("copilot セッションの作成応答を解釈できません"))
		}
		sid = out.SessionID
		sids.Write(h.slotSid, sid)
		mode = currentModeOf(res)
	}

	h.mu.Lock()
	h.cmd, h.cl, h.sid, h.alive = cmd, cl, sid, true
	h.state = agents.TurnCompleted // the child is newborn — no turn can be running
	h.inter, h.permID, h.permOpts = nil, nil, nil
	if m := modeFromACP(mode); m != "" {
		h.settings.Mode = m
	}
	wantMode := h.settings.Mode
	h.mu.Unlock()

	// Re-assert meta's wanted mode when it differs from the runtime's current mode, against a
	// fall back to the default after a resume (the same reasoning as codex's approvalPolicy
	// re-assertion; best effort).
	if wantMode != "" && wantMode != modeFromACP(mode) {
		_, _ = cl.call("session/set_mode", map[string]any{
			"sessionId": sid, "modeId": acpModeID(wantMode),
		}, 15*time.Second)
	}
	// Input a lost child left queued starts now, in order, rather than waiting for the next
	// accept to restart the pump.
	h.resumePump()
	return nil
}

// currentModeOf extracts modes.currentModeId from a session/new or session/load result.
func currentModeOf(res json.RawMessage) string {
	var out struct {
		Modes struct {
			CurrentModeID string `json:"currentModeId"`
		} `json:"modes"`
	}
	_ = json.Unmarshal(res, &out)
	return out.Modes.CurrentModeID
}

// exitRecordWait bounds how long a failed spawn waits for watch: longer than stopChild's
// SIGTERM → SIGKILL sequence (3 s), so a child that ignores SIGTERM is still counted.
const exitRecordWait = 5 * time.Second

// awaitExitRecord holds a failed spawn until watch has written the exit record. Without it the
// caller reports the failure before the record exists, and the write lands after the caller
// has moved on — under a test's TempDir HOME, while or after that tree is removed.
func awaitExitRecord(exited <-chan struct{}) {
	select {
	case <-exited:
	case <-time.After(exitRecordWait):
	}
}

// watch reaps the child and records its exit (record-exit for managed sessions; with a
// per-session child the attribution is exact, unlike a daemon supervisor's). An exit from
// SIGTERM (DropHandle/Shutdown) becomes "stopped" and the Console shows the ordinary stopped
// state.
func (h *threadHandle) watch(cmd *exec.Cmd, tail *agents.StderrTail, cl *acpClient, exited chan struct{}) {
	defer close(exited) // releases a failed spawn's awaitExitRecord
	err := cmd.Wait()
	tail.Release()
	_ = err
	code, sig := 0, 0
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			sig = int(ws.Signal())
			code = 128 + sig
		} else {
			code = ws.ExitStatus()
		}
	}
	oom := false
	base := uint64(0)
	if prev, ok := status.ReadExit(h.name); ok {
		base = prev.OOMBase
	}
	if cur, ok := status.OOMKillCount(); ok && cur > base {
		oom = true
	}
	status.PersistExit(h.name, status.ExitInfo{
		Reason: status.ExitReasonFor(code, sig, oom),
		Code:   code, Signal: sig,
		At:      time.Now().Format(time.RFC3339),
		OOMBase: base,
	})
	cl.markClosed()
	h.mu.Lock()
	stale := h.cl != cl // already replaced by a new child (an old watch after a respawn)
	h.mu.Unlock()
	if !stale {
		h.runtimeLost()
	}
}

// emit pushes an event without ever blocking a state transition (drop on
// overflow — events are advisory; the source of truth is Snapshot + events.jsonl).
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

// runtimeLost drops the handle to unknown (§6-1: the honest state on a disconnect).
func (h *threadHandle) runtimeLost() {
	h.mu.Lock()
	h.alive = false
	h.state = agents.TurnUnknown
	h.inter, h.permID, h.permOpts = nil, nil, nil
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnUnknown})
}

// --- ThreadHandle interface ---------------------------------------------------

func (h *threadHandle) Send(in agents.TurnInput) error {
	_, err := h.accept(in)
	return err
}

// SendQueued is Send reporting whether the input was held behind a running turn.
func (h *threadHandle) SendQueued(in agents.TurnInput) (bool, error) { return h.accept(in) }

// Steer is a driver-held queue: ACP has no opening for mid-turn injection, so the input is
// submitted as the next turn once the current one finishes (the same semantics as opencode).
func (h *threadHandle) Steer(in agents.TurnInput) error {
	_, err := h.accept(in)
	return err
}

// accept queues the input and starts the pump when none runs. queued reports that the input
// waits behind a running turn (or behind earlier queued input) rather than starting now.
func (h *threadHandle) accept(in agents.TurnInput) (queued bool, err error) {
	if strings.TrimSpace(in.Prompt) == "" {
		return false, errors.New("empty prompt")
	}
	in.ClientMessageID = normalizeMsgID(in.ClientMessageID)
	h.mu.Lock()
	if !h.alive {
		h.mu.Unlock()
		return false, errors.New("runtime が停止しています（再開してください）")
	}
	if h.inter != nil {
		h.mu.Unlock()
		return false, agents.ErrQuestionPending
	}
	// The queue records the ledger when the pump takes the entry (LedgerAtTake): recording it
	// here would make a resend after a crash that lost the queue count as "already seen" and be
	// discarded silently.
	if _, dup := h.q.Accept(in); dup {
		// A resend: it is queued or was taken already, so neither the state nor the pump has
		// anything to learn, and "queued" would tell a peer its message still waits.
		h.mu.Unlock()
		return false, nil
	}
	start := !h.pumping
	if start {
		h.pumping = true
	}
	queued = h.running || h.q.Len() > 1
	if queued {
		h.state = agents.TurnQueued
	}
	h.mu.Unlock()
	if start {
		go h.pump()
	}
	return queued, nil
}

// pump processes the queue serially (the child is exclusive, so no waitIdle is needed).
func (h *threadHandle) pump() {
	for {
		h.mu.Lock()
		// A closed client counts as gone before watch marks the handle: the failed turn wakes
		// this loop first, and the next input would be taken, recorded in the ledger and lost
		// on a dead pipe. So the check comes before Take, and the input stays queued for
		// resumePump after the respawn.
		if !h.alive || h.cl == nil || h.cl.dead() {
			h.pumping = false
			h.mu.Unlock()
			return
		}
		t := h.q.Take()
		if t == nil {
			h.pumping = false
			h.mu.Unlock()
			return
		}
		h.running = true
		h.mu.Unlock()

		h.runTurn(t)

		h.mu.Lock()
		h.q.Settle(t)
		h.running = false
		h.mu.Unlock()
	}
}

// resumePump restarts the pump for input that was queued when the child died: the pump exits
// on a dead child and leaves the queue in place.
func (h *threadHandle) resumePump() {
	h.mu.Lock()
	start := !h.pumping && h.alive && h.q.Len() > 0
	if start {
		h.pumping = true
	}
	h.mu.Unlock()
	if start {
		go h.pump()
	}
}

// runTurn executes ONE blocking session/prompt and lands the terminal state.
// The turn-boundary MarkTurnStart/End drive the status store and the docs/log/30 completion
// report (the notify seam).
func (h *threadHandle) runTurn(t *agents.Taken) {
	agents.MarkTurnStart(h.slotSid)
	defer func() { agents.MarkTurnEnd(h.slotSid, h.currentState()) }()
	h.setState(agents.TurnStarting)
	if h.beforeCommit != nil {
		h.beforeCommit()
	}
	h.mu.Lock()
	cl, sid := h.cl, h.sid
	if cl == nil || sid == "" {
		h.mu.Unlock()
		h.setState(agents.TurnFailed)
		return
	}
	// The last act under the lock before the runtime sees the input (ADR 0105 decision 3): a
	// stop or a removal that took the lock first has cancelled it, and it never starts.
	if !h.q.Commit(t) {
		h.mu.Unlock()
		h.setState(agents.TurnCancelled)
		return
	}
	h.stopping = false
	h.state = agents.TurnRunning
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
	written := false
	res, err := cl.callWritten("session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": t.In.Prompt}},
	}, 0, func() { written = true; h.received(t, cl, sid) }) // no timeout — a turn runs as long as it runs
	h.mu.Lock()
	interrupted := h.stopping
	h.inter, h.permID, h.permOpts = nil, nil, nil // the turn ended = nothing is waiting
	h.mu.Unlock()
	if err != nil && !written && cl.dead() {
		// The child died before it got the prompt: put it back for resumePump after the
		// respawn, unless a stop was aimed at it.
		h.mu.Lock()
		requeued := h.q.Requeue(t)
		h.mu.Unlock()
		if !requeued {
			interrupted = true
		}
	}
	if err != nil {
		if interrupted {
			h.setState(agents.TurnCancelled)
		} else {
			// A broken transport = the child is lost: drop honestly to unknown and leave it to §6.
			h.setState(agents.TurnUnknown)
		}
		return
	}
	var out struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(res, &out)
	switch {
	case interrupted || out.StopReason == "cancelled":
		h.setState(agents.TurnCancelled)
	case out.StopReason == "refusal":
		h.setState(agents.TurnFailed)
	default: // end_turn / max_tokens / …
		h.setState(agents.TurnCompleted)
	}
}

// received is the moment the runtime counts as holding t: session/prompt is on the child's
// stdin. A stop that found t committed but not yet written left its delivery to here. Whether
// a session/cancel written right after the prompt reaches the turn is unmeasured, so this stop
// is best effort (ADR 0105 decision 3).
func (h *threadHandle) received(t *agents.Taken, cl *acpClient, sid string) {
	h.mu.Lock()
	stop := h.q.Received(t)
	h.mu.Unlock()
	if stop {
		_ = h.deliverStop(cl, sid)
	}
}

// Interrupt is the Console's stop (ADR 0105 decisions 1-3). The queue decides what the stop
// is; this delivers it to the entry in flight.
func (h *threadHandle) Interrupt(opts agents.InterruptOpts) (agents.InterruptResult, error) {
	h.mu.Lock()
	// busy: every turn this driver runs comes from the queue, so with nothing taken nothing
	// runs. h.running alone also covers the pump's tail after a taken entry was cancelled or
	// removed, when the runtime has no turn.
	out := h.q.Interrupt(opts, h.q.Head() != nil)
	return out.Result, h.stopLocked(out.Head)
}

// stopLocked delivers a stop to the turn in flight per head, and releases h.mu. HeadNone needs
// nothing: every turn this driver runs has a taken entry, so with none there is no turn — only,
// at most, the pump's tail after a cancelled Commit, which must not be left interrupting.
func (h *threadHandle) stopLocked(head agents.HeadAction) error {
	cl, sid := h.cl, h.sid
	switch head {
	case agents.HeadCancelled:
		// Never sent. A taken entry's Commit fails and runTurn lands the same verdict; input the
		// pump had not taken yet has no runTurn at all, and accept left the state at queued.
		h.state = agents.TurnCancelled
		h.mu.Unlock()
		h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnCancelled})
		return nil
	case agents.HeadStopNow, agents.HeadStopPending:
	default:
		h.mu.Unlock()
		return nil
	}
	h.stopping = true
	h.state = agents.TurnInterrupting
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnInterrupting})
	if head == agents.HeadStopPending || cl == nil {
		return nil // HeadStopPending: received delivers it once the prompt is written
	}
	return h.deliverStop(cl, sid)
}

// deliverStop cancels the running turn. ACP requires a pending session/request_permission to
// be answered cancelled once the turn is cancelled; a runtime that waits for that answer keeps
// the turn interrupting and the card up. The stop is reachable under the card, so it cannot
// rely on the card's own Cancel.
func (h *threadHandle) deliverStop(cl *acpClient, sid string) error {
	h.mu.Lock()
	permID := h.permID
	if permID != nil {
		h.inter, h.permID, h.permOpts = nil, nil, nil
	}
	h.mu.Unlock()
	err := cl.notifyPeer("session/cancel", map[string]any{"sessionId": sid})
	if permID != nil {
		_ = cl.respond(permID, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
	}
	return err
}

// RemoveQueued takes one entry out while it is cancellable (decision 5).
func (h *threadHandle) RemoveQueued(id string) (agents.QueueItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.q.Remove(id)
}

// DismissDiscard drops a kept discard (decision 4).
func (h *threadHandle) DismissDiscard(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.q.DismissDiscard(id)
}

// interruptAll is the stop for Agent shutdown (decision 8): the whole queue goes, nothing is
// kept for return, and the turn in flight is stopped.
func (h *threadHandle) interruptAll() error {
	h.mu.Lock()
	h.q.DropAll()
	out := h.q.Interrupt(agents.InterruptOpts{}, h.q.Head() != nil)
	return h.stopLocked(out.Head)
}

// UpdateSettings applies dynamic settings. Mode is native, through session/set_mode
// (measured). Model/Effort are pinned by the child's launch flags and cannot change
// dynamically: Capabilities declares DynamicModel/Effort:false and the Console shows no UI for
// them, but an explicit error is returned defensively.
func (h *threadHandle) UpdateSettings(s agents.ThreadSettings) error {
	if s.Model != "" || s.ClearModel || s.Effort != "" || s.ClearEffort {
		return errors.New("copilot はモデル/effort の稼働中変更に未対応です（セッションを作り直してください）")
	}
	if s.Mode == "" {
		return nil
	}
	h.mu.Lock()
	cl, sid := h.cl, h.sid
	h.mu.Unlock()
	if cl == nil {
		return errors.New("runtime が停止しています")
	}
	if _, err := cl.call("session/set_mode", map[string]any{
		"sessionId": sid, "modeId": acpModeID(s.Mode),
	}, 15*time.Second); err != nil {
		return err
	}
	h.mu.Lock()
	h.settings.Mode = s.Mode
	cur := h.settings
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "settings", Settings: &cur})
	return nil
}

// Respond answers the pending Interaction (§5), which for copilot is a reply to
// session/request_permission. answer/allow converts the choice index into an ACP optionId,
// deny picks a reject-family optionId, and cancel sends outcome:"cancelled".
func (h *threadHandle) Respond(reply agents.InteractionReply) error {
	h.mu.Lock()
	inter, permID, permOpts, cl := h.inter, h.permID, h.permOpts, h.cl
	h.mu.Unlock()
	if inter == nil || inter.ID != reply.ID || cl == nil {
		return fmt.Errorf("interaction %s は待機中ではありません", reply.ID)
	}
	var result map[string]any
	switch reply.Decision {
	case agents.DecisionCancel:
		result = map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	case agents.DecisionDeny:
		opt := findOption(permOpts, "reject")
		if opt == "" {
			result = map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
		} else {
			result = map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": opt}}
		}
	case agents.DecisionAllow, agents.DecisionAnswer:
		opt := ""
		if len(reply.Answers) > 0 && len(reply.Answers[0].Options) > 0 {
			i := reply.Answers[0].Options[0]
			if i < 0 || i >= len(permOpts) {
				return fmt.Errorf("選択肢 %d は範囲外です", i)
			}
			opt = permOpts[i]
		} else {
			opt = findOption(permOpts, "allow")
		}
		if opt == "" {
			return errors.New("承認の選択肢を解決できません")
		}
		result = map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": opt}}
	default:
		return fmt.Errorf("unsupported decision: %s", reply.Decision)
	}
	if err := cl.respond(permID, result); err != nil {
		return err
	}
	h.mu.Lock()
	h.inter, h.permID, h.permOpts = nil, nil, nil
	running := h.running
	if running {
		h.state = agents.TurnRunning
	}
	h.mu.Unlock()
	if running {
		// Never push a false "running" to subscribers while no turn is running.
		h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
	}
	return nil
}

// findOption returns the first optionId containing the substring ("allow" / "reject"; the
// measured vocabulary is allow_once / allow_always / reject_once).
func findOption(opts []string, sub string) string {
	for _, o := range opts {
		if strings.Contains(o, sub) {
			return o
		}
	}
	return ""
}

func (h *threadHandle) Events() <-chan agents.Event { return h.events }

func (h *threadHandle) Snapshot() (agents.ThreadSnapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return agents.ThreadSnapshot{
		TurnState:   h.state,
		Interaction: h.inter,
		Settings:    h.settings,
	}, nil
}

// onServerRequest handles server-initiated requests on the readLoop goroutine —
// MUST NOT block: record the Interaction and return; the answer goes back later
// via Respond → cl.respond.
func (h *threadHandle) onServerRequest(cl *acpClient, id json.RawMessage, method string, params json.RawMessage) {
	if method != "session/request_permission" {
		// An unknown server-initiated request wedges the turn unless it is answered, so reply
		// with an error.
		_ = cl.write(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32601, "message": "unsupported request: " + method},
		})
		return
	}
	var req struct {
		ToolCall struct {
			ToolCallID string `json:"toolCallId"`
			Title      string `json:"title"`
			Kind       string `json:"kind"`
			RawInput   struct {
				Command string `json:"command"`
			} `json:"rawInput"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(params, &req) != nil {
		return
	}
	q := transcript.Question{
		Header:   "許可",
		Question: req.ToolCall.Title,
	}
	if req.ToolCall.RawInput.Command != "" {
		q.Question += "\n`" + req.ToolCall.RawInput.Command + "`"
	}
	var optIDs []string
	for _, o := range req.Options {
		q.Options = append(q.Options, transcript.Option{Label: o.Name})
		optIDs = append(optIDs, o.OptionID)
	}
	interID := req.ToolCall.ToolCallID
	if interID == "" {
		interID = "perm-" + strings.TrimSpace(string(id))
	}
	q.ID = interID
	inter := &agents.Interaction{ID: interID, Kind: "question", Prompt: req.ToolCall.Title,
		Questions: []transcript.Question{q}}
	h.mu.Lock()
	h.inter, h.permID, h.permOpts = inter, id, optIDs
	h.state = agents.TurnWaitingInteraction
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "interaction", TurnState: agents.TurnWaitingInteraction, Interaction: inter})
}

// queuedPrompts surfaces the driver-held queue for the mirror's queued badge.
func (h *threadHandle) queuedPrompts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.q.Texts()
}

// queueView is what the messages payload shows of the queue: its items (decision 5) and the
// kept discards (decision 4).
func (h *threadHandle) queueView() ([]agents.QueueItem, []agents.Discard) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.q.Items(), h.q.Discards()
}

// managedEnrich folds the driver-side state into the read layer's TranscriptData (called from
// transcript.go): it puts the Interaction id on a pending permission, merges the driver-held
// queue into the queued list and the driver's mode setting into the chip. It does nothing for a
// tui session, which has no handle.
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
	td.QueuedItems, td.Discards = h.queueView()
	if modeSet != "" {
		td.Mode = modeSet
	}
}

// normalizeMsgID mirrors the other drivers' convention: empty → the driver mints one.
func normalizeMsgID(id string) string {
	if id != "" {
		return id
	}
	b, err := newSessionID()
	if err != nil {
		return fmt.Sprintf("af-%d", time.Now().UnixNano())
	}
	return "af-" + b
}
