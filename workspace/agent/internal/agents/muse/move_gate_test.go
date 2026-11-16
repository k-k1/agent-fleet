package muse

// The pump's own lease on the session's lifecycle gate (ADR 0115 decision 11.1). The input path
// that queued an entry let go of its lease at the accept, so the pump is what a move has to wait
// for: an entry whose turn/start is out is with the host before the move closes input, and one
// still queued stays there, where the move's snapshot reads it.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
)

func TestPumpStartsBeforeAMoveOrLeavesTheEntryQueued(t *testing.T) {
	h := &threadHandle{name: "muse-move-1"}
	host := newTestHandle(t, h)
	t.Cleanup(func() { lifegate.Reopen(h.name) }) // the gate's memory is process-wide
	// The ClientMessageID ledger lives under HOME and outlives the test: a later round would
	// find this session's inputs already recorded and drop them as resends, so no turn/start.
	t.Cleanup(func() { ledger.Remove(h.name) })
	handlesMu.Lock()
	handles[h.name] = h // where ManagedInFlight looks
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		delete(handles, h.name)
		handlesMu.Unlock()
	})

	// turn/start answers at once until hold is armed; then it waits for the test.
	var mu sync.Mutex
	var hold chan struct{}
	starts := make(chan msp.TurnStartParams, 8)
	host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
		var p msp.TurnStartParams
		json.Unmarshal(m.Params, &p)
		starts <- p
		mu.Lock()
		ch := hold
		mu.Unlock()
		if ch != nil {
			<-ch
		}
		return msp.TurnStartResult{Disposition: msp.TurnStartDispositionStarted, StartedNewTurn: true, TurnID: p.CommandID}, nil
	})
	var once sync.Once
	releaseStart := func() {
		once.Do(func() {
			mu.Lock()
			close(hold)
			hold = nil
			mu.Unlock()
		})
	}

	first := runTurn(t, h, host, starts, "first")
	if err := h.Send(memberInput("second", "cm-second")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	hold = make(chan struct{})
	mu.Unlock()
	t.Cleanup(releaseStart) // a failed assertion must not leave the host parked in turn/start

	// The completion pumps "second": taken, committed and sent, not yet answered.
	completed(t, h, host, first, msp.TurnTerminalCompleted, agents.TurnCompleted)
	second := nextStart(t, starts)

	locked := make(chan *lifegate.Exclusive, 1)
	go func() {
		x, err := lifegate.Lock(context.Background(), h.name)
		if err != nil {
			t.Errorf("Lock: %v", err)
		}
		locked <- x
	}()
	deadline := time.Now().Add(5 * time.Second)
	// Or the move already has the gate, which the select below reports.
	for lifegate.WritersWaiting(h.name) == 0 && len(locked) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the move never started waiting")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case x := <-locked:
		x.Unlock()
		t.Fatal("the move got the gate before turn/start was answered")
	case <-time.After(50 * time.Millisecond):
	}
	releaseStart()
	var x *lifegate.Exclusive
	select {
	case x = <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("the pump kept its lease past turn/start; a running turn must not hold a move off")
	}
	// The runtime has the input, but its own record (and so DriveState) may not show the turn
	// yet: the move's state check has to see it in flight here, or it would start.
	if !ManagedInFlight(h.name) {
		x.Unlock()
		t.Fatal("the writer was admitted with the entry out of the queue, but ManagedInFlight says nothing is in flight")
	}
	x.SetClosed("muse-move-1-target", false)
	x.Unlock()

	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{CommandID: second.CommandID, TurnID: second.CommandID, SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
	// Accepted after input closed (the HTTP gate sits above this layer): it must stay queued.
	if err := h.Send(memberInput("third", "cm-third")); err != nil {
		t.Fatal(err)
	}
	completed(t, h, host, second.CommandID, msp.TurnTerminalCompleted, agents.TurnCompleted)
	noStart(t, starts, "a closed session's pump")
	h.mu.Lock()
	items := h.tq().Items()
	h.mu.Unlock()
	if len(items) != 1 || items[0].ID != "cm-third" {
		t.Fatalf("queue while closed = %+v, want cm-third still queued for the snapshot", items)
	}

	// The move rolls back: the parked pump starts again.
	lifegate.Reopen(h.name)
	third := nextStart(t, starts)
	if got := *third.Input[0].Text; got != "third" {
		t.Fatalf("after the reopen %q started, want third", got)
	}
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{CommandID: third.CommandID, TurnID: third.CommandID, SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
	completed(t, h, host, third.CommandID, msp.TurnTerminalCompleted, agents.TurnCompleted)
	noStart(t, starts, "a second start of the parked entry")
	if !settled(h) {
		t.Fatal("the queue is not settled after the reopened entry ran")
	}
	if ManagedInFlight(h.name) {
		t.Error("ManagedInFlight still reports an entry after the queue drained")
	}
}
