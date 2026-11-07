package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

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
