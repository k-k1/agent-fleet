package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	agentHits map[string]int
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
	hits := map[string]int{}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
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
		st: st, api: newSessionHandoffAPI(mgr, share), owner: owner, recipient: recipient, tenant: tenant,
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
	first := f.agentHits["/repos"]
	if first == 0 {
		t.Fatal("the very first call must sync")
	}
	for i := 0; i < 3; i++ {
		if rec := f.getRecipients(t, "session-1"); rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
	}
	if got := f.agentHits["/repos"]; got != first {
		t.Fatalf("/repos hits=%d (first=%d) - a re-read within the TTL is running the inventory sync", got, first)
	}
}
