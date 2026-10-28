package cursor

// The managed cursor driver (docs/log/40 Track A2), one child process per session: each
// session holds a `cursor-agent acp` child and maps session/new, session/load
// (cross-process resume, measured), session/prompt (blocking), session/cancel and
// session/set_mode onto the turn state machine, Interaction and reconciliation. The turn
// machine itself is the shared acp.Handle (as for copilot and kiro); this file holds the
// registry, cursor's launch flags and the one cursor-specific difference:
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
	if m.Kind != session.KindCursor {
		return nil, errors.New("cursor driver は cursor セッション専用です")
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
	Kind:    session.KindCursor,
	Name:    "cursor",
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
// conversation stays on cursor's server — a later Resume re-spawns and
// session/load reattaches (measured: the history is replayed and the context kept).
func DropHandle(name string) {
	if h, ok := managedKind.Take(name); ok {
		h.Detach()
	}
}

// RemoveLedger drops the ClientMessageID ledger. Only for /stop, which discards the slot's
// identity as well; halt/archive keep it because those sessions can resume.
func RemoveLedger(name string) { managedKind.RemoveLedger(name) }

// ManagedAlive reports whether the session has a live runtime handle.
func ManagedAlive(name string) bool { return managedKind.Alive(name) }

// ManagedBusy reports a turn is running or queued (the wait condition for a graceful
// shutdown).
func ManagedBusy(name string) bool { return managedKind.Busy(name) }

// ManagedInFlight reports that the handle holds an entry it took from the queue and has not
// settled. The pump gives its move-gate lease back once the runtime has the input, which can be
// before the runtime's own record (and so DriveState) shows the turn; a move asks this, under the
// exclusive gate, so it never starts in that window (ADR 0115 decision 11.2). Entries still
// queued do not count: the move carries them.
func ManagedInFlight(name string) bool { return managedKind.InFlight(name) }

// AbortManaged interrupts every running managed turn (the equivalent of graceful
// shutdown's per-pane Ctrl-C).
func AbortManaged() { managedKind.Abort() }

// Shutdown terminates every managed child when the agent exits. The source of truth for
// the conversation stays on cursor's server, and the next boot's ReconcileManaged
// reattaches through session/load.
func Shutdown() { managedKind.Shutdown() }

// ReconcileManaged re-attaches managed cursor sessions after an Agent boot or child
// death. It covers every managed meta that is not marked as stopped. On failure the
// session simply stays stopped and the user's Resume click retries it.
func ReconcileManaged(reason string) { managedKind.Reconcile(reason) }

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

// kind is what the shared ACP driver body needs from cursor.
var kind = &acp.Kind{
	Name:        "cursor",
	StreamState: true, // ACP writes no local trace, so the transcript is built from the stream
	DynamicMode: true,
	// Longer than stopChild's SIGTERM → SIGKILL sequence (3 s).
	ExitRecordWait:   5 * time.Second,
	LoadFailed:       "cursor セッションを読み込めませんでした（時間をおいて再開してください）",
	SettingsRejected: "cursor はモデルの稼働中変更に未対応です（セッションを作り直してください）",
	Ledger:           ledger,
	Sids:             sids,
	NewClient:        newACPClient,
	Command:          command,
	StopChild:        func(cmd *exec.Cmd, _ io.Closer) { stopChild(cmd) },
	// StoreGone is nil: cursor has no local store, so "the conversation really is gone" cannot
	// be decided here (same reasoning as kiro A2-1) and a failed session/load stays an error.
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

// threadHandle is a managed cursor session: the shared ACP handle plus cursor's own reading of
// the stream (onNotify).
type threadHandle struct {
	*acp.Handle
}

func newThreadHandle(name, dir, slotSid string) *threadHandle {
	h := &threadHandle{}
	h.Handle = acp.NewHandle(kind, acp.Hooks{OnNotify: h.onNotify}, name, dir, slotSid)
	return h
}

// command builds the `cursor-agent acp` child.
func command(h *acp.Handle, st agents.ThreadSettings) *exec.Cmd {
	// Base args: suppress the background self-update (a root option, so it must come before
	// acp — measured), the acp subcommand, and skipping workspace trust (measured: without
	// --trust even ACP hangs on the trust prompt). Plan mode drops --force so approvals
	// surface as Interactions.
	args := []string{disableAutoUpdateFlag, "acp", "--trust"}
	if h.BypassNow() && st.Mode != "plan" {
		args = append(args, "--force") // the fleet default bypass ("unless explicitly denied")
	}
	if st.Model != "" && st.Model != "auto" {
		// Pin the model for this per-session child: a launch flag, because ACP has no
		// per-session way to select one.
		args = append(args, "--model", st.Model)
	}
	cmd := exec.Command(bin(), args...)
	// Own process group, so a left-over worker-server can be killed along with the group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Authentication is ambient: the CLI picks up ~/.config/cursor/auth.json itself
	// (measured: a turn completes with no env injection). Only CI is stripped (ci_env.go).
	// cursor scrubs its MCP children's environment (measured: they get HOME, PATH, SHELL and
	// TERM only), so the name reaches the af server through the ${env:…} references mcpreg
	// writes into ~/.cursor/mcp.json, which cursor expands from THIS process's environment.
	cmd.Env = agents.WithSessionEnv(EnvWithoutCI(os.Environ()), h.SessionName())
	return cmd
}

// onNotify accumulates the transcript from session/update on the readLoop goroutine.
// MUST be fast (no RPC, no h.mu) — it only appends to the transcript buffer (tmu)
// and, for current_mode_update, records the mode under h.mu.
func (h *threadHandle) onNotify(method string, params json.RawMessage) {
	h.Touch()
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
		agents.PublishCommands(h.SessionName(), cmds)
	case "user_message_chunk":
		h.Buf().UserChunk(u.Content.Text)
	case "agent_message_chunk":
		h.Buf().AgentChunk(u.Content.Text)
	case "agent_thought_chunk":
		h.Buf().ThoughtChunk(u.Content.Text)
	case "tool_call":
		h.NoteToolCall(u.SessionUpdate, u.ToolCallID, u.Status)
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
		h.Buf().ToolCall(u.ToolCallID, u.Title, toolInfo(u.RawInput), file, verb, edits)
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

// managedLiveState is the live state of the managed route, read by state.go's LiveState
// (acp.Handle.LiveState says why it exists).
func managedLiveState(m session.Meta) string {
	h := handleFor(m.Name)
	if h == nil {
		return "" // stopped or not attached: hold no opinion about the state
	}
	return h.LiveState()
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
	td := agents.TranscriptData{Turns: h.Buf().Snapshot()}
	// Model badge: the model the user picked explicitly (settings.Model, the picker's dash
	// form), or for Auto or no choice ACP's currentModelId, which is default[] (docs/log/40
	// §model display).
	model := h.Enrich(&td)
	stampModel(td.Turns, displayModel(model))
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
