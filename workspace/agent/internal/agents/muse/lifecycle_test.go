package muse

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
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
		{"starting", func(h *threadHandle) { h.starting = "m1" }, true, true},
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

// Agent shutdown (AbortManaged, through the shared Lifecycle.Abort) reaches a turn whose
// turn/start the host accepted but has not reported started: the handle counts h.starting as
// running, so the queue behind it goes and the stop is delivered when turn/started arrives.
// Without that, shutdown leaves the starting turn running and the queued input to follow it.
func TestAbortManagedStopsATurnStillStarting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := &threadHandle{}
	host := newTestHandle(t, h)
	handlesMu.Lock()
	handles[h.name] = h
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		delete(handles, h.name)
		handlesMu.Unlock()
	})
	starts := recordStarts(host)
	acceptInterrupts(host)
	if err := h.Send(memberInput("long", "cm-1")); err != nil {
		t.Fatal(err)
	}
	p := nextStart(t, starts)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		h.mu.Lock()
		startingOnly := h.starting != "" && !h.running && !h.calling
		h.mu.Unlock()
		if startingOnly {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the handle never settled into a started-but-not-running turn")
		}
	}
	if err := h.Send(peerInput("from a peer", "cm-peer")); err != nil {
		t.Fatal(err)
	}

	AbortManaged()
	h.mu.Lock()
	left, kept := h.tq().Len(), h.tq().Discards()
	h.mu.Unlock()
	if left != 0 || len(kept) != 0 {
		t.Errorf("%d entries queued and %d discards kept after the shutdown interrupt, want none", left, len(kept))
	}
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{CommandID: p.CommandID, TurnID: p.CommandID, SessionID: h.sid})
	if got := interruptTarget(t, waitSent(t, host, isMethod(msp.MethodTurnInterrupt))); got != p.CommandID {
		t.Errorf("turn/interrupt aimed at %q, want the starting turn %q", got, p.CommandID)
	}
	completed(t, h, host, p.CommandID, msp.TurnTerminalCancelled, agents.TurnCancelled)
	noStart(t, starts, "the peer input must not start after the shutdown interrupt")
}
