package muse

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
)

// recordStarts answers every turn/start as a started turn whose id is its commandId (the host's
// rule for fresh turns) and hands the params to the test.
func recordStarts(host *msptest.Host) chan msp.TurnStartParams {
	starts := make(chan msp.TurnStartParams, 8)
	host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
		var p msp.TurnStartParams
		json.Unmarshal(m.Params, &p)
		starts <- p
		return msp.TurnStartResult{Disposition: msp.TurnStartDispositionStarted, StartedNewTurn: true, TurnID: p.CommandID}, nil
	})
	return starts
}

func nextStart(t *testing.T, starts chan msp.TurnStartParams) msp.TurnStartParams {
	t.Helper()
	select {
	case p := <-starts:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no turn/start arrived")
		return msp.TurnStartParams{}
	}
}

func noStart(t *testing.T, starts chan msp.TurnStartParams, why string) {
	t.Helper()
	select {
	case p := <-starts:
		t.Fatalf("%s: %q started", why, *p.Input[0].Text)
	case <-time.After(200 * time.Millisecond):
	}
}

// runTurn sends in, and reports the resulting turn started the way the host does.
func runTurn(t *testing.T, h *threadHandle, host *msptest.Host, starts chan msp.TurnStartParams, prompt string) string {
	t.Helper()
	if err := h.Send(agents.TurnInput{Prompt: prompt}); err != nil {
		t.Fatal(err)
	}
	p := nextStart(t, starts)
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{CommandID: p.CommandID, TurnID: p.CommandID, SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
	return p.CommandID
}

// The host reports idle BEFORE turn/completed (measured on 1.4.0). Idle must not release the
// queue: the next turn would start ahead of the completion that ends this one, and a send in
// between would jump the queue.
func TestIdleBeforeCompletedKeepsTheQueueInOrder(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := recordStarts(host)
	t1 := runTurn(t, h, host, starts, "first")
	if err := h.Send(agents.TurnInput{Prompt: "second"}); err != nil {
		t.Fatal(err)
	}

	host.Notify(msp.NotificationSessionStatusChanged, msp.SessionStatusChangedParams{SessionID: h.sid, Status: msp.SessionStatusIdle})
	waitEvent(t, h, agents.TurnCompleted)
	noStart(t, starts, "idle released the queue before turn/completed")
	if err := h.Send(agents.TurnInput{Prompt: "third"}); err != nil {
		t.Fatal(err)
	}
	noStart(t, starts, "a send between idle and turn/completed started at once")

	host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{TurnID: t1, SessionID: h.sid, Terminal: msp.TurnTerminalCompleted})
	if got := *nextStart(t, starts).Input[0].Text; got != "second" {
		t.Fatalf("drained %q first, want second", got)
	}
}

// An idle with no turn/completed behind it still releases the queue, after the grace.
func TestIdleWithoutCompletionReleasesTheQueue(t *testing.T) {
	grace := idleSettleGrace
	idleSettleGrace = 50 * time.Millisecond
	t.Cleanup(func() { idleSettleGrace = grace })
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := recordStarts(host)
	runTurn(t, h, host, starts, "first")
	if err := h.Send(agents.TurnInput{Prompt: "second"}); err != nil {
		t.Fatal(err)
	}

	host.Notify(msp.NotificationSessionStatusChanged, msp.SessionStatusChangedParams{SessionID: h.sid, Status: msp.SessionStatusIdle})
	if got := *nextStart(t, starts).Input[0].Text; got != "second" {
		t.Fatalf("drained %q, want second", got)
	}
}

// A late turn/completed for a turn settleIdle already closed must not end the turn that
// replaced it.
func TestLateCompletionDoesNotEndTheNextTurn(t *testing.T) {
	grace := idleSettleGrace
	idleSettleGrace = 50 * time.Millisecond
	t.Cleanup(func() { idleSettleGrace = grace })
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := recordStarts(host)
	t1 := runTurn(t, h, host, starts, "first")
	if err := h.Send(agents.TurnInput{Prompt: "second"}); err != nil {
		t.Fatal(err)
	}
	host.Notify(msp.NotificationSessionStatusChanged, msp.SessionStatusChangedParams{SessionID: h.sid, Status: msp.SessionStatusIdle})
	second := nextStart(t, starts)
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{CommandID: second.CommandID, TurnID: second.CommandID, SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)

	host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{TurnID: t1, SessionID: h.sid, Terminal: msp.TurnTerminalCompleted})
	time.Sleep(100 * time.Millisecond)
	h.mu.Lock()
	running, turnID := h.running, h.turnID
	h.mu.Unlock()
	if !running || turnID != second.CommandID {
		t.Fatalf("after a stale completion running=%v turnID=%q, want the second turn still running", running, turnID)
	}
}

// A host that dies with input queued keeps it for the respawn, and a send after the respawn
// goes behind it rather than ahead.
func TestHostLossKeepsTheQueueForTheRespawn(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := recordStarts(host)
	runTurn(t, h, host, starts, "first")
	if err := h.Send(agents.TurnInput{Prompt: "second"}); err != nil {
		t.Fatal(err)
	}
	cl := h.cl
	host.Close()
	h.hostLost(cl)
	waitEvent(t, h, agents.TurnAborted)

	// What spawn does once the new host is up.
	host2, cl2 := msptest.New(t, msp.Handler{OnNotification: h.onNotify, OnRequest: h.onRequest})
	starts2 := recordStarts(host2)
	h.mu.Lock()
	h.cl, h.alive = cl2, true
	h.mu.Unlock()
	if err := h.Send(agents.TurnInput{Prompt: "third"}); err != nil {
		t.Fatal(err)
	}
	if got := *nextStart(t, starts2).Input[0].Text; got != "second" {
		t.Fatalf("after the respawn %q started first, want second", got)
	}
	noStart(t, starts2, "the newer send started alongside the older one")
}

