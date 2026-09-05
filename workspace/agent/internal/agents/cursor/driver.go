package cursor

// The managed cursor driver (docs/log/40 Track A2), one child process per session: each
// session holds a `cursor-agent acp` child and maps session/new, session/load
// (cross-process resume, measured), session/prompt (blocking), session/cancel and
// session/set_mode onto the turn state machine, Interaction and reconciliation. Same
// skeleton as copilot's driver.go (docs/log/36), with a single cursor-specific difference:
//
//   cursor's ACP writes no local trace at all — neither the JSONL transcript the TUI/-p
//   path writes nor hooks appear on the ACP path, because the history lives on the server
//   (docs/log/40 §probe). copilot writes events.jsonl on every path, so a managed
//   transcript could still be read from a file; cursor has nothing to read. The driver
//   therefore builds the transcript in memory from `session/update` notifications
//   (agent_message_chunk / agent_thought_chunk / tool_call / tool_call_update) and
//   restores it from session/load's full replay (replayed from user_message_chunk,
//   measured). managedTranscript() feeds the read layer (transcript.go). A stopped managed
//   session has no handle and hence no transcript — resume rebuilds it from the
//   session/load replay, which is the consequence of having no local source of truth.
//
// Why one child per session: ACP has no per-session model selection (the models in a
// session/new response are an enumeration only), so pinning it with a per-child `--model`
// flag is the reliable route. Permission requests (session/request_permission) do not
// happen under --force (measured: echo ran without a confirmation), but plan mode drops
// --force and can therefore reach them. Never trust "it does not appear in the UI, so it
// cannot happen" (the agy df996e4 lesson): always map it to an Interaction(question).

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

// ledger is the persistent ClientMessageID ledger: it makes a re-send or a reconnect
// idempotent across processes.
var ledger = agents.NewMsgLedger("cursor-msgledger")

// acpModeID converts the AF vocabulary ("plan" / "normal") to an ACP mode id. Those ids are
// the bare "agent" / "plan" / "ask" (measured — unlike copilot's URL form).
func acpModeID(mode string) string {
	if mode == "plan" {
		return "plan"
	}
	return "agent"
}

func modeFromACP(id string) string {
	switch id {
	case "plan":
		return "plan"
	case "":
		return ""
	default: // agent / ask
		return "normal"
	}
}

// NewDriver returns the managed cursor Driver, which driverOf looks up from /turn and
// /respond. The read layer is preserved by embedding agentImpl as is.
func NewDriver() agents.Driver { return managedDriver{} }

type managedDriver struct{ agentImpl }

// Capabilities. Steer is a driver-side queue: ACP has no mid-turn injection point (same
// semantics as copilot/opencode). DynamicModel is false because the model is pinned by the
// child's launch flag, so changing it means recreating the session. Mode is native through
// session/set_mode (measured: a {} response plus a current_mode_update notification).
func (managedDriver) Capabilities() agents.Capabilities {
	return agents.Capabilities{
		ProcessModel: "per-session-child",
		Steer:        true,
		DynamicMode:  true,
		Questions:    true,
	}
}

