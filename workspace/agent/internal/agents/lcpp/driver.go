// Package lcpp's managed driver (ADR 0093 decision 4): in-process, no child process and no
// daemon. Resume returns a goroutine + channel pair (threadHandle) wrapping internal/harness's
// Run loop directly against llama-server through the Control Plane's engine gateway. None of
// opencode's Supervisor skeleton (ensure/adopt/generation/drain) applies — there is no process
// to adopt or drain, only a turn goroutine's own lifetime, this file's own settle() (§4.4's
// restart recovery) and shutdown.go's AbortManaged/Shutdown hooks.
package lcpp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpc"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// engineKey is the fixed catalogue key the chat role always uses (control-plane/engines.go's
// {"llm","image","comfy"} — the same constant chatx's own lcppEngineKey pins).
const engineKey = "llm"

// ledger is the persistent ClientMessageID ledger (docs/log/27 §4): makes a resend, or a
// double submission after a reconnect, idempotent.
var ledger = agents.NewMsgLedger("lcpp-msgledger")

// newHarnessClient is harness.NewClient behind a var, so a test can substitute a fake Client
// without a live engine (the same func-var seam shape as harness.EngineToken/EngineWindow).
var newHarnessClient = harness.NewClient

// NewDriver returns the managed lcpp Driver. The read layer is kept as-is by embedding
// agentImpl (agent.go).
func NewDriver() agents.Driver { return managedDriver{} }

type managedDriver struct{ agentImpl }

// Capabilities. ProcessModel is "in-process" (decision 4's fourth value — driver.go's own doc
// comment). Steer queues the next input as the next top-level turn: harness.Run is one
// blocking call with no hook to inject a message mid-tool-loop, so "at the next tool
// boundary" (decision 4's wording) is approximated as "as soon as the current Run call
// returns" — the same simplification kiro/copilot/cursor already make for the same reason
// (no native mid-turn injection). Fork mirrors Caps().CanFork (agent.go): the runtime-level
// equivalent of the read-layer capability. DynamicModel/DynamicMode are true because
// UpdateSettings actually changes what the NEXT runTurn does (which model string is passed to
// newHarnessClient, whether rt.Plan is set) — both are exercised by driver_test.go.
// DynamicEffort stays false: llama-server's reasoning_budget is documented (ADR 0093
// background) as a SERVER LAUNCH FLAG, and no per-request field for it was confirmed live in
// this lane (no GPU engine was used — see this package's own report); claiming it dynamic
// without a verified per-request knob would be exactly the "unverified cap" docs/log/76
// forbids. Permissions stays false: no kind sets it (grep confirms), and lcpp's own approval
// gate reuses Questions the same way kiro's ACP session/request_permission does (approve()
// below), not a dedicated "approval" Interaction kind — nothing reads that vocabulary yet.
func (managedDriver) Capabilities() agents.Capabilities {
	return agents.Capabilities{
		ProcessModel: "in-process",
		Steer:        true,
		Fork:         true,
		DynamicModel: true,
		DynamicMode:  true,
		Questions:    true,
	}
}

// sidFor is the store sid AND the status/notify sid for m's slot: session.UUID(dir, name),
// the same deterministic id cursor's own driver pre-allocates. Decision 4 calls this "自分で
// 発番（imposed）" — since UUID is already a pure function of (dir, name), no SidStore/cache is
// needed at all: a fresh Name (recreate, a plain fork target) yields a fresh sid, and the
// store's own lazy-create-on-first-append (store.go's Open/append) means a session that never
// sent a turn leaves no file — ClearResume has nothing to clear (agent.go keeps its no-op).
func sidFor(m session.Meta) string { return session.UUID(m.Dir, m.Name) }

