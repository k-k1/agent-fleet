package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		HandleSessionHandoffProposal(w, r)
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
	HandleSessionHandoffProposal(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE status=%d body=%s", w.Code, w.Body.String())
	}
	list = decodeProposalsField(t, call(http.MethodGet, "").Body.String())
	if len(list) != 1 || findProposal(list, id) != nil {
		t.Fatalf("discard should remove only the targeted proposal: %+v", list)
	}
}

// The session row has to say that a handoff is waiting to be launched. Without it the session
// that proposed one is idle, i.e. it shows the chip of a session with nothing left to do, and
// the proposal — a card in the mirror, with no notification behind it — is seen only by
// somebody who happens to open that conversation.
func TestWireSessionFlagsAnUnlaunchedHandoffProposal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "handoff3"
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	post := func(body string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/handoff-proposal", strings.NewReader(body))
		r.SetPathValue("name", name)
		w := httptest.NewRecorder()
		HandleSessionHandoffProposal(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("POST %s: status=%d body=%s", body, w.Code, w.Body.String())
		}
	}
	pending := func() bool {
		t.Helper()
		return wireSession(m, true).HandoffPending
	}

	if pending() {
		t.Fatal("a session that has proposed nothing must not claim a pending handoff")
	}
	post(`{"prompt":"Continue with task B.","title":"Continue task B"}`)
	if !pending() {
		t.Fatal("an outstanding proposal did not reach the wire; the row still reads as plain idle")
	}

	// Launching is what clears it — the proposal itself is KEPT (re-reading a handoff is
	// useful and discarding is the user's call), so the flag has to read launched_at rather
	// than the file's existence.
	id, _ := decodeProposalsField(t, func() string {
		r := httptest.NewRequest(http.MethodGet, "/sessions/"+name+"/handoff-proposal", nil)
		r.SetPathValue("name", name)
		w := httptest.NewRecorder()
		HandleSessionHandoffProposal(w, r)
		return w.Body.String()
	}())[0]["id"].(string)
	post(`{"id":"` + id + `","launched":true}`)
	if pending() {
		t.Fatal("the flag survived the launch; the row would go on advertising work that has started")
	}

	// A newer proposal raises it again — the launched one is history, this is the last thing
	// the session handed on.
	post(`{"prompt":"Continue with task D.","title":"Continue task D"}`)
	if !pending() {
		t.Fatal("a proposal made after a launched one did not raise the flag")
	}

	// A stopped session carries it too: one folded away with an unlaunched handoff is
	// precisely the one nobody reopens.
	if !wireSession(m, false).HandoffPending {
		t.Fatal("a stopped row dropped the flag")
	}
}

// The judgement is on the LAST proposal, not on "is any of them unlaunched". Proposals are kept
// after launch, so a session that has handed work on repeatedly holds every card it ever made,
// and a proposal a later one REPLACED would otherwise pin the badge for good — which is what
// happened on the real store (2026-09-10: 5 of the 8 badged rows were exactly this).
func TestWireSessionIgnoresASupersededHandoffProposal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "handoff4"
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	add := func(title string) string {
		t.Helper()
		body := `{"prompt":"Continue.","title":"` + title + `"}`
		r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/handoff-proposal", strings.NewReader(body))
		r.SetPathValue("name", name)
		w := httptest.NewRecorder()
		HandleSessionHandoffProposal(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("POST %s: status=%d body=%s", title, w.Code, w.Body.String())
		}
		var resp struct {
			Proposal struct {
				ID string `json:"id"`
			} `json:"proposal"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode proposal: %v", err)
		}
		return resp.Proposal.ID
	}
	launch := func(id string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/handoff-proposal", strings.NewReader(`{"id":"`+id+`","launched":true}`))
		r.SetPathValue("name", name)
		w := httptest.NewRecorder()
		HandleSessionHandoffProposal(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("mark launched: status=%d body=%s", w.Code, w.Body.String())
		}
	}

	add("The first plan")
	// CreatedAt has millisecond resolution, so give the second proposal a distinct one — two
	// entries stamped the same millisecond are a tie the ordering rule resolves by position,
	// which is not what this test is about.
	time.Sleep(2 * time.Millisecond)
	second := add("The plan that replaced it")
	launch(second)

	if wireSession(m, true).HandoffPending {
		t.Fatal("a proposal that a later, launched one replaced still badges the row; that badge can never be cleared")
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

// A proposal's title check must follow the same rule as the session-create API. Looser here
// and a proposal can be saved and edited but fails with bad_title at the moment of launch,
// which the user only ever sees as "worktree launch failed" (a real incident). So this
// asserts directly that a title which passed saving also passes CleanTitle.
func TestSessionHandoffProposalTitleMatchesCreateRule(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "handoff2"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
	post := func(title string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"prompt": "続きをお願いします", "title": title})
		r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/handoff-proposal", strings.NewReader(string(body)))
		r.SetPathValue("name", name)
		w := httptest.NewRecorder()
		HandleSessionHandoffProposal(w, r)
		return w
	}
	// Exactly 80 runes is accepted (Japanese is 3 bytes per rune, so this is a different
	// rule from the old 512-byte one).
	if got := post(strings.Repeat("あ", SessionTitleMaxRunes)); got.Code != http.StatusOK {
		t.Fatalf("80 runes should be accepted: status=%d body=%s", got.Code, got.Body.String())
	}
	for _, title := range []string{
		strings.Repeat("あ", SessionTitleMaxRunes+1), // one rune over
		"改行を\n含むタイトル",                               // control character
	} {
		got := post(title)
		if got.Code != http.StatusBadRequest {
			t.Fatalf("title %q should be refused at proposal time: status=%d body=%s", title, got.Code, got.Body.String())
		}
	}
	// A stored title must pass the create API unchanged.
	for _, p := range decodeProposalsField(t, func() string {
		r := httptest.NewRequest(http.MethodGet, "/sessions/"+name+"/handoff-proposal", nil)
		r.SetPathValue("name", name)
		w := httptest.NewRecorder()
		HandleSessionHandoffProposal(w, r)
		return w.Body.String()
	}()) {
		title, _ := p["title"].(string)
		if _, ok := CleanTitle(title); !ok {
			t.Fatalf("stored title would be rejected by POST /sessions: %q", title)
		}
	}
}
