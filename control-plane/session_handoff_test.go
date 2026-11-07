package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// handoffAPIFixture is one owner, one candidate recipient and one Agent stub.
type handoffAPIFixture struct {
	st        *store.SQL
	api       sessionHandoffAPI
	res       *resolved
	owner     store.Membership
	recipient store.Membership
	tenant    store.Tenant
	agentHits *pathHits
}

// pathHits counts Agent requests per path. The catalog sync reads /sessions/catalog and
// /repos concurrently, so the stub's handler runs on two goroutines at once.
type pathHits struct {
	mu sync.Mutex
	n  map[string]int
}

func (h *pathHits) add(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.n[path]++
}

func (h *pathHits) get(path string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.n[path]
}

func newHandoffAPIFixture(t *testing.T, sessions []map[string]any) handoffAPIFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	oi, _ := st.UpsertIdentity(ctx, "owner@example.com", "owner", "")
	ri, _ := st.UpsertIdentity(ctx, "recipient@example.com", "recipient", "")
	owner, _ := st.EnsureMembership(ctx, oi.ID, tenant.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, ri.ID, tenant.ID, "member")
	ownerViews, _ := st.ListMemberships(ctx, oi.ID)
	ws := store.Workspace{ID: "ws-owner", TenantID: tenant.ID, MembershipID: owner.ID, ContainerName: "owner",
		Network: "test", DataDir: "/data/owner", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	hits := &pathHits{n: map[string]int{}}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.add(r.URL.Path)
		switch r.URL.Path {
		case "/sessions/catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": sessions})
		case "/repos":
			_ = json.NewEncoder(w).Encode(map[string]any{"repos": []any{map[string]any{"workingCopyId": "wc-1"}}})
		case "/sessions/session-1/handoff-context":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"repo": "private", "vcs": "git", "branch": "main", "remote": "https://example.com/x.git",
				"headSha": "abcdef1234567890", "ahead": 0, "dirty": false,
			})
		default:
			// Answer in the shape a real Agent uses (a JSON error body). Plain
			// http.NotFound returns HTML, which would only ever exercise the CP's
			// "the upstream reply is malformed" path.
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found", "message": "no such session"}})
		}
	}))
	t.Cleanup(agent.Close)
	mgr := &manager{store: st, rts: map[string]cachedRT{owner.ID: {
		rt: stubRuntime{endpoint: agent.URL, token: "tok"}, ws: ws,
	}}}
	share := newSessionShareAPI(mgr)
	return handoffAPIFixture{
		st: st, api: newSessionHandoffAPI(mgr, share, false), owner: owner, recipient: recipient, tenant: tenant,
		agentHits: hits,
		res:       &resolved{ident: oi, mv: ownerViews[0], ws: ws, rt: stubRuntime{endpoint: agent.URL, token: "tok"}},
	}
}

func (f handoffAPIFixture) getRecipients(t *testing.T, session string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+session+"/handoff-recipients", nil)
	req.SetPathValue("name", session)
	rec := httptest.NewRecorder()
	f.api.recipients(rec, req, f.res)
	return rec
}

var oneSession = []map[string]any{{
	"name": "session-1", "kind": "claude", "dir": "/home/dev/repos/private", "repo": "private", "workingCopyId": "wc-1",
}}