// Resume returns the session's ThreadHandle, spawning the child runtime and
// creating/loading the cursor session when needed (Driver interface: start a new one if
// there is none). This doubles as the shared procedure for reconciliation.
func (managedDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	if m.Kind != session.KindCursor {
		return nil, errors.New("cursor driver は cursor セッション専用です")
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
			events:  make(chan agents.Event, 64),
		}
		handles[m.Name] = h
	}
	handlesMu.Unlock()

	// Serialize spawns per handle (same shape as kiro A2-4): if boot's ReconcileManaged and
	// a /turn right after it call Resume concurrently, check-then-spawn interleaves, two
	// children are spawned and the earlier one is orphaned. Re-check liveness after taking
	// the lock.
	h.spawnMu.Lock()
	defer h.spawnMu.Unlock()

	h.mu.Lock()
	if h.alive && h.cl != nil && !h.cl.dead() {
		h.mu.Unlock()
		return h, nil
	}
	if h.settings.Model == "" {
		h.settings.Model = m.Model
	}
	if h.settings.Mode == "" {
		h.settings.Mode = m.Mode
	}
	// Whether to skip permission prompts (docs/log/76) is resolved from meta and ui-prefs on
	// every Resume. It does not live in ThreadSettings because that struct is a dynamic
	// update where "empty = leave unchanged", which cannot express three states for a bool.
	// A re-spawn after a settings change therefore uses the value resolved here.
	h.bypass = agents.SkipPermissions(m)
	st := h.settings
	h.mu.Unlock()

	if err := h.spawn(st); err != nil {
		return nil, err
	}

	// Baseline for exit recording (the same role as the TUI's startSessionTmux).
	base, _ := status.OOMKillCount()
	status.PersistExit(m.Name, status.ExitInfo{OOMBase: base})
	return h, nil
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
// conversation stays on cursor's server — a later Resume re-spawns and
// session/load reattaches (measured: the history is replayed and the context kept).
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
	h.queue = nil
	cmd, cl, sid, running := h.cmd, h.cl, h.sid, h.running
	h.mu.Unlock()
	if running && cl != nil && sid != "" {
		_ = cl.notifyPeer("session/cancel", map[string]any{"sessionId": sid})
	}
	stopChild(cmd)
}

// RemoveLedger drops the ClientMessageID ledger. Only for /stop, which discards the slot's
// identity as well; halt/archive keep it because those sessions can resume.
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

// ManagedBusy reports a turn is running or queued (the wait condition for a graceful
// shutdown).
func ManagedBusy(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running || len(h.queue) > 0
}

// AbortManaged interrupts every running managed turn (the equivalent of graceful
// shutdown's per-pane Ctrl-C).
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

// Shutdown terminates every managed child when the agent exits. The source of truth for
// the conversation stays on cursor's server, and the next boot's ReconcileManaged
// reattaches through session/load.
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

// ReconcileManaged re-attaches managed cursor sessions after an Agent boot or child
// death. It covers every managed meta that is not marked as stopped. On failure the
// session simply stays stopped and the user's Resume click retries it.
func ReconcileManaged(reason string) {
	d := managedDriver{}
	for _, m := range session.ListMetas() {
		if m.Kind != session.KindCursor || m.DriverKind() != session.DriverManaged || m.Archived {
			continue
		}
		if m.StoppedAt != "" && handleFor(m.Name) == nil {
			continue // deliberately stopped — resume only on user action
		}
		if _, err := d.Resume(m); err != nil {
			log.Printf("cursor managed: reconcile %s (%s): %v", m.Name, reason, err)
		}
	}
}

