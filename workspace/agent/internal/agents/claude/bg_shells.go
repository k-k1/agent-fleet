package claude

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
)

// BGShell is one background shell claude left running under a session's pane: a
// run_in_background Bash or a Monitor loop. The mirror lists them so the member can see what
// keeps the session "background busy" and stop one that will never finish (#3279).
type BGShell struct {
	PID int `json:"pid"`
	// Start is the process start time in clock ticks since boot. The stop request sends it
	// back, and a mismatch means the pid now belongs to another process.
	Start   uint64 `json:"start"`
	Command string `json:"command"`
	// StartedAt is Start as wall time (RFC 3339); empty when the boot time is unreadable.
	StartedAt string `json:"startedAt,omitempty"`
	// Running: something in the subtree is on a CPU or in disk wait right now.
	Running bool `json:"running"`
	// CPUSec is the subtree's total user+system CPU time. A shell waiting for 30 minutes with
	// almost none of it is what the Console flags as possibly stuck.
	CPUSec float64 `json:"cpuSec"`
}

// clockTicks is USER_HZ. It is 100 on every Linux ABI this runs on, and reading it properly
// needs sysconf (cgo).
const clockTicks = 100

// BackgroundShells lists the background shells under the session's pane, oldest first.
func BackgroundShells(name string) []BGShell {
	root := paneRoot(name)
	if root == 0 {
		return nil
	}
	return backgroundShellsIn(root, procx.Snapshot(), bootTime())
}

// backgroundShellsIn is the pure core of BackgroundShells. It applies the same signature as
// backgroundShellBusyIn (a shell whose parent is claude, not wrapping another claude), so the
// list never names a shell the busy badge does not count, and vice versa.
func backgroundShellsIn(root int, tab map[int]procInfo, boot time.Time) []BGShell {
	kids := procx.Children(tab)
	var out []BGShell
	for _, pid := range descendants(root, kids) {
		pi, ok := tab[pid]
		if !ok || !isBackgroundShell(pid, pi, kids, tab) {
			continue
		}
		sh := BGShell{PID: pid, Start: pi.StartTicks, Command: shellCommand(procArgv(pid))}
		if !boot.IsZero() && pi.StartTicks > 0 {
			sh.StartedAt = boot.Add(time.Duration(pi.StartTicks) * time.Second / clockTicks).Format(time.RFC3339)
		}
		var ticks uint64
		for _, p := range append([]int{pid}, descendants(pid, kids)...) {
			c := tab[p]
			ticks += c.CPUTicks
			if c.State == 'R' || c.State == 'D' {
				sh.Running = true
			}
		}
		sh.CPUSec = float64(ticks) / clockTicks
		out = append(out, sh)
	}
	// Oldest first: the one most likely to be stuck heads the list.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Start < out[j-1].Start; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// isBackgroundShell is the signature shared by the busy badge and the list.
func isBackgroundShell(pid int, pi procInfo, kids map[int][]int, tab map[int]procInfo) bool {
	return shellComm[pi.Comm] && procIsClaude(pi.PPID, tab[pi.PPID]) && !subtreeHasClaude(pid, kids, tab)
}

// descendants lists every process below root (root excluded), breadth first.
func descendants(root int, kids map[int][]int) []int {
	seen := map[int]bool{root: true}
	var out []int
	queue := append([]int(nil), kids[root]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		out = append(out, pid)
		queue = append(queue, kids[pid]...)
	}
	return out
}

// shellCommand recovers the command the agent asked for from claude's Bash wrapper,
// `bash -c "source <snapshot> … && eval '<command>' && pwd -P >| <cwd file>"`. Inside the
// single quotes a quote is spelled '"'"'. Anything else (a different wrapper, a newer CLI)
// falls back to the whole script, which is still the truth, just noisier.
func shellCommand(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	script := argv[len(argv)-1]
	if len(argv) >= 3 && argv[len(argv)-2] == "-c" {
		const open = "eval '"
		if i := strings.Index(script, open); i >= 0 {
			body := script[i+len(open):]
			if j := strings.LastIndex(body, "' && pwd -P"); j >= 0 {
				body = body[:j]
			} else if j := strings.LastIndex(body, "'"); j >= 0 {
				body = body[:j]
			}
			return strings.ReplaceAll(body, `'"'"'`, `'`)
		}
	}
	return strings.Join(argv, " ")
}

// bootTime reads btime from /proc/stat; zero when unreadable.
func bootTime() time.Time {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "btime "); ok {
			if s, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return time.Unix(s, 0)
			}
		}
	}
	return time.Time{}
}

// ErrBGShellGone is returned when the pid is no longer one of the session's background
// shells, or now belongs to a process with a different start time.
var ErrBGShellGone = errors.New("background shell not found")

// bgStopGrace is how long SIGTERM gets before SIGKILL. Variables for tests.
var (
	bgStopGrace = 3 * time.Second
	freshProcs  = procx.Fresh
	paneRoot    = procx.PaneRoot
	pinProc     = pinProcFD
)

// procPin is a process held by a handle that cannot be redirected to another process: a
// signal through it reaches the process it was opened on or nobody, whatever happens to the
// pid number meanwhile.
type procPin interface {
	Signal(sig syscall.Signal) error
	Close()
}

