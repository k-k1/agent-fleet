package acp

// The managed driver body the ACP kinds share: one child process per session, with
// session/new, session/load (cross-process resume, measured for every kind), session/prompt
// (blocking), session/cancel and session/set_mode mapped onto the turn state machine,
// Interaction and reconciliation (copilot: docs/log/36, cursor: docs/log/40, kiro: docs/log/43
// Track A2).
//
// Why one child per session: ACP has no per-session model selection (the models in a
// session/new response are an enumeration only), so a per-child `--model` flag is the reliable
// route. Permission requests (session/request_permission) do not happen under the kinds' bypass
// flags, but plan mode drops the flag and can therefore reach them. Never trust "it does not
// appear in the UI, so it cannot happen" (the agy df996e4 lesson): always map it to an
// Interaction(question).
//
// What differs between the kinds is a Kind (per-kind parameters and hooks) and the Hooks a
// handle gets from its kind's threadHandle. The kind keeps its own session/update dispatch
// (Hooks.OnNotify) and everything outside the handle: the registry, Resume's checks, the read
// layer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// Kind is what one ACP kind's driver tells the shared body. One value per kind package.
type Kind struct {
	// Name is the kind as users read it in errors and as the Agent log prefixes it ("kiro").
	Name string
	// StreamState: the kind builds its live transcript (TranscriptBuf) and its tool progress
	// (Activity, InFlight) from the ACP stream. copilot reads both from its events.jsonl, so its
	// handles never touch them.
	StreamState bool
	// DynamicMode: UpdateSettings changes the mode through session/set_mode. Without it (kiro,
	// whose mode is a launch flag) a mode change is refused like a model change.
	DynamicMode bool
	// Effort: Start carries meta's effort into the settings the child is launched with.
	Effort bool
	// ExitRecordWait bounds how long a failed spawn waits for watch: longer than StopChild's
	// whole sequence, so a child that ignores the first signal is still counted.
	ExitRecordWait time.Duration
	// LoadFailed is spawn's error when session/load fails while the session's store is intact.
	LoadFailed string
	// SettingsRejected is UpdateSettings' error for a change the running child cannot take.
	SettingsRejected string

	Ledger *agents.MsgLedger
	Sids   agents.SidStore

	// NewClient connects the JSON-RPC client to the child's stdio. It must not start reading:
	// the driver calls Start once the callbacks are known.
	NewClient func(stdin io.Writer, stdout io.Reader) *Client
	// Command builds the child for st: binary, args, env, SysProcAttr. Start sets Dir.
	Command func(h *Handle, st agents.ThreadSettings) *exec.Cmd
	// StopChild terminates a child; stdin is the pipe to it, for a kind that stops by EOF.
	StopChild func(cmd *exec.Cmd, stdin io.Closer)
	// StoreGone reports that sid's local store is gone, i.e. the conversation was deleted, which
	// is the only case in which a failed session/load may fall back to session/new. nil: the
	// kind has no local store (cursor), so a failed load is never read as deleted.
	StoreGone func(sid string) bool
	// BeforeSpawn runs after Start resolved the settings and before the child starts (copilot
	// materialises a fork there). nil: nothing.
	BeforeSpawn func(m session.Meta, slotSid string) error
	// ModeID maps the AF mode ("plan" / "normal") to the kind's ACP mode id; ModeFromACP maps
	// back, "" for an id that says nothing.
	ModeID, ModeFromACP func(string) string
	// NormalizeMsgID fills in a ClientMessageID the caller left empty.
	NormalizeMsgID func(string) string
	// PermissionDetail is the line a permission card adds under the tool call's title, from its
	// rawInput. ok=false drops the request unanswered, which is what copilot did with a rawInput
	// it could not decode.
	PermissionDetail func(raw json.RawMessage) (detail string, ok bool)
}