// spawn drains the queue a lost host left behind without waiting for a send.
func TestPumpAfterRespawnStartsTheLeftoverQueue(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := recordStarts(host)
	runTurn(t, h, host, starts, "first")
	if err := h.Send(agents.TurnInput{Prompt: "second"}); err != nil {
		t.Fatal(err)
	}
	cl := h.cl
	host.Close()
	h.hostLost(cl)

	host2, cl2 := msptest.New(t, msp.Handler{OnNotification: h.onNotify, OnRequest: h.onRequest})
	starts2 := recordStarts(host2)
	h.mu.Lock()
	h.cl, h.alive = cl2, true
	h.mu.Unlock()
	h.pump()
	if got := *nextStart(t, starts2).Input[0].Text; got != "second" {
		t.Fatalf("drained %q, want second", got)
	}
}

// A queued turn the host refuses is surfaced as a failed turn and the queue moves on; it is
// not dropped with only a log line.
func TestRefusedQueuedTurnFailsVisiblyAndTheQueueMovesOn(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := make(chan string, 8)
	host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
		var p msp.TurnStartParams
		json.Unmarshal(m.Params, &p)
		starts <- *p.Input[0].Text
		if *p.Input[0].Text == "refused" {
			return nil, &msp.Error{Code: -32602, Message: "invalid params"}
		}
		return msp.TurnStartResult{Disposition: msp.TurnStartDispositionStarted, TurnID: p.CommandID}, nil
	})
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{TurnID: "t-1", SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
	for _, p := range []string{"refused", "after"} {
		if err := h.Send(agents.TurnInput{Prompt: p}); err != nil {
			t.Fatal(err)
		}
	}
	host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{TurnID: "t-1", SessionID: h.sid, Terminal: msp.TurnTerminalCompleted})
	waitEvent(t, h, agents.TurnFailed)
	for _, want := range []string{"refused", "after"} {
		select {
		case got := <-starts:
			if got != want {
				t.Fatalf("started %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%q never reached the host", want)
		}
	}
}

// A queued turn whose host dies under its turn/start goes back to the head of the queue.
func TestQueuedTurnOnADyingHostGoesBackToTheHead(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
		host.Close() // before the answer is written, so the call fails with ErrClosed
		return nil, nil
	})
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{TurnID: "t-1", SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
	for _, p := range []string{"second", "third"} {
		if err := h.Send(agents.TurnInput{Prompt: p}); err != nil {
			t.Fatal(err)
		}
	}
	host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{TurnID: "t-1", SessionID: h.sid, Terminal: msp.TurnTerminalCompleted})
	host.WaitForMethod(msp.MethodTurnStart)
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		var left []string
		for _, in := range h.queue {
			left = append(left, in.Prompt)
		}
		starting := h.starting
		h.mu.Unlock()
		if starting == "" && len(left) == 2 {
			if left[0] != "second" || left[1] != "third" {
				t.Fatalf("queue = %q, want [second third]", left)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue = %q (starting %q), want the input back at the head", left, starting)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A stop that arrives while the input's turn/start is in flight has no turn to interrupt yet;
// it lands on the turn as soon as the host reports it started.
func TestStopInTheStartGapInterruptsTheTurnOnArrival(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := recordStarts(host)
	host.Handle(msp.MethodTurnInterrupt, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})
	if err := h.Send(agents.TurnInput{Prompt: "in flight"}); err != nil {
		t.Fatal(err)
	}
	p := nextStart(t, starts)
	if err := h.Interrupt(); err != nil {
		t.Fatal(err)
	}
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{CommandID: p.CommandID, TurnID: p.CommandID, SessionID: h.sid})
	m := host.WaitForMethod(msp.MethodTurnInterrupt)
	var ip msp.TurnInterruptParams
	json.Unmarshal(m.Params, &ip)
	if ip.TurnID == nil || *ip.TurnID != p.CommandID {
		t.Fatalf("turn/interrupt targeted %v, want %s", ip.TurnID, p.CommandID)
	}
}

// Peer input is kept by a stop (ADR 0041), and that holds while its turn/start is in flight too.
func TestStopInTheStartGapSparesPeerInput(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := recordStarts(host)
	host.Handle(msp.MethodTurnInterrupt, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})
	if err := h.Send(agents.TurnInput{Prompt: "from a peer", KeepOnInterrupt: true}); err != nil {
		t.Fatal(err)
	}
	p := nextStart(t, starts)
	if err := h.Interrupt(); err != nil {
		t.Fatal(err)
	}
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{CommandID: p.CommandID, TurnID: p.CommandID, SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
	time.Sleep(200 * time.Millisecond)
	for _, m := range host.Received() {
		if m.Method == msp.MethodTurnInterrupt {
			t.Fatal("the stop interrupted the peer message's turn")
		}
	}
}
