package kiro

// Managed driver for kiro (docs/log/43 Track A2) — one child process per session. Each
// session holds a `kiro-cli acp` child and maps session/new, session/load (cross-process
// resume, measured), session/prompt (blocking) and session/cancel onto the turn state
// machine, Interaction and reconciliation. The turn machine itself is the shared acp.Handle (as
// for cursor and copilot, docs/log/40, 36); this file holds the registry, kiro's launch flags and
// the two kiro-specific differences:
//
//  1. Cross-process exclusion through `.lock` (cursor has none). A kiro session is owned by a
//     single process via `~/.kiro/sessions/cli/<sid>.lock` (which holds its pid), and
//     session/load is refused with "Session is active in another process (PID …)" while the
//     previous owner is alive (measured). So stopping closes stdin and lets EOF terminate the
//     child normally (kiro-cli acp exits 0 and removes the .lock — measured), and session/load
//     on resume retries the lock conflict a few times to wait for the previous owner to
//     disappear (stopChild / the retry in spawn).
//  2. The ACP transcript is persisted locally too (cursor writes none). kiro's acp appends
//     every turn to v2 JSONL (~/.kiro/sessions/cli/<sid>.jsonl, shared with the TUI), so when
//     the session is stopped and there is no handle, transcript.go's fileTranscript reads that
//     (managedTranscript's fallback). While a handle is live it is built in memory from
//     session/update notifications (acp.TranscriptBuf) and streamed live, as in cursor.
//
// Why one child per session: pinning model/effort/agent-engine through the child's launch
// flags is the reliable way (ACP has no per-session model setting). Permissions
// (session/request_permission) do not occur under --trust-all-tools, but a plan launch drops
// --trust-all-tools so they can arrive. Never trust "the UI does not show it, so it cannot
// happen" (the lesson of agy df996e4) — always map it to Interaction(question).
//
// Live usage (`_kiro.dev/metadata` contextUsagePercentage / meteringUsage) was confirmed in T0
// as a path cursor could not offer, but showing the context bar needs the registry contextBar
// / get_session_usage wiring, which is outside A2's file scope, so v1 A2 does not wire it to
// the UI. onNotify silently drops that notification here, marking the seam a later track can
// pick up.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/acp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/managed"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// ledger is the persistent ClientMessageID ledger: it makes resends and reconnect
// double-submits idempotent across processes.
var ledger = agents.NewMsgLedger("kiro-msgledger")

// acpModeID converts the AF vocabulary "plan"/"normal" into an ACP mode id, which is a kiro
// agent name (kiro_default / kiro_planner / kiro_guide): plan=planner, anything else=default.
// modeFromACP is the inverse, and treats guide as normal.
func acpModeID(mode string) string {
	if mode == "plan" {
		return "kiro_planner"
	}
	return "kiro_default"
}

func modeFromACP(id string) string {
	switch id {
	case "kiro_planner":
		return "plan"
	case "":
		return ""
	default: // kiro_default / kiro_guide
		return "normal"
	}
}

// NewDriver returns the managed kiro Driver (driverOf looks it up from /turn and /respond).
// The read layer is kept as-is by embedding agentImpl.
func NewDriver() agents.Driver { return managedDriver{} }

type managedDriver struct{ agentImpl }

// Capabilities. Steer is a queue inside the driver (ACP has no mid-turn injection — the same
// semantics as cursor/copilot/opencode). Every Dynamic* is false: model/effort/mode are pinned
// by the child's launch flags and changing one means recreating the session; the registry does
// not expose planMode/effort either (three modes cycle, so it is not a clean boolean, and the
// catalogue carries no effort metadata), so the Console shows no dynamic UI. Questions is true
// because a plan launch (which drops --trust-all-tools) picks up session/request_permission.
func (managedDriver) Capabilities() agents.Capabilities {
	return agents.Capabilities{
		ProcessModel: "per-session-child",
		Steer:        true,
		Questions:    true,
	}
}