// stopChild terminates a child process group: SIGTERM, then SIGKILL after a grace period.
// cursor leaves a resident worker-server process behind after a turn (measured —
// docs/log/40), so killing the process GROUP is the point (spawn puts the child in its own
// group with Setpgid). Reaping is done by the watch goroutine started at spawn (cmd.Wait).
func stopChild(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	// To the whole process group (-pid), so no worker-server is left behind.
	if syscall.Kill(-pid, syscall.SIGTERM) != nil {
		return // already gone
	}
	time.AfterFunc(3*time.Second, func() {
		// Once the child has been reaped the pid (and the group) can be reused, so check the
		// process is still alive before firing a raw -pid SIGKILL at an unrelated process.
		// Signal on an already-waited Process returns ErrProcessDone, so this is race-safe.
		if cmd.Process.Signal(syscall.Signal(0)) == nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
}

// --- thread handle -----------------------------------------------------------

type threadHandle struct {
	name    string
	dir     string
	slotSid string

	spawnMu sync.Mutex // serializes spawns for this handle (no double spawn from a concurrent Resume; kiro A2-4)

	// bypass is the resolved "skip permission prompts" choice (docs/log/76). Resume resolves
	// it from meta and stores it here because spawn has no meta of its own. Plan mode is not
	// read at Resume time but from spawn's st.Mode, since a mode change while running
	// re-spawns the child.
	bypass bool

	mu       sync.Mutex
	cmd      *exec.Cmd
	cl       *acpClient
	sid      string // cursor session UUID
	model    string // ACP currentModelId (for the model badge; Auto comes back as default[])
	alive    bool
	state    agents.TurnState
	running  bool
	pumping  bool
	queue    []agents.TurnInput
	settings agents.ThreadSettings
	inter    *agents.Interaction
	permID   json.RawMessage // JSON-RPC id of the pending session/request_permission
	permOpts []string        // Interaction option index -> ACP optionId
	events   chan agents.Event

	buf transcriptBuf // transcript built from ACP session/update (guarded by its own lock)
}

// bypassNow reports the resolved "skip permission prompts" choice (docs/log/76). Resume writes
// it under h.mu; spawn runs without the lock, so read it through here.
func (h *threadHandle) bypassNow() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.bypass
}

// spawn starts the child runtime, initializes ACP and loads/creates the cursor session.
// Caller must NOT hold h.mu.
func (h *threadHandle) spawn(st agents.ThreadSettings) error {
	// Base args: suppress the background self-update (a root option, so it must come before
	// acp — measured), the acp subcommand, and skipping workspace trust (measured: without
	// --trust even ACP hangs on the trust prompt). Plan mode drops --force so approvals
	// surface as Interactions.
	args := []string{disableAutoUpdateFlag, "acp", "--trust"}
	if h.bypassNow() && st.Mode != "plan" {
		args = append(args, "--force") // the fleet default bypass ("unless explicitly denied")
	}
	if st.Model != "" && st.Model != "auto" {
		// Pin the model for this per-session child: a launch flag, because ACP has no
		// per-session way to select one.
		args = append(args, "--model", st.Model)
	}
	cmd := exec.Command(bin(), args...)
	cmd.Dir = h.dir
	// Own process group, so a left-over worker-server can be killed along with the group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Authentication is ambient: the CLI picks up ~/.config/cursor/auth.json itself
	// (measured: a turn completes with no env injection). Only CI is stripped (ci_env.go).
	cmd.Env = EnvWithoutCI(os.Environ())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cursor runtime を起動できません: %w", err)
	}
	cl := newACPClient(stdin, stdout)
	// Capture this cl in the closure: during the first spawn readLoop can already run while
	// h.cl is still unassigned, so going through h.cl would panic on a nil dereference when
	// answering an unknown method.
	cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		h.onServerRequest(cl, id, method, params)
	}
	cl.onNotify = h.onNotify
	go h.watch(cmd, cl)

	if _, err := cl.call("initialize", map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{},
	}, 30*time.Second); err != nil {
		stopChild(cmd)
		return fmt.Errorf("cursor runtime の initialize に失敗しました: %w", err)
	}

	sid := h.sid
	if sid == "" {
		sid = sids.Read(h.slotSid)
	}
	mode := ""
	modelID := ""
	if sid != "" {
		// Cross-process resume (measured: the session/update replay restores both history and
		// context). The replay notifications rebuild buf, so empty it first or the content is
		// counted twice.
		h.buf.reset()
		h.buf.setLoading(true)
		res, err := cl.call("session/load", map[string]any{
			"sessionId": sid, "cwd": h.dir, "mcpServers": []any{},
		}, 180*time.Second)
		h.buf.setLoading(false) // flush the last assistant turn
		if err != nil {
			// Same reasoning as kiro A2-1: falling back to session/new on a transient failure
			// (timeout, lock contention, ...) silently detaches a live conversation and
			// overwrites the slot's sid with an empty new one. cursor has no local store, so
			// "the conversation really is gone" cannot be decided here; return a retryable
			// error and leave the session stopped instead.
			h.buf.reset()
			stopChild(cmd)
			return fmt.Errorf("cursor セッションを読み込めませんでした（時間をおいて再開してください）: %w", err)
		} else {
			mode = currentModeOf(res)
			modelID = currentModelOf(res)
		}
	}
	if sid == "" {
		res, err := cl.call("session/new", map[string]any{
			"cwd": h.dir, "mcpServers": []any{},
		}, 60*time.Second)
		if err != nil {
			stopChild(cmd)
			return fmt.Errorf("cursor セッションを作成できません: %w", err)
		}
		var out struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(res, &out) != nil || out.SessionID == "" {
			stopChild(cmd)
			return errors.New("cursor セッションの作成応答を解釈できません")
		}
		sid = out.SessionID
		sids.Write(h.slotSid, sid)
		mode = currentModeOf(res)
		modelID = currentModelOf(res)
	}

	h.mu.Lock()
	h.cmd, h.cl, h.sid, h.alive = cmd, cl, sid, true
	h.state = agents.TurnCompleted // the child is brand new, so no turn is running
	h.model = modelID              // ACP currentModelId (Auto is default[]) for the model badge
	h.inter, h.permID, h.permOpts = nil, nil, nil
	if m := modeFromACP(mode); m != "" {
		h.settings.Mode = m
	}
	wantMode := h.settings.Mode
	h.mu.Unlock()

	// Re-assert the mode when meta's wanted mode differs from the runtime's current one,
	// which guards against falling back to the default after a resume (best-effort).
	if wantMode != "" && wantMode != modeFromACP(mode) {
		_, _ = cl.call("session/set_mode", map[string]any{
			"sessionId": sid, "modeId": acpModeID(wantMode),
		}, 15*time.Second)
	}
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