// Hooks are a handle's callbacks into its kind's threadHandle.
type Hooks struct {
	// OnNotify handles a notification on the readLoop goroutine; it must not block. nil: the
	// kind reads no notification (copilot).
	OnNotify func(method string, params json.RawMessage)
	// DiscoverSid finds the session to load when no sid is stored. nil: none.
	DiscoverSid func() string
	// Load runs session/load. nil: one call, with the transcript replay when StreamState.
	Load func(cl *Client, sid string) (json.RawMessage, error)
	// Spawned runs once a spawn committed, with the runtime's currentModelId. nil: nothing.
	Spawned func(modelID string)
}

var (
	_ agents.ThreadHandle   = (*Handle)(nil)
	_ agents.QueueingSender = (*Handle)(nil)
)

// Handle is one managed session's runtime: its child, its turn queue and the turn in flight.
type Handle struct {
	kind  *Kind
	hooks Hooks

	name    string
	dir     string
	slotSid string

	spawnMu sync.Mutex // serializes spawns for this handle (no double spawn from a concurrent Resume; kiro A2-4)

	// bypass is the resolved "skip permission prompts" choice (docs/log/76). Start resolves it
	// from meta and stores it here because spawn has no meta of its own. Plan mode is not read
	// at Start time but from spawn's st.Mode, since a mode change while running re-spawns the
	// child.
	bypass bool

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser // kept so a kind that stops by EOF can close it (kiro's .lock release)
	exited  chan struct{}  // closed by watch when the current child exits (DetachWait)
	cl      *Client
	sid     string // the runtime's session id
	model   string // ACP currentModelId, for the model badge
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
	permOpts     []string        // Interaction option index -> ACP optionId
	events       chan agents.Event

	buf TranscriptBuf // transcript built from ACP session/update (guarded by its own lock; StreamState)

	// act stamps every message the child sends, for the busy-row progress bound (StreamState).
	act agents.Activity
	// calls holds the ACP tool calls reported open and not finished (NoteToolCall; StreamState).
	calls agents.InFlight
}

// NewHandle is a handle with no child yet; Start or Spawn gives it one.
func NewHandle(k *Kind, hooks Hooks, name, dir, slotSid string) *Handle {
	return &Handle{
		kind:    k,
		hooks:   hooks,
		name:    name,
		dir:     dir,
		slotSid: slotSid,
		q:       agents.NewTurnQueue(name, k.Ledger, agents.LedgerAtTake),
		events:  make(chan agents.Event, 64),
	}
}

// SessionName is the session the handle runs.
func (h *Handle) SessionName() string { return h.name }

// Dir is the child's working directory.
func (h *Handle) Dir() string { return h.dir }

// Buf is the transcript the kind's OnNotify builds (StreamState).
func (h *Handle) Buf() *TranscriptBuf { return &h.buf }

// Touch stamps a message from the child (StreamState).
func (h *Handle) Touch() { h.act.Touch() }

// Connected reports a live child behind a client that is still open.
func (h *Handle) Connected() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.alive && h.cl != nil && !h.cl.Dead()
}

// Start gives the handle a child unless it has a live one: it resolves the settings from m and
// spawns. This is Resume's shared half; the kind's Resume does the checks and the registry.
func (h *Handle) Start(m session.Meta) error {
	// Serialize spawns per handle (kiro A2-4): if boot's ReconcileManaged and a /turn right after
	// it call Resume concurrently, check-then-spawn interleaves, two children are spawned and the
	// earlier one is orphaned. Re-check liveness after taking the lock.
	h.spawnMu.Lock()
	defer h.spawnMu.Unlock()

	h.mu.Lock()
	if h.alive && h.cl != nil && !h.cl.Dead() {
		h.mu.Unlock()
		return nil
	}
	if h.settings.Model == "" {
		h.settings.Model = m.Model
	}
	if h.kind.Effort && h.settings.Effort == "" {
		h.settings.Effort = m.Effort
	}
	if h.settings.Mode == "" {
		h.settings.Mode = m.Mode
	}
	// Whether to skip permission prompts (docs/log/76) is resolved from meta and ui-prefs on
	// every Resume. It does not live in ThreadSettings because that struct is a dynamic update
	// where "empty = leave unchanged", which cannot express three states for a bool. A re-spawn
	// after a settings change therefore uses the value resolved here.
	h.bypass = agents.SkipPermissions(m)
	st := h.settings
	h.mu.Unlock()

	if h.kind.BeforeSpawn != nil {
		if err := h.kind.BeforeSpawn(m, h.slotSid); err != nil {
			return err
		}
	}
	if err := h.Spawn(st); err != nil {
		return err
	}

	// Baseline for exit recording (the same role as the TUI's startSessionTmux).
	base, _ := status.OOMKillCount()
	status.PersistExit(m.Name, status.ExitInfo{OOMBase: base})
	return nil
}

