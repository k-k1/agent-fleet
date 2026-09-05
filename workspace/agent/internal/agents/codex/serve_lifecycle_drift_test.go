//go:build drift

// Tier 1 drift detection that runs the shared daemon's whole life (docs/log/27 §7.1) against the
// real codex binary. It spends no turn - it only brings the app-server up and back down.
// Sibling: drift_test.go.
//
// What this adds is the process-level confirmation of "start on demand, fold up at zero demand":
// the unit tests only look at the gate and the counting, never at whether anything actually
// started or stopped.

package codex

import (
	"os/exec"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func TestDriftCodexDaemonStartsOnDemandAndStopsWhenIdle(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex binary not on PATH")
	}
	if !loggedIn() {
		t.Skip("codex is not logged in — the auth gate would (correctly) refuse to start")
	}
	// Avoid the default port; the real fleet's daemon may be sitting on it.
	const addr = "ws://127.0.0.1:7897"
	t.Setenv(appServerAddrEnv, addr)
	if healthy(addr) {
		t.Fatalf("something is already listening on %s — pick another test port", addr)
	}

	prev := TUIDependents
	needs := 1
	TUIDependents = func() int { return needs }
	t.Cleanup(func() { TUIDependents = prev })

	s := &Supervisor{}
	t.Cleanup(s.Shutdown) // do not strand the daemon if the stop did not take

	if _, _, err := s.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !healthy(addr) {
		t.Fatal("Ensure succeeded but the daemon is not listening")
	}

	// Drive the zero-demand watch directly with a short grace period; armIdleWatchLocked's default
	// of 2 minutes is too long to wait for.
	prevTick := agents.IdleTickForTest(10 * time.Millisecond)
	t.Cleanup(func() { agents.IdleTickForTest(prevTick) })
	stopped := make(chan bool, 1)
	go func() {
		agents.WatchIdle("drift", dependents, s.stopIfIdle, 50*time.Millisecond)
		stopped <- true
	}()

	// It must not fold up while there is demand.
	time.Sleep(300 * time.Millisecond)
	if !healthy(addr) {
		t.Fatal("the daemon stopped while there was still demand")
	}

	needs = 0
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("demand hit zero but the watch never decided to stop")
	}
	deadline := time.Now().Add(10 * time.Second)
	for healthy(addr) {
		if time.Now().After(deadline) {
			t.Fatal("the daemon should have stopped but is still listening")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
