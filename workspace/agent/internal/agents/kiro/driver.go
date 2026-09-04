package kiro

// Managed driver for kiro (docs/log/43 Track A2) — one child process per session. Each
// session holds a `kiro-cli acp` child and maps session/new, session/load (cross-process
// resume, measured), session/prompt (blocking) and session/cancel onto the turn state
// machine, Interaction and reconciliation. Same skeleton as the cursor / copilot drivers
// (docs/log/40, 36), with two kiro-specific differences:
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
//     session/update notifications (mirror.go transcriptBuf) and streamed live, as in cursor.
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
	if m.Kind != session.KindKiro {
		return nil, errors.New("kiro driver は kiro セッション専用です")
	}
	if !session.DirExists(m.Dir) {
		return nil, agents.DirGoneErr(m.Dir)
	}
	ensureSettings()                       // idempotent: autoupdate off, --trust-all danger dialog suppressed
	slotSid := session.UUID(m.Dir, m.Name) // identity: the working copy, never the subdir
	handlesMu.Lock()
	h := handles[m.Name]
	if h == nil {
		h = &threadHandle{
			name:      m.Name,
			dir:       m.CWD(), // Dir, or the subdir chosen at launch
			slotSid:   slotSid,
			createdAt: slotCreatedAt(m), // fence for discoverSid (same as the read layer's resolveSid)
			events:    make(chan agents.Event, 64),
		}
		handles[m.Name] = h
	}
	handlesMu.Unlock()

	h.mu.Lock()
	alive := h.alive && h.cl != nil && !h.cl.dead()
	h.mu.Unlock()
	if alive {
		return h, nil
	}

	// Serialize spawns per handle (A2-4): when boot's ReconcileManaged and a /turn right after
	// it call Resume concurrently, check-then-spawn is not serialized and spawns twice; the
	// later child is rejected by the earlier one's .lock and, once retries are exhausted, could
	// go straight to session/new. Take spawnMu, re-check liveness, and reuse the handle the
	// earlier caller already established (never spawn a second time).
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
	if h.settings.Effort == "" {
		h.settings.Effort = m.Effort
	}
	if h.settings.Mode == "" {
		h.settings.Mode = m.Mode
	}
	// Whether to skip permission confirmation (docs/log/76) is resolved from meta and ui-prefs
	// on every Resume. It is not carried in ThreadSettings because that is for dynamic updates
	// where "empty = leave unchanged", which cannot make a bool three-valued — so a re-spawn
	// after a settings change still uses the value resolved here.
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
	cmd, cl, stdin, sid, running, exited := h.cmd, h.cl, h.stdin, h.sid, h.running, h.exited
	h.mu.Unlock()
	if running && cl != nil && sid != "" {
		_ = cl.notifyPeer("session/cancel", map[string]any{"sessionId": sid})
	}
	stopChild(cmd, stdin)
	if wait > 0 && exited != nil {
		select {
		case <-exited:
		case <-time.After(wait):
		}
	}
}

// RemoveLedger drops the ClientMessageID ledger (/stop — only when the slot's identity itself
// is discarded; halt/archive keep it because they can be resumed).
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
	h.mu.Lock()
	alive := h.alive
	model = h.model
	h.mu.Unlock()
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
func ManagedBusy(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running || len(h.queue) > 0
}

// AbortManaged interrupts every running managed turn (the equivalent of the per-pane Ctrl-C
// in graceful shutdown).
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

// Shutdown terminates every managed child (on agent exit). The conversation of record stays in
// the v2 JSONL and the next boot's ReconcileManaged reattaches with session/load. The point is
// to close stdin so the .lock is released cleanly, so the next boot's resume hits no lock
// conflict.
func Shutdown() {
	handlesMu.Lock()
	type child struct {
		cmd   *exec.Cmd
		stdin io.Closer
	}
	var kids []child
	for _, h := range handles {
		h.mu.Lock()
		h.alive = false
		kids = append(kids, child{h.cmd, h.stdin})
		h.mu.Unlock()
	}
	handlesMu.Unlock()
	for _, c := range kids {
		stopChild(c.cmd, c.stdin)
	}
}