// Resume returns the session's ThreadHandle, spawning the child runtime and
// creating/loading the kiro session when needed (Driver interface: start a new one if there
// is none). It doubles as the shared procedure for reconciliation.
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
	if m.Kind != session.KindKiro {
		return nil, errors.New("kiro driver は kiro セッション専用です")
	}
	if !session.DirExists(m.Dir) {
		return nil, agents.DirGoneErr(m.Dir)
	}
	ensureSettings()                       // idempotent: autoupdate off, --trust-all danger dialog suppressed
	slotSid := session.UUID(m.Dir, m.Name) // identity: the working copy, never the subdir
	h := managedKind.GetOrAdd(m.Name, func() *threadHandle { return newThreadHandle(m, slotSid) })

	if h.Connected() {
		return h, nil
	}
	// Start serializes spawns per handle (A2-4): when boot's ReconcileManaged and a /turn right
	// after it call Resume concurrently, check-then-spawn is not serialized and spawns twice; the
	// later child is rejected by the earlier one's .lock and, once retries are exhausted, could
	// go straight to session/new. It re-checks liveness under the spawn lock and reuses the handle
	// the earlier caller already established (never spawn a second time).
	if err := h.Start(m); err != nil {
		return nil, err
	}
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

// managedKind is the shared scaffold over this registry (managed.Lifecycle); its hooks are set in
// init because they reach the registry themselves.
var managedKind = &managed.Lifecycle[*threadHandle]{
	Kind:    session.KindKiro,
	Name:    "kiro",
	Mu:      &handlesMu,
	Handles: &handles,
	Ledger:  ledger,
}

// Shutdown stops a handle's child only through managed.Downer: a Down that drifts from it would be
// skipped without a word.
var _ managed.Downer = (*threadHandle)(nil)

func init() {
	managedKind.Resume = func(m session.Meta) error {
		_, err := managedDriver{}.Resume(m)
		return err
	}
}

func handleFor(name string) *threadHandle {
	h, _ := managedKind.Get(name)
	return h
}

func liveHandles() []*threadHandle { return managedKind.Live() }

// DropHandle detaches a managed session from its runtime (stop/halt/archive):
// interrupt any running turn, gracefully terminate the child (stdin EOF → kiro exits
// and releases its .lock), forget the handle. The conversation stays on disk (v2 JSONL);
// a later Resume re-spawns and session/load reattaches (measured: history replay plus context
// retention).
func DropHandle(name string) { dropHandle(name, 0) }

// DropHandleWait is DropHandle that additionally waits (bounded) for the child to
// actually exit. Used by the managed→TUI driver switch (A2-2): the TUI relaunch does a
// `--resume-id` on the same session, and kiro's per-sid .lock rejects that (or mints a
// new sid = split-brain) while the managed child still holds it. Waiting for the graceful
// stdin-EOF exit (which releases the .lock) closes that race. Best-effort: on timeout we
// proceed anyway (the TUI launch will surface any residual lock error itself).
func DropHandleWait(name string, wait time.Duration) { dropHandle(name, wait) }

func dropHandle(name string, wait time.Duration) {
	h, ok := managedKind.Take(name)
	if !ok {
		return
	}
	exited := h.Detach()
	if wait > 0 && exited != nil {
		select {
		case <-exited:
		case <-time.After(wait):
		}
	}
}

// RemoveLedger drops the ClientMessageID ledger (/stop — only when the slot's identity itself
// is discarded; halt/archive keep it because they can be resumed).
func RemoveLedger(name string) { managedKind.RemoveLedger(name) }

// ManagedAlive reports whether the session has a live runtime handle.
func ManagedAlive(name string) bool { return managedKind.Alive(name) }

// ManagedContext returns the live context fill for a managed kiro session (Track D —
// docs/log/43 §10): the latest `_kiro.dev/metadata` contextUsagePercentage (0–100), the
// running model's context window in tokens, the accumulated meteringUsage credits, and
// the model id. ok=false when there's no live handle or no metadata has arrived yet —
// so TUI sessions and pre-first-turn managed sessions show no context bar. Cheap
// (in-memory read); safe to call from the /sessions/usage aggregation.
func ManagedContext(name string) (pct float64, window int, credits float64, model string, ok bool) {
	h := handleFor(name)
	if h == nil {
		return 0, 0, 0, "", false
	}
	model, alive := h.LiveModel()
	if !alive {
		return 0, 0, 0, "", false
	}
	h.usageMu.Lock()
	defer h.usageMu.Unlock()
	if !h.hasUsage {
		return 0, 0, 0, "", false
	}
	window = h.ctxWindow
	if window <= 0 {
		window = kiroDefaultWindow
	}
	return h.ctxPct, window, h.credits, model, true
}

