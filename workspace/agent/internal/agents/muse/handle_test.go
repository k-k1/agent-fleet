package muse

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
)

// The tests drive the handle through a fake MSP host rather than a real `muse serve`.
// That is not only a CI convenience: measured, the vendor's credential-free provider
// (`--provider echo`) is an `exec` startup flag with no `serve` equivalent, so a real turn
// against a real host always costs a credential (ADR 0095 P2-1).

// newTestHandle wires a handle to a fake host that is already past the handshake, and returns
// both. The spawn path itself needs a real binary and is covered by the live test.
func newTestHandle(t *testing.T, h *threadHandle) *msptest.Host {
	t.Helper()
	host, cl := msptest.New(t, msp.Handler{OnNotification: h.onNotify, OnRequest: h.onRequest})
	h.cl = cl
	h.alive = true
	h.sid = "01a0c1d6-0000-7000-8000-000000000001"
	if h.events == nil {
		h.events = make(chan agents.Event, 64)
	}
	if h.name == "" {
		h.name = "test-" + t.Name()
	}
	if h.slotSid == "" {
		h.slotSid = "00000000-0000-5000-8000-000000000001"
	}
	return host
}

func waitEvent(t *testing.T, h *threadHandle, want agents.TurnState) agents.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if ev.TurnState == want {
				return ev
			}
		case <-deadline:
			t.Fatalf("no event with state %s arrived", want)
		}
	}
}

func TestSendStartsATurnAndTracksState(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
		var p msp.TurnStartParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Errorf("turn/start params: %v", err)
		}
		if len(p.Input) != 1 || p.Input[0].Text == nil || *p.Input[0].Text != "hello" {
			t.Errorf("input = %+v", p.Input)
		}
		if p.SessionID != h.sid {
			t.Errorf("sessionId = %q", p.SessionID)
		}
		if p.CommandID == "" {
			t.Error("commandId is empty; the server never mints one")
		}
		return msp.CommandAcceptedResult{}, nil
	})

	if err := h.Send(agents.TurnInput{Prompt: "hello"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	// The command being accepted is not the turn starting: only turn/started moves the state,
	// so a host that accepted and then refused cannot leave the session stuck on "working".
	h.mu.Lock()
	running := h.running
	h.mu.Unlock()
	if running {
		t.Error("the handle marked the turn running before turn/started arrived")
	}

	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{TurnID: "t-1", SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)

	host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{
		TurnID: "t-1", SessionID: h.sid, Terminal: msp.TurnTerminalCompleted,
	})
	waitEvent(t, h, agents.TurnCompleted)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		t.Error("the turn is still marked running after turn/completed")
	}
	if h.turnID != "" {
		t.Errorf("turnID = %q, want cleared", h.turnID)
	}
}

// A turn that ended because nobody is signed in must not read as a completed answer: the
// member fixes the credential and resends, which is the aborted contract, not the failed one.
func TestAuthRequiredEndsTheTurnAsAborted(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{TurnID: "t-1", SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)

	host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{
		TurnID: "t-1", SessionID: h.sid, Terminal: msp.TurnTerminalFailed,
		Error: &msp.TurnError{Kind: "authRequired", Message: "not logged in"},
	})
	waitEvent(t, h, agents.TurnAborted)
}

func TestFailedAndCancelledTurnsKeepTheirOwnState(t *testing.T) {
	for _, tc := range []struct {
		terminal msp.TurnTerminal
		want     agents.TurnState
	}{
		{msp.TurnTerminalFailed, agents.TurnFailed},
		{msp.TurnTerminalCancelled, agents.TurnCancelled},
	} {
		h := &threadHandle{}
		host := newTestHandle(t, h)
		host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{
			TurnID: "t-1", SessionID: h.sid, Terminal: tc.terminal,
			Error: &msp.TurnError{Kind: "toolFailure", Message: "boom"},
		})
		waitEvent(t, h, tc.want)
	}
}

// A second Send while a turn runs queues rather than racing a second turn onto the host, and
// the queue drains when the first turn settles.
func TestSecondSendQueuesAndDrains(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := make(chan string, 4)
	host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
		var p msp.TurnStartParams
		json.Unmarshal(m.Params, &p)
		starts <- *p.Input[0].Text
		return msp.CommandAcceptedResult{}, nil
	})

	if err := h.Send(agents.TurnInput{Prompt: "first"}); err != nil {
		t.Fatal(err)
	}
	<-starts
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{TurnID: "t-1", SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)

	if err := h.Send(agents.TurnInput{Prompt: "second"}); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-starts:
		t.Fatalf("the second turn started while the first was running: %q", s)
	case <-time.After(200 * time.Millisecond):
	}
	h.mu.Lock()
	queued := len(h.queue)
	h.mu.Unlock()
	if queued != 1 {
		t.Errorf("%d turns queued, want 1", queued)
	}

	host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{
		TurnID: "t-1", SessionID: h.sid, Terminal: msp.TurnTerminalCompleted,
	})
	select {
	case s := <-starts:
		if s != "second" {
			t.Errorf("drained %q, want second", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the queued turn never started")
	}
}

// Steer is native on MSP, not a queue in disguise: it must reach turn/steer with the running
// turn's id, which is the host's own race guard.
func TestSteerReachesTurnSteerWithTheRunningTurnID(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Handle(msp.MethodTurnSteer, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{TurnID: "t-9", SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)

	if err := h.Steer(agents.TurnInput{Prompt: "also this"}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	m := host.WaitForMethod(msp.MethodTurnSteer)
	var p msp.TurnSteerParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.ExpectedTurnID != "t-9" {
		t.Errorf("expectedTurnId = %q, want t-9", p.ExpectedTurnID)
	}
}

// With no turn running there is nothing to steer, so a steer is a plain send — otherwise the
// member's input silently disappears.
func TestSteerWithNoRunningTurnStartsOne(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})
	if err := h.Steer(agents.TurnInput{Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	host.WaitForMethod(msp.MethodTurnStart)
}

// The ledger is what makes a resend after a reconnect idempotent.
func TestResendWithTheSameClientMessageIDStartsOneTurn(t *testing.T) {
	h := &threadHandle{name: "ledger-" + t.Name()}
	host := newTestHandle(t, h)
	t.Cleanup(func() { ledger.Remove(h.name) })
	starts := make(chan struct{}, 4)
	host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
		starts <- struct{}{}
		return msp.CommandAcceptedResult{}, nil
	})

	in := agents.TurnInput{Prompt: "once", ClientMessageID: "cm-1"}
	if err := h.Send(in); err != nil {
		t.Fatal(err)
	}
	<-starts
	if err := h.Send(in); err != nil {
		t.Fatal(err)
	}
	select {
	case <-starts:
		t.Fatal("the resend started a second turn")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestInterruptCancelsTheQueueAndCallsTurnInterrupt(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})
	host.Handle(msp.MethodTurnInterrupt, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{TurnID: "t-3", SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
	if err := h.Send(agents.TurnInput{Prompt: "queued"}); err != nil {
		t.Fatal(err)
	}

	if err := h.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	m := host.WaitForMethod(msp.MethodTurnInterrupt)
	var p msp.TurnInterruptParams
	json.Unmarshal(m.Params, &p)
	if p.TurnID == nil || *p.TurnID != "t-3" {
		t.Errorf("turnId = %v, want t-3", p.TurnID)
	}
	h.mu.Lock()
	queued := len(h.queue)
	h.mu.Unlock()
	if queued != 0 {
		t.Errorf("%d turns still queued after an interrupt", queued)
	}
}