// Resume returns m's ThreadHandle, creating one (and running this slot's own one-time fork
// materialization and restart settle) the first time this process sees it. Idempotent: a
// second call for a name whose handle is already in the map just returns it, matching every
// other driver's contract ("starts a new thread when there is none").
func (managedDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	if m.Kind != session.KindLcpp {
		return nil, errors.New("lcpp driver は lcpp セッション専用です")
	}
	if !session.DirExists(m.Dir) {
		return nil, agents.DirGoneErr(m.Dir)
	}
	sid := sidFor(m)

	handlesMu.Lock()
	if h := handles[m.Name]; h != nil {
		handlesMu.Unlock()
		return h, nil
	}
	mcpCtx, mcpCancel := context.WithCancel(context.Background())
	h := &threadHandle{
		name:      m.Name,
		sid:       sid,
		cwd:       m.CWD(),
		store:     Open(sid),
		events:    make(chan agents.Event, 64),
		state:     agents.TurnCompleted,
		settings:  agents.ThreadSettings{Model: m.Model, Effort: m.Effort, Mode: m.Mode},
		skipPerm:  agents.SkipPermissions(m),
		mcpCtx:    mcpCtx,
		mcpCancel: mcpCancel,
		// mcpMgr's own servers are dialed lazily, on the first runTurn's syncMCPServers call, not
		// here: constructing the Manager itself does no I/O (mcpc.NewManager just starts a
		// ctx-watcher goroutine), so this stays as cheap as every other Resume field. See mcp.go.
		mcpMgr: mcpc.NewManager(mcpCtx),
	}
	handles[m.Name] = h
	handlesMu.Unlock()

	if err := ensureForked(m, sid); err != nil {
		handlesMu.Lock()
		delete(handles, m.Name)
		handlesMu.Unlock()
		return nil, err
	}
	h.settle()
	return h, nil
}

// ensureForked materializes decision 3's fork-at on this slot's FIRST Resume: m.ForkFrom (set
// by agent.go's Forker.ForkSource — the source session's own sid, since lcpp's store IS keyed
// by sid) names the source store, m.ForkAt (agent.go's ForkAtResolver.ResolveForkAt) the
// record id to cut at, or "" for "copy the whole conversation" (HandleForkSession's own
// convention: forkAt stays "" whenever no `at` was requested at all — ResolveForkAt never
// returns "" itself, see its own doc comment, so the two "" origins never collide here).
// A second call (e.g. a concurrent Resume that lost the handles-map race, though today only
// the map winner ever reaches this) is a harmless no-op: the destination file already exists.
func ensureForked(m session.Meta, sid string) error {
	if m.ForkFrom == "" {
		return nil
	}
	dst := Open(sid)
	if _, err := os.Stat(dst.Path()); err == nil {
		return nil
	}
	src := Open(m.ForkFrom)
	anchor := m.ForkAt
	if anchor == "" {
		recs, _, err := src.Records()
		if err != nil {
			return fmt.Errorf("lcpp: フォーク元を読めません: %w", err)
		}
		if len(recs) == 0 {
			return nil // nothing to copy — the new store starts empty naturally
		}
		anchor = recs[len(recs)-1].ID
	}
	if _, err := src.ForkAt(sid, anchor); err != nil && !os.IsExist(err) {
		return fmt.Errorf("lcpp: フォークに失敗しました: %w", err)
	}
	return nil
}

// --- handle registry -----------------------------------------------------------

var handlesMu sync.Mutex
var handles = map[string]*threadHandle{}

func handleFor(name string) *threadHandle {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	return handles[name]
}

// ManagedAlive reports whether name has a live handle. Unlike a child-process kind there is no
// separate "spawned but not yet alive" state — Resume creates and fully settles the handle
// before returning it, so existence in the map IS aliveness.
func ManagedAlive(name string) bool { return handleFor(name) != nil }

// ManagedBusy reports a turn running or queued (the refusal condition an exclusive driver
// switch would use — lcpp has no switch target, ManagedOnly, but kept for parity/tests).
func ManagedBusy(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running || len(h.queue) > 0
}

// DropHandle detaches a managed session from its runtime: interrupt any running turn and
// forget the handle. The store on disk is untouched, so a later Resume reattaches to the same
// conversation with no data lost — there is no process to stop, only the in-memory handle.
func DropHandle(name string) { dropHandle(name) }

