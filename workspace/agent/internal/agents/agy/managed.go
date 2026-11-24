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
	// code and sig are the exit, set under the handle's mu before exited closes.
	code, sig int
	// gen is the teardown generation the child was spawned in.
	gen int

	// reapMu orders every signal against the reap: while reaped is false the leader is alive or
	// a zombie, so its pid, and therefore its process group id, cannot belong to anyone else.
	reapMu sync.Mutex
	reaped bool
	// leaderOnly: waitid is refused, so the group id is not known to stay ours until the reap;
	// group signals go to the leader alone, through os.Process, which never signals a reaped pid.
	leaderOnly bool

	// Under the handle's mu:
	tr *turnTracker
	// fresh: spawned for the turn now using it and not yet through a turn.
	fresh bool
	// stopAsked: a stop, an idle close or a teardown was aimed at this child, so its exit is
	// ours, not a lost runtime, and no further turn goes to it.
	stopAsked bool
	// dead: the process has exited (it may still be draining).
	dead bool
	// heard: the child wrote at least one event.
	heard bool
}

// signalGroup sends sig to the child's whole group, never after the reap.
func (c *streamChild) signalGroup(sig syscall.Signal) {
	c.reapMu.Lock()
	defer c.reapMu.Unlock()
	switch {
	case c.leaderOnly:
		_ = c.cmd.Process.Signal(sig)
	case !c.reaped:
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

// terminate ends the child's group: SIGTERM, then SIGKILL after grace. A leader that goes on
// TERM while a tool ignores it is covered by watch, which kills the group once the leader exits.
func (c *streamChild) terminate(grace time.Duration) {
	c.signalGroup(syscall.SIGTERM)
	time.AfterFunc(grace, func() { c.signalGroup(syscall.SIGKILL) })
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
	termGrace time.Duration

	mu       sync.Mutex
	alive    bool // attached: accepts input. The child may be absent between turns.
	gen      int  // bumped by every teardown (Detach, Down); a spawn from before it retires its child
	child    *streamChild
	spawning *spawnWait
	// kids is every child started and not yet reaped, retiring ones included, so a teardown can
	// wait for all of them.
	kids map[*streamChild]struct{}
	// sending is the child a prompt is being written to, nil once the write returned.
	sending *streamChild
	// turnChild is the child that holds the committed turn, nil between turns and while the
	// turn is moving to a new child.
	turnChild *streamChild
	// delivered: turnChild has the prompt (the write returned).
	delivered bool
	// dropped: a detach discarded the turn in flight, so nothing may put it back.
	dropped  bool
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
	// afterEnsure after the turn got (or failed to get) its child, beforeWrite between the Commit
	// and the prompt's write, beforeLand between the result and the turn's landing, beforeSignal
	// between a stop's decision and its SIGINT. mu is released for each.
	beforeCommit, afterEnsure, beforeWrite, beforeLand, beforeSignal func()

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
		termGrace: streamTermGrace,
		kids:      map[*streamChild]struct{}{},
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
			if old := h.oldestKidLocked(); old != nil {
				// A child a teardown retired is still on its way out: two agy processes on one
				// conversation must never overlap.
				c = old
			}
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
		case <-time.After(h.stopGrace + h.termGrace + time.Second):
			c.terminate(h.termGrace)
			select {
			case <-c.exited:
			case <-time.After(2*h.termGrace + time.Second):
				return nil, errors.New("agy の前のプロセスが終了しません")
			}
		}
	}
}

// oldestKidLocked is a child that is started and not reaped, nil when there is none.
func (h *streamHandle) oldestKidLocked() *streamChild {
	for k := range h.kids {
		return k
	}
	return nil
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
	c := &streamChild{cmd: cmd, stdin: inW, exited: make(chan struct{}), gen: gen, fresh: true, tr: newTurnTracker(conv != "")}
	// Registered, and published or retired, before watch starts: a child that exits at once is
	// then let go by watch rather than published after it was reaped.
	h.mu.Lock()
	if h.spawning == sw {
		h.spawning = nil
	}
	h.kids[c] = struct{}{}
	stale := h.gen != gen || !h.alive
	if stale {
		c.stopAsked = true
	} else {
		h.child = c
	}
	h.mu.Unlock()
	close(sw.done) // from here the child is in kids, where a teardown finds it
	readDone := make(chan struct{})
	go h.read(c, outR, readDone)
	go h.watch(c, tail, outR, readDone)
	if stale {
		h.retire(c, false)
		return nil, errDetached
	}
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
	c.heard = true
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
// false: waitid failed, and nothing is known about pid.
func waitExitedNoReap(pid int) bool {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err == nil
		}
	}
}