// currentModelOf extracts models.currentModelId from a session/new or session/load result.
// Auto comes back as `default[]` and an explicit choice in bracket form (measured;
// docs/log/40 §model display).
func currentModelOf(res json.RawMessage) string {
	var out struct {
		Models struct {
			CurrentModelID string `json:"currentModelId"`
		} `json:"models"`
	}
	_ = json.Unmarshal(res, &out)
	return out.Models.CurrentModelID
}

// watch reaps the child and records its exit. With one child per session the exit can be
// attributed exactly, unlike a daemon supervisor. An exit caused by SIGTERM
// (DropHandle/Shutdown) becomes "stopped" and the Console shows the ordinary stopped state.
func (h *threadHandle) watch(cmd *exec.Cmd, cl *acpClient) {
	_ = cmd.Wait()
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
	stale := h.cl != cl // already replaced by a newer child: this is an old watch after a respawn
	h.mu.Unlock()
	if !stale {
		h.runtimeLost()
	}
}

// emit pushes an event without ever blocking a state transition (drop on overflow).
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

// runtimeLost drops the handle to unknown, the honest state once the child is gone.
func (h *threadHandle) runtimeLost() {
	h.mu.Lock()
	h.alive = false
	h.state = agents.TurnUnknown
	h.inter, h.permID, h.permOpts = nil, nil, nil
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnUnknown})
}

// --- ThreadHandle interface ---------------------------------------------------

func (h *threadHandle) Send(in agents.TurnInput) error { return h.accept(in) }

// Steer is a driver-side queue: ACP has no mid-turn injection point, so the input is
// submitted as the next turn once the current one finishes.
func (h *threadHandle) Steer(in agents.TurnInput) error { return h.accept(in) }