// dropHandle is DropHandle returning a channel closed once closeIdleResources has finished
// (nil when name had no handle), so a caller that must not leave the handle's resources
// behind — a test's own cleanup, before the next test reuses the package-global map — can wait
// for it instead of guessing a delay.
func dropHandle(name string) <-chan struct{} {
	handlesMu.Lock()
	h := handles[name]
	delete(handles, name)
	handlesMu.Unlock()
	if h == nil {
		return nil
	}
	_ = h.Interrupt()
	// Close the store's cached write handle (Store.Close's own doc comment: optional, but
	// this IS the "eventual session-shutdown path" it names) and the MCP manager (mcpMgr.Close,
	// killing any stdio children — decision 6's "子プロセスを取り残さないこと") only once any
	// turn Interrupt just cancelled has actually finished: closing either synchronously here
	// would race runTurn's own post-Run persistence, or an in-flight tools/call, still running
	// on another goroutine (a write to an already-closed *os.File fails; a tools/call against a
	// just-killed stdio child errors instead of completing). Bounded and off the caller's own
	// goroutine, so DropHandle itself stays the fast, synchronous call every existing caller
	// (stop/halt/archive/recreate) already expects.
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.closeIdleResources()
	}()
	return done
}

// closeStoreIdleWait bounds closeIdleResources' poll — generous next to an ordinary tool round
// trip, short next to leaving a store fd or an MCP stdio child open indefinitely.
const closeStoreIdleWait = 10 * time.Second

func (h *threadHandle) closeIdleResources() {
	deadline := time.Now().Add(closeStoreIdleWait)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		running := h.running
		h.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := h.store.Close(); err != nil {
		log.Printf("lcpp: closing store for %s: %v", h.name, err)
	}
	if err := h.mcpMgr.Close(); err != nil {
		log.Printf("lcpp: closing mcp manager for %s: %v", h.name, err)
	}
	h.mcpCancel()
}

// RemoveLedger drops the ClientMessageID ledger (/stop only — halt/archive can be resumed).
func RemoveLedger(name string) { ledger.Remove(name) }

// AbortManaged interrupts every running managed turn (shutdown.go's Ctrl-C equivalent for the
// managed route).
func AbortManaged() {
	handlesMu.Lock()
	hs := make([]*threadHandle, 0, len(handles))
	for _, h := range handles {
		hs = append(hs, h)
	}
	handlesMu.Unlock()
	for _, h := range hs {
		h.mu.Lock()
		running := h.running
		h.mu.Unlock()
		if running {
			_ = h.Interrupt()
		}
	}
}

// Shutdown has nothing of its own to release (no child process, no daemon) — kept only for
// parity with the other kinds' boot/shutdown wiring (shutdown.go calls it unconditionally).
func Shutdown() {}

// ReconcileManaged re-settles every non-archived, not-deliberately-stopped lcpp managed
// session after an Agent boot (§4.4): Resume is called for each, which (for a brand new
// handle) reads the store's tail and settles TurnUnknown into completed/aborted — the eager,
// at-boot counterpart to the lazy settle a user's next /turn would otherwise trigger. Unlike a
// child-process kind there is no runtime to actually restart; this only resolves any status
// left stuck on "working" by a crash mid-turn.
func ReconcileManaged(reason string) {
	d := managedDriver{}
	for _, m := range session.ListMetas() {
		if m.Kind != session.KindLcpp || m.DriverKind() != session.DriverManaged || m.Archived {
			continue
		}
		if m.StoppedAt != "" && handleFor(m.Name) == nil {
			continue // deliberately stopped — resume only on user action
		}
		if _, err := d.Resume(m); err != nil {
			log.Printf("lcpp managed: reconcile %s (%s): %v", m.Name, reason, err)
		}
	}
}

// --- thread handle ---------------------------------------------------------------