// ManagedBusy reports a turn is running or queued (the wait condition for graceful shutdown).
func ManagedBusy(name string) bool { return managedKind.Busy(name) }

// ManagedInFlight reports that the handle holds an entry it took from the queue and has not
// settled. The pump gives its move-gate lease back once the runtime has the input, which can be
// before the runtime's own record (and so DriveState) shows the turn; a move asks this, under the
// exclusive gate, so it never starts in that window (ADR 0115 decision 11.2). Entries still
// queued do not count: the move carries them.
func ManagedInFlight(name string) bool { return managedKind.InFlight(name) }

// AbortManaged interrupts every running managed turn (the equivalent of the per-pane Ctrl-C
// in graceful shutdown).
func AbortManaged() { managedKind.Abort() }

// Shutdown terminates every managed child (on agent exit). The conversation of record stays in
// the v2 JSONL and the next boot's ReconcileManaged reattaches with session/load. The point is
// to close stdin so the .lock is released cleanly, so the next boot's resume hits no lock
// conflict.
func Shutdown() { managedKind.Shutdown() }

// ReconcileManaged re-attaches managed kiro sessions after an Agent boot or child
// death. It covers every managed meta not already treated as stopped. On failure the session
// stays stopped and the user's resume click retries it.
func ReconcileManaged(reason string) { managedKind.Reconcile(reason) }

// stopChild terminates a child gracefully-first: close stdin (EOF) so kiro-cli acp exits
// 0 and removes its .lock (measured — this avoids "active in another process" on a later
// session/load). As a safety net, SIGTERM then SIGKILL to the process group if the child
// ignores EOF and stays. Reaping is done by the watch goroutine (cmd.Wait) started at spawn.
func stopChild(cmd *exec.Cmd, stdin io.Closer) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if stdin != nil {
		_ = stdin.Close() // graceful: EOF → exit 0 and .lock removal
	}
	pid := cmd.Process.Pid
	// Safety net: if EOF does not bring it down, kill the whole group (-pid). Setpgid puts the
	// child in its own group, so the helper processes it holds are swept too. Once the child is
	// reaped the pid (group) can be reused, so check the process itself is alive before sending
	// a raw -pid signal (Signal on an already-waited Process returns ErrProcessDone, so this is
	// race safe).
	time.AfterFunc(4*time.Second, func() {
		if cmd.Process.Signal(syscall.Signal(0)) != nil {
			return // already reaped — do not signal the group
		}
		if syscall.Kill(-pid, syscall.SIGTERM) == nil {
			time.AfterFunc(3*time.Second, func() {
				if cmd.Process.Signal(syscall.Signal(0)) == nil {
					_ = syscall.Kill(-pid, syscall.SIGKILL)
				}
			})
		}
	})
}

// --- thread handle -----------------------------------------------------------

// kind is what the shared ACP driver body needs from kiro.
var kind = &acp.Kind{
	Name:        "kiro",
	StreamState: true, // the live transcript; the persisted v2 JSONL is the fallback without a handle
	// DynamicMode is false: model, effort and mode are all launch flags.
	Effort: true,
	// Longer than stopChild's EOF → SIGTERM → SIGKILL sequence (4 s + 3 s), so a child that
	// ignores EOF is still counted.
	ExitRecordWait:   10 * time.Second,
	LoadFailed:       "kiro セッションを読み込めませんでした（別プロセスが占有中の可能性・時間をおいて再開してください）",
	SettingsRejected: "kiro は稼働中の設定変更に未対応です（セッションを作り直してください）",
	Ledger:           ledger,
	Sids:             sids,
	NewClient:        newACPClient,
	Command:          command,
	StopChild:        stopChild,
	// The store is <sid>.json: only when it is gone was the conversation deleted. Calling
	// session/new after a load failure while the store is intact (another process holds the
	// .lock / wording drift kept the lock from being recognized / a transient failure) detaches a
	// live conversation and overwrites the slot's sid with a new session (A2-1).
	StoreGone: func(sid string) bool {
		_, err := os.Stat(sessionJSONPath(sid))
		return err != nil
	},
	ModeID:           acpModeID,
	ModeFromACP:      modeFromACP,
	NormalizeMsgID:   normalizeMsgID,
	PermissionDetail: func(raw json.RawMessage) (string, bool) { return toolInfo(raw), true },
}