// ReconcileManaged re-attaches managed kiro sessions after an Agent boot or child
// death. It covers every managed meta not already treated as stopped. On failure the session
// stays stopped and the user's resume click retries it.
func ReconcileManaged(reason string) {
	d := managedDriver{}
	for _, m := range session.ListMetas() {
		if m.Kind != session.KindKiro || m.DriverKind() != session.DriverManaged || m.Archived {
			continue
		}
		if m.StoppedAt != "" && handleFor(m.Name) == nil {
			continue // deliberately stopped — resume only on user action
		}
		if _, err := d.Resume(m); err != nil {
			log.Printf("kiro managed: reconcile %s (%s): %v", m.Name, reason, err)
		}
	}
}

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

type threadHandle struct {
	name      string
	dir       string
	slotSid   string
	createdAt time.Time // slot creation time (discoverSid fence: no adopting a predecessor session)

	spawnMu sync.Mutex // serializes spawns for this handle (no double spawn from concurrent Resume, A2-4)

	// bypass is whether to skip permission confirmation (docs/log/76). Resume resolves it from
	// meta and puts it here — spawn has no meta, so it is carried through the handle. plan is
	// read from spawn's st.Mode rather than at Resume time, because a mode change while running
	// re-spawns.
	bypass bool

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser // kept so stop can EOF-close it (graceful .lock release)
	exited   chan struct{}  // closed by watch when the current child exits (bounded wait for the switch, A2-2)
	cl       *acpClient
	sid      string // kiro session UUID (assigned by the CLI)
	model    string // ACP currentModelId (for the model badge; auto is "auto")
	alive    bool
	state    agents.TurnState
	running  bool
	pumping  bool
	queue    []agents.TurnInput
	settings agents.ThreadSettings
	inter    *agents.Interaction
	permID   json.RawMessage // JSON-RPC id of the pending session/request_permission
	permOpts []string        // Interaction option index → ACP optionId
	events   chan agents.Event

	buf transcriptBuf // transcript built from ACP session/update (guarded by its own lock)

	// Live usage (Track D — docs/log/43 §10). Holds the contextUsagePercentage (latest value)
	// and the meteringUsage (accumulated credits) carried by `_kiro.dev/metadata` notifications.
	// onNotify (the readLoop goroutine) updates them, so they take a lock separate from h.mu and
	// do not contend with the turn plumbing. The reader is ManagedContext (wired through
	// context.go / session_usage.go to the mirror's ContextBar and get_session_usage).
	usageMu   sync.Mutex
	ctxWindow int     // context window of the current model (tokens; fixed at spawn via ModelWindow)
	ctxPct    float64 // latest contextUsagePercentage (0–100)
	credits   float64 // accumulated meteringUsage (in-memory, for this handle's lifetime)
	hasUsage  bool    // whether metadata arrived at least once (no context shown until it does)
}

// bypassNow reports the resolved "skip permission confirmation" choice (docs/log/76). Resume
// writes it under h.mu; spawn runs without the lock, so read it through here.
func (h *threadHandle) bypassNow() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.bypass
}