type threadHandle struct {
	name string
	sid  string
	cwd  string

	// store is opened once, in Resume, and reused for the handle's whole life — never a fresh
	// Open(h.sid) per call. store.go's own append() now caches ITS write handle across calls
	// on the SAME *Store (docs/log/99's re-examination of decision 3: reopening per record cost
	// ~4 syscalls each), and that caching only pays off if this driver stops discarding the
	// *Store (and, with it, the fd append() had just opened) at the end of every runTurn — a
	// fresh Open(h.sid) per turn would reopen every turn and leave the previous turn's fd
	// reachable only through the finalizer, not close()'d, exactly the non-determinism #821 set
	// out to avoid. DropHandle below closes it (Store.Close's own doc comment: optional, but
	// this is precisely the "eventual session-shutdown path" it was written for).
	store *Store

	// mcpCtx/mcpCancel bound mcpMgr's whole SESSION lifetime, unlike runTurn's own per-turn ctx
	// (the cancel field below): they live from Resume until closeIdleResources, so a connected
	// stdio server survives across turns instead of redialing every single one. mcpc.NewManager's
	// own ctx-watcher goroutine closes every attached server if mcpCtx is ever cancelled without
	// an explicit mcpMgr.Close() first — belt and suspenders for "stdio 子はセッションと共に死ぬ
	// こと" (the ADR 0093 段2 MCP wiring instruction's own wording).
	mcpCtx    context.Context
	mcpCancel context.CancelFunc
	mcpMgr    *mcpc.Manager
	// mcpFailures is the per-server backoff/error state syncMCPServers (mcp.go) maintains
	// across turns: a server present here is known broken as of its own mcpFailure.err, and is
	// skipped (not retried) until mcpFailure.next. See syncMCPServers' own doc comment for why
	// this exists — a server that never connects must not cost every future turn a full
	// connect attempt.
	mcpFailures map[string]mcpFailure
	// mcpLastNotedErr is the error signature (server name -> message) the store's most recent
	// NoteMCPError record was built from — compared against the CURRENT mcpFailures snapshot
	// every syncMCPServers call so a persistently broken server is only logged to the store
	// once per STATE CHANGE, not once per turn forever.
	mcpLastNotedErr map[string]string

	// skipPerm is the resolved "skip permission confirmation" choice (docs/log/76), captured
	// once at Resume from session.Meta/ui-prefs — the same resolution every other driver makes
	// at spawn time, combined with the CURRENT settings.Mode at each runTurn (a plan launch
	// drops bypass whatever the stored preference is — agents.BypassPermissions' own rule).
	skipPerm bool

	mu           sync.Mutex
	settings     agents.ThreadSettings
	todos        []harness.TodoItem
	state        agents.TurnState
	running      bool
	pumping      bool
	queue        []agents.TurnInput
	cancel       context.CancelFunc
	runningSince time.Time
	inter        *agents.Interaction
	replyCh      chan agents.InteractionReply
	events       chan agents.Event
}

// bypassNow resolves the effective "run Mutates tools without asking" decision for mode (a
// plan launch always shows approvals — agents.BypassPermissions' own rule, applied here
// against the CURRENT settings.Mode rather than the meta captured at Resume, so a mid-session
// UpdateSettings(mode=plan) takes effect on the very next turn).
func (h *threadHandle) bypassNow(mode string) bool {
	return h.skipPerm && mode != "plan"
}

// settle is ADR 0093 §4.4's restart recovery, run once per handle (Resume, right after
// creation): read the store's tail and turn TurnUnknown into a real verdict without ever
// detecting a live runtime (there is none to detect — decision 4's "状態は検出せず発生させる"
// extends to recovery too: the record log alone is the source of truth). An assistant record
// with no ToolCalls closed the turn cleanly (completed); anything else — an assistant record
// still carrying unanswered ToolCalls, a tool result, or a user/continuation turn with no
// reply yet — means the process died mid-turn (aborted, since a resend/retry is exactly what
// fixes it, docs/log/47's distinction). Only the aborted case notifies (agents.MarkTurnEnd):
// a clean completed tail was already reported before the crash, and reporting it again would
// duplicate the operator's completion report.
func (h *threadHandle) settle() {
	recs, _, err := h.store.Records()
	if err != nil || len(recs) == 0 {
		h.state = agents.TurnCompleted
		return
	}
	last := recs[len(recs)-1]
	switch {
	case last.Kind == KindAssistant && len(last.ToolCalls) == 0:
		h.state = agents.TurnCompleted
	case last.Kind == KindAssistant, last.Kind == KindToolResult, last.Kind == KindUser, last.Kind == KindContinuation:
		h.state = agents.TurnAborted
	default: // system-note / usage — not mid-turn
		h.state = agents.TurnCompleted
	}
	if h.state == agents.TurnAborted {
		agents.MarkTurnEnd(h.sid, h.state)
	}
}

// --- ThreadHandle interface --------------------------------------------------------

func (h *threadHandle) Send(in agents.TurnInput) error  { return h.accept(in) }
func (h *threadHandle) Steer(in agents.TurnInput) error { return h.accept(in) }

