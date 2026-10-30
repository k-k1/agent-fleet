package codex

import "testing"

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
		{"turn started", func(h *threadHandle) { h.turnID = "t1" }, true, true},
	} {
		h := &threadHandle{name: "pred-" + c.name, alive: true}
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