// waitExited is waitExitedNoReap, replaceable by tests that need waitid refused.
var waitExited = waitExitedNoReap

// watch waits for the child to exit, ends what is left of its group, reaps it, lets the reader
// finish, then lets go of it.
func (h *streamHandle) watch(c *streamChild, tail *agents.StderrTail, r *os.File, readDone chan struct{}) {
	pid := c.cmd.Process.Pid
	if waitExited(pid) {
		h.mu.Lock()
		c.dead = true // before the drain: a turn must not be handed to a child that is gone
		h.mu.Unlock()
		c.reapMu.Lock()
		// The leader is a zombie, so the group id is still ours: what agy left in its group goes
		// with it. A tool's background job does not outlive the child; under the read-only preset
		// no tool can start one anyway (command(*) is denied).
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = c.cmd.Wait()
		c.reaped = true
		c.reapMu.Unlock()
	} else {
		// waitid is refused here: group signalling ends before the reap can happen, and Wait
		// alone follows, with no group kill.
		c.reapMu.Lock()
		c.leaderOnly = true
		c.reapMu.Unlock()
		_ = c.cmd.Wait()
		h.mu.Lock()
		c.dead = true
		h.mu.Unlock()
		c.reapMu.Lock()
		c.reaped = true
		c.reapMu.Unlock()
	}
	tail.Release()
	code, sig := -1, 0
	if c.cmd.ProcessState != nil {
		if ws, ok := c.cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() {
				sig = int(ws.Signal())
				code = 128 + sig
			} else {
				code = ws.ExitStatus()
			}
		}
	}
	h.mu.Lock()
	c.code, c.sig = code, sig
	h.mu.Unlock()
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
	delete(h.kids, c)
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
			c.terminate(h.termGrace)
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
		gen := h.gen
		h.mu.Unlock()

		st, failure := h.runTurn(t, lease, gen)
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
	h.turnChild, h.delivered, h.dropped = nil, false, false
	h.state = st
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: st})
	return st
}