// accept queues in as the next turn (see Capabilities' own doc comment on the Steer
// simplification) and starts the pump goroutine if it is not already draining the queue.
func (h *threadHandle) accept(in agents.TurnInput) error {
	if strings.TrimSpace(in.Prompt) == "" {
		return errors.New("empty prompt")
	}
	in.ClientMessageID = normalizeMsgID(in.ClientMessageID)
	h.mu.Lock()
	if h.inter != nil {
		h.mu.Unlock()
		return agents.ErrQuestionPending
	}
	h.queue = append(h.queue, in)
	start := !h.pumping
	if start {
		h.pumping = true
	}
	h.mu.Unlock()
	// Always move off whatever terminal state the handle was last left in — synchronously,
	// on the caller's own goroutine — so a caller polling Snapshot() right after Send/Steer
	// returns can never mistake the PREVIOUS turn's leftover TurnCompleted for THIS one's
	// (runTurn's own setState(TurnStarting) only runs once the pump goroutine gets to it,
	// which is not guaranteed to have happened yet when accept returns).
	h.setState(agents.TurnQueued)
	if start {
		go h.pump()
	}
	return nil
}

// pump drains the queue serially — one runTurn at a time, matching every other driver's
// single-flight turn model.
func (h *threadHandle) pump() {
	for {
		h.mu.Lock()
		if len(h.queue) == 0 {
			h.pumping = false
			h.mu.Unlock()
			return
		}
		in := h.queue[0]
		h.queue = h.queue[1:]
		if ledger.SeenOrRecord(h.name, in.ClientMessageID) {
			h.mu.Unlock()
			continue // resend — the ledger makes it idempotent at start
		}
		h.running = true
		h.mu.Unlock()

		h.runTurn(in)

		h.mu.Lock()
		h.running = false
		h.mu.Unlock()
	}
}

// runTurn is Send's own "ループ1周（harness.Run）" (decision 4): append the real user input,
// read the store's full history back, run the tool loop once against llama-server, and
// persist exactly the delta Run produced (diff.go's newMessagesSince — see store.go's
// AppendMessage doc comment for why the naive "everything past the old length" is wrong once
// a compaction fires inside the same Run call).
func (h *threadHandle) runTurn(in agents.TurnInput) {
	agents.MarkTurnStart(h.sid)
	h.setState(agents.TurnStarting)

	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	h.cancel = cancel
	settings := h.settings
	todos := h.todos
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.cancel = nil
		h.mu.Unlock()
	}()

	st := h.store
	if _, err := st.AppendUser(in.Prompt); err != nil {
		log.Printf("lcpp: persisting user turn: %v", err)
		h.finishTurn(agents.TurnFailed)
		return
	}
	before, err := st.Full()
	if err != nil {
		log.Printf("lcpp: reading history: %v", err)
		h.finishTurn(agents.TurnFailed)
		return
	}

	model := strings.TrimSpace(settings.Model)
	if model == "" {
		h.failTurn(st, "モデルが設定されていません。セッション設定でモデルを選んでください。")
		return
	}
	if harness.EngineToken == nil {
		h.failTurn(st, "この配備には自己ホスト型エンジンが設定されていません。")
		return
	}
	conn, ok := harness.EngineToken(ctx, engineKey, h.sid)
	if !ok {
		h.failTurn(st, "チャット用エンジンに接続できません。しばらくしてから再送してください。")
		return
	}
	client := newHarnessClient(conn, model)

	window := 0
	if harness.EngineWindow != nil {
		window = harness.EngineWindow(ctx, engineKey)
	}

	rt := &harness.Runtime{
		Cwd:          h.cwd,
		Plan:         settings.Mode == "plan",
		AskUser:      h.askUser,
		Todos:        todos,
		SystemPrompt: harness.SystemPrompt(h.cwd, session.KindLcpp),
		Window:       window,
	}
	if h.bypassNow(settings.Mode) {
		rt.Approve = harness.AutoApprove
	} else {
		rt.Approve = h.approve
	}

	// MCP wiring (ADR 0093 決定6, 段2): resolve this turn's enabled servers and reconcile the
	// session-lifetime manager against them BEFORE building the registry, so h.mcpTools() below
	// reflects whatever is live right now — including a server enabled/disabled since the last
	// turn (mcpreg.ForSession is re-read every turn, the same "次ターンから反映" convention
	// UpdateSettings already uses for model/mode). A resolution failure or a per-server connect
	// failure never fails the turn — see syncMCPServers' own doc comment.
	mcpDefs, mcpErr := mcpServersForSession(session.KindLcpp)
	if mcpErr != nil {
		log.Printf("lcpp: %s: resolving MCP servers: %v", h.name, mcpErr)
		mcpDefs = nil
	}
	// The builtin af server needs this session's own name to resolve its owner
	// (mcpOwningSession) once dialStdio spawns it — see injectSessionName's own doc comment for
	// why the Agent daemon has to hand it down explicitly here rather than it already being in
	// the child's inherited environment.
	mcpDefs = injectSessionName(mcpDefs, h.name)
	h.syncMCPServers(ctx, mcpDefs)
	reg := harness.NewRegistry(append(harness.BuiltinTools(), h.mcpTools()...)...)

	h.setState(agents.TurnRunning)
	result, runErr := harness.Run(ctx, client, reg, rt, before)

	h.mu.Lock()
	h.todos = rt.Todos
	h.mu.Unlock()

	for _, m := range newMessagesSince(before, result.Messages) {
		if _, aerr := st.AppendMessage(m); aerr != nil {
			log.Printf("lcpp: persisting turn message: %v", aerr)
		}
	}
	if result.Final.Usage != (harness.Usage{}) {
		if _, aerr := st.AppendUsage(result.Final.Usage, window); aerr != nil {
			log.Printf("lcpp: persisting usage: %v", aerr)
		}
	}

	h.mu.Lock()
	interrupted := h.state == agents.TurnInterrupting
	h.inter, h.replyCh = nil, nil
	h.mu.Unlock()

	switch {
	case interrupted:
		h.finishTurn(agents.TurnCancelled)
	case runErr != nil:
		var ee *harness.EngineError
		if errors.As(runErr, &ee) && ee.Retryable() {
			h.finishTurn(agents.TurnAborted) // e.g. engine_waking timeout — a resend can still work
		} else {
			h.finishTurn(agents.TurnFailed)
		}
	default:
		h.finishTurn(agents.TurnCompleted)
	}
}

