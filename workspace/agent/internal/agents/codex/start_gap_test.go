package codex

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// A stop pressed while turn/start is in flight has no turn id to name yet. It still reaches
// the turn: turn/interrupt goes out as soon as the answer names it.
func TestStopDuringTurnStartInterruptsTheTurn(t *testing.T) {
	m, cl := newMockCodexServer(t)
	hold := make(chan struct{})
	m.holdStart = hold
	h := newCodexTestHandle(t, cl, "codex-stop-in-start")
	registerCodexTestHandle(t, h)
	if err := h.Send(agents.TurnInput{Prompt: "in flight", ClientMessageID: "af_inflight"}); err != nil {
		t.Fatal(err)
	}
	waitCodexCalls(t, m, "turn/start", 1)
	if err := h.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if got := m.callCount("turn/interrupt"); got != 0 {
		t.Fatalf("turn/interrupt sent before the turn had an id: %d", got)
	}
	close(hold)
	waitCodexCalls(t, m, "turn/interrupt", 1)
	waitCodexState(t, h, agents.TurnCancelled)
}

// Peer input is spared by a stop (ADR 0041), and that holds while its turn/start is in flight.
func TestStopDuringTurnStartSparesPeerInput(t *testing.T) {
	m, cl := newMockCodexServer(t)
	hold := make(chan struct{})
	m.holdStart = hold
	h := newCodexTestHandle(t, cl, "codex-stop-in-start-peer")
	registerCodexTestHandle(t, h)
	if err := h.Send(agents.TurnInput{Prompt: "from a peer", ClientMessageID: "af_peer", KeepOnInterrupt: true}); err != nil {
		t.Fatal(err)
	}
	waitCodexCalls(t, m, "turn/start", 1)
	if err := h.Interrupt(); err != nil {
		t.Fatal(err)
	}
	close(hold)
	waitCodexState(t, h, agents.TurnRunning)
	time.Sleep(200 * time.Millisecond)
	if got := m.callCount("turn/interrupt"); got != 0 {
		t.Fatalf("the stop interrupted the peer message's turn: %d turn/interrupt", got)
	}
	m.complete("completed") // settle the turn while this test's HOME is still in place
	waitCodexState(t, h, agents.TurnCompleted)
}
