package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestShareRelayRefusesFieldsOutsideTheAllowList(t *testing.T) {
	cases := []struct {
		name, path, body, code string
	}{
		{"plain turn", "/sessions/s/turn", `{"op":"start","prompt":"hi"}`, ""},
		{"steer with id", "/sessions/s/turn", `{"op":"steer","prompt":"hi","clientMessageID":"c1"}`, ""},
		{"empty attachments", "/sessions/s/turn", `{"op":"start","prompt":"hi","attachments":[]}`, "share_attachments_refused"},
		{"attachment path", "/sessions/s/turn", `{"op":"start","prompt":"hi","attachments":["/home/dev/.ssh/id"]}`, "share_attachments_refused"},
		{"unknown field", "/sessions/s/turn", `{"op":"start","prompt":"hi","discard_queue":true}`, "share_field_refused"},
		{"queue op", "/sessions/s/turn", `{"op":"remove","id":"x"}`, "share_field_refused"},
		{"interrupt op", "/sessions/s/turn", `{"op":"interrupt"}`, "share_op_refused"},
		{"respond", "/sessions/s/respond", `{"id":"i","decision":"allow","scope":"once"}`, ""},
		{"respond extra", "/sessions/s/respond", `{"id":"i","decision":"allow","x":1}`, "share_field_refused"},
		{"plan expect", "/sessions/s/plan-respond", `{"decision":"approve","expect_plan":"p"}`, "share_field_refused"},
		{"plan ok", "/sessions/s/plan-respond", `{"decision":"reject","feedback":"no"}`, ""},
		{"answer ok", "/sessions/s/answer-question", `{"choices":[1]}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, _ := refuseShareRelay(c.path, []byte(c.body)); code != c.code {
				t.Fatalf("code = %q, want %q", code, c.code)
			}
		})
	}
}

func TestShareOperationGuardRefusesBeforeClaimAndLeavesOwnerRequestsAlone(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	var calls atomic.Int32
	h := withShareOperationIdempotency(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	const id = "0123456789abcdef0123456789abcdef"
	body := `{"op":"start","prompt":"p","attachments":["/home/dev/secret"]}`
	req := httptest.NewRequest(http.MethodPost, "/sessions/s/turn", strings.NewReader(body))
	req.Header.Set(shareOperationHeader, id)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("relayed attachment: status=%d calls=%d", rec.Code, calls.Load())
	}
	if _, ok := readShareOperation(id); ok {
		t.Fatal("a refused operation must not be claimed")
	}
	// The owner's own Console sends no operation id: their attachments are theirs to send.
	own := httptest.NewRequest(http.MethodPost, "/sessions/s/turn", strings.NewReader(body))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, own)
	if rec.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("owner request: status=%d calls=%d", rec.Code, calls.Load())
	}
}

func TestShareOperationGuardRefusesTerminalSessions(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	session.WriteMeta(session.Meta{Name: "term0001", Kind: session.KindShell})
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.Handle("POST /sessions/{name}/turn", withShareOperationIdempotency(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) })))
	req := httptest.NewRequest(http.MethodPost, "/sessions/term0001/turn", strings.NewReader(`{"op":"start","prompt":"ls"}`))
	req.Header.Set(shareOperationHeader, "0123456789abcdef0123456789abcde0")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d", rec.Code, calls.Load())
	}
}
