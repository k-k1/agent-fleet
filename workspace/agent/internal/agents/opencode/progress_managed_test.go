package opencode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func progressTestHandle(t *testing.T) (*threadHandle, session.Meta) {
	t.Helper()
	h := &threadHandle{name: "prog", ses: "ses_p", alive: true, events: make(chan agents.Event, 8)}
	handlesMu.Lock()
	handles[h.name] = h
	handlesMu.Unlock()
	t.Cleanup(func() { handlesMu.Lock(); delete(handles, h.name); handlesMu.Unlock() })
	return h, session.Meta{Name: h.name}
}

func feed(typ, id string) {
	noteProgress(typ, json.RawMessage(`{"sessionID":"ses_p","id":"`+id+`"}`))
}

func stale(h *threadHandle) { h.act.TouchAt(time.Now().Add(-2 * time.Hour)) }

// The sequence measured on opencode 2.0.32 (`sleep 120`): after session.tool.progress nothing
// arrives for the whole sleep, then session.tool.success and session.step.ended.
func TestSilentToolStaysAliveUntilItsEnd(t *testing.T) {
	h, m := progressTestHandle(t)
	feed("session.execution.started", "") // the turn began on this stream
	feed("session.tool.called", "call_1")
	feed("session.tool.progress", "call_1")
	stale(h)

	got, ok := (managedDriver{}).ManagedProgress(m)
	if !ok || time.Since(got) > time.Minute {
		t.Fatalf("silent tool in flight: got %v, %v; want now", got, ok)
	}
	feed("session.tool.success", "call_1")
	stale(h)
	got, ok = (managedDriver{}).ManagedProgress(m)
	if !ok || time.Since(got) < time.Hour {
		t.Fatalf("after the tool ended: got %v, %v; want the stale stamp, so the bound can lapse", got, ok)
	}
}

// The stream is not replayed: a called event lost in a gap is restored by the call's next event.
func TestToolWhoseCalledEventWasMissedIsRestoredByProgress(t *testing.T) {
	h, m := progressTestHandle(t)
	h.tools.Unknown() // reconnect
	feed("session.tool.progress", "call_1")
	stale(h)
	got, ok := (managedDriver{}).ManagedProgress(m)
	if !ok || time.Since(got) > time.Minute {
		t.Fatalf("got %v, %v; want now", got, ok)
	}
}

// After a gap, or on a handle that adopted a running daemon, no event may revive trust in the
// set before a turn boundary: one unrelated event leaves a silent tool undetected.
func TestGapKeepsAStaleRowHoldingUntilATurnBoundary(t *testing.T) {
	h, m := progressTestHandle(t)
	feed("session.reasoning.delta", "") // zero value: adopted mid-turn
	stale(h)
	if got, ok := (managedDriver{}).ManagedProgress(m); ok {
		t.Fatalf("answered %v although the in-flight set is unknown", got)
	}
	feed("session.step.ended", "")
	stale(h)
	if _, ok := (managedDriver{}).ManagedProgress(m); !ok {
		t.Fatal("no answer after a turn boundary")
	}
	h.runtimeLost()
	stale(h)
	if got, ok := (managedDriver{}).ManagedProgress(m); ok {
		t.Fatalf("answered %v after the runtime was lost", got)
	}
}

func TestParallelToolsAreTrackedByCallID(t *testing.T) {
	h, _ := progressTestHandle(t)
	feed("session.execution.started", "")
	feed("session.tool.called", "call_1")
	feed("session.tool.called", "call_2")
	feed("session.tool.success", "call_1")
	if alive, _ := h.tools.State(); !alive {
		t.Fatal("the second call was dropped when the first ended")
	}
	feed("session.tool.failed", "call_2")
	if alive, _ := h.tools.State(); alive {
		t.Fatal("a failed call stayed in flight")
	}
}

// A missed end event must not pin the row beyond the turn.
func TestStepEndClearsALostToolEnd(t *testing.T) {
	for _, end := range []string{"session.step.ended", "session.execution.succeeded", "session.execution.interrupted"} {
		h, _ := progressTestHandle(t)
		feed("session.tool.called", "call_1")
		feed(end, "")
		if alive, _ := h.tools.State(); alive {
			t.Errorf("%s left a tool in flight", end)
		}
		handlesMu.Lock()
		delete(handles, h.name)
		handlesMu.Unlock()
	}
}

func TestEventsOfAnotherSessionDoNotStamp(t *testing.T) {
	h, _ := progressTestHandle(t)
	noteProgress("session.tool.called", json.RawMessage(`{"sessionID":"ses_other","id":"c"}`))
	if _, ok := h.act.At(); ok {
		t.Fatal("another session's event reached this handle")
	}
}

// A real SSE stream that delivers a turn boundary and then ends: the sets must be unknown as
// soon as streamEvents returns, before any reconnect succeeds.
func TestStreamEndInvalidatesTheInFlightSets(t *testing.T) {
	h, m := progressTestHandle(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"session.execution.started\",\"data\":{\"sessionID\":\"ses_p\"}}\n\n")
	}))
	t.Cleanup(srv.Close)
	s := &Supervisor{}
	gen := s.Generation()

	_ = s.streamEvents(srv.URL, gen) // returns at EOF
	stale(h)
	if got, ok := (managedDriver{}).ManagedProgress(m); ok {
		t.Fatalf("answered %v after the event stream ended", got)
	}

	// A refused connection is an exit too.
	h.tools.Reset()
	srv.Close()
	_ = s.streamEvents(srv.URL, gen)
	stale(h)
	if got, ok := (managedDriver{}).ManagedProgress(m); ok {
		t.Fatalf("answered %v after a failed reconnect", got)
	}

	// A superseded generation's monitor leaves the current generation's known state alone.
	h.tools.Reset()
	_ = s.streamEvents(srv.URL, gen+1)
	stale(h)
	if _, ok := (managedDriver{}).ManagedProgress(m); !ok {
		t.Fatal("a stale generation's stream end cleared the current generation's state")
	}
}
