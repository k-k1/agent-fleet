package codex

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// A stop pressed while turn/start is in flight has no turn id to name yet. It still reaches
// the turn, exactly once: turn/interrupt goes out as soon as the answer names it, and a second
// press in the same window does not add a second one.
func TestStopDuringTurnStartInterruptsTheTurn(t *testing.T) {
	m, cl := newMockCodexServer(t)
	hold := make(chan struct{})
	m.holdStart = hold
	h := newCodexTestHandle(t, cl, "codex-stop-in-start")
	registerCodexTestHandle(t, h)
	if err := h.Send(memberInput("in flight", "af_inflight")); err != nil {
		t.Fatal(err)
	}
	waitCodexCalls(t, m, "turn/start", 1)
	for i := 0; i < 2; i++ {
		res, err := h.Interrupt(agents.InterruptOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Stop != agents.StopFirst || res.Discard != nil {
			t.Fatalf("stop %d = %+v, want a first stop discarding nothing", i+1, res)
		}
	}
	if got := m.callCount("turn/interrupt"); got != 0 {
		t.Fatalf("turn/interrupt sent before the turn had an id: %d", got)
	}
	close(hold)
	waitCodexCalls(t, m, "turn/interrupt", 1)
	waitCodexState(t, h, agents.TurnCancelled)
	waitPumpDone(t, h)
	time.Sleep(100 * time.Millisecond)
	if got := m.callCount("turn/interrupt"); got != 1 {
		t.Fatalf("turn/interrupt count = %d, want exactly 1", got)
	}
}

// An input whose start is in flight while no other turn runs is the turn being stopped
// (ADR 0105 decision 1), whoever sent it: a peer message is stopped like the member's own.
func TestStopDuringTurnStartStopsPeerInputToo(t *testing.T) {
	m, cl := newMockCodexServer(t)
	hold := make(chan struct{})
	m.holdStart = hold
	h := newCodexTestHandle(t, cl, "codex-stop-in-start-peer")
	registerCodexTestHandle(t, h)
	if err := h.Send(peerInput("from a peer", "af_peer")); err != nil {
		t.Fatal(err)
	}
	waitCodexCalls(t, m, "turn/start", 1)
	if _, err := h.Interrupt(agents.InterruptOpts{}); err != nil {
		t.Fatal(err)
	}
	close(hold)
	waitCodexCalls(t, m, "turn/interrupt", 1)
	waitCodexState(t, h, agents.TurnCancelled)
	waitPumpDone(t, h)
}