// BypassNow reports the resolved "skip permission prompts" choice (docs/log/76). Start writes
// it under h.mu; spawn runs without the lock, so read it through here.
func (h *Handle) BypassNow() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.bypass
}

// Spawn starts the child runtime, initializes ACP and loads/creates the session.
// Caller must NOT hold h.mu.
func (h *Handle) Spawn(st agents.ThreadSettings) error {
	k := h.kind
	cmd := k.Command(h, st)
	cmd.Dir = h.dir
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
		return fmt.Errorf("%s runtime を起動できません: %w", k.Name, err)
	}
	defer tail.Settle() // after any failure snapshot; see StderrTail.Release
	// Closed by watch once the exit is recorded; a failed start waits on it (awaitExitRecord).
	exited := make(chan struct{})
	// Snapshot the tail before StopChild: the stop sequence can make the CLI print noise
	// that pushes the real cause out of the budget.
	fail := func(err error) error {
		err = tail.Wrap(err)
		k.StopChild(cmd, stdin)
		h.awaitExitRecord(exited)
		return err
	}
	cl := k.NewClient(stdin, stdout)
	cl.Start(h.callbacks())
	go h.watch(cmd, tail, cl, exited)

	if _, err := cl.Call("initialize", map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{},
	}, 30*time.Second); err != nil {
		return fail(fmt.Errorf("%s runtime の initialize に失敗しました: %w", k.Name, err))
	}

	sid := h.sid
	if sid == "" {
		sid = k.Sids.Read(h.slotSid)
	}
	if sid == "" && h.hooks.DiscoverSid != nil {
		if d := h.hooks.DiscoverSid(); d != "" {
			sid = d
			k.Sids.Write(h.slotSid, d)
		}
	}
	mode := ""
	modelID := ""
	if sid != "" {
		// Cross-process resume (measured: the replay restores both history and context). The
		// replay is proportional to the conversation's length, so allow plenty of time.
		res, err := h.load(cl, sid)
		if err != nil {
			// Falling back to session/new is allowed only when sid's local store is actually
			// gone, i.e. the conversation was deleted (kiro A2-1). Doing it on a transient failure
			// (timeout, lock contention, ...) with the store intact would silently detach a live
			// conversation and overwrite the slot's sid with an empty new one, so return a
			// retryable error and leave the session stopped instead. The decision uses the
			// store's presence on disk, never a brittle string match.
			if k.StoreGone != nil && k.StoreGone(sid) {
				log.Printf("%s managed: session/load %s: store gone (%v) — restarting with a new session", k.Name, h.name, err)
				sid = ""
				if k.StreamState {
					h.buf.Reset()
				}
			} else {
				// Do not leave a partial replay in buf: it would be shown as the transcript.
				if k.StreamState {
					h.buf.Reset()
				}
				return fail(fmt.Errorf("%s: %w", k.LoadFailed, err))
			}
		} else {
			mode = currentModeOf(res)
			modelID = currentModelOf(res)
		}
	}
	if sid == "" {
		res, err := cl.Call("session/new", map[string]any{
			"cwd": h.dir, "mcpServers": []any{},
		}, 60*time.Second)
		if err != nil {
			return fail(fmt.Errorf("%s セッションを作成できません: %w", k.Name, err))
		}
		var out struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(res, &out) != nil || out.SessionID == "" {
			return fail(errors.New(k.Name + " セッションの作成応答を解釈できません"))
		}
		sid = out.SessionID
		k.Sids.Write(h.slotSid, sid) // shared with the read layer
		mode = currentModeOf(res)
		modelID = currentModelOf(res)
	}

	h.mu.Lock()
	h.cmd, h.stdin, h.cl, h.sid, h.alive = cmd, stdin, cl, sid, true
	if k.StreamState {
		h.calls.Unknown() // a new connection: calls open on the old one (or in a replay) are not known
	}
	h.exited = exited              // this child's exit signal (DetachWait's bounded wait)
	h.state = agents.TurnCompleted // the child is brand new, so no turn is running
	h.model = modelID              // ACP currentModelId for the model badge
	h.inter, h.permID, h.permOpts = nil, nil, nil
	if m := k.ModeFromACP(mode); m != "" {
		h.settings.Mode = m
	}
	wantMode := h.settings.Mode
	h.mu.Unlock()

	if h.hooks.Spawned != nil {
		h.hooks.Spawned(modelID)
	}

	// Re-assert the mode when meta's wanted mode differs from the runtime's current one,
	// which guards against falling back to the default after a resume (best-effort).
	if wantMode != "" && wantMode != k.ModeFromACP(mode) {
		_, _ = cl.Call("session/set_mode", map[string]any{
			"sessionId": sid, "modeId": k.ModeID(wantMode),
		}, 15*time.Second)
	}
	// Input a lost child left queued starts now, in order, rather than waiting for the next
	// accept to restart the pump.
	h.resumePump()
	return nil
}

