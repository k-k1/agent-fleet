package agy

// The Managed agy runtime (#2158; measurements and design in docs/log/32, "stream-json での
// Managed 方式の再評価"): one `agy` child per session in stream-json print mode, one stdin line
// per turn, the turn ended by its `result` event.
//
// Not wired yet: sessionx's managedDrivers does not list agy, so no session reaches this file.
// The isolated read-only HOME, the read layer that follows it and the registration land
// together, because a child under the user's real HOME would run with their own permission
// settings, not a read-only preset.
//
// What the wire cannot do, and how the handle answers it:
//
//   - A stop has no in-band form. SIGINT ends the turn (result.status ERROR, error
//     "interrupted", the partial response kept) and then the process exits 1, so the next turn
//     needs a new child, started with `--conversation <id>` (measured: the context and the
//     interrupted turn's partial output survive). The child is therefore not the session: it
//     comes and goes, and the handle respawns it for the next turn the queue hands over (ADR
//     0105: after a first stop the queue continues), without waiting for new input.
//   - A result received outranks the exit that follows it: the turn lands on what the result
//     said, and an exit that we asked for (stop, idle close, detach) is not a lost runtime.
//   - A child that dies without a result while a turn runs, and without being asked to, is a
//     lost runtime (an OOM kill, a crash): the turn lands unknown and the handle stops taking
//     input until Resume, as for the other per-session kinds. Respawning on its own would turn
//     an OOM into a loop.
//   - No approvals, no questions: print mode cannot prompt, so a tool outside the allow list
//     is refused and the turn goes on (soft-deny). The refusals come back on TurnOutcome.
//   - No steer: input sent during a turn waits in the driver's queue and becomes the next turn.
//   - The model is a launch flag; there is no way to change it in a running child.

import (
	"bufio"
	"context"
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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// streamLedger is the persistent ClientMessageID ledger of the Managed route.
var streamLedger = agents.NewMsgLedger("agy-msgledger")

const (
	// streamStopGrace is how long a SIGINT or a closed stdin may take to end the child before it
	// is terminated. Measured: an interrupted turn answers and exits at once.
	streamStopGrace = 10 * time.Second
	// streamTermGrace is stopStreamChild's SIGTERM → SIGKILL gap.
	streamTermGrace = 3 * time.Second
	// streamIdleClose is how long a child sits with no turn before its stdin is closed (it then
	// exits 0, and the next turn respawns it with --conversation). A child costs memory the
	// Workspace's limit is shared with; a respawn costs about 4 s (measured).
	streamIdleClose = 15 * time.Minute
	// streamDrainWait bounds the wait for the rest of stdout after the child was reaped: a
	// grandchild that inherited stdout would otherwise hold the reader open.
	streamDrainWait = 2 * time.Second
)

// streamCommandFunc builds the child for st, resuming conv when it is not empty. Start sets Dir
// and the pipes.
type streamCommandFunc func(h *streamHandle, st agents.ThreadSettings, conv string) *exec.Cmd

// streamCommand is the production child: `agy` in stream-json print mode. No -p / --print: it
// would take the next flag as its prompt (measured: exit 1 at once), and stream-json input
// alone selects print mode. No --print-timeout: its default, 0, waits for the turn to end
// (agy 1.3.5 --help), which is what a turn without a deadline needs.
func streamCommand(h *streamHandle, st agents.ThreadSettings, conv string) *exec.Cmd {
	args := []string{"--input-format", "stream-json", "--output-format", "stream-json"}
	if st.Model != "" {
		args = append(args, "--model", st.Model)
	}
	if conv != "" {
		args = append(args, "--conversation", conv)
	}
	cmd := exec.Command("agy", args...)
	// Own process group, so a stop can take the tools agy started along with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	env := agents.WithSessionEnv(os.Environ(), h.name)
	if h.home != "" {
		env = withEnvVar(env, "HOME", h.home)
	}
	cmd.Env = Env(withoutEnvVar(env, "OPENSSL_ia32cap"))
	return cmd
}

func withoutEnvVar(env []string, key string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, key+"=") {
			out = append(out, e)
		}
	}
	return out
}

func withEnvVar(env []string, key, val string) []string {
	return append(withoutEnvVar(env, key), key+"="+val)
}

