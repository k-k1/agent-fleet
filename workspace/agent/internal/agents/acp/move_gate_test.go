package acp

// The pump's own lease on the session's lifecycle gate (ADR 0115 decision 11.1). The input path
// that queued an entry let go of its lease at the accept, so the pump is what a move has to wait
// for: an entry about to be committed is handed over before the move closes input, and one still
// queued stays there, where the move's snapshot reads it.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
)

func TestPumpCommitsBeforeAMoveOrLeavesTheEntryQueued(t *testing.T) {
	h, f := newTestHandle(t)
	h.name = "acp-move-1"
	t.Cleanup(func() { lifegate.Reopen(h.name) }) // the gate's memory is process-wide

	taken, rel := holdAtCommit(h)
	var once sync.Once
	release := func() { once.Do(rel) }
	t.Cleanup(release) // a failed assertion must not leave the pump parked before its commit
	mustSend(t, h, member("m1", "one"))
	<-taken // taken, not committed: the pump holds its lease

	locked := make(chan *lifegate.Exclusive, 1)
	go func() {
		x, err := lifegate.Lock(context.Background(), h.name)
		if err != nil {
			t.Errorf("Lock: %v", err)
		}
		locked <- x
	}()
	deadline := time.Now().Add(hangGuard)
	// Or the move already has the gate, which the select below reports.
	for lifegate.WritersWaiting(h.name) == 0 && len(locked) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the move never started waiting")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case x := <-locked:
		x.Unlock() // or the pump waits on the gate and the cleanup hangs
		t.Fatal("the move got the gate while the pump was about to commit an entry")
	case <-time.After(50 * time.Millisecond):
	}
	// Committed but not yet on the child's stdin: the runtime does not have it, so the move
	// still waits.
	f.writes.hold()
	release()
	waitState(t, h, agents.TurnRunning)
	select {
	case x := <-locked:
		x.Unlock()
		f.writes.release() // or the cleanup waits on the held write forever
		t.Fatal("the move got the gate before session/prompt reached the runtime")
	case <-time.After(50 * time.Millisecond):
	}
	f.writes.release()
	first := expectPrompt(t, f, "one")
	var x *lifegate.Exclusive
	select {
	case x = <-locked:
	case <-time.After(hangGuard):
		t.Fatal("the pump kept its lease past the handover; a running turn must not hold a move off")
	}
	// The runtime has the input, but its own record (and so DriveState) may not show the turn
	// yet: the move's state check has to see it in flight here, or it would start.
	if !h.HoldsEntry() {
		x.Unlock()
		t.Fatal("the writer was admitted with the entry out of the queue, but HoldsEntry says nothing is in flight")
	}
	x.SetClosed("cursor-move-1-target", false)
	x.Unlock()

	// Accepted after input closed (the HTTP gate sits above this layer): it must stay queued.
	mustSend(t, h, member("m2", "two"))
	f.reply(first, map[string]any{"stopReason": "end_turn"})
	expectNoPrompt(t, h, f)
	h.mu.Lock()
	items := h.q.Items()
	h.mu.Unlock()
	if !equal(ids(items), []string{"m2"}) {
		t.Fatalf("queue while closed = %v, want m2 still queued for the snapshot", ids(items))
	}

	// The move rolls back: the parked pump starts again.
	lifegate.Reopen(h.name)
	second := expectPrompt(t, f, "two")
	f.reply(second, map[string]any{"stopReason": "end_turn"})
	waitPumpIdle(t, h)
	if st := h.TurnState(); st != agents.TurnCompleted {
		t.Errorf("state = %s, want completed", st)
	}
	if h.HoldsEntry() {
		t.Error("HoldsEntry still reports an entry after the queue drained")
	}
}
