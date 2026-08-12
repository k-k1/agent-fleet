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
	list := decodeProposalsField(t, call(http.MethodGet, "").Body.String())
	if len(list) != 1 || list[0]["prompt"] != "Continue with task B." || list[0]["title"] != "Continue task B" {
		t.Fatalf("GET after create = %+v", list)
	}
	id, _ := list[0]["id"].(string)
	if id == "" {
		t.Fatal("no id minted for the new proposal")
	}

	// A second create (no id — the shape propose_session_handoff always sends) adds a
	// SECOND outstanding proposal rather than clobbering the first: this is the fix for
	// the sbm2uo3 incident, where three propose_session_handoff calls in one turn used
	// to collapse into a single stored proposal.
	if got := call(http.MethodPost, `{"prompt":"Continue with task D.","title":"Continue task D"}`); got.Code != http.StatusOK {
		t.Fatalf("second create status=%d body=%s", got.Code, got.Body.String())
	}
	list = decodeProposalsField(t, call(http.MethodGet, "").Body.String())
	if len(list) != 2 {
		t.Fatalf("want 2 outstanding proposals after a second create, got %+v", list)
	}

	// An edit (id supplied) keeps created_at: the mirror places the card at that point in
	// the conversation, so re-stamping it would slide the card back to the bottom and hide
	// every later message again (the 2026-08-04 bug). It also does not add a third entry.
	created, _ := list[0]["created_at"].(float64)
	if created == 0 {
		t.Fatal("created_at missing on first write")
	}
	editBody := `{"id":"` + id + `","prompt":"Continue with task C.","title":"Continue task C"}`
	if got := call(http.MethodPost, editBody); got.Code != http.StatusOK {
		t.Fatalf("edit status=%d body=%s", got.Code, got.Body.String())
	}
	list = decodeProposalsField(t, call(http.MethodGet, "").Body.String())
	if len(list) != 2 {
		t.Fatalf("edit must not add an entry, got %+v", list)
	}
	edited := findProposal(list, id)
	if edited == nil {
		t.Fatalf("edited proposal %q missing: %+v", id, list)
	}
	if after, _ := edited["created_at"].(float64); after != created {
		t.Fatalf("created_at moved on edit: %v → %v", created, after)
	}
	if edited["prompt"] != "Continue with task C." {
		t.Fatalf("edit did not take: %+v", edited)
	}

	// {"id":..., "launched":true} alone badges that one proposal without touching it or
	// the others — and keeps it (discarding a handoff is the user's call).
	if got := call(http.MethodPost, `{"id":"`+id+`","launched":true}`); got.Code != http.StatusOK {
		t.Fatalf("mark launched status=%d body=%s", got.Code, got.Body.String())
	}
	list = decodeProposalsField(t, call(http.MethodGet, "").Body.String())
	launched := findProposal(list, id)
	if launched == nil || launched["launched_at"] == nil {
		t.Fatalf("launched_at not recorded: %+v", list)
	}
	if launched["prompt"] != "Continue with task C." {
		t.Fatalf("marking launched lost the prompt: %+v", launched)
	}

	// Marking launched without an id is rejected now that there is more than one
	// proposal to choose from.
	if got := call(http.MethodPost, `{"launched":true}`); got.Code != http.StatusBadRequest {
		t.Fatalf("mark launched without id: status=%d body=%s", got.Code, got.Body.String())
	}

	// Discard only the edited/launched proposal — the second one survives.
	if got := call(http.MethodDelete, ""); got.Code != http.StatusBadRequest {
		t.Fatalf("DELETE without id: status=%d body=%s", got.Code, got.Body.String())
	}
	r := httptest.NewRequest(http.MethodDelete, "/sessions/"+name+"/handoff-proposal?id="+id, nil)
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	handleSessionHandoffProposal(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE status=%d body=%s", w.Code, w.Body.String())
	}
	list = decodeProposalsField(t, call(http.MethodGet, "").Body.String())
	if len(list) != 1 || findProposal(list, id) != nil {
		t.Fatalf("discard should remove only the targeted proposal: %+v", list)
	}
}

// decodeProposalsField pulls the proposals array out of a {"proposals":[…]} response.
func decodeProposalsField(t *testing.T, body string) []map[string]any {
	t.Helper()
	var resp struct {
		Proposals []map[string]any `json:"proposals"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode proposals: %v (body=%s)", err, body)
	}
	return resp.Proposals
}

func findProposal(list []map[string]any, id string) map[string]any {
	for _, p := range list {
		if p["id"] == id {
			return p
		}
	}
	return nil
}