// stopStreamChild terminates a child's process group: SIGTERM, then SIGKILL. Reaping is the
// watch goroutine's.
func stopStreamChild(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if syscall.Kill(-pid, syscall.SIGTERM) != nil {
		return // already gone
	}
	time.AfterFunc(streamTermGrace, func() {
		// A reaped pid can be reused; Signal on a waited Process answers ErrProcessDone, so this
		// never reaches an unrelated group.
		if cmd.Process.Signal(syscall.Signal(0)) == nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
}

// streamChild is one agy process.
type streamChild struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	// exited closes once the process is reaped and its stdout read to the end, so a result
	// written before the exit is always delivered before it.
	exited chan struct{}
	// code and sig are the exit, set before exited closes.
	code, sig int

	// Under the handle's mu:
	tr *turnTracker
	// stopAsked: a stop, an idle close or a detach was aimed at this child, so its exit is
	// ours, not a lost runtime.
	stopAsked bool
}

// streamHandle is one Managed agy session. Every field below mu is under it; q follows
// agents.TurnQueue's contract (every method under mu).
type streamHandle struct {
	name    string
	dir     string
	slotSid string
	// home is the child's HOME; empty inherits the Agent's.
	home    string
	command streamCommandFunc
	sids    agents.SidStore

	idleAfter time.Duration
	stopGrace time.Duration

	mu       sync.Mutex
	alive    bool // attached: accepts input. The child may be absent between turns.
	child    *streamChild
	conv     string // agy's conversation id, from init
	settings agents.ThreadSettings
	state    agents.TurnState
	running  bool
	pumping  bool
	stopping bool // the turn in flight is one a stop was aimed at
	q        *agents.TurnQueue
	turnDone chan streamResult // the result of the turn in flight; nil between turns
	last     *TurnOutcome
	idleGen  int
	// beforeCommit, when set, runs between the pump's Take and its Commit with mu released.
	// Tests only.
	beforeCommit func()

	events chan agents.Event
	act    agents.Activity
}

var (
	_ agents.ThreadHandle   = (*streamHandle)(nil)
	_ agents.QueueingSender = (*streamHandle)(nil)
)

func newStreamHandle(name, dir, slotSid, home string) *streamHandle {
	return &streamHandle{
		name: name, dir: dir, slotSid: slotSid, home: home,
		command:   streamCommand,
		sids:      sids,
		idleAfter: streamIdleClose,
		stopGrace: streamStopGrace,
		q:         agents.NewTurnQueue(name, streamLedger, agents.LedgerAtTake),
		events:    make(chan agents.Event, 64),
	}
}

// StreamCapabilities is what the Managed agy route can do; the driver will declare it once it
// is registered. Everything the wire lacks is false (the file comment says why).
func StreamCapabilities() agents.Capabilities {
	return agents.Capabilities{ProcessModel: "per-session-child"}
}

// Start attaches the handle: settings from m, the conversation from the slot's sid store. The
// child is spawned by the first turn, not here: init only arrives after a message anyway, and
// a boot-time reconcile would otherwise start one idle child per session.
func (h *streamHandle) Start(m session.Meta) {
	h.mu.Lock()
	if !h.alive {
		if h.settings.Model == "" {
			h.settings.Model = m.Model
		}
		if h.conv == "" {
			h.conv = h.sids.Read(h.slotSid)
		}
		h.alive = true
		if h.state == "" || h.state == agents.TurnUnknown {
			h.state = agents.TurnCompleted
		}
	}
	h.mu.Unlock()
	// Baseline for recordLost's OOM attribution (the same role as the TUI's startSessionTmux).
	base, _ := status.OOMKillCount()
	status.PersistExit(m.Name, status.ExitInfo{OOMBase: base})
	// Input a lost child left queued runs now, in order.
	h.resumePump()
}

// --- the child ---------------------------------------------------------------

// ensureChild returns a child that can take a turn: the current one, or a new one once a child
// that was asked to stop has gone (its conversation is not free before that).
func (h *streamHandle) ensureChild() (*streamChild, error) {
	h.mu.Lock()
	c := h.child
	if c != nil && !c.stopAsked {
		h.mu.Unlock()
		return c, nil
	}
	h.mu.Unlock()
	if c != nil {
		select {
		case <-c.exited:
		case <-time.After(h.stopGrace + streamTermGrace + time.Second):
			stopStreamChild(c.cmd)
			select {
			case <-c.exited:
			case <-time.After(2 * streamTermGrace):
				return nil, errors.New("agy の前のプロセスが終了しません")
			}
		}
	}
	return h.spawn()
}

// spawn starts a child, resuming the handle's conversation when it has one.
func (h *streamHandle) spawn() (*streamChild, error) {
	h.mu.Lock()
	st, conv := h.settings, h.conv
	h.mu.Unlock()
	cmd := h.command(h, st, conv)
	cmd.Dir = h.dir
	// stdout is a pipe of our own rather than cmd.StdoutPipe: Wait closes that one, and the
	// reader must be able to finish what the child wrote before it exited (a result outranks
	// the exit).
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, err
	}
	cmd.Stdout = w
	tail, err := agents.StartWithStderrTail(cmd)
	_ = w.Close()
	if err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("agy を起動できません: %w", err)
	}
	tail.Settle() // the stderr tail is kept for spawn failures only (StderrTail's contract)
	c := &streamChild{cmd: cmd, stdin: stdin, exited: make(chan struct{}), tr: newTurnTracker(conv != "")}
	h.mu.Lock()
	h.child = c
	h.mu.Unlock()
	readDone := make(chan struct{})
	go h.read(c, r, readDone)
	go h.watch(c, tail, r, readDone)
	return c, nil
}