// StopBackgroundShell ends one background shell of the session and everything that belongs
// to it: its subtree, and the members of the process group it leads (claude starts each
// background shell as a session leader, and a child that re-parented away from the tree is
// still in that group). The pid must still be one of this session's background shells in a
// FRESH snapshot and carry the start time the caller saw.
//
// Every process is signalled through a pidfd opened BEFORE its start time is re-checked, so a
// pid (or a process group id) reused at any point can never be signalled; the group itself is
// never signalled by number for the same reason. A process that joins during the grace (a
// child forked by a TERM handler) is taken in on the next scan. Returns once nothing is left or
// SIGKILL has been sent to what is.
func StopBackgroundShell(name string, pid int, start uint64) error {
	root := paneRoot(name)
	if root == 0 {
		return ErrBGShellGone
	}
	tab := freshProcs()
	if err := checkBGShell(root, tab, pid, start); err != nil {
		return err
	}
	st := &bgStop{pgid: pid, pins: map[bgTarget]procPin{}}
	defer st.close()
	if !st.absorb(tab, bgTarget{pid, start}) {
		return ErrBGShellGone // exited between the scan and the pin
	}
	st.signalNew(syscall.SIGTERM)
	for deadline := time.Now().Add(bgStopGrace); time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
		tab = freshProcs()
		st.absorb(tab, bgTarget{})
		st.signalNew(syscall.SIGTERM)
		if len(st.live(tab)) == 0 {
			return nil
		}
	}
	for _, t := range st.live(freshProcs()) {
		_ = st.pins[t].Signal(syscall.SIGKILL)
	}
	return nil
}

// bgTarget is a process identity: a pid alone can name a different process later.
type bgTarget struct {
	pid   int
	start uint64
}

// checkBGShell validates the request against tab: pid is one of the pane's background shells
// and still the process the caller saw.
func checkBGShell(root int, tab map[int]procInfo, pid int, start uint64) error {
	pi, ok := tab[pid]
	if !ok || pi.StartTicks != start {
		return ErrBGShellGone
	}
	kids := procx.Children(tab)
	for _, p := range descendants(root, kids) {
		if p == pid {
			if isBackgroundShell(pid, pi, kids, tab) {
				return nil
			}
			break
		}
	}
	return ErrBGShellGone
}

// bgStop is one stop in progress: the processes pinned so far, and which of them have not
// been sent SIGTERM yet.
type bgStop struct {
	pgid   int
	pins   map[bgTarget]procPin
	fresh  []bgTarget
	leader bgTarget
}

// absorb pins every process in tab that belongs to the stop and is not pinned yet. leader is
// set on the first call only. A process belongs when its parent is a pinned process alive in
// this same table, or when it is in the shell's process group while a pinned process alive in
// this table still is: a group id cannot be reused while any member lives, so that member
// vouches for the group. Reports whether the leader was pinned.
func (st *bgStop) absorb(tab map[int]procInfo, leader bgTarget) bool {
	if leader.pid != 0 {
		st.leader = leader
		if !st.pin(leader) {
			return false
		}
	}
	for changed := true; changed; {
		changed = false
		alive := map[int]bool{}
		groupVouched := false
		for t := range st.pins {
			if pi, ok := tab[t.pid]; ok && pi.StartTicks == t.start && pi.State != 'Z' {
				alive[t.pid] = true
				if pi.Pgrp == st.pgid {
					groupVouched = true
				}
			}
		}
		for pid, pi := range tab {
			t := bgTarget{pid, pi.StartTicks}
			if _, done := st.pins[t]; done || pi.State == 'Z' {
				continue
			}
			if alive[pi.PPID] || (groupVouched && pi.Pgrp == st.pgid) {
				if st.pin(t) {
					changed = true
				}
			}
		}
	}
	return true
}

// pin opens a handle on t and keeps it only if the process behind it is still t.
func (st *bgStop) pin(t bgTarget) bool {
	h, ok := pinProc(t.pid, t.start)
	if !ok {
		return false
	}
	st.pins[t] = h
	st.fresh = append(st.fresh, t)
	return true
}

// signalNew sends sig to the processes pinned since the last call, the shell last so it does
// not outlive its children and report each of them as killed.
func (st *bgStop) signalNew(sig syscall.Signal) {
	for _, t := range st.fresh {
		if t != st.leader {
			_ = st.pins[t].Signal(sig)
		}
	}
	for _, t := range st.fresh {
		if t == st.leader {
			_ = st.pins[t].Signal(sig)
		}
	}
	st.fresh = nil
}

// live lists the pinned processes still running in tab (a zombie has already exited).
func (st *bgStop) live(tab map[int]procInfo) []bgTarget {
	var out []bgTarget
	for t := range st.pins {
		if pi, ok := tab[t.pid]; ok && pi.StartTicks == t.start && pi.State != 'Z' {
			out = append(out, t)
		}
	}
	return out
}

func (st *bgStop) close() {
	for _, h := range st.pins {
		h.Close()
	}
}

// pidfdPin is a procPin over a pidfd.
type pidfdPin int

func (p pidfdPin) Signal(sig syscall.Signal) error {
	return unix.PidfdSendSignal(int(p), sig, nil, 0)
}

func (p pidfdPin) Close() { _ = unix.Close(int(p)) }

// pinProcFD opens a pidfd on pid and then checks that the process it refers to started at
// start. The order matters: once the pidfd is open it can only ever reach that process, so a
// start time read afterwards that matches proves the handle is on the right one.
func pinProcFD(pid int, start uint64) (procPin, bool) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if pi, ok := procx.Parse(pid, string(b)); err != nil || !ok || pi.StartTicks != start {
		_ = unix.Close(fd)
		return nil, false
	}
	return pidfdPin(fd), true
}
