package main

import (
	"encoding/json"
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
	if got := call(http.MethodPost, `{"prompt":"  Continue with task B.  ","title":"Continue task B"}`); got.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", got.Code, got.Body.String())
	}
	got := call(http.MethodGet, "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "Continue with task B.") || !strings.Contains(got.Body.String(), "Continue task B") {
		t.Fatalf("GET status=%d body=%s", got.Code, got.Body.String())
	}
	// An edit keeps created_at: the mirror places the card at that point in the
	// conversation, so re-stamping it would slide the card back to the bottom and hide
	// every later message again (the 2026-08-04 bug).
	created := decodeProposalField(t, call(http.MethodGet, "").Body.String(), "created_at")
	if created == 0 {
		t.Fatal("created_at missing on first write")
	}
	if got := call(http.MethodPost, `{"prompt":"Continue with task C.","title":"Continue task C"}`); got.Code != http.StatusOK {
		t.Fatalf("edit status=%d body=%s", got.Code, got.Body.String())
	}
	if after := decodeProposalField(t, call(http.MethodGet, "").Body.String(), "created_at"); after != created {
		t.Fatalf("created_at moved on edit: %d → %d", created, after)
	}
	// {"launched":true} alone badges the proposal without touching it — and keeps it
	// (discarding a handoff is the user's call).
	if got := call(http.MethodPost, `{"launched":true}`); got.Code != http.StatusOK {
		t.Fatalf("mark launched status=%d body=%s", got.Code, got.Body.String())
	}
	body := call(http.MethodGet, "").Body.String()
	if decodeProposalField(t, body, "launched_at") == 0 {
		t.Fatalf("launched_at not recorded: %s", body)
	}
	if !strings.Contains(body, "Continue with task C.") {
		t.Fatalf("marking launched lost the prompt: %s", body)
	}
	if got := call(http.MethodDelete, ""); got.Code != http.StatusNoContent {
		t.Fatalf("DELETE status=%d body=%s", got.Code, got.Body.String())
	}
	got = call(http.MethodGet, "")
	if !strings.Contains(got.Body.String(), `"proposal":null`) {
		t.Fatalf("proposal remains after delete: %s", got.Body.String())
	}
	// Nothing outstanding: the launched flag has nothing to mark and must say so
	// rather than minting an empty proposal.
	if got := call(http.MethodPost, `{"launched":true}`); got.Code != http.StatusNotFound {
		t.Fatalf("mark launched without proposal: status=%d body=%s", got.Code, got.Body.String())
	}
}

// decodeProposalField pulls one numeric field out of a {"proposal":{…}} response.
func decodeProposalField(t *testing.T, body, field string) int64 {
	t.Helper()
	var resp struct {
		Proposal map[string]any `json:"proposal"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode %s: %v (body=%s)", field, err, body)
	}
	v, _ := resp.Proposal[field].(float64)
	return int64(v)
}
