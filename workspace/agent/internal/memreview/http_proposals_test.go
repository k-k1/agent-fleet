package memreview

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin/memberorigintest"
)

// decisionRoutes are the routes that change anything. Their handlers are mounted exactly as
// workspace/agent/routes.go mounts them (the real table is checked in package main too).
var decisionRoutes = map[string]http.HandlerFunc{
	"/agents/memory-review/proposals/list":       HandleProposals,
	"/agents/memory-review/proposals/approve":    HandleApprove,
	"/agents/memory-review/proposals/reject":     HandleReject,
	"/agents/memory-review/proposals/reject-all": HandleRejectAll,
	"/agents/memory-review/runs/revert":          HandleRevertRun,
}

func decisionMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /agents/memory-review", HandleStatus)
	for path, h := range decisionRoutes {
		mux.Handle("POST "+path, memberorigin.Require(DecideMaxRequest, h))
	}
	return mux
}

// bodyFor is a request that would do something if it were allowed to.
func bodyFor(path string, p Proposal) string {
	switch path {
	case "/agents/memory-review/proposals/reject-all", "/agents/memory-review/proposals/list":
		return `{}`
	case "/agents/memory-review/runs/revert":
		return `{"run":"` + p.RunID + `"}`
	}
	return `{"id":"` + p.ID + `"}`
}

