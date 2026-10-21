package runtime

import (
	"context"
	"testing"
)

// A Stop that lands while the background half of a Start is still converging must win:
// the goroutine may not raise desiredCount afterwards, the claim must not hold the
// workspace at `starting`, and no phase may be left behind.
func TestECSEC2StopWhileStartConvergesWins(t *testing.T) {
	ctx := context.Background()
	h := memberWipeHarness(t)
	defer h.rt.setPhase("")
	h.ci.registered["i-hot"] = false // not registered yet: the Start goes to the background
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.deferred) != 1 {
		t.Fatalf("want a background Start, deferred=%d", len(h.deferred))
	}
	if got := h.rt.State(ctx); got != "starting" {
		t.Fatalf("State while converging = %q, want starting", got)
	}

	if err := h.rt.Stop(ctx); err != nil {
		t.Fatalf("Stop while starting: %v", err)
	}
	if got := h.rt.State(ctx); got != "stopped" {
		t.Errorf("State right after Stop = %q, want stopped (the claim must not outlive it)", got)
	}
	if ph := h.rt.BootPhase(); ph != "" {
		t.Errorf("phase left behind by the stop: %q", ph)
	}

	// The slot registers and the goroutine carries on: it must find itself overtaken.
	h.ci.registered["i-hot"] = true
	h.runDeferred(ctx)
	if scaledUp(h) {
		t.Error("the overtaken Start raised desiredCount after the Stop")
	}
	if got := h.rt.State(ctx); got != "stopped" {
		t.Errorf("State after the overtaken Start ended = %q, want stopped", got)
	}
	if ph := h.rt.BootPhase(); ph != "" {
		t.Errorf("phase left behind by the overtaken start: %q", ph)
	}
	// A fresh Start afterwards is unaffected by the earlier one's count.
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start after Stop: %v", err)
	}
	h.runDeferred(ctx)
	if !scaledUp(h) {
		t.Error("a Start after the Stop did not scale up")
	}
}
