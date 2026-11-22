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
// keeps the lock open until nothing but itself is left in the group: the lock is an flock,
// dropped only when the last holder of the file description exits, so a postinstall child that
// does not inherit it still cannot outlive the lock. The caller owns the deadline and kills the
// whole group (supervisor included), so no signal handling is needed here.
func supervise(npm string, args []string) int {
	// The step signals its whole group at the end: it must lead its own, never a caller's.
	lock := os.NewFile(3, "npm-lock")
	if lock == nil || syscall.Getpgrp() != os.Getpid() {
		return 125
	}
	defer lock.Close() // the last holder if npm's children did not inherit it: live until the end
	cmd := exec.Command(npm, args...)
	cmd.ExtraFiles = []*os.File{lock}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err) // the cause main reports for a direct start
		return 127
	}
	werr := cmd.Wait()
	// Nothing npm started may outlive it: kill what is left, wait for it to be gone.
	// Re-listed each round: a child that forks between a listing and the kill is caught by the next.
	pgid := syscall.Getpgrp()
	for deadline := time.Now().Add(superviseGrace); len(groupMembers(pgid)) > 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		killGroupMembers(pgid)
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

// groupMembers lists the live (not zombie) processes of group pgid other than this one. A
// zombie is a dead process nobody has reaped yet: it writes nothing, so it does not count.
func groupMembers(pgid int) []int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	self := os.Getpid()
	var out []int
	for _, d := range ents {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid == self {
			continue
		}
		b, err := os.ReadFile("/proc/" + d.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:]) // state ppid pgrp ...
		if len(f) < 3 || f[0] == "Z" || f[0] == "X" {
			continue
		}
		if g, err := strconv.Atoi(f[2]); err == nil && g == pgid {
			out = append(out, pid)
		}
	}
	return out
}

func killGroupMembers(pgid int) {
	for _, p := range groupMembers(pgid) {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
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