// failTurn ends runTurn as TurnFailed for a precondition it could not even attempt to satisfy
// (no model configured, no engine reachable) AND persists msg as a visible NoteTurnError
// record (store.go's own doc comment). Without the note, a precondition failure here would
// leave the store holding only the user's own prompt — AppendUser above always runs first, so
// the turn is durably recorded before anything can fail — with nothing explaining why no
// reply ever came (docs/log/109).
func (h *threadHandle) failTurn(st *Store, msg string) {
	log.Printf("lcpp: %s: %s", h.name, msg)
	if _, err := st.AppendTurnErrorNote(msg); err != nil {
		log.Printf("lcpp: %s: persisting turn error note: %v", h.name, err)
	}
	h.finishTurn(agents.TurnFailed)
}

// Interrupt cancels the running turn's context and clears the queued follow-ups. A blocked
// approve()/askUser() (waitInteraction's own ctx.Done() case) unblocks the same way a
// mid-Send/mid-tool cancellation would — Interrupt does not need to know which of the three
// harness.Run was doing when it was called.
func (h *threadHandle) Interrupt() error {
	h.mu.Lock()
	running := h.running
	cancel := h.cancel
	h.queue = nil
	if running {
		h.state = agents.TurnInterrupting
	}
	h.mu.Unlock()
	if !running {
		return nil
	}
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnInterrupting})
	if cancel != nil {
		cancel()
	}
	return nil
}

// UpdateSettings applies model/mode for the NEXT runTurn (decision 4: "次ターンから反映" —
// there is no running-turn hot-swap, matching every other kind's dynamic settings). Effort is
// refused outright rather than silently accepted and ignored — see Capabilities' own doc
// comment for why DynamicEffort is false.
func (h *threadHandle) UpdateSettings(s agents.ThreadSettings) error {
	if s.Effort != "" || s.ClearEffort {
		return errors.New("lcpp は reasoning effort の動的変更に未対応です")
	}
	h.mu.Lock()
	changed := false
	if s.ClearModel && h.settings.Model != "" {
		h.settings.Model = ""
		changed = true
	}
	if s.Model != "" && s.Model != h.settings.Model {
		h.settings.Model = s.Model
		changed = true
	}
	if s.Mode != "" {
		h.settings.Mode = s.Mode
	}
	model := h.settings.Model
	cur := h.settings
	h.mu.Unlock()
	if changed {
		if _, err := h.store.AppendModelChangeNote(model); err != nil {
			log.Printf("lcpp: recording model change: %v", err)
		}
	}
	h.emit(agents.Event{Kind: "settings", Settings: &cur})
	return nil
}