// TestHandoffRecipientsUnsharedSessionAnswers: a session shared with nobody must not
// leave the screen waiting.
//
// With no share at all there is no `shared_session_catalog` row, and answering 404 from
// `catalogForOwnedSession` makes "no candidate recipients" indistinguishable from "no
// such session" in the UI — which reads as a load that never finishes. Being unshared is
// a NORMAL state, so the answer is 200 with an empty candidate list, and the UI says
// "share it first".
func TestHandoffRecipientsUnsharedSessionAnswers(t *testing.T) {
	f := newHandoffAPIFixture(t, oneSession)
	rec := f.getRecipients(t, "session-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s - being unshared is a normal state, so the answer must be 200", rec.Code, rec.Body.String())
	}
	var got struct {
		Members []map[string]string `json:"members"`
		Context map[string]any      `json:"context"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body=%s err=%v", rec.Body.String(), err)
	}
	if len(got.Members) != 0 {
		t.Fatalf("members=%v, want empty", got.Members)
	}
	// The coordinates come back even when nothing is shared: the push gate applies
	// independently of sharing.
	if got.Context["branch"] != "main" {
		t.Fatalf("context=%v, want the working copy's coordinates", got.Context)
	}
}

// A session that does not exist stays a 404 — never conflated with "not shared".
func TestHandoffRecipientsUnknownSessionIs404(t *testing.T) {
	f := newHandoffAPIFixture(t, oneSession)
	if rec := f.getRecipients(t, "no-such-session"); rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandoffRecipientsListsSharedMembers(t *testing.T) {
	ctx := context.Background()
	f := newHandoffAPIFixture(t, oneSession)
	// Put a share rule in place, then sync: the catalog row is created by the sync.
	if err := f.st.PutSessionShare(ctx, store.SessionShare{ID: "share-1", TenantID: f.tenant.ID,
		OwnerMembershipID: f.owner.ID, RecipientMembershipID: f.recipient.ID, ScopeType: "session",
		ScopeKey: "session-1", Permission: "ro", CreatedAt: store.NowTS(), UpdatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	rec := f.getRecipients(t, "session-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "recipient@example.com") {
		t.Fatalf("the RO share recipient is missing from the candidate list: %s", rec.Body.String())
	}
}

// TestHandoffRecipientsThrottlesInventorySync: READING the recipient list must not run
// the owner Workspace's inventory sync every time.
//
// `/repos` runs git once per working copy, so it gets heavier with every worktree.
// Running it on each opening of the modal leaves the modal on "loading" for seconds —
// the same reason shared reads are thinned through `freshCatalog`. Only the moment an
// offer is made re-reads it exactly.
func TestHandoffRecipientsThrottlesInventorySync(t *testing.T) {
	f := newHandoffAPIFixture(t, oneSession)
	if rec := f.getRecipients(t, "session-1"); rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	first := f.agentHits.get("/repos")
	if first == 0 {
		t.Fatal("the very first call must sync")
	}
	for i := 0; i < 3; i++ {
		if rec := f.getRecipients(t, "session-1"); rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
	}
	if got := f.agentHits.get("/repos"); got != first {
		t.Fatalf("/repos hits=%d (first=%d) - a re-read within the TTL is running the inventory sync", got, first)
	}
}

// The modal opens on the recipient list alone: ?context=skip must not make the owner's Agent
// run the git status behind the verdict, and the verdict is read from its own route.
func TestHandoffRecipientsContextSkipDoesNotAskTheAgentForTheVerdict(t *testing.T) {
	f := newHandoffAPIFixture(t, oneSession)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/session-1/handoff-recipients?context=skip", nil)
	req.SetPathValue("name", "session-1")
	rec := httptest.NewRecorder()
	f.api.recipients(rec, req, f.res)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, has := got["context"]; has {
		t.Fatalf("body has a context although it was skipped: %s", rec.Body.String())
	}
	if _, has := got["members"]; !has {
		t.Fatalf("body has no members: %s", rec.Body.String())
	}
	if n := f.agentHits.get("/sessions/session-1/handoff-context"); n != 0 {
		t.Fatalf("the Agent was asked for the verdict %d times", n)
	}
}

func TestHandoffContextRouteRelaysTheAgentsVerdict(t *testing.T) {
	f := newHandoffAPIFixture(t, oneSession)
	get := func(name string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+name+"/handoff-context", nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		f.api.handoffContextGET(rec, req, f.res)
		return rec
	}
	rec := get("session-1")
	var got struct {
		Context map[string]any `json:"context"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Context["branch"] != "main" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// "No such session" stays a 404, never "no working copy" and never an empty verdict.
	if rec := get("no-such-session"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// The create handler signs a member-origin mark for pushFirst, so it refuses a request a page other
// than the Console could have made before reading the body or touching the Agent. The handler is
// called directly, so the global write gate is out of the picture.
func TestHandoffCreateRefusesCrossOriginRequests(t *testing.T) {
	f := newHandoffAPIFixture(t, oneSession)
	const body = `{"sessionName":"session-1","title":"t","prompt":"p","recipientMembershipId":"x"}`
	send := func(h map[string]string) int {
		req := httptest.NewRequest(http.MethodPost, "https://console.example/api/session-handoff-offers", strings.NewReader(body))
		for k, v := range h {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		f.api.create(rec, req, f.res)
		return rec.Code
	}
	for name, c := range map[string]struct {
		h    map[string]string
		want int
	}{
		"text/plain no-cors from a preview host": {map[string]string{"Content-Type": "text/plain;charset=UTF-8",
			"Origin": "https://p-5173.preview.example", "Sec-Fetch-Site": "same-site"}, http.StatusUnsupportedMediaType},
		"no content type":            {map[string]string{}, http.StatusUnsupportedMediaType},
		"JSON from a preview host":   {map[string]string{"Content-Type": "application/json", "Origin": "https://p-5173.preview.example"}, http.StatusForbidden},
		"JSON, same-site metadata":   {map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		"JSON, cross-site metadata":  {map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		"JSON from an opaque origin": {map[string]string{"Content-Type": "application/json", "Origin": "null"}, http.StatusForbidden},
	} {
		if got := send(c.h); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	if n := f.agentHits.get("/sessions/catalog") + f.agentHits.get("/repos"); n != 0 {
		t.Fatalf("a refused request reached the Agent %d times", n)
	}
	// The Console's own request passes the check (it may fail later for other reasons).
	req := httptest.NewRequest(http.MethodPost, "https://console.example/api/session-handoff-offers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Origin", "https://console.example")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	f.api.create(rec, req, f.res)
	if rec.Code == http.StatusUnsupportedMediaType || strings.Contains(rec.Body.String(), "own origin") {
		t.Fatalf("Console request refused by the origin check: %d %s", rec.Code, rec.Body.String())
	}
}
