package opencode

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// heldBehindAForeignTurn sends in while another client's turn keeps the session busy, and
// waits until the pump holds it in waitIdle. The pump marks the hold before waitIdle's first
// status poll, so a second poll means the hold is in place.
func heldBehindAForeignTurn(t *testing.T, m *mockServe, h *threadHandle, in agents.TurnInput) {
	t.Helper()
	m.mu.Lock()
	m.busy = true
	m.statusPolls = 0
	m.mu.Unlock()
	if err := h.Send(in); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		polls := m.statusPolls
		m.mu.Unlock()
		if polls >= 2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the pump never held the input behind the foreign turn")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sentTurns(m *mockServe) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.turns...)
}

func endForeignTurn(m *mockServe) {
	m.mu.Lock()
	m.busy = false
	m.mu.Unlock()
}

// An input the pump holds behind another client's turn is queued, not the turn being stopped
// (ADR 0105 decision 1): a first stop ends what runs and the held input goes out after it.
func TestFirstStopLetsHeldInputContinue(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 50 * time.Millisecond
	h := newTestHandle(t, srv)
	heldBehindAForeignTurn(t, m, h, memberInput("own follow-up", "msg_own"))

	res, err := h.Interrupt(agents.InterruptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != agents.StopFirst || res.Discard != nil {
		t.Fatalf("first stop = %+v, want first with nothing discarded", res)
	}
	endForeignTurn(m) // the aborted foreign turn ends
	waitPumpIdle(t, h)
	if got := sentTurns(m); len(got) != 1 || got[0] != "own follow-up" {
		t.Fatalf("turns sent after the stop = %q, want the held input", got)
	}
	waitState(t, h, agents.TurnCompleted)
}

// A second stop reaches the held input, whoever sent it: it is never sent, and it comes back
// as discarded input. discard_queue does the same without a first stop.
func TestSecondStopDiscardsHeldInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stops []agents.InterruptOpts
		want  agents.StopKind
	}{
		{"second stop", []agents.InterruptOpts{{}, {}}, agents.StopSecond},
		{"discard queue", []agents.InterruptOpts{{DiscardQueue: true}}, agents.StopDiscard},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, srv := newMockServe(t)
			h := newTestHandle(t, srv)
			heldBehindAForeignTurn(t, m, h, peerInput("from a peer", "msg_peer"))

			var res agents.InterruptResult
			for _, o := range tc.stops {
				var err error
				if res, err = h.Interrupt(o); err != nil {
					t.Fatal(err)
				}
			}
			if res.Stop != tc.want || res.Discard == nil || len(res.Discard.Items) != 1 || res.Discard.Items[0].ID != "msg_peer" {
				t.Fatalf("last stop = %+v, want %s discarding the held peer message", res, tc.want)
			}
			endForeignTurn(m)
			waitPumpIdle(t, h)
			if got := sentTurns(m); len(got) != 0 {
				t.Fatalf("turns sent after the stop = %q, want none", got)
			}
			waitState(t, h, agents.TurnCancelled)
			h.mu.Lock()
			kept := h.tq().Discards()
			h.mu.Unlock()
			if len(kept) != 1 || kept[0].Items[0].Origin.Kind != agents.OriginPeer {
				t.Fatalf("kept discards = %+v, want the peer message kept for return", kept)
			}
		})
	}
}

// The held input has not been sent, so it can still be removed (decision 5).
func TestRemoveHeldInput(t *testing.T) {
	m, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	heldBehindAForeignTurn(t, m, h, memberInput("take it back", "msg_back"))
	it, err := h.RemoveQueued("msg_back")
	if err != nil || it.Text != "take it back" {
		t.Fatalf("remove = %+v, %v", it, err)
	}
	if _, err := h.RemoveQueued("msg_back"); !errors.Is(err, agents.ErrNotQueued) {
		t.Fatalf("second remove = %v, want not queued", err)
	}
	endForeignTurn(m)
	waitPumpIdle(t, h)
	if got := sentTurns(m); len(got) != 0 {
		t.Fatalf("turns sent after the removal = %q, want none", got)
	}
}

// The pump asks serve whether another client's turn runs before it waits behind it. The input
// counts as held from before that question, so a first stop landing while the answer is still
// on the way lets it continue behind the foreign turn instead of taking it for the turn being
// started.
func TestFirstStopDuringTheBusyCheckKeepsTheInput(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 50 * time.Millisecond
	h := newTestHandle(t, srv)
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	release := func() {
		once.Do(func() {
			m.mu.Lock()
			m.statusGate, m.statusEntered = nil, nil
			m.mu.Unlock()
			close(gate)
		})
	}
	// Registered after newTestHandle's pump wait, so it runs first: a failing test must not
	// leave the pump parked in the status call.
	t.Cleanup(release)
	m.mu.Lock()
	m.busy, m.statusGate, m.statusEntered = true, gate, entered
	m.mu.Unlock()
	if err := h.Send(memberInput("own follow-up", "msg_own")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the pump never asked serve whether it was busy")
	}
	res, err := h.Interrupt(agents.InterruptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != agents.StopFirst || res.Discard != nil {
		t.Fatalf("stop = %+v, want a first stop that leaves the waiting input alone", res)
	}
	release()
	endForeignTurn(m)
	waitPumpIdle(t, h)
	if got := sentTurns(m); len(got) != 1 || got[0] != "own follow-up" {
		t.Fatalf("turns sent after the stop = %q, want the input that waited", got)
	}
	waitState(t, h, agents.TurnCompleted)
}
