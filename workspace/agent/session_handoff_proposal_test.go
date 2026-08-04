package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestSessionHandoffProposalRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "handoff1"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
	call := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/sessions/"+name+"/handoff-proposal", strings.NewReader(body))
		r.SetPathValue("name", name)
		w := httptest.NewRecorder()
		handleSessionHandoffProposal(w, r)
		return w
	}
	if got := call(http.MethodPost, `{"prompt":"  Continue with task B.  "}`); got.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", got.Code, got.Body.String())
	}
	got := call(http.MethodGet, "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "Continue with task B.") {
		t.Fatalf("GET status=%d body=%s", got.Code, got.Body.String())
	}
	if got := call(http.MethodDelete, ""); got.Code != http.StatusNoContent {
		t.Fatalf("DELETE status=%d body=%s", got.Code, got.Body.String())
	}
	got = call(http.MethodGet, "")
	if !strings.Contains(got.Body.String(), `"proposal":null`) {
		t.Fatalf("proposal remains after delete: %s", got.Body.String())
	}
}