func doPost(mux http.Handler, path, body string, sign func(*http.Request, []byte)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer a-sessions-agent-token")
	if sign != nil {
		sign(r, []byte(body))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// The authorisation of ADR 0117 decision 6 and of this phase: only the member approves, rejects or
// undoes. A session holds AGENT_TOKEN and can set the relay header; neither is a mark. Every way of
// not being the member is refused, and the queue and the store are exactly as they were.
func TestNoRouteDecidesWithoutAGenuineMemberMark(t *testing.T) {
	approvalEnv(t)
	member := memberorigintest.Install(t)
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	attacker := &memberorigintest.Signer{Priv: otherPriv, KID: memberorigintest.KID}

	seed := uprop(1, apRun1, OpCreate, "published", "v1", 0)
	queue(t, seed)
	if _, err := Approve(seed.ID, apNow); err != nil { // the run has something to undo
		t.Fatal(err)
	}
	pending := uprop(2, apRun1, OpCreate, "wanted", "SENTINEL-WANTED", 0)
	queue(t, pending)
	mux := decisionMux()

	unchanged := func(when string) {
		t.Helper()
		if got := Pending(); len(got) != 1 || got[0].ID != pending.ID || got[0].Stale != "" {
			t.Fatalf("%s: the queue changed: %+v", when, got)
		}
		if Rejected(pending.Hash) || target(t, "wanted").Exists || !target(t, "published").Exists {
			t.Fatalf("%s: the store or the rejected set changed", when)
		}
	}

	for path := range decisionRoutes {
		body := bodyFor(path, pending)
		other := body + " " // any other byte string hashes differently
		cases := map[string]func() *httptest.ResponseRecorder{
			"agent token only": func() *httptest.ResponseRecorder { return doPost(mux, path, body, nil) },
			"agent token and the relay hint": func() *httptest.ResponseRecorder {
				return doPost(mux, path, body, func(r *http.Request, _ []byte) { r.Header.Set("X-AF-Relay", "cp") })
			},
			"a mark signed with a key the Agent does not hold": func() *httptest.ResponseRecorder {
				return doPost(mux, path, body, attacker.Sign)
			},
			"a garbage mark": func() *httptest.ResponseRecorder {
				return doPost(mux, path, body, func(r *http.Request, _ []byte) { r.Header.Set(memberorigin.Header, "v1.e30.AAAA") })
			},
			"a member's mark for another body": func() *httptest.ResponseRecorder {
				return doPost(mux, path, body, func(r *http.Request, _ []byte) { member.Sign(r, []byte(other)) })
			},
			"a member's mark for another route": func() *httptest.ResponseRecorder {
				return doPost(mux, path, body, func(r *http.Request, b []byte) {
					r2 := httptest.NewRequest(http.MethodPost, "/agents/memory-review/run", nil)
					r.Header.Set(memberorigin.Header, member.Mark(memberorigintest.Claims(r2, b)))
				})
			},
		}
		for name, do := range cases {
			w := do()
			if w.Code != http.StatusForbidden {
				t.Errorf("%s / %s: status %d: %s", path, name, w.Code, w.Body)
			}
			if strings.Contains(w.Body.String(), "SENTINEL-WANTED") {
				t.Errorf("%s / %s: a refusal carries the proposal text", path, name)
			}
			unchanged(path + " / " + name)
		}

		// A mark that was used once is not good a second time (a session that saw the CP's
		// request on the wire cannot replay it).
		// The body names no valid id or run, so a request that gets past the mark does nothing.
		harmless := `{"id":"nothex","run":"nothex"}`
		var captured string
		w := doPost(mux, path, harmless, func(r *http.Request, b []byte) {
			member.Sign(r, b)
			captured = r.Header.Get(memberorigin.Header)
		})
		if w.Code == http.StatusForbidden {
			t.Fatalf("%s: a genuine mark was refused: %s", path, w.Body)
		}
		w = doPost(mux, path, harmless, func(r *http.Request, _ []byte) { r.Header.Set(memberorigin.Header, captured) })
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: a replayed mark was accepted: %d", path, w.Code)
		}
		unchanged(path + " / replay")
	}
}

// The handlers do not lean on the wrapper alone: reached without memberorigin.Require (a route
// registered without it, a future refactor) they still refuse. The mutation that deletes the
// RefuseUnverified calls turns this red.
func TestDecisionHandlersRefuseWithoutTheWrapper(t *testing.T) {
	approvalEnv(t)
	pending := uprop(1, apRun1, OpCreate, "wanted", "x", 0)
	queue(t, pending)
	for path, h := range decisionRoutes {
		body := bodyFor(path, pending)
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), memberorigin.CodeRequired) {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if len(Pending()) != 1 || target(t, "wanted").Exists {
		t.Fatal("an unmarked call decided something")
	}
}

type errBody struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func codeOf(w *httptest.ResponseRecorder) string {
	var e errBody
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	return e.Error.Code
}

// With the member's mark the routes do what they say, and say why when they cannot.
func TestDecisionRoutesWithTheMemberMark(t *testing.T) {
	approvalEnv(t)
	member := memberorigintest.Install(t)
	mux := decisionMux()

	ok := uprop(1, apRun1, OpCreate, "fine", "good", 0)
	rej := uprop(2, apRun1, OpCreate, "meh", "SENTINEL-HTTP-REJECT", 0)
	secret := uprop(3, apRun2, OpCreate, "keyed", "placeholder", 0)
	queue(t, ok, rej, secret)

	// The list: the queue, each with a diff; no secret anywhere.
	w := doPost(mux, "/agents/memory-review/proposals/list", `{}`, member.Sign)
	var list ProposalsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list.Proposals) != 3 || list.Proposals[0].Diff == "" {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}

	w = doPost(mux, "/agents/memory-review/proposals/approve", `{"id":"`+ok.ID+`"}`, member.Sign)
	var ap ApprovedResponse
	if err := json.Unmarshal(w.Body.Bytes(), &ap); err != nil || w.Code != 200 || ap.Commit == "" || !target(t, "fine").Exists {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	if w = doPost(mux, "/agents/memory-review/proposals/approve", `{"id":"`+ok.ID+`"}`, member.Sign); w.Code != 404 || codeOf(w) != "not_found" {
		t.Fatalf("approve again: %d %s", w.Code, w.Body)
	}
	if w = doPost(mux, "/agents/memory-review/proposals/approve", `{"id":"zz"}`, member.Sign); w.Code != 400 {
		t.Fatalf("bad id: %d %s", w.Code, w.Body)
	}

	// A stale proposal: 409 with its own code, still pending.
	if _, err := Approve(func() string {
		e := uprop(4, apRun1, OpCreate, "meh", "x", 0) // someone creates the name another proposal wanted
		queue(t, e)
		return e.ID
	}(), apNow); err != nil {
		t.Fatal(err)
	}
	// Reject the proposal that is now stale-by-name, via the route.
	if w = doPost(mux, "/agents/memory-review/proposals/approve", `{"id":"`+rej.ID+`"}`, member.Sign); w.Code != 409 || codeOf(w) != "proposal_stale" {
		t.Fatalf("stale: %d %s", w.Code, w.Body)
	}
	if w = doPost(mux, "/agents/memory-review/proposals/reject", `{"id":"`+rej.ID+`"}`, member.Sign); w.Code != 200 {
		t.Fatalf("reject: %d %s", w.Code, w.Body)
	}
	assertNoneContains(t, stateRoot(), "SENTINEL-HTTP-REJECT")

	if w = doPost(mux, "/agents/memory-review/proposals/reject-all", `{"run":"`+apRun2+`"}`, member.Sign); w.Code != 200 || !strings.Contains(w.Body.String(), `"rejected":1`) {
		t.Fatalf("reject-all: %d %s", w.Code, w.Body)
	}
	if w = doPost(mux, "/agents/memory-review/proposals/reject-all", `{"run":"not a run"}`, member.Sign); w.Code != 400 {
		t.Fatalf("reject-all bad run: %d %s", w.Code, w.Body)
	}

	w = doPost(mux, "/agents/memory-review/runs/revert", `{"run":"`+apRun1+`"}`, member.Sign)
	var rep struct {
		Items []struct{ Name, Result string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil || w.Code != 200 || len(rep.Items) == 0 {
		t.Fatalf("revert: %d %s", w.Code, w.Body)
	}
	if target(t, "fine").Exists {
		t.Fatal("the run was not undone")
	}
	if w = doPost(mux, "/agents/memory-review/runs/revert", `{"run":"x"}`, member.Sign); w.Code != 400 {
		t.Fatalf("revert bad run: %d", w.Code)
	}
}

// A proposal that carries a secret by the time it is approved (the scan's rules grew, or the file
// was edited by hand) is refused with its own code and not published.
func TestApproveRefusesASecretThatGotIntoTheQueue(t *testing.T) {
	approvalEnv(t)
	member := memberorigintest.Install(t)
	p := uprop(1, apRun1, OpCreate, "tampered", "clean at first", 0)
	queue(t, p)
	// The queue file is edited on disk after the scan (same uid as every session).
	q, _ := pendingStore.Read(p.ID)
	q.Op.Body = "now with " + fakeKey()
	if err := pendingStore.Write(p.ID, q); err != nil {
		t.Fatal(err)
	}
	w := doPost(decisionMux(), "/agents/memory-review/proposals/approve", `{"id":"`+p.ID+`"}`, member.Sign)
	if w.Code != 422 || codeOf(w) != "proposal_secret" {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), fakeKey()[:8]) || target(t, "tampered").Exists {
		t.Fatal("the secret was echoed or published")
	}
	if v := Proposals(); len(v) != 1 || v[0].State != StateStale || v[0].StaleReason != "secret" {
		t.Fatalf("view = %+v", v)
	}
}

// The count the Console shows is a number; the status carries no text of any proposal.
func TestStatusCountsPendingWithoutText(t *testing.T) {
	approvalEnv(t)
	queue(t, uprop(1, apRun1, OpCreate, "a", "SENTINEL-COUNT-TEXT", 0), uprop(2, apRun1, OpCreate, "b", "other", 0))
	w := httptest.NewRecorder()
	decisionMux().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/agents/memory-review", nil))
	var st StatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.Pending != 2 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "SENTINEL") || strings.Contains(w.Body.String(), "about a") {
		t.Fatalf("the status carries text: %s", w.Body)
	}
}

// The queue is text nobody has published: a session with only AGENT_TOKEN reads none of it, and the
// refusal itself carries none.
func TestTheQueueListIsNotReadableWithAgentTokenAlone(t *testing.T) {
	approvalEnv(t)
	memberorigintest.Install(t)
	queue(t, uprop(1, apRun1, OpCreate, "private", "SENTINEL-UNPUBLISHED", 0))
	mux := decisionMux()
	for _, r := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/agents/memory-review/proposals/list", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodGet, "/agents/memory-review/proposals", nil),
		httptest.NewRequest(http.MethodGet, "/agents/memory-review/proposals/list", nil),
	} {
		r.Header.Set("Authorization", "Bearer a-sessions-agent-token")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "SENTINEL-UNPUBLISHED") {
			t.Errorf("%s %s: %d %s", r.Method, r.URL.Path, w.Code, w.Body)
		}
	}
}
