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
	readStat    = readProcStat
	uptimeTicks = readUptimeTicks
)

// procPin is a process held by a handle that cannot be redirected to another process: a
// signal through it reaches the process it was opened on or nobody, whatever happens to the
// pid number meanwhile.
type procPin interface {
	Signal(sig syscall.Signal) error
	// Alive reports that the process has not been reaped. While it holds, its pid (and the
	// session or group id equal to it) cannot be handed to anyone else.
	Alive() bool
	Close()
}

// StopBackgroundShell ends one background shell of the session and what belongs to it, and
// reports how many processes of its session may still be running (not proven to belong, so
// left alone). The pid must still be one of this session's background shells in a FRESH
// snapshot and carry the start time the caller saw.
//
// What it signals, it signals through a pidfd opened BEFORE the process is re-read, so a
// reused pid is never reached; nothing is signalled by group or session number. The /proc
// scan is not atomic — it can mix a process that has since exited with one that took its
// number — so the scan only nominates; a nominee is taken only on evidence re-read after its
// pidfd is open, checked against a pinned process that is confirmed alive AFTER that read:
//
//   - its parent is a pinned process still alive, or
//   - the shell leads its own session (claude starts it that way), the nominee is in that
//     session, and it started before a moment the shell was confirmed alive. A session id
//     changes only by setsid, which makes it the caller's own pid, so a process that started
//     while the original session existed and still carries its id was born into it.
//
// A process of the session that fits neither — a child forked by a TERM handler just before
// the shell exited — is counted, not signalled.
func StopBackgroundShell(name string, pid int, start uint64) (int, error) {
	root := paneRoot(name)
	if root == 0 {
		return 0, ErrBGShellGone
	}
	tab := freshProcs()
	if err := checkBGShell(root, tab, pid, start); err != nil {
		return 0, err
	}
	leader := bgTarget{pid, start}
	h, ok := pinProc(pid, start)
	if !ok {
		return 0, ErrBGShellGone // exited between the scan and the pin
	}
	st := &bgStop{leader: leader, pins: map[bgTarget]procPin{leader: h}, fresh: []bgTarget{leader}}
	defer st.close()
	if r, ok := readStat(pid); ok && r.StartTicks == start && r.Sid == pid {
		st.leadsSession = true
	}
	st.absorb(tab)
	st.signalNew(syscall.SIGTERM)
	for deadline := time.Now().Add(bgStopGrace); time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
		tab = freshProcs()
		st.absorb(tab)
		st.signalNew(syscall.SIGTERM)
		if len(st.live()) == 0 {
			return st.leftovers(), nil
		}
	}
	for _, t := range st.live() {
		_ = st.pins[t].Signal(syscall.SIGKILL)
	}
	time.Sleep(100 * time.Millisecond)
	return st.leftovers(), nil
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

// bgStop is one stop in progress.
type bgStop struct {
	leader       bgTarget
	leadsSession bool
	// aliveAt is an uptime (clock ticks) at which the shell was confirmed alive; a process of
	// its session that started before it was born into the original session.
	aliveAt uint64
	pins    map[bgTarget]procPin
	fresh   []bgTarget // pinned, not yet sent SIGTERM
}

// absorb takes in the processes tab nominates and the evidence confirms (see
// StopBackgroundShell), repeating while it finds any, since a new member can vouch for its
// children.
func (st *bgStop) absorb(tab map[int]procInfo) {
	if st.leadsSession {
		u := uptimeTicks()
		if u > 0 && st.pins[st.leader].Alive() {
			st.aliveAt = u
		}
	}
	for changed := true; changed; {
		changed = false
		byPID := map[int]bgTarget{}
		for t := range st.pins {
			byPID[t.pid] = t
		}
		for pid, pi := range tab {
			t := bgTarget{pid, pi.StartTicks}
			if _, done := st.pins[t]; done || pi.State == 'Z' {
				continue
			}
			_, parentPinned := byPID[pi.PPID]
			if !parentPinned && !(st.leadsSession && pi.Sid == st.leader.pid) {
				continue // not nominated
			}
			if st.confirm(t, byPID) {
				changed = true
			}
		}
	}
}

// confirm pins t and keeps it only on evidence read after the pin (see StopBackgroundShell).
func (st *bgStop) confirm(t bgTarget, byPID map[int]bgTarget) bool {
	h, ok := pinProc(t.pid, t.start)
	if !ok {
		return false
	}
	r, ok := readStat(t.pid)
	member := false
	if ok && r.StartTicks == t.start && r.State != 'Z' {
		if p, pinned := byPID[r.PPID]; pinned && st.pins[p].Alive() {
			member = true
		} else if st.leadsSession && r.Sid == st.leader.pid && st.aliveAt > 0 && r.StartTicks < st.aliveAt {
			member = true
		}
	}
	if !member {
		h.Close()
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

// live lists the pinned processes still running (re-read through their own pid, start-checked;
// a zombie has already exited).
func (st *bgStop) live() []bgTarget {
	var out []bgTarget
	for t := range st.pins {
		if r, ok := readStat(t.pid); ok && r.StartTicks == t.start && r.State != 'Z' {
			out = append(out, t)
		}
	}
	return out
}

// leftovers counts running processes of the shell's session that the stop did not take: the
// member is told rather than the stop reporting a clean end. It may include a process of a
// later session that reused the id; it is only counted, never signalled.
func (st *bgStop) leftovers() int {
	if !st.leadsSession {
		return 0
	}
	n := 0
	for pid, pi := range freshProcs() {
		if pi.Sid != st.leader.pid || pi.State == 'Z' {
			continue
		}
		if _, pinned := st.pins[bgTarget{pid, pi.StartTicks}]; pinned {
			continue
		}
		n++
	}
	return n
}

func (st *bgStop) close() {
	for _, h := range st.pins {
		h.Close()
	}
}

// readProcStat reads one process's stat now.
func readProcStat(pid int) (procInfo, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procInfo{}, false
	}
	return procx.Parse(pid, string(b))
}

// readUptimeTicks is the time since boot in clock ticks, the clock of StartTicks; 0 if unread.
func readUptimeTicks() uint64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	sec, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	return uint64(sec * clockTicks)
}

// pidfdPin is a procPin over a pidfd.
type pidfdPin int

func (p pidfdPin) Signal(sig syscall.Signal) error {
	return unix.PidfdSendSignal(int(p), sig, nil, 0)
}

// Alive: signal 0 through the pidfd fails with ESRCH only once the process has been reaped.
func (p pidfdPin) Alive() bool { return unix.PidfdSendSignal(int(p), 0, nil, 0) == nil }

func (p pidfdPin) Close() { _ = unix.Close(int(p)) }

// pinProcFD opens a pidfd on pid and then checks that the process it refers to started at
// start. The order matters: once the pidfd is open it can only ever reach that process, so a
// start time read afterwards that matches proves the handle is on the right one.
func pinProcFD(pid int, start uint64) (procPin, bool) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, false
	}
	if pi, ok := readProcStat(pid); !ok || pi.StartTicks != start {
		_ = unix.Close(fd)
		return nil, false
	}
	return pidfdPin(fd), true
}