// load runs session/load through the kind's hook, or once. With StreamState the replay's
// notifications rebuild buf, so it is emptied first or the content is counted twice.
func (h *Handle) load(cl *Client, sid string) (json.RawMessage, error) {
	if h.hooks.Load != nil {
		return h.hooks.Load(cl, sid)
	}
	if h.kind.StreamState {
		h.buf.Reset()
		h.buf.SetLoading(true)
	}
	res, err := cl.Call("session/load", map[string]any{
		"sessionId": sid, "cwd": h.dir, "mcpServers": []any{},
	}, 180*time.Second)
	if h.kind.StreamState {
		h.buf.SetLoading(false) // flush the last assistant turn
	}
	return res, err
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
// cursor's Auto comes back as `default[]` and an explicit choice in bracket form (docs/log/40
// §model display); kiro's auto is "auto" and a named model the bare id.
func currentModelOf(res json.RawMessage) string {
	var out struct {
		Models struct {
			CurrentModelID string `json:"currentModelId"`
		} `json:"models"`
	}
	_ = json.Unmarshal(res, &out)
	return out.Models.CurrentModelID
}

// awaitExitRecord holds a failed spawn until watch has written the exit record. Without it the
// caller reports the failure before the record exists, and the write lands after the caller
// has moved on — under a test's TempDir HOME, while or after that tree is removed.
func (h *Handle) awaitExitRecord(exited <-chan struct{}) {
	select {
	case <-exited:
	case <-time.After(h.kind.ExitRecordWait):
	}
}

// watch reaps the child and records its exit. With one child per session the exit can be
// attributed exactly, unlike a daemon supervisor. An exit caused by a stop (Detach/Down)
// becomes "stopped" and the Console shows the ordinary stopped state.
func (h *Handle) watch(cmd *exec.Cmd, tail *agents.StderrTail, cl *Client, exited chan struct{}) {
	defer close(exited) // releases a failed spawn's awaitExitRecord
	_ = cmd.Wait()
	tail.Release()
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
	cl.MarkClosed()
	h.mu.Lock()
	stale := h.cl != cl // already replaced by a newer child: this is an old watch after a respawn
	h.mu.Unlock()
	if !stale {
		h.runtimeLost()
	}
}

// emit pushes an event without ever blocking a state transition (drop on overflow).
func (h *Handle) emit(e agents.Event) {
	select {
	case h.events <- e:
	default:
	}
}

func (h *Handle) setState(st agents.TurnState) {
	h.mu.Lock()
	h.state = st
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: st})
}