// spawn starts the child runtime, initializes ACP and loads/creates the kiro session.
// Caller must NOT hold h.mu.
func (h *threadHandle) spawn(st agents.ThreadSettings) error {
	// The acp subcommand, plus an explicit v2 engine pin (the engine that writes the v2 JSONL
	// the read layer treats as the record; v2 is the default, but pin it so a future v3 cannot
	// swing it), plus the fleet default bypass (--trust-all-tools). plan drops --trust-all-tools
	// so approvals surface as an Interaction.
	args := []string{"acp", "--agent-engine", "v2"}
	if h.bypassNow() && st.Mode != "plan" {
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
	cmd.Dir = h.dir
	// Own process group, so helper processes can be killed along with the group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Auth is ambient (the CLI picks up ~/.local/share/kiro-cli/data.sqlite3 itself — measured:
	// it runs through with no env injection). Unauthenticated, ACP exits immediately with
	// "You are not logged in" on stderr (fail-fast).
	cmd.Env = os.Environ()
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
		return fmt.Errorf("kiro runtime を起動できません: %w", err)
	}
	cl := newACPClient(stdin, stdout)
	// Capture this cl in the closure: during the first spawn readLoop can run while h.cl is
	// still unassigned, so going through h.cl would nil-dereference and panic when responding
	// to an unknown method.
	cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		h.onServerRequest(cl, id, method, params)
	}
	cl.onNotify = h.onNotify
	exited := make(chan struct{})
	go h.watch(cmd, cl, exited)

	if _, err := cl.call("initialize", map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{},
	}, 30*time.Second); err != nil {
		stopChild(cmd, stdin)
		return fmt.Errorf("kiro runtime の initialize に失敗しました: %w", err)
	}

	sid := h.sid
	if sid == "" {
		sid = sids.Read(h.slotSid)
	}
	// Even with an empty sid cache, pick up an existing kiro session for this cwd (the same
	// discover — cwd+mtime — as the read layer's resolveSid). When a Terminal→managed switch
	// happens while the TUI-side sid is not yet cached in sidstore, without this the driver
	// silently starts a new conversation (A2-3). The fence (slot creation time) is required:
	// recreate cuts a new slug in the same dir, so an unfenced discover picks up the
	// predecessor's old session .json and A2-1's "load succeeds while the store is intact" logic
	// silently continues the old conversation (A-1 recurring on the managed path). The same-cwd
	// constraint, which assumes worktrees separate the dirs, matches resolveSid too.
	if sid == "" {
		if d := discoverSid(h.dir, h.createdAt); d != "" {
			sid = d
			sids.Write(h.slotSid, d)
		}
	}
	mode := ""
	modelID := ""
	if sid != "" {
		// Cross-process resume (measured: the session/update replay restores history and
		// context). If the previous owner still holds the .lock this is rejected with "active in
		// another process", so retry a few times to cover the grace period in which stopChild
		// takes the old child down.
		res, lerr := h.loadWithLockRetry(cl, sid)
		if lerr != nil {
			// Falling back to session/new is allowed only when that sid's store (<sid>.json) is
			// actually gone, i.e. the conversation was deleted. Calling session/new after a load
			// failure while the store is intact (another process holds the .lock / wording drift
			// kept the lock from being recognized / a transient failure) detaches a live
			// conversation and overwrites the slot's sid with a new session (A2-1). So while the
			// store is intact, return an error and leave the session stopped (the resume click can
			// retry). The decision uses the store's presence on disk (deterministic), never a
			// brittle string match.
			if _, statErr := os.Stat(sessionJSONPath(sid)); statErr != nil {
				log.Printf("kiro managed: session/load %s: store gone (%v) — restarting with a new session", h.name, lerr)
				sid = ""
				h.buf.reset()
			} else {
				// Do not leave a partial replay in buf — it would be shown in preference to the
				// complete file transcript.
				h.buf.reset()
				stopChild(cmd, stdin)
				return fmt.Errorf("kiro セッションを読み込めませんでした（別プロセスが占有中の可能性・時間をおいて再開してください）: %w", lerr)
			}
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
			stopChild(cmd, stdin)
			return fmt.Errorf("kiro セッションを作成できません: %w", err)
		}
		var out struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(res, &out) != nil || out.SessionID == "" {
			stopChild(cmd, stdin)
			return errors.New("kiro セッションの作成応答を解釈できません")
		}
		sid = out.SessionID
		sids.Write(h.slotSid, sid) // CLI-assigned sid, shared with the read layer (resolveSid)
		mode = currentModeOf(res)
		modelID = currentModelOf(res)
	}

	h.mu.Lock()
	h.cmd, h.stdin, h.cl, h.sid, h.alive = cmd, stdin, cl, sid, true
	h.exited = exited              // this child's exit signal (for DropHandleWait's bounded wait)
	h.state = agents.TurnCompleted // the child is brand new — no turn is running
	h.model = modelID
	h.inter, h.permID, h.permOpts = nil, nil, nil
	if m := modeFromACP(mode); m != "" {
		h.settings.Mode = m
	}
	wantMode := h.settings.Mode
	h.mu.Unlock()

	// Track D: fix the context window of this child's model (the denominator of the pct→token
	// conversion). The metadata pct does not carry the window, so read it from the catalogue
	// (--list-models). On resume/re-spawn, pct and credits are not carried over and only
	// ctxWindow is updated (credits counts in memory for this handle's lifetime).
	if win := ModelWindow(modelID); win > 0 {
		h.usageMu.Lock()
		h.ctxWindow = win
		h.usageMu.Unlock()
	}

	// If meta's wanted mode differs from the runtime's current mode, re-assert it (guards
	// against falling back to the default after a resume; best-effort). A plan launch goes to
	// kiro_planner.
	if wantMode != "" && wantMode != modeFromACP(mode) {
		_, _ = cl.call("session/set_mode", map[string]any{
			"sessionId": sid, "modeId": acpModeID(wantMode),
		}, 15*time.Second)
	}
	return nil
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
func (h *threadHandle) loadWithLockRetry(cl *acpClient, sid string) (json.RawMessage, error) {
	var lastErr error
	for attempt := 0; attempt < lockRetryAttempts; attempt++ {
		h.buf.reset()
		h.buf.setLoading(true)
		res, err := cl.call("session/load", map[string]any{
			"sessionId": sid, "cwd": h.dir, "mcpServers": []any{},
		}, 180*time.Second)
		h.buf.setLoading(false) // flush the last assistant turn
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !isLockBusy(err) {
			return nil, err
		}
		h.buf.reset()
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
	var re *rpcError
	if !errors.As(err, &re) {
		return false
	}
	return re.Code == -32603 && strings.Contains(strings.ToLower(re.Error()), "active in another process")
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

// currentModelOf extracts models.currentModelId from a session/new or session/load result
// (measured: auto is "auto", a named model is the bare id).
func currentModelOf(res json.RawMessage) string {
	var out struct {
		Models struct {
			CurrentModelID string `json:"currentModelId"`
		} `json:"models"`
	}
	_ = json.Unmarshal(res, &out)
	return out.Models.CurrentModelID
}

// watch reaps the child and records its exit (attribution is exact here, unlike a daemon
// supervisor, because there is one child per session). An exit caused by stdin EOF
// (DropHandle/Shutdown) is exit 0, i.e. "stopped", so the Console shows the normal stopped
// state.
func (h *threadHandle) watch(cmd *exec.Cmd, cl *acpClient, exited chan struct{}) {
	defer close(exited) // release the switch's DropHandleWait (a channel specific to this child)
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
	stale := h.cl != cl // already replaced by a newer child (an old watch after a respawn)
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

// runtimeLost drops the handle to unknown (the honest state after a disconnect).
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

// Steer queues inside the driver (ACP has no mid-turn injection — the input is submitted as
// the next turn once the current one finishes).
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
	// Resend idempotency (the ledger) happens when pump starts executing — recording it
	// persistently before the queue insert would make a resend after a crash lost the queue be
	// silently discarded as "already seen".
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

// pump processes the queue serially (the child is exclusive, so no waitIdle is needed).
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
			continue // resend — the ledger (persistent, cross-process) makes it idempotent at start
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
// The MarkTurnStart/End turn boundary drives the status store and the completion report of
// docs/log/30.
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
	// ACP emits no user_message_chunk on a live turn (measured), so the user turn is committed
	// to the transcript here (a separate path from replay's user_message_chunk).
	h.buf.addUserTurn(in.Prompt)
	h.setState(agents.TurnRunning)
	res, err := cl.call("session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": in.Prompt}},
	}, 0) // no timeout — a turn runs as long as it runs
	h.buf.flushAsst() // close the assistant turn left open (ACP has no turn_ended notification)
	h.mu.Lock()
	interrupted := h.state == agents.TurnInterrupting
	h.inter, h.permID, h.permOpts = nil, nil, nil // the turn ended, so nothing is pending
	h.mu.Unlock()
	if err != nil {
		if interrupted {
			h.setState(agents.TurnCancelled)
		} else {
			// A transport break means the child is lost: fall back honestly to unknown.
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

// UpdateSettings applies dynamic settings. kiro pins model, effort and mode entirely through
// the child's launch flags (Capabilities declares every Dynamic* false, so the Console shows no
// UI), so this defensively returns an explicit error (recreate the session to apply a change).
func (h *threadHandle) UpdateSettings(s agents.ThreadSettings) error {
	if s.Model != "" || s.ClearModel || s.Effort != "" || s.ClearEffort || s.Mode != "" {
		return errors.New("kiro は稼働中の設定変更に未対応です（セッションを作り直してください）")
	}
	return nil
}

// Respond answers the pending Interaction — for kiro, a reply to session/request_permission.
// answer/allow converts the option index into an ACP optionId, deny picks a reject-type
// optionId, cancel sends outcome:"cancelled".
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
		// Do not push a false "running" to subscribers when no turn is active.
		h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
	}
	return nil
}