// runTurn runs one entry on a child and lands it. gen is the teardown generation the entry was
// taken in: a turn that outlived a teardown must not take down the generation a Resume started.
// It returns the landed state and, for a failed turn, the reason, which the pump persists:
// re-reading the shared state there would pick up whatever the next input set meanwhile. lease is
// released once the child has the input; a turn can run for hours and must not hold a move off.
func (h *streamHandle) runTurn(t *agents.Taken, lease *lifegate.Lease, gen int) (agents.TurnState, string) {
	defer lease.Release()
	agents.MarkTurnStartRun(h.slotSid, t.In)
	h.setState(agents.TurnStarting)
	if h.beforeCommit != nil {
		h.beforeCommit()
	}
	// Until Commit: a child an earlier turn left that is not usable any more (it exited, or a
	// stop retired it) is replaced; a fresh child that died before its first prompt is a runtime
	// that does not start, and is not respawned.
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
		if h.afterEnsure != nil {
			h.afterEnsure()
		}
		if err != nil {
			if !errors.Is(err, errDetached) {
				log.Printf("agy managed: %s: %v", h.name, err)
			}
			h.mu.Lock()
			return h.requeueLostLocked(t, gen), ""
		}
		h.mu.Lock()
		if !h.alive {
			return h.requeueLostLocked(t, gen), ""
		}
		if !usableLocked(c) {
			if c.fresh && !c.stopAsked {
				log.Printf("agy managed: %s: agy exited before its first turn", h.name)
				return h.requeueLostLocked(t, gen), ""
			}
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
	h.sending, h.turnChild = c, c
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
			return h.requeueLostLocked(t, gen), ""
		}
		// A child an earlier turn left behind went away after answering it; the prompt never
		// reached it, so one new child gets it.
		retried = true
		c.stopAsked = true
		h.turnChild = nil // the turn is not on that child any more: a stop now reaches it
		h.mu.Unlock()
		nc, err := h.ensureChild()
		h.mu.Lock()
		if err != nil && !errors.Is(err, errDetached) {
			log.Printf("agy managed: %s: %v", h.name, err)
			return h.requeueLostLocked(t, gen), ""
		}
		if err != nil || h.stopping || !h.alive {
			return h.landLocked(t, agents.TurnCancelled), ""
		}
		c = nc
		h.sending, h.turnChild = c, c
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
	if !got && !asked && !stopped && c.fresh && !c.heard {
		// Spawned for this prompt and gone without a word: agy never started (init comes
		// before any work), so the input waits for Resume instead of being lost with it.
		st := h.requeueLostLocked(t, gen)
		h.recordLost(c)
		return st, ""
	}
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
	if lost && h.gen == gen {
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

// requeueLostLocked puts t back after no child could take it and, when it did go back, takes the
// handle down: the input waits for Resume rather than being retried into a runtime that does not
// start. An entry a stop or a detach already claimed is not put back, and the rest of the queue
// goes on. A turn from before a teardown leaves the handle as the teardown and any Resume after
// it left it. Called with mu held; it releases mu.
func (h *streamHandle) requeueLostLocked(t *agents.Taken, gen int) agents.TurnState {
	if h.dropped || !h.q.Requeue(t) {
		return h.landLocked(t, agents.TurnCancelled)
	}
	if h.gen == gen {
		h.alive = false
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
	h.delivered = true
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

// turnEndedLocked reports that the turn in flight has already ended, though it has not landed
// yet: its result is in, or the child that took its prompt is gone. A stop then has nothing to
// stop, and must not turn a crash into a cancellation. A prompt not yet delivered has not
// ended, whatever happened to the child it was meant for.
func (h *streamHandle) turnEndedLocked() bool {
	return h.resultIn || h.delivered && h.turnChild != nil && h.turnChild.dead
}

// Interrupt is the Console's stop (ADR 0105 decisions 1-3).
func (h *streamHandle) Interrupt(opts agents.InterruptOpts) (agents.InterruptResult, error) {
	h.mu.Lock()
	if h.q.Head() != nil && h.turnEndedLocked() && !opts.DiscardQueue && !h.q.Episode() {
		// A first stop on a turn that has already ended stops nothing, so it opens no stop
		// episode either: the next stop is a first stop for the turn it finds.
		h.mu.Unlock()
		return agents.InterruptResult{Stop: agents.StopFirst}, nil
	}
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
	if h.turnEndedLocked() {
		// The turn ended before the stop (a discarding stop still emptied the queue); it lands
		// on its own outcome.
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
				pending.terminate(h.termGrace)
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
// child to end (nil when there is none or it is already retiring) and a channel that closes once
// every child started before the teardown has exited, retiring ones included.
func (h *streamHandle) teardownLocked() (*streamChild, <-chan struct{}) {
	h.alive = false
	h.gen++
	h.idleGen++
	if h.running && !h.turnEndedLocked() {
		// A turn that already answered, or whose child already died, keeps its own outcome: the
		// teardown only cleans up after it.
		h.stopping = true
	}
	tdGen, sw := h.gen, h.spawning
	var end *streamChild
	if c := h.child; c != nil && !c.stopAsked {
		c.stopAsked = true
		end = c
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if sw != nil {
			<-sw.done // its child, if it started, is in kids from here
		}
		h.mu.Lock()
		var wait []chan struct{}
		for k := range h.kids {
			if k.gen < tdGen {
				wait = append(wait, k.exited)
			}
		}
		h.mu.Unlock()
		for _, ch := range wait {
			<-ch
		}
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
	h.dropped = running
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
			end.terminate(h.termGrace)
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
