package cursor

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// A child that dies with input queued leaves it queued — not taken off by the pump, recorded
// in the ledger and lost on the dead pipe — and the respawn starts it without waiting for
// another send.
func TestQueueSurvivesChildDeathAndStartsAfterRespawn(t *testing.T) {
	h, f := newTestHandle(t)
	if err := h.Send(agents.TurnInput{Prompt: "one", ClientMessageID: "m1"}); err != nil {
		t.Fatal(err)
	}
	<-f.gotPrompt
	waitState(t, h, agents.TurnRunning)
	if err := h.Send(agents.TurnInput{Prompt: "two", ClientMessageID: "m2"}); err != nil {
		t.Fatal(err)
	}

	// The child dies. watch has not marked the handle yet (it waits for the process to be
	// reaped), which is the window the pump used to take the next input in.
	f.toClient.Close()
	waitState(t, h, agents.TurnUnknown)
	waitPumpIdle(t, h)
	if got := h.queuedPrompts(); len(got) != 1 || got[0] != "two" {
		t.Fatalf("queue after the child died = %q, want [two]", got)
	}

	// What spawn does once the new child is up.
	cl2, f2 := newFakeACP(t)
	h.mu.Lock()
	h.cl, h.alive = cl2, true
	h.mu.Unlock()
	h.cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		h.onServerRequest(h.cl, id, method, params)
	}
	h.cl.onNotify = h.onNotify
	h.resumePump()
	select {
	case id := <-f2.gotPrompt:
		if got := f2.promptTexts(); len(got) != 1 || got[0] != "two" {
			t.Fatalf("the respawned child got %q, want [two]", got)
		}
		f2.reply(id, map[string]any{"stopReason": "end_turn"})
	case <-time.After(5 * time.Second):
		t.Fatal("the queued input never reached the respawned child")
	}
	waitState(t, h, agents.TurnCompleted)
}
