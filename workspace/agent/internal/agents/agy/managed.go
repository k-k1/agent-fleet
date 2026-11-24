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
//     0105: after a first stop the queue continues), without waiting for new input. A child a
//     stop was aimed at is marked retiring in the same critical section that decided the stop,
//     so no later turn is ever written to it.
//   - A result received outranks the exit that follows it: the turn lands on what the result
//     said, and an exit that we asked for (stop, idle close, detach) is not a lost runtime.
//   - A child that dies without a result while a turn runs, and without being asked to, is a
//     lost runtime (an OOM kill, a crash): the turn lands unknown and the handle stops taking
//     input until Resume, as for the other per-session kinds. Respawning on its own would turn
//     an OOM into a loop. The one retry is a prompt the child provably never took (the write
//     failed) on a child an earlier turn left behind.
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

	"golang.org/x/sys/unix"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// streamLedger is the persistent ClientMessageID ledger of the Managed route.
var streamLedger = agents.NewMsgLedger("agy-msgledger")

const (
	// streamStopGrace is how long a SIGINT, a closed stdin or a stop waiting on a prompt write
	// may take before the child is terminated. Measured: an interrupted turn answers and exits
	// at once.
	streamStopGrace = 10 * time.Second
	// streamTermGrace is the SIGTERM → SIGKILL gap of terminate.
	streamTermGrace = 3 * time.Second
	// streamIdleClose is how long a child sits with no turn before its stdin is closed (it then
	// exits 0, and the next turn respawns it with --conversation). A child costs memory the
	// Workspace's limit is shared with; a respawn costs about 4 s (measured).
	streamIdleClose = 15 * time.Minute
	// streamDrainWait bounds the wait for the rest of stdout after the child exited: a
	// grandchild that inherited stdout would otherwise hold the reader open.
	streamDrainWait = 2 * time.Second
)

// errDetached: the handle was detached or shut down while a turn was getting its child.
var errDetached = errors.New("agy managed: detached")

// streamCommandFunc builds the child for st, resuming conv when it is not empty. spawn sets Dir
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

// streamChild is one agy process, the leader of its own process group.
type streamChild struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	// exited closes once the process is reaped and its stdout read to the end, so a result
	// written before the exit is always delivered before it.
	exited chan struct{}
	// code and sig are the exit, set before exited closes.
	code, sig int

	// reapMu orders every signal against the reap: while reaped is false the leader is alive or
	// a zombie, so its pid, and therefore its process group id, cannot belong to anyone else.
	reapMu sync.Mutex
	reaped bool

	// Under the handle's mu:
	tr *turnTracker
	// fresh: spawned for the turn now using it and not yet through a turn.
	fresh bool
	// stopAsked: a stop, an idle close or a teardown was aimed at this child, so its exit is
	// ours, not a lost runtime, and no further turn goes to it.
	stopAsked bool
	// dead: the process has exited (it may still be draining).
	dead bool
}

// signalGroup sends sig to the child's whole group, never after the reap.
func (c *streamChild) signalGroup(sig syscall.Signal) {
	c.reapMu.Lock()
	defer c.reapMu.Unlock()
	if !c.reaped {
		_ = syscall.Kill(-c.cmd.Process.Pid, sig)
	}
}

// signalLeader sends sig to agy itself (a stop is agy's to answer, not its tools').
func (c *streamChild) signalLeader(sig syscall.Signal) {
	c.reapMu.Lock()
	defer c.reapMu.Unlock()
	if !c.reaped {
		_ = c.cmd.Process.Signal(sig)
	}
}

// terminate ends the child's group: SIGTERM, then SIGKILL. A leader that goes on TERM while a
// tool ignores it is covered by watch, which kills the group once the leader exits.
func (c *streamChild) terminate() {
	c.signalGroup(syscall.SIGTERM)
	time.AfterFunc(streamTermGrace, func() { c.signalGroup(syscall.SIGKILL) })
}

// spawnWait is a spawn in flight, so a teardown that finds no child yet can still wait for it.
type spawnWait struct{ done chan struct{} }

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
	gen      int  // bumped by every teardown (Detach, Down); a spawn from before it retires its child
	child    *streamChild
	spawning *spawnWait
	// sending is the child a prompt is being written to, nil once the write returned.
	sending  *streamChild
	conv     string // agy's conversation id, from init
	settings agents.ThreadSettings
	state    agents.TurnState
	running  bool
	pumping  bool
	stopping bool // the turn in flight is one a stop or a teardown was aimed at
	q        *agents.TurnQueue
	turnDone chan streamResult // the result of the turn in flight; nil between turns
	// resultIn: the turn in flight has its result, so it has ended even before it lands, and a
	// stop has nothing left to stop.
	resultIn bool
	last     *TurnOutcome
	idleGen  int

	// Test hooks, nil in production: beforeCommit runs between the pump's Take and its Commit,
	// beforeWrite between the Commit and the prompt's write, beforeLand between the result and
	// the turn's landing, beforeSignal between a stop's decision and its SIGINT. mu is released
	// for each.
	beforeCommit, beforeWrite, beforeLand, beforeSignal func()

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