func (h *threadHandle) accept(in agents.TurnInput) error {
	if strings.TrimSpace(in.Prompt) == "" {
		return errors.New("empty prompt")
	}
	in.ClientMessageID = normalizeMsgID(in.ClientMessageID)
	h.mu.Lock()
	if !h.alive {
		h.mu.Unlock()
		return errors.New("runtime が停止しています（再開してください）")
	}
	if h.inter != nil {
		h.mu.Unlock()
		return agents.ErrQuestionPending
	}
	// The ledger makes a re-send idempotent when pump starts executing it, not here:
	// recording it persistently before it is queued would make a re-send after a crash that
	// lost the queue look "already seen" and be dropped silently.
	h.queue = append(h.queue, in)
	start := !h.pumping
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

// pump processes the queue serially. The child is exclusive, so no waitIdle is needed.
func (h *threadHandle) pump() {
	for {
		h.mu.Lock()
		if len(h.queue) == 0 || !h.alive {
			h.pumping = false
			h.mu.Unlock()
			return
		}
		in := h.queue[0]
		h.queue = h.queue[1:]
		if ledger.SeenOrRecord(h.name, in.ClientMessageID) {
			h.mu.Unlock()
			continue // re-send: the persistent, cross-process ledger dedupes it at execution time
		}
		h.running = true
		h.mu.Unlock()

		h.runTurn(in)

		h.mu.Lock()
		h.running = false
		h.mu.Unlock()
	}
}

// runTurn executes ONE blocking session/prompt and lands the terminal state.
// MarkTurnStart/End at the turn boundary drive the status store and the completion report
// of docs/log/30.
func (h *threadHandle) runTurn(in agents.TurnInput) {
	agents.MarkTurnStart(h.slotSid)
	defer func() { agents.MarkTurnEnd(h.slotSid, h.currentState()) }()
	h.setState(agents.TurnStarting)
	h.mu.Lock()
	cl, sid := h.cl, h.sid
	h.mu.Unlock()
	if cl == nil || sid == "" {
		h.setState(agents.TurnFailed)
		return
	}
	// ACP emits no user_message_chunk during a live turn (measured), so the user turn is
	// committed to the transcript here — a separate path from the replay's
	// user_message_chunk.
	h.buf.addUserTurn(in.Prompt)
	h.setState(agents.TurnRunning)
	res, err := cl.call("session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": in.Prompt}},
	}, 0) // no timeout — a turn runs as long as it runs
	h.buf.flushAsst() // close the open assistant turn: ACP has no turn_ended notification
	h.mu.Lock()
	interrupted := h.state == agents.TurnInterrupting
	h.inter, h.permID, h.permOpts = nil, nil, nil // the turn ended, so nothing is pending
	h.mu.Unlock()
	if err != nil {
		if interrupted {
			h.setState(agents.TurnCancelled)
		} else {
			// A broken transport means the child is gone: fall back to unknown honestly.
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

// Interrupt cancels the running turn and clears the queued follow-ups.
func (h *threadHandle) Interrupt() error {
	h.mu.Lock()
	cl, sid := h.cl, h.sid
	running := h.running
	h.queue = nil
	if running {
		h.state = agents.TurnInterrupting
	}
	h.mu.Unlock()
	if !running || cl == nil {
		return nil
	}
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnInterrupting})
	return cl.notifyPeer("session/cancel", map[string]any{"sessionId": sid})
}

