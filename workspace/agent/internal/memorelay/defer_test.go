package memorelay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func serveDefer(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/memos/defer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	HandleDefer(rec, req)
	return rec
}

// The relay hands back the run the calling session's meta names, with the session's name and this
// host's time of the call, and nothing the model could have chosen.
func TestDeferRelaysTheSessionsOwnRun(t *testing.T) {
	f := newFakeCP(t)
	session.WriteMeta(session.Meta{Name: "run01", Kind: session.KindClaude, QueueDelivery: "dlv1"})
	f.answer("POST /internal/memo-deliveries/dlv1/defer", http.StatusOK, `{"delivery":"dlv1","deferred":true}`)
	before := time.Now()
	rec := serveDefer(`{"from":"run01","reason":"waiting for #12","notBefore":"2026-11-20T12:00:00Z","delivery":"other"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(f.calls) != 1 || f.calls[0] != "POST /internal/memo-deliveries/dlv1/defer" {
		t.Fatalf("calls = %v", f.calls)
	}
	got := decode(t, f.bodies[0])
	if got["session"] != "run01" || got["reason"] != "waiting for #12" || got["notBefore"] != "2026-11-20T12:00:00Z" {
		t.Fatalf("body = %v", got)
	}
	at, err := time.Parse(time.RFC3339Nano, got["at"].(string))
	if err != nil || at.Before(before.Add(-time.Second)) || at.After(time.Now().Add(time.Second)) {
		t.Fatalf("at = %v (%v)", got["at"], err)
	}
}

// A session no queue run started has nothing to hand back, and the Control Plane is not asked.
func TestDeferRefusesASessionWithoutARun(t *testing.T) {
	f := newFakeCP(t)
	for _, from := range []string{"slot01", "nobody", ""} {
		rec := serveDefer(`{"from":"` + from + `","reason":"x"}`)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_queue_delivery") {
			t.Fatalf("from %q: status %d: %s", from, rec.Code, rec.Body)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("the Control Plane was asked: %v", f.calls)
	}
}
