package opencode

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// heldBehindAForeignTurn sends in while another client's turn keeps the session busy, and
// waits until the pump has taken it out of the queue and holds it in waitIdle.
func heldBehindAForeignTurn(t *testing.T, m *mockServe, h *threadHandle, in agents.TurnInput) {
	t.Helper()
	m.mu.Lock()
	m.busy = true
	m.mu.Unlock()
	if err := h.Send(in); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		held := h.held
		h.mu.Unlock()
		if held {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the pump never took the input out of the queue")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sentTurns(m *mockServe) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.turns...)
}

// The pump holds an input out of the queue while another client's turn runs. A stop in that
// wait reaches it as if it were still queued: it is not sent once the session goes idle.
func TestStopDiscardsInputHeldBehindAForeignTurn(t *testing.T) {
	m, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	heldBehindAForeignTurn(t, m, h, agents.TurnInput{Prompt: "own follow-up", ClientMessageID: "msg_own"})

	if err := h.Interrupt(); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.busy = false // the other client's turn ends
	m.mu.Unlock()
	waitPumpIdle(t, h)
	if got := sentTurns(m); len(got) != 0 {
		t.Fatalf("turns sent after the stop = %q, want none", got)
	}
	waitState(t, h, agents.TurnCancelled)
}

// A peer message is spared by a stop (ADR 0041) while it is held, as it is while queued.
func TestStopSparesPeerInputHeldBehindAForeignTurn(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 50 * time.Millisecond
	h := newTestHandle(t, srv)
	heldBehindAForeignTurn(t, m, h, agents.TurnInput{Prompt: "from a peer", ClientMessageID: "msg_peer", KeepOnInterrupt: true})

	if err := h.Interrupt(); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.busy = false
	m.mu.Unlock()
	waitPumpIdle(t, h)
	if got := sentTurns(m); len(got) != 1 || got[0] != "from a peer" {
		t.Fatalf("turns sent after the stop = %q, want the peer message", got)
	}
}