// The shared handle's method set is what the session layer asserts on a ThreadHandle; the
// embedding must keep promoting it.
var (
	_ agents.ThreadHandle   = (*threadHandle)(nil)
	_ agents.QueueingSender = (*threadHandle)(nil)
)

// threadHandle is a managed kiro session: the shared ACP handle plus kiro's own reading of the
// stream (onNotify, onMetadata), its .lock-aware session/load and its live usage.
type threadHandle struct {
	*acp.Handle
	createdAt time.Time // slot creation time (discoverSid fence: no adopting a predecessor session)

	// Live usage (Track D — docs/log/43 §10). Holds the contextUsagePercentage (latest value)
	// and the meteringUsage (accumulated credits) carried by `_kiro.dev/metadata` notifications.
	// onNotify (the readLoop goroutine) updates them, so they take a lock separate from the
	// handle's and do not contend with the turn plumbing. The reader is ManagedContext (wired
	// through context.go / session_usage.go to the mirror's ContextBar and get_session_usage).
	usageMu   sync.Mutex
	ctxWindow int     // context window of the current model (tokens; fixed at spawn via ModelWindow)
	ctxPct    float64 // latest contextUsagePercentage (0–100)
	credits   float64 // accumulated meteringUsage (in-memory, for this handle's lifetime)
	hasUsage  bool    // whether metadata arrived at least once (no context shown until it does)
}

func newThreadHandle(m session.Meta, slotSid string) *threadHandle {
	// createdAt fences discoverSid (same as the read layer's resolveSid).
	h := &threadHandle{createdAt: slotCreatedAt(m)}
	h.Handle = acp.NewHandle(kind, acp.Hooks{
		OnNotify:    h.onNotify,
		DiscoverSid: h.discoverSid,
		Load:        h.loadWithLockRetry,
		Spawned:     h.spawned,
	}, m.Name, m.CWD(), slotSid) // CWD: Dir, or the subdir chosen at launch
	return h
}

// command builds the `kiro-cli acp` child.
func command(h *acp.Handle, st agents.ThreadSettings) *exec.Cmd {
	// The acp subcommand, plus an explicit v2 engine pin (the engine that writes the v2 JSONL
	// the read layer treats as the record; v2 is the default, but pin it so a future v3 cannot
	// swing it), plus the fleet default bypass (--trust-all-tools). plan drops --trust-all-tools
	// so approvals surface as an Interaction.
	args := []string{"acp", "--agent-engine", "v2"}
	if h.BypassNow() && st.Mode != "plan" {
		args = append(args, "--trust-all-tools")
	}
	if st.Model != "" && st.Model != "auto" {
		// Pin the model for this per-session child (ACP has no per-session setting).
		args = append(args, "--model", st.Model)
	}
	if st.Effort != "" {
		args = append(args, "--effort", st.Effort)
	}
	cmd := exec.Command(bin(), args...)
	// Own process group, so helper processes can be killed along with the group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Auth is ambient (the CLI picks up ~/.local/share/kiro-cli/data.sqlite3 itself — measured:
	// it runs through with no env injection). Unauthenticated, ACP exits immediately with
	// "You are not logged in" on stderr (fail-fast).
	// One child per session, and kiro hands its own environment to MCP children (measured),
	// so this is how the af server learns which session it serves.
	cmd.Env = agents.WithSessionEnv(os.Environ(), h.SessionName())
	return cmd
}

