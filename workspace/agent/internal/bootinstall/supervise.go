package bootinstall

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
func MaybeSupervise(args []string) {
	if len(args) < 3 || args[0] != "boot-install" || args[1] != superviseStep {
		return
	}
	os.Exit(supervise(args[2], args[3:]))
}

// supervise runs npm in its own (the supervisor's) process group, passes the lock on to it, and
// keeps the lock open until nothing npm started is left: the lock is an flock, dropped only when
// the last holder of the file description exits, so a postinstall child that does not inherit it
// still cannot outlive the lock. The caller owns the deadline and kills the whole group
// (supervisor included), so no signal handling is needed here.
func supervise(npm string, args []string) int {
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
	if !stopDescendants(os.Getpid(), reaper, superviseGrace) && werr == nil {
		// The caller's own rule for its group: an install whose processes may still write is
		// not a success.
		fmt.Fprintln(os.Stderr, "npm's processes did not stop")
		return 1
	}
	if werr == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(werr, &ee) {
		if c := ee.ExitCode(); c >= 0 {
			return c
		}
	}
	return 1
}

// stopDescendants kills every live descendant of self and member of self's group, reaps them,
// and reports whether none was left within d. Every round re-lists: a child forked between a
// listing and the kill is caught by the next one. With reaper, the end is the kernel's answer
// that self has no child at all, which no listing race can fake; without it, an orphan goes to
// init and is seen only in a listing, so three empty listings in a row are asked for as well.
func stopDescendants(self int, reaper bool, d time.Duration) bool {
	empty := 0
	for deadline := time.Now().Add(d); ; time.Sleep(20 * time.Millisecond) {
		none := reapChildren()
		if none && reaper {
			return true
		}
		left := strays(self)
		for _, p := range left {
			killProc(p)
		}
		if len(left) > 0 {
			empty = 0
		} else {
			empty++
		}
		if none && empty >= 3 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// reapChildren collects every exited child without blocking; none is true when self has no
// child left, live or exited.
func reapChildren() (none bool) {
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ECHILD):
			return true
		case err != nil || pid <= 0:
			return false
		}
	}
}

// procInfo is one /proc/<pid>/stat. pid and start together name one process: a pid is reused,
// a (pid, start time) pair is not.
type procInfo struct {
	pid, ppid, pgrp int
	start           string // field 22, clock ticks since boot
	live            bool   // not a zombie
}

func readStat(pid int) (procInfo, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procInfo{}, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return procInfo{}, false
	}
	f := strings.Fields(s[i+1:]) // state ppid pgrp session tty tpgid flags ... starttime is f[19]
	if len(f) < 20 {
		return procInfo{}, false
	}
	ppid, err1 := strconv.Atoi(f[1])
	pgrp, err2 := strconv.Atoi(f[2])
	if err1 != nil || err2 != nil {
		return procInfo{}, false
	}
	return procInfo{pid: pid, ppid: ppid, pgrp: pgrp, start: f[19], live: f[0] != "Z" && f[0] != "X"}, true
}

func listProcs() []procInfo {
	ents, err := os.ReadDir("/proc")
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

// strays lists the live processes, other than self, that are in self's group or descend from
// self, from one /proc snapshot.
func strays(self int) []procInfo {
	all := listProcs()
	parent := make(map[int]int, len(all))
	for _, p := range all {
		parent[p.pid] = p.ppid
	}
	var out []procInfo
	for _, p := range all {
		if p.pid != self && p.live && (p.pgrp == self || descends(p.pid, self, parent)) {
			out = append(out, p)
		}
	}
	return out
}

// descends reports whether pid has root as an ancestor in the snapshot parent. The walk is
// bounded: a snapshot read while processes come and go can hold a cycle of reused pids.
func descends(pid, root int, parent map[int]int) bool {
	for i := 0; i <= len(parent) && pid > 1; i++ {
		pid = parent[pid]
		if pid == root {
			return true
		}
	}
	return false
}

// killProc sends SIGKILL to the process p was read from, and never to one that took its pid
// since: the pidfd pins whatever has the pid when it is opened, and the start time read after
// that proves it is still p. Without pidfds (a kernel before 5.3, or a seccomp filter that
// refuses the call) the check and the kill go by pid, which leaves only the window between them.
func killProc(p procInfo) {
	fd, err := unix.PidfdOpen(p.pid, 0)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return
		}
		if q, ok := readStat(p.pid); ok && q.start == p.start {
			_ = unix.Kill(p.pid, unix.SIGKILL)
		}
		return
	}
	defer unix.Close(fd)
	if q, ok := readStat(p.pid); ok && q.start == p.start {
		_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
	}
}

// groupMembers lists the live (not zombie) processes of group pgid other than this one. A
// zombie is a dead process nobody has reaped yet: it writes nothing, so it does not count.
func groupMembers(pgid int) []int {
	self := os.Getpid()
	var out []int
	for _, p := range listProcs() {
		if p.pid != self && p.live && p.pgrp == pgid {
			out = append(out, p.pid)
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