// approve is Runtime.ApproveFunc (decision 5): it surfaces a Mutates tool call as a
// question-kind Interaction — the same reuse kiro's ACP session/request_permission already
// makes (there is no separate "approval" Interaction kind wired anywhere yet, driver.go's own
// Interaction.Kind doc comment) — and blocks the tool loop until Respond (or Interrupt)
// answers it. Because this process is the one about to run the tool, blocking here really
// does block it (decision 5's own point).
func (h *threadHandle) approve(ctx context.Context, call harness.ToolCall, tool harness.Tool, summary string) (bool, error) {
	id := "perm-" + call.ID
	inter := &agents.Interaction{
		ID: id, Kind: "question", Prompt: summary,
		Questions: []transcript.Question{{
			ID: id, Header: "承認", Question: summary,
			Options: []transcript.Option{{Label: "許可"}, {Label: "拒否"}},
		}},
	}
	reply, err := h.waitInteraction(ctx, inter)
	if err != nil {
		return false, err
	}
	switch reply.Decision {
	case agents.DecisionAllow:
		return true, nil
	case agents.DecisionAnswer:
		return len(reply.Answers) > 0 && len(reply.Answers[0].Options) > 0 && reply.Answers[0].Options[0] == 0, nil
	default: // deny / cancel
		return false, nil
	}
}

// askUser is Runtime.AskUserFunc (the ask_user builtin, docs/log/99 §4.6): the same
// waitInteraction gate as approve, answered with free text or a picked option.
func (h *threadHandle) askUser(ctx context.Context, question string, options []string) (string, error) {
	id := "ask-" + newInteractionID()
	var opts []transcript.Option
	for _, o := range options {
		opts = append(opts, transcript.Option{Label: o})
	}
	inter := &agents.Interaction{
		ID: id, Kind: "question",
		Questions: []transcript.Question{{ID: id, Question: question, Options: opts}},
	}
	reply, err := h.waitInteraction(ctx, inter)
	if err != nil {
		return "", err
	}
	if len(reply.Answers) > 0 {
		a := reply.Answers[0]
		if a.Text != "" {
			return a.Text, nil
		}
		if len(a.Options) > 0 && a.Options[0] >= 0 && a.Options[0] < len(options) {
			return options[a.Options[0]], nil
		}
	}
	return "", nil
}

// waitInteraction registers inter as the one pending Interaction (only one at a time — the
// same single-card shape TranscriptData.Pending already assumes), emits it, and blocks until
// Respond delivers a reply or ctx is cancelled.
func (h *threadHandle) waitInteraction(ctx context.Context, inter *agents.Interaction) (agents.InteractionReply, error) {
	ch := make(chan agents.InteractionReply, 1)
	h.mu.Lock()
	h.inter, h.replyCh = inter, ch
	h.mu.Unlock()
	h.setState(agents.TurnWaitingInteraction)
	select {
	case reply := <-ch:
		h.mu.Lock()
		h.inter, h.replyCh = nil, nil
		h.mu.Unlock()
		h.setState(agents.TurnRunning)
		return reply, nil
	case <-ctx.Done():
		h.mu.Lock()
		h.inter, h.replyCh = nil, nil
		h.mu.Unlock()
		return agents.InteractionReply{}, ctx.Err()
	}
}

// Respond answers the pending Interaction (a permission question or an ask_user question —
// both go through waitInteraction above, so this one method serves both).
func (h *threadHandle) Respond(reply agents.InteractionReply) error {
	h.mu.Lock()
	inter, ch := h.inter, h.replyCh
	h.mu.Unlock()
	if inter == nil || inter.ID != reply.ID || ch == nil {
		return fmt.Errorf("interaction %s は待機中ではありません", reply.ID)
	}
	select {
	case ch <- reply:
	default: // already answered/cancelled concurrently — drop, not an error
	}
	return nil
}

func (h *threadHandle) Events() <-chan agents.Event { return h.events }

func (h *threadHandle) Snapshot() (agents.ThreadSnapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return agents.ThreadSnapshot{TurnState: h.state, Interaction: h.inter, Settings: h.settings}, nil
}