// findOption returns the first optionId containing the substring ("allow" / "reject" —
// the ACP vocabulary allow_once / allow_always / reject_once).
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
// MUST be fast (no RPC, no h.mu) — it only appends to the transcript buffer and, for
// current_mode_update, records the mode under h.mu. kiro's `_kiro.dev/*` notifications
// (metadata / subagent / commands / retry_warning) arrive here too, but v1 A2 does not use
// them (wiring live usage into the UI is a later track — see the top of driver.go).
func (h *threadHandle) onNotify(method string, params json.RawMessage) {
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
		h.buf.userChunk(u.Content.Text)
	case "agent_message_chunk":
		h.buf.agentChunk(u.Content.Text)
	case "agent_thought_chunk":
		h.buf.thoughtChunk(u.Content.Text)
	case "tool_call":
		h.buf.toolCall(u.ToolCallID, u.Title, toolInfo(u.RawInput))
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

// onServerRequest handles server-initiated requests on the readLoop goroutine —
// MUST NOT block: record the Interaction and return; the answer goes back later via
// Respond → cl.respond. It does not happen under --trust-all-tools, but a plan launch can
// reach it.
func (h *threadHandle) onServerRequest(cl *acpClient, id json.RawMessage, method string, params json.RawMessage) {
	if method != "session/request_permission" {
		// An unanswered server-initiated request wedges the turn, so reply with an error.
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

// pendingPermission folds a pending ACP `session/request_permission` into one line saying what
// was being asked (docs/log/75 P5). Empty when nothing is pending.
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

// managedLiveState is the live state of the managed route (read by state.go's LiveState).
//
// Why it is needed: managed sessions have no pane, so the TUI string contract always returns
// empty and there was neither a chip in the list nor material for the reaper to classify. A
// managed session stuck waiting for approval fell to "unknown" and tier 1 never folded it (the
// unknown of docs/log/75). The turn state machine is the only source of truth, so supply it
// from there (the same shape as cursor).
//
// Waiting for approval calls itself question to match the vocabulary of the permission card
// the mirror draws (td.Pending) and of the TUI route's classification (classifyPane's
// "requires approval" → question). The carry-over Kind being permission is a different axis
// (what can be delivered after a resume).
func managedLiveState(m session.Meta) string {
	h := handleFor(m.Name)
	if h == nil {
		return "" // stopped / not connected — hold no opinion about the state
	}
	switch h.currentState() {
	case agents.TurnWaitingInteraction:
		return "question"
	case agents.TurnQueued, agents.TurnStarting, agents.TurnRunning, agents.TurnInterrupting:
		return "working"
	}
	return "idle"
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

// managedTranscript builds the read-layer TranscriptData for a managed kiro session (called
// from transcript.go's DriverManaged branch). With a live handle it returns the in-memory
// transcript the driver built from session/update (live streaming included). With no handle
// (stopped / not started) fileTranscript reads the v2 JSONL kiro persisted — cursor has no
// local transcript so it was empty while stopped, but kiro persists, so history shows even
// then.
func managedTranscript(m session.Meta) agents.TranscriptData {
	h := handleFor(m.Name)
	if h == nil || h.buf.empty() {
		return fileTranscript(m)
	}
	td := agents.TranscriptData{Turns: h.buf.snapshot()}
	h.mu.Lock()
	inter := h.inter
	modeSet := h.settings.Mode
	// Model badge: prefer the model the user chose explicitly (settings.Model); with auto or
	// nothing set, use ACP's currentModelId.
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

// normalizeMsgID mirrors the other drivers' convention: empty → the driver assigns one.
func normalizeMsgID(id string) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("af-%d", time.Now().UnixNano())
}