// TurnState is the handle's current turn state.
func (h *Handle) TurnState() agents.TurnState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// runtimeLost drops the handle to unknown, the honest state once the child is gone.
func (h *Handle) runtimeLost() {
	h.mu.Lock()
	h.alive = false
	h.state = agents.TurnUnknown
	h.inter, h.permID, h.permOpts = nil, nil, nil
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnUnknown})
}

// SetRuntimeMode records a mode the runtime reported (current_mode_update) and tells
// subscribers.
func (h *Handle) SetRuntimeMode(mode string) {
	h.mu.Lock()
	h.settings.Mode = mode
	cur := h.settings
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "settings", Settings: &cur})
}

// --- registry-side operations (the kind's package-level functions) ------------

// Alive reports whether the handle has a live runtime.
func (h *Handle) Alive() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.alive
}

// LiveModel is the runtime's currentModelId and whether the runtime is alive, read together.
func (h *Handle) LiveModel() (model string, alive bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.model, h.alive
}

// Busy reports a turn is running or queued (the wait condition for a graceful shutdown).
func (h *Handle) Busy() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running || h.q.Len() > 0
}

// HoldsEntry reports that the handle holds an entry it took from the queue and has not settled.
// The pump gives its move-gate lease back once the runtime has the input, which can be before
// the runtime's own record (and so DriveState) shows the turn; a move asks this, under the
// exclusive gate, so it never starts in that window (ADR 0115 decision 11.2). Entries still
// queued do not count: the move carries them.
func (h *Handle) HoldsEntry() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running || h.q.Head() != nil
}

// Abort interrupts the running turn, if there is one (the equivalent of graceful shutdown's
// per-pane Ctrl-C).
func (h *Handle) Abort() {
	h.mu.Lock()
	running := h.running
	h.mu.Unlock()
	if running {
		_ = h.interruptAll()
	}
}

// Detach takes the handle off its runtime (stop/halt/archive): it drops the queue, cancels a
// running turn and stops the child. The conversation stays with the runtime; a later Resume
// re-spawns and session/load reattaches. The channel closes when the child has exited.
func (h *Handle) Detach() <-chan struct{} {
	h.mu.Lock()
	h.alive = false
	h.q.DropAll()
	cmd, cl, stdin, sid, running, exited := h.cmd, h.cl, h.stdin, h.sid, h.running, h.exited
	h.mu.Unlock()
	if running && cl != nil && sid != "" {
		_ = cl.NotifyPeer("session/cancel", map[string]any{"sessionId": sid})
	}
	h.kind.StopChild(cmd, stdin)
	return exited
}

// Down marks the handle dead for Agent shutdown and returns the stop of its child, which the
// caller runs once it has released the registry.
func (h *Handle) Down() (stop func()) {
	h.mu.Lock()
	h.alive = false
	cmd, stdin := h.cmd, h.stdin
	h.mu.Unlock()
	return func() { h.kind.StopChild(cmd, stdin) }
}

// --- ThreadHandle interface ---------------------------------------------------

func (h *Handle) Send(in agents.TurnInput) error {
	_, err := h.accept(in)
	return err
}

// SendQueued is Send reporting whether the input was held behind a running turn.
func (h *Handle) SendQueued(in agents.TurnInput) (bool, error) { return h.accept(in) }

// Steer is a driver-side queue: ACP has no mid-turn injection point, so the input is
// submitted as the next turn once the current one finishes.
func (h *Handle) Steer(in agents.TurnInput) error {
	_, err := h.accept(in)
	return err
}

