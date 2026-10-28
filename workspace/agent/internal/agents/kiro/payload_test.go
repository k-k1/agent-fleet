package kiro

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// payload is what the messages payload gets from the test handle, through the path the
// read layer takes.
func payload(t *testing.T, h *threadHandle) agents.TranscriptData {
	t.Helper()
	handlesMu.Lock()
	handles[h.SessionName()] = h
	handlesMu.Unlock()
	defer func() {
		handlesMu.Lock()
		delete(handles, h.SessionName())
		handlesMu.Unlock()
	}()
	return managedTranscript(session.Meta{Name: h.SessionName(), Driver: session.DriverManaged})
}

// The read layer shows the shared handle's queue: input held behind a running turn reaches the
// messages payload through this kind's own transcript path.
func TestPayloadCarriesTheQueue(t *testing.T) {
	h, f := newTestHandle(t)
	if err := h.Send(agents.TurnInput{Prompt: "one", ClientMessageID: "m1"}); err != nil {
		t.Fatal(err)
	}
	first := <-f.gotPrompt
	waitState(t, h, agents.TurnRunning)
	if err := h.Send(agents.TurnInput{Prompt: "two", ClientMessageID: "m2"}); err != nil {
		t.Fatal(err)
	}
	// The running head stays in the queue as committed until it settles; m2 waits behind it.
	td := payload(t, h)
	n := len(td.QueuedItems)
	if n == 0 || td.QueuedItems[n-1].ID != "m2" || td.QueuedItems[n-1].State != agents.EntryQueued ||
		len(td.Queued) == 0 || td.Queued[len(td.Queued)-1] != "two" {
		t.Fatalf("payload queue = items %+v, queued %q; want m2 / two waiting last", td.QueuedItems, td.Queued)
	}
	f.reply(first, map[string]any{"stopReason": "end_turn"})
	f.reply(<-f.gotPrompt, map[string]any{"stopReason": "end_turn"})
	waitState(t, h, agents.TurnCompleted)
	waitPumpIdle(t, h)
}
