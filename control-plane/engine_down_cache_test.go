package main

// The negative cache in front of the generation path's health probe (ADR 0082 unresolved 1).
//
// The measurement that made this worth having: a LAN host that is switched off answers a connect
// attempt with `No route to host` after ~3 seconds (measured 2026-09-14, four samples, from a
// container on the same network), and ensureReady used to pay that on every picture — in front of
// the PREFERRED route, where the whole point of the ordering is that falling through to the next
// provider is cheap.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// countingEngine is an external row pointing at a server that counts health probes and can be
// switched between answering and refusing.
func countingEngine(t *testing.T, healthy *atomic.Bool, probes *atomic.Int32) (*engineRuntimeState, engineGateway) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	e := newTestExternalEngine(t, srv.URL, nil)
	return e, engineGateway{reg: &engineRegistry{byKey: map[string]*engineRuntimeState{e.def.Key: e}}}
}

// The whole point: the SECOND request does not dial at all. Nothing here can start an external
// row, so the probe's only outcome is the refusal ensureStarted gives anyway.
func TestExternalRowFoundDownIsNotProbedAgainWithinTheWindow(t *testing.T) {
	var healthy atomic.Bool
	var probes atomic.Int32
	e, g := countingEngine(t, &healthy, &probes)

	first := g.ensureReady(context.Background(), e)
	if first == nil {
		t.Fatal("ensureReady on a dead external row returned nil, want the refusal")
	}
	if n := probes.Load(); n != 1 {
		t.Fatalf("first ensureReady made %d probes, want exactly 1", n)
	}

	second := g.ensureReady(context.Background(), e)
	if n := probes.Load(); n != 1 {
		t.Errorf("second ensureReady probed again (%d total) — the window did not hold", n)
	}
	// And the member is told the same thing, not something vaguer for having skipped the dial.
	if second == nil || second.Error() != first.Error() {
		t.Errorf("cached refusal = %v, want the same sentence as %v", second, first)
	}
}

// A box that has come up must be usable at once: only the negative is remembered.
func TestAHealthyProbeClearsTheWindow(t *testing.T) {
	var healthy atomic.Bool
	var probes atomic.Int32
	e, g := countingEngine(t, &healthy, &probes)

	if err := g.ensureReady(context.Background(), e); err == nil {
		t.Fatal("ensureReady on a dead external row returned nil, want the refusal")
	}
	healthy.Store(true)
	// Still inside the window, so this one is refused from the cache — that is the cost of the
	// cache and it is bounded by engineExternalDownTTL.
	e.extDown.mu.Lock()
	e.extDown.at = time.Now().Add(-engineExternalDownTTL - time.Second)
	e.extDown.mu.Unlock()

	if err := g.ensureReady(context.Background(), e); err != nil {
		t.Fatalf("ensureReady after the window and with the box up = %v, want nil", err)
	}
	before := probes.Load()
	healthy.Store(false)
	if err := g.ensureReady(context.Background(), e); err == nil {
		t.Fatal("ensureReady after the box went away again returned nil, want the refusal")
	}
	if probes.Load() != before+1 {
		t.Errorf("a healthy answer was remembered: the next request did not re-probe (%d → %d)",
			before, probes.Load())
	}
}

// 🔴 The branch a managed row must never take. "Not answering" there means the box this process
// just bought is still booting, and remembering it would make ensureReady refuse the engine it is
// waiting for.
func TestAManagedRowIsProbedEveryTime(t *testing.T) {
	var healthy atomic.Bool
	var probes atomic.Int32
	e, g := countingEngine(t, &healthy, &probes)
	e.def.Lifecycle = "" // this deployment's own ECS service, not a LAN box

	for i := 1; i <= 2; i++ {
		if err := g.ensureReady(context.Background(), e); err == nil {
			t.Fatalf("attempt %d returned nil for an engine that is not answering", i)
		}
		if n := probes.Load(); int(n) != i {
			t.Fatalf("after %d attempts a managed row had been probed %d times, want %d — "+
				"the negative cache reached a row whose 'not answering' means 'still starting'", i, n, i)
		}
	}
}