// discoverSid picks up an existing kiro session for this cwd even with an empty sid cache (the
// same discover — cwd+mtime — as the read layer's resolveSid). When a Terminal→managed switch
// happens while the TUI-side sid is not yet cached in sidstore, without this the driver silently
// starts a new conversation (A2-3). The fence (slot creation time) is required: recreate cuts a
// new slug in the same dir, so an unfenced discover picks up the predecessor's old session .json
// and A2-1's "load succeeds while the store is intact" logic silently continues the old
// conversation (A-1 recurring on the managed path). The same-cwd constraint, which assumes
// worktrees separate the dirs, matches resolveSid too.
func (h *threadHandle) discoverSid() string { return discoverSid(h.Dir(), h.createdAt) }

// spawned fixes the context window of this child's model (Track D: the denominator of the
// pct→token conversion). The metadata pct does not carry the window, so read it from the
// catalogue (--list-models). On resume/re-spawn, pct and credits are not carried over and only
// ctxWindow is updated (credits counts in memory for this handle's lifetime).
func (h *threadHandle) spawned(modelID string) {
	if win := ModelWindow(modelID); win > 0 {
		h.usageMu.Lock()
		h.ctxWindow = win
		h.usageMu.Unlock()
	}
}

// lockRetryAttempts / lockRetryDelay bound the .lock wait (~6s by default). They are vars so
// tests can shorten them.
var (
	lockRetryAttempts = 10
	lockRetryDelay    = 600 * time.Millisecond
)

// loadWithLockRetry calls session/load, retrying while the prior owner still holds the
// session's .lock ("active in another process"). Non-lock errors return immediately so
// spawn can fall back to a fresh session/new. Each attempt resets the replay buffer so a
// partial replay before an error isn't double-counted.
func (h *threadHandle) loadWithLockRetry(cl *acp.Client, sid string) (json.RawMessage, error) {
	var lastErr error
	for attempt := 0; attempt < lockRetryAttempts; attempt++ {
		h.Buf().Reset()
		h.Buf().SetLoading(true)
		res, err := cl.Call("session/load", map[string]any{
			"sessionId": sid, "cwd": h.Dir(), "mcpServers": []any{},
		}, 180*time.Second)
		h.Buf().SetLoading(false) // flush the last assistant turn
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !isLockBusy(err) {
			return nil, err
		}
		h.Buf().Reset()
		time.Sleep(lockRetryDelay) // wait for the previous owner to exit and release the .lock
	}
	return nil, lastErr
}

// isLockBusy reports the "Session is active in another process" refusal (kiro's per-sid
// .lock is still held by a just-killed prior owner — retry until it exits). Requires the
// JSON-RPC error code (-32603) AND the message so an unrelated error can't be misread as a
// lock-busy. Only gates the RETRY decision — the session/new-vs-fail choice in spawn is made
// from the on-disk store, not this string, so a message drift stays fail-safe (A2-1).
func isLockBusy(err error) bool {
	var re *acp.RPCError
	if !errors.As(err, &re) {
		return false
	}
	return re.Code == -32603 && strings.Contains(strings.ToLower(re.Error()), "active in another process")
}

