package bootinstall

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// superviseStep is the hidden boot-install step npmInstall re-executes this binary as. Its
// file descriptor 3 is the npm lock.
const superviseStep = "npm-supervise"

// superviseGrace bounds how long a wait for a process group to empty lasts.
const superviseGrace = 5 * time.Second

// MaybeSupervise runs the supervisor and exits when args (os.Args[1:]) ask for it; otherwise it
// returns. Run calls it first, and the package's TestMain does too, because a test binary is
// what the re-exec starts there.
func MaybeSupervise(args []string) { maybeSupervise(args, superviseGrace, killPid, os.Stdout) }

// killPid is the supervisor's SIGKILL of one process.
func killPid(pid int) { _ = unix.Kill(pid, unix.SIGKILL) }

// maybeSupervise is MaybeSupervise with the grace, the kill and the report's writer given. Only
// the tests' TestMain passes others (a shorter grace, a kill that fails as one into
// uninterruptible I/O would, a report padded past any size limit);
// production never reads them from anywhere, so nothing inherited can change them.
func maybeSupervise(args []string, grace time.Duration, kill func(pid int), report io.Writer) {
	if len(args) < 3 || args[0] != "boot-install" || args[1] != superviseStep {
		return
	}
	os.Exit(supervise(args[2], args[3:], grace, kill, report))
}

// supervise runs npm in its own (the supervisor's) process group, passes the lock on to it, and
// keeps the lock open until nothing npm started is left: the lock is an flock, dropped only when
// the last holder of the file description exits, so a postinstall child that does not inherit it
// still cannot outlive the lock. The caller owns the deadline and kills the whole group
// (supervisor included), so no signal handling is needed here.
func supervise(npm string, args []string, grace time.Duration, kill func(pid int), report io.Writer) int {
	// The step signals its whole group at the end: it must lead its own, never a caller's.
	lock := os.NewFile(3, "npm-lock")
	if lock == nil || syscall.Getpgrp() != os.Getpid() {
		return 125
	}
	defer lock.Close() // the last holder if npm's children did not inherit it: live until the end
	// As a subreaper, every orphan of npm's tree is re-parented here rather than to init, so
	// "no child left" proves that no descendant is left, including one that left the group with
	// setsid or was forked while /proc was being read. It needs no privilege (Linux 3.4).
	reaper := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) == nil
	cmd := exec.Command(npm, args...)
	cmd.ExtraFiles = []*os.File{lock}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err) // the cause main reports for a direct start
		return 127
	}
	werr := cmd.Wait()
	deadline := time.Now().Add(grace)
	stopped := false
	if reaper {
		var known bool
		stopped, known = stopChildren(deadline, kill)
		if !known {
			stopped = stopGroup(deadline, kill) // waitid refused: the group listing is what is left
		}
	} else {
		stopped = stopGroup(deadline, kill)
	}
	if !stopped {
		reportStuck(report)
	}
	return superviseExit(werr, stopped)
}

// reportStuck names, on w (the caller reads the supervisor's stdout, which npm never gets), each
// child still there once the grace ran out, as "stuck <pid> <start>". Only this process can tell
// them apart while they are its unreaped children, and one that left npm's group with setsid is
// invisible to the caller's group listing; the caller records the ones still running so that no
// later start writes under them.
func reportStuck(w io.Writer) {
	self := os.Getpid()
	for _, p := range listProcs() {
		if p.ppid != self {
			continue
		}
		if child, err := isChild(p.pid); err == nil && !child {
			continue // a /proc of another pid namespace: not a process of ours
		}
		fmt.Fprintf(w, "stuck %d %d\n", p.pid, p.start)
	}
}

// superviseExit is the supervisor's exit code: npm's own, except that a successful npm whose
// processes could not be stopped fails, the rule the caller applies to its group.
func superviseExit(werr error, stopped bool) int {
	if werr == nil {
		if stopped {
			return 0
		}
		fmt.Fprintln(os.Stderr, "npm's processes did not stop")
		return 1
	}
	var ee *exec.ExitError
	if errors.As(werr, &ee) {
		if c := ee.ExitCode(); c >= 0 {
			return c
		}
	}
	return 1
}

// stopChildren kills this subreaper's children until it has none, and reports whether that
// happened before deadline. Only its own children are signalled, by pid: a child's pid cannot be
// reused before this process reaps it, and nothing reaps between the listing and the kill, so
// the pid still names the child that was listed. A grandchild is reached once its parent dies and
// it is re-parented here. A child whose stat says zombie is signalled too: when only the leader
// thread of a multi-threaded process has exited, the rest still run, and a kill reaches them all.
// known is false when waitid itself is refused, so neither "no child left" nor "this is my child"
// can be told.
func stopChildren(deadline time.Time, kill func(pid int)) (stopped, known bool) {
	self := os.Getpid()
	for ; ; time.Sleep(20 * time.Millisecond) {
		none, err := reapChildren()
		if err != nil {
			return false, false
		}
		if none {
			return true, true
		}
		for _, pid := range childrenOf(self) {
			child, err := isChild(pid)
			if err != nil {
				return false, false
			}
			if child {
				kill(pid)
			}
		}
		if time.Now().After(deadline) {
			return false, true
		}
	}
}

// reapChildren collects every exited child without blocking; none is true when this process has
// no child left, live or exited. waitid rather than wait4: Go's os/exec already depends on it.
func reapChildren() (none bool, err error) {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_ALL, 0, &info, unix.WEXITED|unix.WNOHANG, nil)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ECHILD):
			return true, nil
		case err != nil:
			return false, err
		case info.Signo == 0: // WNOHANG and nothing has exited
			return false, nil
		}
	}
}