// accept queues the input and starts the pump when none runs. queued reports that the input
// waits behind a running turn (or behind earlier queued input) rather than starting now.
func (h *Handle) accept(in agents.TurnInput) (queued bool, err error) {
	if strings.TrimSpace(in.Prompt) == "" {
		return false, errors.New("empty prompt")
	}
	in.ClientMessageID = h.kind.NormalizeMsgID(in.ClientMessageID)
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
func (h *Handle) pump() {
	for {
		// Each entry gets a lease of its own on the session's lifecycle gate (ADR 0115 decision
		// 11.1), taken before it leaves the queue and held until the runtime has it. The input
		// path that queued it released its lease at the accept and does not wait for this, so a
		// move finds every accepted entry either handed over (and in the transcript) or still
		// queued, where its snapshot reads it. Taken without h.mu: a move reads the queue under
		// h.mu while it holds the gate.
		lease, err := lifegate.Enter(context.Background(), h.name)
		if err != nil {
			h.parkForMove(err)
			return
		}
		h.mu.Lock()
		// A closed client counts as gone before watch marks the handle: the failed turn wakes
		// this loop first, and the next input would be taken, recorded in the ledger and lost
		// on a dead pipe. So the check comes before Take, and the input stays queued for
		// resumePump after the respawn.
		if !h.alive || h.cl == nil || h.cl.Dead() {
			h.pumping = false
			h.mu.Unlock()
			lease.Release()
			return
		}
		t := h.q.Take()
		if t == nil {
			h.pumping = false
			h.mu.Unlock()
			lease.Release()
			return
		}
		h.running = true
		h.mu.Unlock()

		h.runTurn(t, lease)

		h.mu.Lock()
		h.q.Settle(t)
		h.running = false
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
func (h *Handle) parkForMove(err error) {
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

// resumePump restarts the pump for input that was queued when the child died: the pump exits
// on a dead child and leaves the queue in place.
func (h *Handle) resumePump() {
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
//
// lease is the entry's hold on the lifecycle gate (pump), released once session/prompt is on the
// child's stdin; a turn can run for hours and must not hold a move off.
func (h *Handle) runTurn(t *agents.Taken, lease *lifegate.Lease) {
	defer lease.Release()
	agents.MarkTurnStartRun(h.slotSid, t.In)
	defer func() { agents.MarkTurnEnd(h.slotSid, h.TurnState()) }()
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
	stream := h.kind.StreamState
	if stream {
		// ACP emits no user_message_chunk during a live turn (measured), so the user turn is
		// committed to the transcript here — a separate path from the replay's
		// user_message_chunk. Only after Commit: a cancelled entry never became a turn.
		h.buf.AddUserTurn(t.In.Prompt)
	}
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
	written := false
	if stream {
		h.calls.Reset() // a turn boundary on a live connection: nothing from before it is open
	}
	res, err := cl.CallWritten("session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": t.In.Prompt}},
	}, 0, func() {
		written = true
		// The runtime has the input now. A write that fails keeps the lease (the deferred release)
		// until the entry is requeued below, so a move never snapshots it in neither place.
		lease.Release()
		h.received(t, cl, sid)
	}) // no timeout — a turn runs as long as it runs
	if stream {
		h.calls.Reset()
		h.buf.FlushAsst() // close the open assistant turn: ACP has no turn_ended notification
	}
	h.mu.Lock()
	interrupted := h.stopping
	h.inter, h.permID, h.permOpts = nil, nil, nil // the turn ended = nothing is waiting
	h.mu.Unlock()
	if err != nil && !written && cl.Dead() {
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
func (h *Handle) received(t *agents.Taken, cl *Client, sid string) {
	h.mu.Lock()
	stop := h.q.Received(t)
	h.mu.Unlock()
	if stop {
		_ = h.deliverStop(cl, sid)
	}
}

// Interrupt is the Console's stop (ADR 0105 decisions 1-3). The queue decides what the stop
// is; this delivers it to the entry in flight.
func (h *Handle) Interrupt(opts agents.InterruptOpts) (agents.InterruptResult, error) {
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
func (h *Handle) stopLocked(head agents.HeadAction) error {
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
func (h *Handle) deliverStop(cl *Client, sid string) error {
	h.mu.Lock()
	permID := h.permID
	if permID != nil {
		h.inter, h.permID, h.permOpts = nil, nil, nil
	}
	h.mu.Unlock()
	err := cl.NotifyPeer("session/cancel", map[string]any{"sessionId": sid})
	if permID != nil {
		_ = cl.Respond(permID, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
	}
	return err
}

// RemoveQueued takes one entry out while it is cancellable (decision 5).
func (h *Handle) RemoveQueued(id string) (agents.QueueItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.q.Remove(id)
}

// DismissDiscard drops a kept discard (decision 4).
func (h *Handle) DismissDiscard(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.q.DismissDiscard(id)
}

// interruptAll is the stop for Agent shutdown (decision 8): the whole queue goes, nothing is
// kept for return, and the turn in flight is stopped.
func (h *Handle) interruptAll() error {
	h.mu.Lock()
	h.q.DropAll()
	out := h.q.Interrupt(agents.InterruptOpts{}, h.q.Head() != nil)
	return h.stopLocked(out.Head)
}

// UpdateSettings applies dynamic settings. Mode is native through session/set_mode (measured)
// for a DynamicMode kind. Model and effort are pinned by the child's launch flags and cannot be
// changed while running: Capabilities declares DynamicModel:false and the Console shows no UI
// for it, but an explicit error is returned defensively.
func (h *Handle) UpdateSettings(s agents.ThreadSettings) error {
	if s.Model != "" || s.ClearModel || s.Effort != "" || s.ClearEffort || (!h.kind.DynamicMode && s.Mode != "") {
		return errors.New(h.kind.SettingsRejected)
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
	if _, err := cl.Call("session/set_mode", map[string]any{
		"sessionId": sid, "modeId": h.kind.ModeID(s.Mode),
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

// Respond answers the pending Interaction, a reply to session/request_permission:
// answer/allow maps the option index to an ACP optionId, deny picks a reject option and cancel
// sends outcome:"cancelled".
func (h *Handle) Respond(reply agents.InteractionReply) error {
	h.mu.Lock()
	inter, permID, permOpts, cl := h.inter, h.permID, h.permOpts, h.cl
	h.mu.Unlock()
	if inter == nil || inter.ID != reply.ID || cl == nil {
		return fmt.Errorf("interaction %s is not pending: %w", reply.ID, agents.ErrInteractionNotPending)
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
	if err := cl.Respond(permID, result); err != nil {
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

func (h *Handle) Events() <-chan agents.Event { return h.events }

func (h *Handle) Snapshot() (agents.ThreadSnapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return agents.ThreadSnapshot{
		TurnState:   h.state,
		Interaction: h.inter,
		Settings:    h.settings,
	}, nil
}

// onServerRequest handles server-initiated requests on the readLoop goroutine — MUST NOT
// block: record the Interaction and return; the answer goes back later via Respond ->
// cl.Respond. It does not happen under the bypass flags, but plan mode can reach it.
func (h *Handle) onServerRequest(cl *Client, id json.RawMessage, method string, params json.RawMessage) {
	if h.kind.StreamState {
		h.act.Touch()
	}
	if method != "session/request_permission" {
		// An unknown server-initiated request wedges the turn if it goes unanswered, so
		// return an error.
		_ = cl.RespondError(id, -32601, "unsupported request: "+method)
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
	detail, ok := h.kind.PermissionDetail(req.ToolCall.RawInput)
	if !ok {
		return
	}
	q := transcript.Question{Header: "許可", Question: req.ToolCall.Title}
	if detail != "" {
		q.Question += "\n`" + detail + "`"
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

// PendingPermission folds a pending ACP `session/request_permission` into one line saying
// what was being asked (docs/log/75 P5). Empty when nothing is pending.
func (h *Handle) PendingPermission() string {
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

// LiveState is the live state of the managed route, read by the kind's LiveState.
//
// Why it exists: ACP writes no local transcript the TUI classifier could read, so the JSONL tail
// classification always returns empty for managed sessions, leaving both the list chip and the
// reaper's classification input blank. A managed session stuck waiting for an approval then fell
// to "unknown" (in docs/log/75's vocabulary), where tier 1 never folds it and tier 2 never keeps
// it awake. The turn state machine is the only source of truth, so it feeds this.
//
// Waiting for an approval is called question to match the vocabulary of the permission card
// the mirror draws (td.Pending): when the badge in the list and the chip in the body
// disagree, the user cannot tell which is true (the same lesson as EffectiveModal). The
// carry-over Kind being permission is a different axis, deciding what can be delivered
// after a resume.
func (h *Handle) LiveState() string {
	switch h.TurnState() {
	case agents.TurnWaitingInteraction:
		return "question"
	case agents.TurnQueued, agents.TurnStarting, agents.TurnRunning, agents.TurnInterrupting:
		return "working"
	}
	return "idle"
}

// queuedPrompts surfaces the driver-held queue for the mirror's "queued" badge.
func (h *Handle) queuedPrompts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.q.Texts()
}

// queueView is what the messages payload shows of the queue: its items (decision 5) and the
// kept discards (decision 4).
func (h *Handle) queueView() ([]agents.QueueItem, []agents.Discard) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.q.Items(), h.q.Discards()
}

// Enrich folds the driver-side state into the read layer's TranscriptData: it puts the
// Interaction id on a pending permission, merges the driver-held queue into the queued list and
// the driver's mode setting into the chip. model is the model badge, read under the same lock:
// the model the user picked explicitly (settings.Model), or for auto or no choice the runtime's
// currentModelId.
func (h *Handle) Enrich(td *agents.TranscriptData) (model string) {
	h.mu.Lock()
	inter := h.inter
	modeSet := h.settings.Mode
	model = h.settings.Model
	if model == "" || model == "auto" {
		model = h.model
	}
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
	return model
}

// ToolProgress is the newest sign of life (StreamState). The child sends nothing while a tool
// runs, but the tool is a process under it, which toolIn finds in the process table (the kinds
// differ in how the tool sits under the child). A child that cannot be located in the process
// table (no process, an unreadable /proc) is "could not observe", not "no tool": the stale stamp
// then gives no answer and the row keeps holding.
func (h *Handle) ToolProgress(now time.Time, table func() map[int]procx.Info,
	toolIn func(pid int, tab map[int]procx.Info) bool) (time.Time, bool) {
	return h.act.Progress(now, func() (alive, observed bool) {
		h.mu.Lock()
		cmd := h.cmd
		h.mu.Unlock()
		if cmd == nil || cmd.Process == nil {
			return false, false
		}
		tab := table()
		if _, ok := tab[cmd.Process.Pid]; !ok {
			return false, false
		}
		callsAlive, callsKnown := h.calls.State()
		return callsAlive || toolIn(cmd.Process.Pid, tab), callsKnown
	})
}

// NoteToolCall keeps the set of open ACP tool calls (StreamState). A tool that is not a local
// process (an MCP server's, or work done inside a resident helper) leaves nothing in the process
// table, so the protocol's own status is the only trace of it.
func (h *Handle) NoteToolCall(sessionUpdate, id, status string) {
	switch status {
	case "completed", "failed", "cancelled":
		h.calls.Done(id)
	default:
		if sessionUpdate == "tool_call" || status == "in_progress" {
			h.calls.Add(id)
		}
	}
}

// callbacks is how a client delivers into this handle. OnRequest gets the receiving client from
// readLoop rather than reading h.cl: during the first spawn h.cl is still unassigned.
func (h *Handle) callbacks() Callbacks {
	return Callbacks{
		OnRequest: h.onServerRequest,
		OnNotify:  h.hooks.OnNotify,
	}
}
