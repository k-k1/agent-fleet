package copilot

// The managed copilot driver (docs/log/36 Track A2) — a per-session child. Each session holds
// one `copilot --acp` (Agent Client Protocol, stdio JSON-RPC) child process, and session/new,
// session/load (cross-process resume, measured), session/prompt (blocking), session/cancel and
// session/set_mode are mapped onto the turn state machine (§4), Interaction (§5) and
// reconciliation (§6). The turn machine itself is the shared acp.Handle (as for cursor and
// kiro); this file holds the registry, copilot's launch flags and the fork materialisation.
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
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/acp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/managed"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
	if m.Kind != session.KindCopilot {
		return nil, errors.New("copilot driver は copilot セッション専用です")
	}
	if !session.DirExists(m.Dir) {
		return nil, agents.DirGoneErr(m.Dir)
	}
	slotSid := session.UUID(m.Dir, m.Name) // identity: the working copy, never the subdir
	// CWD: Dir, or the subdir chosen at launch.
	h := managedKind.GetOrAdd(m.Name, func() *threadHandle { return newThreadHandle(m.Name, m.CWD(), slotSid) })

	if err := h.Start(m); err != nil {
		return nil, err
	}
	return h, nil
}

// materializeFork runs on the first Resume of a forked slot (docs/log/55): it mints this slot's
// session id and builds its session-state directory from the source before spawning, so the
// spawn takes the ordinary session/load path. Without this the slot has no sid, spawn falls to
// session/new, and the branch quietly opens as an empty conversation.
func materializeFork(m session.Meta, slotSid string) error {
	if m.ForkFrom != "" && sids.Read(slotSid) == "" {
		sid, err := newSessionID()
		if err != nil {
			return fmt.Errorf("セッション ID を採番できません: %w", err)
		}
		if err := MaterializeForkAt(m.ForkFrom, sid, m.ForkAt); err != nil {
			return fmt.Errorf("分岐を作成できませんでした: %w", err)
		}
		sids.Write(slotSid, sid)
	}
	return nil
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
	Kind:    session.KindCopilot,
	Name:    "copilot",
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
// interrupt any running turn, terminate the child, forget the handle. The
// conversation stays in $COPILOT_HOME/session-state — a later Resume re-spawns
// and session/load reattaches (measured: history replay plus context retention).
func DropHandle(name string) {
	if h, ok := managedKind.Take(name); ok {
		h.Detach()
	}
}

// RemoveLedger drops the ClientMessageID ledger. Only for /stop, which discards the slot's
// identity along with it; halt/archive keep it because they can be resumed.
func RemoveLedger(name string) { managedKind.RemoveLedger(name) }

// ManagedAlive reports whether the session has a live runtime handle.
func ManagedAlive(name string) bool { return managedKind.Alive(name) }

// ManagedBusy reports a turn is running or queued (the wait condition of a graceful shutdown).
func ManagedBusy(name string) bool { return managedKind.Busy(name) }

// ManagedInFlight reports that the handle holds an entry it took from the queue and has not
// settled. The pump gives its move-gate lease back once the runtime has the input, which can be
// before the runtime's own record (and so DriveState) shows the turn; a move asks this, under the
// exclusive gate, so it never starts in that window (ADR 0115 decision 11.2). Entries still
// queued do not count: the move carries them.
func ManagedInFlight(name string) bool { return managedKind.InFlight(name) }

// AbortManaged interrupts every running managed turn (the equivalent of a graceful shutdown's
// per-pane Ctrl-C).
func AbortManaged() { managedKind.Abort() }

// Shutdown terminates every managed child, at agent exit. The conversation of record stays in
// copilot's own store and the next boot's ReconcileManaged reconnects to it.
func Shutdown() { managedKind.Shutdown() }

// ReconcileManaged re-attaches managed copilot sessions after an Agent boot or
// child death (§6). It covers every managed meta not counted as stopped. On failure the
// session simply stays stopped, and the user's Resume click retries it.
func ReconcileManaged(reason string) { managedKind.Reconcile(reason) }

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

// kind is what the shared ACP driver body needs from copilot.
var kind = &acp.Kind{
	Name: "copilot",
	// StreamState is false: the transcript and the turn's progress come from events.jsonl,
	// which copilot writes on every path.
	DynamicMode: true,
	Effort:      true,
	// Longer than stopChild's SIGTERM → SIGKILL sequence (3 s).
	ExitRecordWait:   5 * time.Second,
	LoadFailed:       "copilot セッションを読み込めませんでした（時間をおいて再開してください）",
	SettingsRejected: "copilot はモデル/effort の稼働中変更に未対応です（セッションを作り直してください）",
	Ledger:           ledger,
	Sids:             sids,
	NewClient:        newACPClient,
	Command:          command,
	StopChild:        func(cmd *exec.Cmd, _ io.Closer) { stopChild(cmd) },
	// The local store is session-state/<sid>: only when it is gone was the conversation deleted.
	StoreGone: func(sid string) bool {
		_, err := os.Stat(sessionStateDir(sid))
		return err != nil
	},
	BeforeSpawn:      materializeFork,
	ModeID:           acpModeID,
	ModeFromACP:      modeFromACP,
	NormalizeMsgID:   normalizeMsgID,
	PermissionDetail: permissionDetail,
}

// The shared handle's method set is what the session layer asserts on a ThreadHandle; the
// embedding must keep promoting it.
var (
	_ agents.ThreadHandle   = (*threadHandle)(nil)
	_ agents.QueueingSender = (*threadHandle)(nil)
)

// threadHandle is a managed copilot session: the shared ACP handle. copilot reads no
// notification (OnNotify is nil): its transcript comes from events.jsonl.
type threadHandle struct {
	*acp.Handle
}

func newThreadHandle(name, dir, slotSid string) *threadHandle {
	return &threadHandle{Handle: acp.NewHandle(kind, acp.Hooks{}, name, dir, slotSid)}
}

// command builds the `copilot --acp` child.
func command(h *acp.Handle, st agents.ThreadSettings) *exec.Cmd {
	bin := os.Getenv("AGENT_COPILOT_BIN")
	if bin == "" {
		bin = "copilot"
	}
	args := append([]string{"--acp"}, privacyFlags(bin)...)
	if h.BypassNow() && st.Mode != "plan" {
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
	cmd := exec.Command(bin, args...)
	env := append(os.Environ(), "COPILOT_AUTO_UPDATE=false")
	if tok := Token(); tok != "" {
		// Inject the gh transparent-auth token explicitly. The ambient fallback does work
		// (measured) but is undocumented, and a child process's env can be made deterministic,
		// so this is the path of record.
		env = append(env, "COPILOT_GITHUB_TOKEN="+tok)
	}
	// One child per session, and copilot hands its own environment to MCP children
	// (measured), so this is how the af server learns which session it serves.
	cmd.Env = agents.WithSessionEnv(env, h.SessionName())
	return cmd
}

// permissionDetail is the command under a permission card's title. A rawInput that does not
// decode as an object with a string command drops the request, as copilot's decoder always did.
func permissionDetail(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", true
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return "", false
	}
	return in.Command, true
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
	h.Enrich(td)
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