// read follows the child's stdout until EOF.
func (h *streamHandle) read(c *streamChild, r io.Reader, done chan struct{}) {
	defer close(done)
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if ev, ok := parseStreamLine(line); ok {
			h.onEvent(c, ev)
		}
		if err != nil {
			return
		}
	}
}

// onEvent handles one event on the reader goroutine. It never blocks: turnDone has room for
// the one result a turn gets.
func (h *streamHandle) onEvent(c *streamChild, ev streamEvent) {
	h.act.Touch()
	if id := ev.conversationID(); id != "" {
		h.noteConversation(c, id)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.child != c {
		return
	}
	switch ev.Event {
	case "step_update":
		c.tr.step(ev.Step)
	case "result":
		if h.turnDone == nil {
			log.Printf("agy managed: %s: result with no turn in flight (status %s)", h.name, ev.Result.Status)
			return
		}
		select {
		case h.turnDone <- ev.Result:
		default:
		}
	}
}

// noteConversation records the conversation id the child reported. A resume that agy could not
// honour starts a new conversation, and the new id is the one to resume next time.
func (h *streamHandle) noteConversation(c *streamChild, id string) {
	h.mu.Lock()
	changed := h.child == c && h.conv != id
	if changed {
		h.conv = id
	}
	h.mu.Unlock()
	if changed {
		h.sids.Write(h.slotSid, id)
	}
}

// watch reaps the child, lets the reader finish, then lets go of it.
func (h *streamHandle) watch(c *streamChild, tail *agents.StderrTail, r *os.File, readDone chan struct{}) {
	_ = c.cmd.Wait()
	tail.Release()
	if ws, ok := c.cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			c.sig = int(ws.Signal())
			c.code = 128 + c.sig
		} else {
			c.code = ws.ExitStatus()
		}
	}
	select {
	case <-readDone:
	case <-time.After(streamDrainWait):
		_ = r.Close()
		<-readDone
	}
	h.mu.Lock()
	asked := c.stopAsked
	if h.child == c {
		h.child = nil
	}
	h.mu.Unlock()
	close(c.exited)
	if !asked {
		log.Printf("agy managed: %s: child exited unasked (code %d, signal %d)", h.name, c.code, c.sig)
	}
}

// retire ends a child we no longer want: SIGINT when a turn runs (agy answers it and exits),
// a closed stdin otherwise (exit 0), and termination if neither works within stopGrace.
// The caller has set c.stopAsked under mu.
func (h *streamHandle) retire(c *streamChild, interrupt bool) {
	if interrupt {
		_ = c.cmd.Process.Signal(syscall.SIGINT)
	} else {
		_ = c.stdin.Close()
	}
	time.AfterFunc(h.stopGrace, func() {
		select {
		case <-c.exited:
		default:
			stopStreamChild(c.cmd)
		}
	})
}

// --- turns -------------------------------------------------------------------

func (h *streamHandle) emit(e agents.Event) {
	select {
	case h.events <- e:
	default:
	}
}