// usableLocked reports a child a turn can be written to.
func usableLocked(c *streamChild) bool { return c != nil && !c.stopAsked && !c.dead }

// ensureChild returns a child that can take a turn: the current one, or a new one once a child
// that is retiring or exited has gone (its conversation is not free before that). errDetached
// when a teardown came first.
func (h *streamHandle) ensureChild() (*streamChild, error) {
	for {
		h.mu.Lock()
		if !h.alive {
			h.mu.Unlock()
			return nil, errDetached
		}
		c := h.child
		if usableLocked(c) {
			h.mu.Unlock()
			return c, nil
		}
		if c == nil {
			sw := &spawnWait{done: make(chan struct{})}
			h.spawning = sw
			gen := h.gen
			h.mu.Unlock()
			return h.spawn(gen, sw)
		}
		h.mu.Unlock()
		select {
		case <-c.exited:
		case <-time.After(h.stopGrace + streamTermGrace + time.Second):
			c.terminate()
			select {
			case <-c.exited:
			case <-time.After(2 * streamTermGrace):
				return nil, errors.New("agy の前のプロセスが終了しません")
			}
		}
	}
}

// spawn starts a child for teardown generation gen, resuming the handle's conversation when it
// has one. A teardown that ran meanwhile gets the child retired, and sw closes once it is gone.
func (h *streamHandle) spawn(gen int, sw *spawnWait) (*streamChild, error) {
	failed := func(err error) (*streamChild, error) {
		h.mu.Lock()
		if h.spawning == sw {
			h.spawning = nil
		}
		h.mu.Unlock()
		close(sw.done)
		return nil, err
	}
	h.mu.Lock()
	st, conv := h.settings, h.conv
	h.mu.Unlock()
	cmd := h.command(h, st, conv)
	cmd.Dir = h.dir
	// All three pipes are our own files: Wait would close a StdoutPipe under the reader, which
	// must finish what the child wrote before it exited (a result outranks the exit), and an
	// unstarted command would leak StdinPipe's read end.
	inR, inW, err := os.Pipe()
	if err != nil {
		return failed(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return failed(err)
	}
	cmd.Stdin, cmd.Stdout = inR, outW
	tail, err := agents.StartWithStderrTail(cmd)
	_ = inR.Close()
	_ = outW.Close()
	if err != nil {
		_ = inW.Close()
		_ = outR.Close()
		return failed(fmt.Errorf("agy を起動できません: %w", err))
	}
	tail.Settle() // the stderr tail is kept for spawn failures only (StderrTail's contract)
	c := &streamChild{cmd: cmd, stdin: inW, exited: make(chan struct{}), fresh: true, tr: newTurnTracker(conv != "")}
	readDone := make(chan struct{})
	go h.read(c, outR, readDone)
	go h.watch(c, tail, outR, readDone)

	h.mu.Lock()
	if h.spawning == sw {
		h.spawning = nil
	}
	if h.gen != gen || !h.alive {
		c.stopAsked = true
		h.mu.Unlock()
		h.retire(c, false)
		go func() {
			<-c.exited
			close(sw.done)
		}()
		return nil, errDetached
	}
	h.child = c
	h.mu.Unlock()
	close(sw.done)
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
			h.resultIn = true
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

// waitExitedNoReap blocks until pid has exited and leaves it a zombie for cmd.Wait to reap.
func waitExitedNoReap(pid int) {
	var info unix.Siginfo
	for {
		if err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil); err != unix.EINTR {
			return
		}
	}
}

// watch waits for the child to exit, ends what is left of its group, reaps it, lets the reader
// finish, then lets go of it.
func (h *streamHandle) watch(c *streamChild, tail *agents.StderrTail, r *os.File, readDone chan struct{}) {
	pid := c.cmd.Process.Pid
	waitExitedNoReap(pid)
	h.mu.Lock()
	c.dead = true // before the drain: a turn must not be handed to a child that is gone
	h.mu.Unlock()
	c.reapMu.Lock()
	// The leader is a zombie, so the group id is still ours: tools agy left behind go with it.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = c.cmd.Wait()
	c.reaped = true
	c.reapMu.Unlock()
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
	}
	_ = r.Close()
	<-readDone
	_ = c.stdin.Close()
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
// a closed stdin otherwise (exit 0), and termination if neither works within stopGrace. The
// caller has set c.stopAsked under mu.
func (h *streamHandle) retire(c *streamChild, interrupt bool) {
	if interrupt {
		c.signalLeader(syscall.SIGINT)
	} else {
		_ = c.stdin.Close()
	}
	time.AfterFunc(h.stopGrace, func() {
		select {
		case <-c.exited:
		default:
			c.terminate()
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

		st, failure := h.runTurn(t, lease)
		agents.MarkTurnEndErr(h.slotSid, st, failure)
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
	if gen != h.idleGen || !usableLocked(c) || h.running || h.q.Head() != nil || h.q.Len() > 0 {
		h.mu.Unlock()
		return
	}
	c.stopAsked = true
	h.mu.Unlock()
	h.retire(c, false)
}

// landLocked settles t and lands st in one critical section, so a stop never sees a turn that
// has ended as one still in flight. It releases mu.
func (h *streamHandle) landLocked(t *agents.Taken, st agents.TurnState) agents.TurnState {
	h.q.Settle(t)
	h.running, h.stopping, h.sending, h.turnDone, h.resultIn = false, false, nil, nil, false
	h.state = st
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: st})
	return st
}

// runTurn runs one entry on a child and lands it. It returns the landed state and, for a failed
// turn, the reason, which the pump persists: re-reading the shared state there would pick up
// whatever the next input set meanwhile. lease is released once the child has the input; a
// turn can run for hours and must not hold a move off.
func (h *streamHandle) runTurn(t *agents.Taken, lease *lifegate.Lease) (agents.TurnState, string) {
	defer lease.Release()
	agents.MarkTurnStartRun(h.slotSid, t.In)
	h.setState(agents.TurnStarting)
	if h.beforeCommit != nil {
		h.beforeCommit()
	}
	// Until Commit: a child that is not usable any more (it exited, or a stop retired it) is
	// replaced, and a teardown puts the entry back where the queue's rules say.
	var c *streamChild
	for {
		h.mu.Lock()
		if h.q.Head() != t {
			// A stop or a teardown took the entry already: no child is started for it.
			return h.landLocked(t, agents.TurnCancelled), ""
		}
		h.mu.Unlock()
		var err error
		c, err = h.ensureChild()
		if err != nil {
			if !errors.Is(err, errDetached) {
				log.Printf("agy managed: %s: %v", h.name, err)
			}
			return h.requeueLost(t), ""
		}
		h.mu.Lock()
		if !h.alive {
			h.mu.Unlock()
			return h.requeueLost(t), ""
		}
		if !usableLocked(c) {
			h.mu.Unlock()
			continue
		}
		// The last act under the lock before the child sees the input (ADR 0105 decision 3).
		if !h.q.Commit(t) {
			return h.landLocked(t, agents.TurnCancelled), ""
		}
		break
	}
	h.stopping = false
	h.sending = c
	h.turnDone = make(chan streamResult, 1)
	c.tr.begin()
	h.state = agents.TurnRunning
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
	if h.beforeWrite != nil {
		h.beforeWrite()
	}

	retried := false
	for {
		_, werr := c.stdin.Write(userLine(t.In.Prompt))
		if werr == nil {
			break
		}
		h.mu.Lock()
		h.sending = nil
		if h.stopping || !h.alive {
			// A stop or a teardown was aimed at this input, and ending the child is how it was
			// delivered: the input is cancelled, not restored, and a first stop's queue goes on.
			return h.landLocked(t, agents.TurnCancelled), ""
		}
		if c.fresh || retried {
			// A child spawned for this very prompt died before taking it: lost, not retried.
			h.mu.Unlock()
			return h.requeueLost(t), ""
		}
		// A child an earlier turn left behind went away after answering it; the prompt never
		// reached it, so one new child gets it.
		retried = true
		c.stopAsked = true
		h.mu.Unlock()
		nc, err := h.ensureChild()
		h.mu.Lock()
		if err != nil && !errors.Is(err, errDetached) {
			log.Printf("agy managed: %s: %v", h.name, err)
			h.mu.Unlock()
			return h.requeueLost(t), ""
		}
		if err != nil || h.stopping || !h.alive {
			return h.landLocked(t, agents.TurnCancelled), ""
		}
		c = nc
		h.sending = c
		h.turnDone = make(chan streamResult, 1)
		c.tr.begin()
		h.mu.Unlock()
	}
	lease.Release()
	h.received(t, c)

	h.mu.Lock()
	done := h.turnDone
	h.mu.Unlock()
	r, got := awaitResult(done, c.exited)
	if h.beforeLand != nil {
		h.beforeLand()
	}
	h.mu.Lock()
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
	c.fresh = false
	lost := !got && !asked
	if lost {
		h.alive = false
	}
	st := outcomeState(out, got, stopped)
	h.landLocked(t, st)
	failure := ""
	if st == agents.TurnFailed {
		failure = out.Error
		if failure == "" {
			failure = "agy: " + out.Status
		}
	}
	if lost {
		h.recordLost(c)
	}
	return st, failure
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

// requeueLost puts t back after no child could take it and takes the handle down: the input
// waits for Resume rather than being retried into a runtime that does not start. An entry a
// stop or a detach already claimed is not put back.
func (h *streamHandle) requeueLost(t *agents.Taken) agents.TurnState {
	h.mu.Lock()
	requeued := h.q.Requeue(t)
	h.alive = false
	if !requeued {
		return h.landLocked(t, agents.TurnCancelled)
	}
	return h.landLocked(t, agents.TurnUnknown)
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
// left its delivery to here; the child is retired in the same critical section, so the next
// turn never goes to it.
func (h *streamHandle) received(t *agents.Taken, c *streamChild) {
	h.mu.Lock()
	h.sending = nil
	stop := h.q.Received(t) && !c.stopAsked
	if stop {
		c.stopAsked = true
	}
	h.mu.Unlock()
	if stop {
		h.signalStop(c)
	}
}

// signalStop interrupts the child a stop retired. It exits after answering, and the next turn
// respawns it.
func (h *streamHandle) signalStop(c *streamChild) {
	if h.beforeSignal != nil {
		h.beforeSignal()
	}
	h.retire(c, true)
}

// Interrupt is the Console's stop (ADR 0105 decisions 1-3).
func (h *streamHandle) Interrupt(opts agents.InterruptOpts) (agents.InterruptResult, error) {
	h.mu.Lock()
	out := h.q.Interrupt(opts, h.q.Head() != nil)
	h.stopLocked(out.Head)
	return out.Result, nil
}

// stopLocked delivers a stop per head and releases mu. The child the stop is aimed at is
// marked under the same lock that decided it.
func (h *streamHandle) stopLocked(head agents.HeadAction) {
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
	if h.resultIn {
		// The turn answered before the stop; it lands on that answer.
		h.mu.Unlock()
		return
	}
	h.stopping = true
	h.state = agents.TurnInterrupting
	var now, pending *streamChild
	if c := h.child; head == agents.HeadStopNow && usableLocked(c) {
		c.stopAsked = true
		now = c
	}
	if head == agents.HeadStopPending {
		pending = h.sending
	}
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnInterrupting})
	if now != nil {
		h.signalStop(now)
	}
	if pending != nil {
		// received delivers the stop once the prompt is written. A write that does not return
		// (a child that stopped reading stdin) would hold it forever, so the child is ended
		// after stopGrace and the write fails into the cancelled path.
		time.AfterFunc(h.stopGrace, func() {
			h.mu.Lock()
			stuck := h.sending == pending && !pending.stopAsked
			if stuck {
				pending.stopAsked = true
			}
			h.mu.Unlock()
			if stuck {
				pending.terminate()
			}
		})
	}
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

// teardownLocked is what Detach and Down share: the handle stops taking input, a spawn in
// flight will retire its child, and the current child is marked ours to end. It returns the
// child to end (nil when there is none or it is already retiring) and a channel that closes
// once every child of the handle has exited.
func (h *streamHandle) teardownLocked() (*streamChild, <-chan struct{}) {
	h.alive = false
	h.gen++
	h.idleGen++
	if h.running {
		h.stopping = true
	}
	c, sw := h.child, h.spawning
	var end *streamChild
	if c != nil && !c.stopAsked {
		c.stopAsked = true
		end = c
	}
	done := make(chan struct{})
	go func() {
		if c != nil {
			<-c.exited
		}
		if sw != nil {
			<-sw.done
		}
		close(done)
	}()
	return end, done
}

// Detach takes the handle off its runtime (stop/halt/archive): the queue goes, a running turn
// is interrupted, the child ends. The conversation stays on disk for a later Resume. The
// channel closes when the child, and one a spawn in flight started, have exited.
func (h *streamHandle) Detach() <-chan struct{} {
	h.mu.Lock()
	h.q.DropAll()
	running := h.running
	end, done := h.teardownLocked()
	h.mu.Unlock()
	if end != nil {
		h.retire(end, running)
	}
	return done
}

// Down marks the handle dead for Agent shutdown and returns the stop of its child. A turn it
// cuts short lands cancelled; the queue's held inputs stay for the next boot.
func (h *streamHandle) Down() (stop func()) {
	h.mu.Lock()
	end, _ := h.teardownLocked()
	h.mu.Unlock()
	return func() {
		if end != nil {
			end.terminate()
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