// onNotify accumulates the transcript from session/update on the readLoop goroutine.
// MUST be fast (no RPC, no h.mu) — it only appends to the transcript buffer and, for
// current_mode_update, records the mode under h.mu. kiro's `_kiro.dev/*` notifications
// (metadata / subagent / commands / retry_warning) arrive here too, but v1 A2 does not use
// them (wiring live usage into the UI is a later track — see the top of driver.go).
func (h *threadHandle) onNotify(method string, params json.RawMessage) {
	h.Touch()
	if method == "_kiro.dev/metadata" {
		h.onMetadata(params) // Track D: live context% / credits
		return
	}
	if method != "session/update" {
		return // the other _kiro.dev/* (subagent / commands / retry_warning) are unused
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
			// current_mode_update
			CurrentModeID string `json:"currentModeId"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	u := p.Update
	switch u.SessionUpdate {
	case "user_message_chunk":
		h.Buf().UserChunk(u.Content.Text)
	case "agent_message_chunk":
		h.Buf().AgentChunk(u.Content.Text)
	case "agent_thought_chunk":
		h.Buf().ThoughtChunk(u.Content.Text)
	case "tool_call":
		h.NoteToolCall(u.SessionUpdate, u.ToolCallID, u.Status)
		h.Buf().ToolCall(u.ToolCallID, u.Title, toolInfo(u.RawInput), "", "", nil)
	case "tool_call_update":
		h.NoteToolCall(u.SessionUpdate, u.ToolCallID, u.Status)
		if out := toolOutput(u.RawOutput); out != "" {
			h.Buf().ToolOutput(u.ToolCallID, out)
		}
	case "current_mode_update":
		if m := modeFromACP(u.CurrentModeID); m != "" {
			h.SetRuntimeMode(m)
		}
	}
}

// onMetadata folds a `_kiro.dev/metadata` notification into the handle's live usage
// (Track D). Called on the readLoop goroutine — must be fast (a single lock, no RPC).
// The percentage is the CURRENT context fill (it can shrink after compaction, so we
// keep the latest, not a max); credits accumulate per turn (measured: value is that turn's
// consumption and is attached only at the end of the turn). A metadata notification may carry
// only one of the two (measured: percentage alone / percentage+credits) — a nil field leaves
// the prior value.
func (h *threadHandle) onMetadata(params json.RawMessage) {
	var p struct {
		ContextUsagePercentage *float64 `json:"contextUsagePercentage"`
		MeteringUsage          []struct {
			Value float64 `json:"value"`
			Unit  string  `json:"unit"`
		} `json:"meteringUsage"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	h.usageMu.Lock()
	defer h.usageMu.Unlock()
	if p.ContextUsagePercentage != nil {
		h.ctxPct = *p.ContextUsagePercentage
		h.hasUsage = true
	}
	for _, mu := range p.MeteringUsage {
		if mu.Unit == "credit" {
			h.credits += mu.Value
			h.hasUsage = true
		}
	}
}

// toolInfo extracts a short label from a tool_call rawInput (command carries the most).
func toolInfo(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var in struct {
		Command     string `json:"command"`
		Description string `json:"description"`
		Path        string `json:"path"`
		FilePath    string `json:"file_path"`
		Purpose     string `json:"__tool_use_purpose"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	for _, s := range []string{in.Command, in.Description, in.Path, in.FilePath, in.Purpose} {
		if s != "" {
			return s
		}
	}
	return ""
}

// toolOutput renders a tool_call_update rawOutput (a shell's exit_status/stdout/stderr). It
// accepts both the shape kiro's v2 JSONL toolResult uses and the ACP-standard
// exitCode/stdout/stderr.
func toolOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var out struct {
		ExitCode   *int   `json:"exitCode"`
		ExitStatus string `json:"exit_status"`
		Stdout     string `json:"stdout"`
		Stderr     string `json:"stderr"`
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
	if s == "" {
		if out.ExitCode != nil && *out.ExitCode != 0 {
			s = fmt.Sprintf("(exit %d)", *out.ExitCode)
		} else if out.ExitStatus != "" && !strings.HasSuffix(out.ExitStatus, "0") {
			s = "(" + out.ExitStatus + ")"
		}
	}
	return s
}

// managedLiveState is the live state of the managed route (read by state.go's LiveState;
// acp.Handle.LiveState says why it exists).
func managedLiveState(m session.Meta) string {
	h := handleFor(m.Name)
	if h == nil {
		return "" // stopped or not attached: hold no opinion about the state
	}
	return h.LiveState()
}

// managedTranscript builds the read-layer TranscriptData for a managed kiro session (called
// from transcript.go's DriverManaged branch). With a live handle it returns the in-memory
// transcript the driver built from session/update (live streaming included). With no handle
// (stopped / not started) fileTranscript reads the v2 JSONL kiro persisted — cursor has no
// local transcript so it was empty while stopped, but kiro persists, so history shows even
// then.
func managedTranscript(m session.Meta) agents.TranscriptData {
	h := handleFor(m.Name)
	if h == nil || h.Buf().Empty() {
		return fileTranscript(m)
	}
	td := agents.TranscriptData{Turns: h.Buf().Snapshot()}
	// Model badge: prefer the model the user chose explicitly (settings.Model); with auto or
	// nothing set, use ACP's currentModelId.
	model := h.Enrich(&td)
	stampModel(td.Turns, displayModel(model))
	return td
}

// normalizeMsgID mirrors the other drivers' convention: empty → the driver assigns one.
func normalizeMsgID(id string) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("af-%d", time.Now().UnixNano())
}
