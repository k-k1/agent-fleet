package opencode

import (
	"os/exec"
	"testing"
	"time"
)

// The listener going quiet must not end the wait: only the process exit does.
func TestAwaitStopWaitsForExit(t *testing.T) {
	exited := make(chan struct{})
	var termed, killed bool
	go func() { time.Sleep(150 * time.Millisecond); close(exited) }()
	start := time.Now()
	ok, k := awaitStop(func() { termed = true }, func() { killed = true }, exited, 2*time.Second, time.Second)
	if !ok || k || !termed || killed {
		t.Fatalf("ok=%v killed=%v termed=%v killedCalled=%v", ok, k, termed, killed)
	}
	if time.Since(start) < 140*time.Millisecond {
		t.Fatalf("returned before the process exited: %v", time.Since(start))
	}
}

func TestAwaitStopKillsThenGivesUp(t *testing.T) {
	exited := make(chan struct{})
	var killed bool
	start := time.Now()
	ok, k := awaitStop(func() {}, func() { killed = true }, exited, 50*time.Millisecond, 50*time.Millisecond)
	if ok || !k || !killed {
		t.Fatalf("ok=%v killed=%v called=%v", ok, k, killed)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("wait not bounded: %v", d)
	}
}

func TestAwaitStopKillWorks(t *testing.T) {
	exited := make(chan struct{})
	ok, k := awaitStop(func() {}, func() { close(exited) }, exited, 30*time.Millisecond, time.Second)
	if !ok || !k {
		t.Fatalf("ok=%v killed=%v", ok, k)
	}
}

// stopProcess on a real, watched child that keeps running after its "listener" is gone returns
// only after the reap.
func TestStopProcessReturnsAfterReap(t *testing.T) {
	cmd := fakeSleeper(t, "trap 'sleep 0.4; exit 0' TERM; while :; do sleep 0.05; done")
	watchExit(cmd)
	go func() { _ = cmd.Wait(); noteExited(cmd) }()
	start := time.Now()
	stopProcess(cmd, "http://127.0.0.1:1") // nothing listens: health is false from the start
	if time.Since(start) < 350*time.Millisecond {
		t.Fatalf("stopProcess returned %v after SIGTERM, before the process exited", time.Since(start))
	}
	if exitSignal(cmd) != nil {
		t.Fatal("exit not recorded")
	}
}

func fakeSleeper(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	time.Sleep(100 * time.Millisecond) // let the trap install
	return cmd
}
