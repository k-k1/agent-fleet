package bootinstall

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// superviseStep is the hidden boot-install step npmInstall re-executes this binary as. Its
// file descriptor 3 is the npm lock.
const superviseStep = "npm-supervise"

// superviseGrace bounds how long the supervisor waits for the group to empty after SIGKILL.
const superviseGrace = 5 * time.Second

// MaybeSupervise runs the supervisor and exits when args (os.Args[1:]) ask for it; otherwise it
// returns false. Run calls it first, and the package's TestMain does too, because a test binary
// is what os.Executable names there.
func MaybeSupervise(args []string) bool {
	if len(args) < 3 || args[0] != "boot-install" || args[1] != superviseStep {
		return false
	}
	os.Exit(supervise(args[2], args[3:]))
	return true
}

// supervise runs npm in its own process group and keeps the lock (inherited as fd 3, and passed
// on to npm) open until that group is gone, whatever happens to its parent: the lock is an
// flock, dropped only when the last holder of the file description exits. SIGTERM (the parent's
// deadline) kills the group. The wait is bounded: a group that survives SIGKILL (an
// uninterruptible process, or zombies nobody reaps) is not waited on forever.
func supervise(npm string, args []string) int {
	lock := os.NewFile(3, "npm-lock")
	if lock == nil {
		return 125
	}
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM, syscall.SIGINT)
	cmd := exec.Command(npm, args...)
	cmd.ExtraFiles = []*os.File{lock}
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 127
	}
	pgid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var werr error
	select {
	case werr = <-done:
	case <-term:
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		werr = <-done
	}
	// Nothing npm started may outlive it; the lock goes only when the group is empty.
	deadline := time.Now().Add(superviseGrace)
	for {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			break // unreaped zombies keep a group alive; npm's own result still stands
		}
		time.Sleep(20 * time.Millisecond)
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