// UpdateSettings applies dynamic settings. Mode is native through session/set_mode
// (measured). Model is pinned by the child's launch flag and cannot be changed while
// running: Capabilities declares DynamicModel:false and the Console shows no UI for it, but
// an explicit error is returned defensively.
func (h *threadHandle) UpdateSettings(s agents.ThreadSettings) error {
	if s.Model != "" || s.ClearModel || s.Effort != "" || s.ClearEffort {
		return errors.New("cursor はモデルの稼働中変更に未対応です（セッションを作り直してください）")
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

// Respond answers the pending Interaction, which for cursor is a reply to
// session/request_permission: answer/allow maps the option index to an ACP optionId, deny
// picks a reject option and cancel sends outcome:"cancelled".
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

// findOption returns the first optionId containing the substring ("allow" / "reject" —
// the ACP vocabulary is allow_once / allow_always / reject_once).
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

// onNotify accumulates the transcript from session/update on the readLoop goroutine.
// MUST be fast (no RPC, no h.mu) — it only appends to the transcript buffer (tmu)
// and, for current_mode_update, records the mode under h.mu.
func (h *threadHandle) onNotify(method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}
	var p struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Text string `json:"text"`
			} `json:"content"`
			// tool_call / tool_call_update
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Status     string          `json:"status"`
			RawInput   json.RawMessage `json:"rawInput"`
			RawOutput  json.RawMessage `json:"rawOutput"`
			// ACP classifies a tool call itself: read | edit | delete | move | search |
			// execute | think | fetch | other, with the files it touches in `locations`.
			// That is what the changed-files list (docs/log/68) keys off here — the protocol's
			// own vocabulary, rather than the CLI's tool NAMES which are free to change.
			Kind      string `json:"kind"`
			Locations []struct {
				Path string `json:"path"`
			} `json:"locations"`
			// current_mode_update
			CurrentModeID string `json:"currentModeId"`
			// available_commands_update: the skills/commands the CLI advertises (builtin
			// skills plus global plus project). Measured: {name:"simplify",
			// description:"…(global)"} shape, with no leading slash.
			AvailableCommands []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"availableCommands"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	u := p.Update
	switch u.SessionUpdate {
	case "available_commands_update":
		// Publish to the skill picker (docs/log/50 v2). One map store, so readLoop is not
		// blocked.
		cmds := make([]agents.AdvertisedCommand, 0, len(u.AvailableCommands))
		for _, c := range u.AvailableCommands {
			cmds = append(cmds, agents.AdvertisedCommand{
				Name:        strings.TrimPrefix(c.Name, "/"),
				Description: c.Description,
			})
		}
		agents.PublishCommands(h.name, cmds)
	case "user_message_chunk":
		h.buf.userChunk(u.Content.Text)
	case "agent_message_chunk":
		h.buf.agentChunk(u.Content.Text)
	case "agent_thought_chunk":
		h.buf.thoughtChunk(u.Content.Text)
	case "tool_call":
		file, verb, edits := "", "", []transcript.Edit(nil)
		switch u.Kind {
		case "edit", "move":
			verb = "edit"
		case "delete":
			verb = "delete"
		}
		if verb != "" {
			// `move` reports its DESTINATION last (source first), and for an edit there is
			// normally exactly one location; either way the last one is where the content
			// lives now, which is what a reader wants to open.
			if n := len(u.Locations); n > 0 {
				file = u.Locations[n-1].Path
			}
			// rawInput still carries the before/after, so the +/- counters work here too.
			// Read by SHAPE, not by name: `title` is a display string ("Write /tmp/x"),
			// and turning it back into a tool name would be exactly the string contract
			// the protocol's `kind` lets us avoid.
			f, es := editsFromInput(u.RawInput)
			if file == "" {
				file = f
			}
			edits = es
		}
		h.buf.toolCall(u.ToolCallID, u.Title, toolInfo(u.RawInput), file, verb, edits)
	case "tool_call_update":
		if out := toolOutput(u.RawOutput); out != "" {
			h.buf.toolOutput(u.ToolCallID, out)
		}
	case "current_mode_update":
		if m := modeFromACP(u.CurrentModeID); m != "" {
			h.mu.Lock()
			h.settings.Mode = m
			cur := h.settings
			h.mu.Unlock()
			h.emit(agents.Event{Kind: "settings", Settings: &cur})
		}
	}
}