// --- small helpers --------------------------------------------------------------

// finishTurn writes the status file before setting the terminal state so that any
// caller waking on the emitted event (WireLive, sessionx's DriveState, tests) sees
// status=idle in status.Read immediately — not the stale "working" MarkTurnStart wrote.
// Calling MarkTurnEnd after setState (e.g. via a defer) lets the event fire first and
// introduces a window where TurnCompleted is visible but status.Read still returns
// "working" (the shape TestDriverSendPersistsTurnAndCompletes's status assertion catches).
func (h *threadHandle) finishTurn(st agents.TurnState) {
	agents.MarkTurnEnd(h.sid, st)
	h.setState(st)
}

func (h *threadHandle) setState(st agents.TurnState) {
	h.mu.Lock()
	h.state = st
	// runningSince has to be stamped on TurnStarting too, not only TurnRunning: the engine-wake
	// wait lastSay reports (below) happens BEFORE runTurn ever reaches TurnRunning (it is inside
	// harness.EngineToken/harness.Run, called while still TurnStarting). Leaving it unset for
	// TurnStarting left h.runningSince at its zero value during exactly that wait, and
	// time.Since(zero value) overflows time.Duration's own range (~292 years) rather than
	// panicking — Sub clamps to the max representable Duration, so lastSay printed
	// "エンジン起動待ち（9223372036秒）" (math.MaxInt64 ns, truncated to seconds) instead of the
	// real elapsed time (found live 2026-09-21, the lcpp kind's first end-to-end run).
	if st == agents.TurnStarting || st == agents.TurnRunning {
		h.runningSince = time.Now()
	}
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: st})
}

func (h *threadHandle) currentState() agents.TurnState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// emit never blocks a state transition (drop on overflow) — the same convention every other
// driver's event channel uses.
func (h *threadHandle) emit(e agents.Event) {
	select {
	case h.events <- e:
	default:
	}
}

// wakingLastSayThreshold is how long a turn must have been running before lastSay starts
// showing the cold-start line — short enough that the box's real 3.5-5 minute cold start
// (ADR 0093's own measured figure) is visible quickly, long enough that an ordinary fast
// reply never flashes it.
const wakingLastSayThreshold = 5 * time.Second

// wakingLastSayCeiling is a sanity cap, not a real bound on how long a turn may run: no genuine
// wait this driver produces (engineWakeRetryBudget's 10 minutes, plus generation) comes anywhere
// close to it. Its only job is to catch runningSince reading as its zero value — an unset or
// misordered stamp — before elapsed is shown to anyone, rather than printing whatever
// time.Since(zero value) clamps to (time.Duration's own ~292-year ceiling: the exact shape of the
// bug this guards, see setState's own doc comment).
const wakingLastSayCeiling = 24 * time.Hour

// lastSay is ADR 0093 decision 4's v1 cold-start signal: "working ＋ 『エンジン起動待ち（n
// 秒）』の last-say 行で出す". There is no per-token hook from harness.Client.Send today, so
// this cannot distinguish "still waiting for the engine to wake" from "generating a long
// reply" — both read as TurnRunning from here — and errs on the side of showing elapsed time
// rather than nothing, exactly as the ADR asks for v1 (a dedicated `waking` state is deferred
// to a later Console-vocabulary change, per the ADR's own text).
func (h *threadHandle) lastSay() string {
	h.mu.Lock()
	st, since := h.state, h.runningSince
	h.mu.Unlock()
	if st != agents.TurnRunning && st != agents.TurnStarting {
		return ""
	}
	if since.IsZero() {
		return ""
	}
	elapsed := time.Since(since)
	if elapsed < wakingLastSayThreshold || elapsed > wakingLastSayCeiling {
		return ""
	}
	return fmt.Sprintf("エンジン起動待ち（%d秒）", int(elapsed.Seconds()))
}

// normalizeMsgID mirrors every other driver's convention: empty → the driver assigns one.
func normalizeMsgID(id string) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("af-%d", time.Now().UnixNano())
}

var interactionIDSeq uint64

// newInteractionID mints an ask_user Interaction id (approve's own id is derived from the
// ToolCall.ID instead, which is already unique).
func newInteractionID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddUint64(&interactionIDSeq, 1))
}
