package lcpp

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// The shared lifecycle asks the handle (Alive, Busy, HoldsEntry): each case is one of the
// fields this kind counts as a turn in flight.
func TestLifecycleAsksTheHandle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, c := range []struct {
		name        string
		set         func(h *threadHandle)
		busy, holds bool
	}{
		{"idle", func(*threadHandle) {}, false, false},
		{"running", func(h *threadHandle) { h.running = true }, true, true},
	} {
		h := &threadHandle{name: "pred-" + c.name, q: agents.NewTurnQueue("pred-"+c.name, ledger, agents.LedgerAtTake)}
		c.set(h)
		handlesMu.Lock()
		handles[h.name] = h
		handlesMu.Unlock()
		if !ManagedAlive(h.name) || ManagedBusy(h.name) != c.busy || ManagedInFlight(h.name) != c.holds {
			t.Errorf("%s: alive %v busy %v inflight %v, want true %v %v", c.name,
				ManagedAlive(h.name), ManagedBusy(h.name), ManagedInFlight(h.name), c.busy, c.holds)
		}
		handlesMu.Lock()
		delete(handles, h.name)
		handlesMu.Unlock()
		if ManagedAlive(h.name) || ManagedBusy(h.name) || ManagedInFlight(h.name) {
			t.Errorf("%s: an unregistered handle still answers", c.name)
		}
	}
}
