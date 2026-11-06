//go:build linux

package routegw

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// Only a kernel that does not know SO_PEERPIDFD may fall back to the bare pid; every other
// failure refuses, above all EINVAL, which a kernel that has the option answers when the
// connector has already exited and its pid may name another process.
func TestPeerStartFallsBackOnlyWithoutTheOption(t *testing.T) {
	prev := peerPIDFD
	t.Cleanup(func() { peerPIDFD = prev })
	self := os.Getpid()
	me, ok := readProc("/proc", self)
	if !ok {
		t.Fatal("cannot read the test process")
	}
	for _, c := range []struct {
		name string
		err  error
		ok   bool
	}{
		{"option unknown (kernel before 6.5)", unix.ENOPROTOOPT, true},
		{"connector already exited", unix.EINVAL, false},
		{"no such process", unix.ESRCH, false},
		{"out of descriptors", unix.EMFILE, false},
		{"system out of files", unix.ENFILE, false},
		{"not permitted", unix.EPERM, false},
	} {
		peerPIDFD = func(int) (int, error) { return -1, c.err }
		start, got := peerStart(-1, self)
		if got != c.ok || (got && start != me.StartTicks) {
			t.Errorf("%s: peerStart = (%d, %v), want ok=%v", c.name, start, got, c.ok)
		}
	}

	// A pidfd: accepted only when it names the pid SO_PEERCRED gave.
	pidfdOf := func(pid int) func(int) (int, error) {
		return func(int) (int, error) { return unix.PidfdOpen(pid, 0) }
	}
	peerPIDFD = pidfdOf(self)
	if start, got := peerStart(-1, self); !got || start != me.StartTicks {
		t.Fatalf("own pidfd: peerStart = (%d, %v)", start, got)
	}
	c := startCaller(t, "/nonexistent")
	peerPIDFD = pidfdOf(c.pid())
	if _, got := peerStart(-1, self); got {
		t.Fatal("a pidfd naming another process was accepted")
	}
	// The pidfd's process has exited: refused, whatever the pid now names.
	peerPIDFD = pidfdOf(c.pid())
	fd, _ := peerPIDFD(-1)
	_ = c.cmd.Process.Kill()
	_ = c.cmd.Wait()
	peerPIDFD = func(int) (int, error) { return fd, nil }
	if _, got := peerStart(-1, c.pid()); got {
		t.Fatal("the pidfd of an exited process was accepted")
	}
}