// toolInfo extracts a short label from a tool_call rawInput (command carries the most
// information).
func toolInfo(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var in struct {
		Command     string `json:"command"`
		Description string `json:"description"`
		Path        string `json:"path"`
		FilePath    string `json:"file_path"`
		TargetFile  string `json:"target_file"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	for _, s := range []string{in.Command, in.Description, in.Path, in.FilePath, in.TargetFile} {
		if s != "" {
			return s
		}
	}
	return ""
}

// toolOutput renders a tool_call_update rawOutput (a shell's exitCode/stdout/stderr,
// measured).
func toolOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var out struct {
		ExitCode *int   `json:"exitCode"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	s := out.Stdout
	if out.Stderr != "" {
		if s != "" {
			s += "\n"
		}
		s += out.Stderr
	}
	if s == "" && out.ExitCode != nil && *out.ExitCode != 0 {
		s = fmt.Sprintf("(exit %d)", *out.ExitCode)
	}
	return s
}

// onServerRequest handles server-initiated requests on the readLoop goroutine —
// MUST NOT block: record the Interaction and return; the answer goes back later via
// Respond -> cl.respond. It does not happen under --force, but plan mode can reach it.
func (h *threadHandle) onServerRequest(cl *acpClient, id json.RawMessage, method string, params json.RawMessage) {
	if method != "session/request_permission" {
		// An unknown server-initiated request wedges the turn if it goes unanswered, so
		// return an error.
		_ = cl.write(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32601, "message": "unsupported request: " + method},
		})
		return
	}
	var req struct {
		ToolCall struct {
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Kind       string          `json:"kind"`
			RawInput   json.RawMessage `json:"rawInput"`
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
	q := transcript.Question{Header: "許可", Question: req.ToolCall.Title}
	if cmd := toolInfo(req.ToolCall.RawInput); cmd != "" {
		q.Question += "\n`" + cmd + "`"
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

// pendingPermission folds a pending ACP `session/request_permission` into one line saying
// what was being asked (docs/log/75 P5). Empty when nothing is pending.
func (h *threadHandle) pendingPermission() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inter == nil {
		return ""
	}
	if len(h.inter.Questions) > 0 && strings.TrimSpace(h.inter.Questions[0].Question) != "" {
		return h.inter.Questions[0].Question
	}
	return h.inter.Prompt
}

// managedLiveState is the live state of the managed route, read by state.go's LiveState.
//
// Why it exists: ACP writes no local transcript, so the JSONL tail classification always
// returns empty for managed sessions, leaving both the list chip and the reaper's
// classification input blank. A managed session stuck waiting for an approval then fell to
// "unknown" (in docs/log/75's vocabulary), where tier 1 never folds it and tier 2 never
// keeps it awake. The turn state machine is the only source of truth, so it feeds this.
//
// Waiting for an approval is called question to match the vocabulary of the permission card
// the mirror draws (td.Pending): when the badge in the list and the chip in the body
// disagree, the user cannot tell which is true (the same lesson as EffectiveModal). The
// carry-over Kind being permission is a different axis, deciding what can be delivered
// after a resume.
func managedLiveState(m session.Meta) string {
	h := handleFor(m.Name)
	if h == nil {
		return "" // stopped or not attached: hold no opinion about the state
	}
	switch h.currentState() {
	case agents.TurnWaitingInteraction:
		return "question"
	case agents.TurnQueued, agents.TurnStarting, agents.TurnRunning, agents.TurnInterrupting:
		return "working"
	}
	return "idle"
}

// queuedPrompts surfaces the driver-held queue for the mirror's "queued" badge.
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

// managedTranscript builds the read-layer TranscriptData for a managed cursor session
// entirely from the driver's in-memory accumulator; it is called from transcript.go. ACP
// writes no local transcript, so this is the only source the mirror has. A stopped session
// (no handle) is empty.
func managedTranscript(m session.Meta) agents.TranscriptData {
	h := handleFor(m.Name)
	if h == nil {
		return agents.TranscriptData{}
	}
	td := agents.TranscriptData{Turns: h.buf.snapshot()}
	h.mu.Lock()
	inter := h.inter
	modeSet := h.settings.Mode
	// Model badge: prefer the model the user picked explicitly (settings.Model, the picker's
	// dash form); for Auto or no choice fall back to ACP's currentModelId, which is
	// default[] (docs/log/40 §model display).
	modelID := h.settings.Model
	if modelID == "" || modelID == "auto" {
		modelID = h.model
	}
	h.mu.Unlock()
	stampModel(td.Turns, displayModel(modelID))
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
		td.Mode = modeSet
	}
	return td
}

// normalizeMsgID mirrors the other drivers' convention: empty means the driver assigns one.
func normalizeMsgID(id string) string {
	if id != "" {
		return id
	}
	if b, err := newChatID(); err == nil {
		return "af-" + b
	}
	return fmt.Sprintf("af-%d", time.Now().UnixNano())
}
