package opencode

import (
	"bufio"
	"net/http"
	"net/http/httptest"
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

// startFake runs script under sh, reaps it like waitDaemon does, and returns once the script has
// printed "ready" (after its trap is installed) so no test depends on a sleep.
func startFake(t *testing.T, script string) (*exec.Cmd, chan struct{}) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(out).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	reaped := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited); close(reaped) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-reaped })
	return cmd, exited
}

// stopProcess on a real child whose "listener" is gone from the start returns only after the reap.
func TestStopProcessReturnsAfterReap(t *testing.T) {
	cmd, exited := startFake(t, "trap 'sleep 0.4; exit 0' TERM; echo ready; while :; do sleep 0.05; done")
	start := time.Now()
	stopProcess(cmd, exited, "http://127.0.0.1:1")
	if time.Since(start) < 350*time.Millisecond {
		t.Fatalf("stopProcess returned %v after SIGTERM, before the process exited", time.Since(start))
	}
	select {
	case <-exited:
	default:
		t.Fatal("returned before the exit was recorded")
	}
}

// A process that ignores SIGTERM is killed by the PID we own, inside the bounds.
func TestStopProcessKillsATermIgnoringChild(t *testing.T) {
	oldT, oldK := stopTermWait, stopKillWait
	stopTermWait, stopKillWait = 200*time.Millisecond, 2*time.Second
	t.Cleanup(func() { stopTermWait, stopKillWait = oldT, oldK })
	cmd, exited := startFake(t, "trap '' TERM; echo ready; while :; do sleep 0.05; done")
	start := time.Now()
	stopProcess(cmd, exited, "http://127.0.0.1:1")
	select {
	case <-exited:
	default:
		t.Fatal("process survived stopProcess")
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > 1500*time.Millisecond {
		t.Fatalf("kill not applied after the term wait: %v", d)
	}
}

// An already-reaped process is not signalled and the call does not wait, even when another
// server answers on the address (the concurrent replacement of a Restart).
func TestStopProcessAlreadyExitedReturnsAtOnce(t *testing.T) {
	cmd, exited := startFake(t, "echo ready")
	<-exited
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"2.0.28"}`))
	}))
	defer srv.Close()
	if !healthy(srv.URL) {
		t.Fatal("control: the fake server must look healthy, or the health-poll path would return at once too")
	}
	oldT := stopTermWait
	stopTermWait = 5 * time.Second
	t.Cleanup(func() { stopTermWait = oldT })
	start := time.Now()
	stopProcess(cmd, exited, srv.URL)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v on a process that was already gone", d)
	}
}

// stopIfIdle waits for the exit outside the lock; a replacement installed meanwhile must stay.
func TestStopIfIdleKeepsAReplacementInstalledDuringTheStop(t *testing.T) {
	cmd, exited := startFake(t, "trap 'sleep 0.4; exit 0' TERM; echo ready; while :; do sleep 0.05; done")
	other := &exec.Cmd{}
	s := &Supervisor{up: true, cmd: cmd, exited: exited}
	done := make(chan bool)
	go func() { done <- s.stopIfIdle() }()
	for {
		s.mu.Lock()
		stopping := s.stopping
		s.mu.Unlock()
		if stopping {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.mu.Lock()
	s.cmd = other
	s.mu.Unlock()
	<-done
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != other {
		t.Fatal("stopIfIdle orphaned the replacement daemon")
	}
}