// isChild asks the kernel whether pid is a child of this process, without reaping it (WNOWAIT).
// The /proc listing alone does not prove it: a /proc mounted for another pid namespace numbers
// processes differently from the kill that follows. ECHILD is a plain no; any other error means
// the question cannot be asked here (a seccomp filter on WNOWAIT), and the caller has to stop the
// children some other way.
func isChild(pid int) (bool, error) {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ECHILD):
			return false, nil
		}
		return err == nil, err
	}
}

// childrenOf lists the processes whose parent is ppid, in any state.
func childrenOf(ppid int) []int {
	var out []int
	for _, p := range listProcs() {
		if p.ppid == ppid {
			out = append(out, p.pid)
		}
	}
	return out
}

// stopGroup kills the live members of this process's group until none is left, and reports
// whether that happened before deadline. It is the rule without a subreaper: an orphan goes to
// init and is seen only in a /proc listing, which can miss a child forked while it is read, so
// the group counts as empty only after three empty listings in a row.
func stopGroup(deadline time.Time, kill func(pid int)) bool {
	pgid := syscall.Getpgrp()
	for empty := 0; empty < 3; time.Sleep(20 * time.Millisecond) {
		m := groupMembers(pgid)
		if len(m) == 0 {
			empty++
			continue
		}
		empty = 0
		if time.Now().After(deadline) {
			return false
		}
		for _, p := range m {
			kill(p)
		}
	}
	return true
}

// procInfo is one /proc/<pid>/stat.
type procInfo struct {
	pid, ppid, pgrp int
	start           uint64 // clock ticks after boot: with pid, names one process for good
	live            bool   // not a zombie
}

// procRoot is where procfs is read; tests point it at a fake tree.
var procRoot = "/proc"

// procPath is a path under procRoot for pid.
func procPath(pid int, rest ...string) string {
	return filepath.Join(append([]string{procRoot, strconv.Itoa(pid)}, rest...)...)
}

// procfsOurs reports whether procRoot numbers processes as this process's pid namespace does,
// which every kill and waitid here uses. A procfs mounted for another namespace names other
// processes by the same numbers, and its /proc/self may even carry the same number by chance:
// NSpid lists the pid from the procfs's namespace down to ours, so it is one entry, ours, only in
// our own. A kernel without NSpid (before 4.1) is judged by /proc/self alone.
func procfsOurs() bool {
	me := strconv.Itoa(os.Getpid())
	if t, err := os.Readlink(filepath.Join(procRoot, "self")); err != nil || t != me {
		return false
	}
	b, err := os.ReadFile(filepath.Join(procRoot, "self", "status"))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(l, "NSpid:"); ok {
			f := strings.Fields(rest)
			return len(f) == 1 && f[0] == me
		}
	}
	return true
}

func readStat(pid int) (procInfo, bool) {
	return readStatFile(pid, procPath(pid, "stat"))
}

func readStatFile(pid int, path string) (procInfo, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return procInfo{}, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return procInfo{}, false
	}
	f := strings.Fields(s[i+1:]) // state ppid pgrp session tty tpgid flags ... starttime (20th)
	if len(f) < 20 {
		return procInfo{}, false
	}
	ppid, err1 := strconv.Atoi(f[1])
	pgrp, err2 := strconv.Atoi(f[2])
	start, err3 := strconv.ParseUint(f[19], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return procInfo{}, false
	}
	return procInfo{pid: pid, ppid: ppid, pgrp: pgrp, start: start, live: f[0] != "Z" && f[0] != "X"}, true
}

func listProcs() []procInfo {
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	var out []procInfo
	for _, d := range ents {
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}
		if p, ok := readStat(pid); ok {
			out = append(out, p)
		}
	}
	return out
}

// groupMembers lists the running processes of group pgid other than this one. A zombie whose
// threads have all exited writes nothing and does not count; one whose leader thread alone exited
// still runs.
func groupMembers(pgid int) []int {
	var out []int
	for _, p := range groupProcs(pgid) {
		out = append(out, p.pid)
	}
	return out
}

// threadsRun reports whether a process whose stat says zombie still has a thread that has not
// exited (its leader thread alone exited). A task list that cannot be read for another reason
// than the process being gone answers yes: when in doubt, it may still write.
func threadsRun(pid int) bool {
	tasks, err := os.ReadDir(procPath(pid, "task"))
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	for _, d := range tasks {
		tid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}
		if t, ok := readStatFile(tid, procPath(pid, "task", d.Name(), "stat")); ok && t.live {
			return true
		}
	}
	return false
}

// groupProcs is groupMembers with each member's start time, read from the same stat.
func groupProcs(pgid int) []npmProc {
	self := os.Getpid()
	var out []npmProc
	for _, p := range listProcs() {
		// The threads of a zombie are looked at only in a procfs of our own namespace: in another,
		// the number names someone else's process, which main never waited for either.
		if p.pid != self && p.pgrp == pgid && (p.live || procfsOurs() && threadsRun(p.pid)) {
			out = append(out, npmProc{pid: p.pid, start: p.start})
		}
	}
	return out
}

// waitGroupGone polls until group pgid has no live member besides the caller; it reports whether
// that happened within d.
func waitGroupGone(pgid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for len(groupMembers(pgid)) > 0 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}