func (h *streamHandle) setState(st agents.TurnState) {
	h.mu.Lock()
	h.state = st
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: st})
}

// TurnState is the handle's current turn state.
func (h *streamHandle) TurnState() agents.TurnState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// LastTurn is the outcome of the last turn that reached the child, nil before the first.
func (h *streamHandle) LastTurn() *TurnOutcome {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.last == nil {
		return nil
	}
	out := *h.last
	out.PermissionDenied = append([]Denial(nil), h.last.PermissionDenied...)
	return &out
}

func (h *streamHandle) Send(in agents.TurnInput) error {
	_, err := h.accept(in)
	return err
}

// SendQueued is Send reporting whether the input waits behind a running turn.
func (h *streamHandle) SendQueued(in agents.TurnInput) (bool, error) { return h.accept(in) }

// Steer queues like Send: the wire has no way into a running turn.
func (h *streamHandle) Steer(in agents.TurnInput) error {
	_, err := h.accept(in)
	return err
}

func (h *streamHandle) accept(in agents.TurnInput) (queued bool, err error) {
	if strings.TrimSpace(in.Prompt) == "" {
		return false, errors.New("empty prompt")
	}
	if len(in.Attachments) > 0 {
		// stream-json's user message carries text only (the measured envelope has no other
		// part); a dropped attachment would be a silent lie about what the model saw.
		return false, errors.New("agy の Managed は添付に未対応です")
	}
	h.mu.Lock()
	if !h.alive {
		h.mu.Unlock()
		return false, errors.New("runtime が停止しています（再開してください）")
	}
	if _, dup := h.q.Accept(in); dup {
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

// pump runs the queue one turn at a time.
func (h *streamHandle) pump() {
	for {
		// The entry's lease on the session's lifecycle gate (ADR 0115 decision 11.1), held until
		// the child has it.
		lease, err := lifegate.Enter(context.Background(), h.name)
		if err != nil {
			h.parkForMove(err)
			return
		}
		h.mu.Lock()
		if !h.alive {
			h.pumping = false
			h.mu.Unlock()
			lease.Release()
			return
		}
		t := h.q.Take()
		if t == nil {
			h.pumping = false
			h.armIdleLocked()
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

// parkForMove stops a pump a move refused; see acp.Handle.parkForMove, whose rules this follows.
func (h *streamHandle) parkForMove(err error) {
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

func (h *streamHandle) resumePump() {
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

// armIdleLocked schedules the idle close of the current child. Each arm supersedes the last,
// so a timer from an earlier quiet spell cannot close a child that worked since.
func (h *streamHandle) armIdleLocked() {
	h.idleGen++
	if h.child == nil || h.idleAfter <= 0 {
		return
	}
	gen := h.idleGen
	time.AfterFunc(h.idleAfter, func() { h.idleClose(gen) })
}

func (h *streamHandle) idleClose(gen int) {
	h.mu.Lock()
	c := h.child
	if gen != h.idleGen || c == nil || c.stopAsked || h.running || h.q.Head() != nil || h.q.Len() > 0 {
		h.mu.Unlock()
		return
	}
	c.stopAsked = true
	h.mu.Unlock()
	h.retire(c, false)
}

// runTurn runs one entry on the child and lands its terminal state. lease is released once the
// child has the input: a turn can run for hours and must not hold a move off.
func (h *streamHandle) runTurn(t *agents.Taken, lease *lifegate.Lease) {
	defer lease.Release()
	agents.MarkTurnStartRun(h.slotSid, t.In)
	failure := ""
	defer func() { agents.MarkTurnEndErr(h.slotSid, h.TurnState(), failure) }()
	h.setState(agents.TurnStarting)
	if h.beforeCommit != nil {
		h.beforeCommit()
	}
	c, err := h.ensureChild()
	if err != nil {
		log.Printf("agy managed: %s: %v", h.name, err)
		h.requeueLost(t)
		return
	}
	h.mu.Lock()
	// The last act under the lock before the child sees the input (ADR 0105 decision 3).
	if !h.q.Commit(t) {
		h.mu.Unlock()
		h.setState(agents.TurnCancelled)
		return
	}
	h.stopping = false
	done := make(chan streamResult, 1)
	h.turnDone = done
	c.tr.begin()
	h.state = agents.TurnRunning
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})

	if _, err := c.stdin.Write(userLine(t.In.Prompt)); err != nil {
		// The child went before it had the prompt.
		h.mu.Lock()
		h.turnDone = nil
		h.mu.Unlock()
		h.requeueLost(t)
		return
	}
	lease.Release()
	h.received(t, c)

	r, got := awaitResult(done, c.exited)
	h.mu.Lock()
	h.turnDone = nil
	stopped := h.stopping
	asked := c.stopAsked
	var out TurnOutcome
	if got {
		out = c.tr.finish(r)
	} else {
		out = c.tr.abandon()
	}
	if out.ConversationID == "" {
		out.ConversationID = h.conv
	}
	h.last = &out
	lost := !got && !asked
	if lost {
		h.alive = false
	}
	h.mu.Unlock()
	st := outcomeState(out, got, stopped)
	switch st {
	case agents.TurnFailed:
		failure = out.Error
		if failure == "" {
			failure = "agy: " + out.Status
		}
	}
	if lost {
		h.recordLost(c)
	}
	h.setState(st)
}

// awaitResult waits for the turn's result or the child's exit. exited closes only after the
// reader finished, so a result the child wrote before it went is already in done; when both
// are ready, the result wins (a select alone would pick at random).
func awaitResult(done <-chan streamResult, exited <-chan struct{}) (streamResult, bool) {
	select {
	case r := <-done:
		return r, true
	case <-exited:
		select {
		case r := <-done:
			return r, true
		default:
			return streamResult{}, false
		}
	}
}

// outcomeState maps a finished turn onto the turn state machine.
func outcomeState(out TurnOutcome, gotResult, stopped bool) agents.TurnState {
	switch {
	case !gotResult && stopped:
		return agents.TurnCancelled
	case !gotResult:
		// Gone without an answer and without being asked: §6's honest state.
		return agents.TurnUnknown
	case out.Status == "SUCCESS":
		// A refusal leaves the turn completed; the denials ride on the outcome.
		return agents.TurnCompleted
	case stopped, out.Error == "interrupted", out.Error == "context canceled":
		return agents.TurnCancelled
	case out.Status == "CANCELED" && len(out.PermissionDenied) > 0:
		// Older agy ended a turn on a soft-deny this way (measured on 1.2.x for the image
		// route); the refusal is the whole story, so it reads like 1.3.5's SUCCESS.
		return agents.TurnCompleted
	default:
		return agents.TurnFailed
	}
}

// requeueLost puts t back after the child could not take it, and takes the handle down: the
// input waits for Resume rather than being retried into a runtime that does not start.
func (h *streamHandle) requeueLost(t *agents.Taken) {
	h.mu.Lock()
	requeued := h.q.Requeue(t)
	h.alive = false
	h.mu.Unlock()
	if !requeued {
		h.setState(agents.TurnCancelled) // the stop was aimed at it
		return
	}
	h.setState(agents.TurnUnknown)
}

// recordLost writes the exit of a child that died under a turn, where the Console reads why a
// session went down.
func (h *streamHandle) recordLost(c *streamChild) {
	oom := false
	base := uint64(0)
	if prev, ok := status.ReadExit(h.name); ok {
		base = prev.OOMBase
	}
	if cur, ok := status.OOMKillCount(); ok && cur > base {
		oom = true
	}
	status.PersistExit(h.name, status.ExitInfo{
		Reason: status.ExitReasonFor(c.code, c.sig, oom),
		Code:   c.code, Signal: c.sig,
		At:      time.Now().Format(time.RFC3339),
		OOMBase: base,
	})
}

// received is the moment the child holds t. A stop that found t committed but not yet written
// left its delivery to here.
func (h *streamHandle) received(t *agents.Taken, c *streamChild) {
	h.mu.Lock()
	stop := h.q.Received(t)
	h.mu.Unlock()
	if stop {
		h.deliverStop(c)
	}
}

// deliverStop interrupts the child's turn. The child exits after answering, and the next turn
// respawns it.
func (h *streamHandle) deliverStop(c *streamChild) {
	h.mu.Lock()
	if c.stopAsked {
		h.mu.Unlock()
		return
	}
	c.stopAsked = true
	h.mu.Unlock()
	h.retire(c, true)
}

// Interrupt is the Console's stop (ADR 0105 decisions 1-3).
func (h *streamHandle) Interrupt(opts agents.InterruptOpts) (agents.InterruptResult, error) {
	h.mu.Lock()
	out := h.q.Interrupt(opts, h.q.Head() != nil)
	h.stopLocked(out.Head)
	return out.Result, nil
}

// stopLocked delivers a stop per head and releases mu.
func (h *streamHandle) stopLocked(head agents.HeadAction) {
	c := h.child
	switch head {
	case agents.HeadCancelled:
		h.state = agents.TurnCancelled
		h.mu.Unlock()
		h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnCancelled})
		return
	case agents.HeadStopNow, agents.HeadStopPending:
	default:
		h.mu.Unlock()
		return
	}
	h.stopping = true
	h.state = agents.TurnInterrupting
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnInterrupting})
	if head == agents.HeadStopPending || c == nil {
		return // received delivers it once the prompt is written
	}
	h.deliverStop(c)
}

func (h *streamHandle) RemoveQueued(id string) (agents.QueueItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.q.Remove(id)
}

func (h *streamHandle) DismissDiscard(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.q.DismissDiscard(id)
}

// UpdateSettings refuses every change: the model is the child's launch flag and the route has
// no modes.
func (h *streamHandle) UpdateSettings(s agents.ThreadSettings) error {
	if s == (agents.ThreadSettings{}) {
		return nil
	}
	return errors.New("agy の Managed はモデル・モードの稼働中変更に未対応です（セッションを作り直してください）")
}

// Respond has nothing to answer: print mode raises no Interaction.
func (h *streamHandle) Respond(reply agents.InteractionReply) error {
	return fmt.Errorf("interaction %s is not pending: %w", reply.ID, agents.ErrInteractionNotPending)
}

func (h *streamHandle) Events() <-chan agents.Event { return h.events }

func (h *streamHandle) Snapshot() (agents.ThreadSnapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return agents.ThreadSnapshot{TurnState: h.state, Settings: h.settings}, nil
}

// --- registry-side operations --------------------------------------------------

// Alive reports that the handle takes input.
func (h *streamHandle) Alive() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.alive
}

