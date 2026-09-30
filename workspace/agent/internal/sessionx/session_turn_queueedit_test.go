package sessionx

import (
	"net/http"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// liveStopWireDriver is stopWireDriver with a LiveHandles answer: live=false is a session whose
// runtime is down.
type liveStopWireDriver struct {
	stopWireDriver
	live bool
}

func (d *liveStopWireDriver) LiveHandle(session.Meta) (agents.ThreadHandle, bool) {
	if !d.live {
		return nil, false
	}
	return d.h, true
}

// A queue edit from a stale tab to a session whose runtime is down must not start one: the
// queue and the discards went with the handle, so the answer is "nothing there", without Resume.
func TestQueueEditDoesNotResumeAStoppedRuntime(t *testing.T) {
	h := useStopWire(t)
	d := &liveStopWireDriver{stopWireDriver: stopWireDriver{h: h}}
	managedDrivers[session.KindMuse] = d // useStopWire's cleanup restores the registry
	session.WriteMeta(session.Meta{Name: "qe-down", Dir: t.TempDir(), Kind: session.KindMuse, Driver: session.DriverManaged})

	rec := postTurn(t, "qe-down", `{"op":"remove","id":"cm_b"}`)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not_queued") {
		t.Fatalf("remove: status = %d, body = %s, want 404 not_queued", rec.Code, rec.Body.String())
	}
	rec = postTurn(t, "qe-down", `{"op":"dismiss_discard","id":"dsc_1"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"dismissed":false`) {
		t.Fatalf("dismiss: status = %d, body = %s, want dismissed=false", rec.Code, rec.Body.String())
	}
	rec = postTurn(t, "qe-down", `{"op":"remove"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("remove without id: status = %d, want 400", rec.Code)
	}
	if h.resumeCalls != 0 {
		t.Fatalf("Resume called %d times: a queue edit started the runtime", h.resumeCalls)
	}

	// With the runtime up, the edit reaches the live handle, still without Resume.
	d.live = true
	h.dismissed = true
	rec = postTurn(t, "qe-down", `{"op":"dismiss_discard","id":"dsc_1"}`)
	if !strings.Contains(rec.Body.String(), `"dismissed":true`) || len(h.dismissIDs) != 1 || h.resumeCalls != 0 {
		t.Fatalf("live dismiss: body = %s, ids = %v, resumes = %d", rec.Body.String(), h.dismissIDs, h.resumeCalls)
	}
}