// Busy reports a turn running or queued.
func (h *streamHandle) Busy() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running || h.q.Len() > 0
}

// HoldsEntry reports an entry taken from the queue and not settled (ADR 0115 decision 11.2).
func (h *streamHandle) HoldsEntry() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running || h.q.Head() != nil
}

// Abort stops the running turn, if there is one, for graceful shutdown (decision 8): the
// queue goes too.
func (h *streamHandle) Abort() {
	h.mu.Lock()
	if !h.running {
		h.mu.Unlock()
		return
	}
	h.q.DropAll()
	out := h.q.Interrupt(agents.InterruptOpts{}, h.q.Head() != nil)
	h.stopLocked(out.Head)
}

// Detach takes the handle off its runtime (stop/halt/archive): the queue goes, a running turn
// is interrupted, the child ends. The conversation stays on disk for a later Resume. The
// channel closes when the child has exited.
func (h *streamHandle) Detach() <-chan struct{} {
	h.mu.Lock()
	h.alive = false
	h.q.DropAll()
	h.idleGen++
	c := h.child
	running := h.running
	if running {
		h.stopping = true
	}
	if c == nil || c.stopAsked {
		h.mu.Unlock()
		if c != nil {
			return c.exited
		}
		done := make(chan struct{})
		close(done)
		return done
	}
	c.stopAsked = true
	h.mu.Unlock()
	h.retire(c, running)
	return c.exited
}

// Down marks the handle dead for Agent shutdown and returns the stop of its child. A turn it
// cuts short lands unknown: the next boot's reconcile settles it from the conversation.
func (h *streamHandle) Down() (stop func()) {
	h.mu.Lock()
	h.alive = false
	h.idleGen++
	c := h.child
	if c != nil {
		c.stopAsked = true
	}
	h.mu.Unlock()
	return func() {
		if c != nil {
			stopStreamChild(c.cmd)
		}
	}
}

// LiveState is the list chip's state, from the turn machine (there is no pane to classify).
func (h *streamHandle) LiveState() string {
	switch h.TurnState() {
	case agents.TurnQueued, agents.TurnStarting, agents.TurnRunning, agents.TurnInterrupting:
		return "working"
	}
	return "idle"
}
