package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestNormalizeProposalPayload(t *testing.T) {
	ok := []struct{ action, in, want string }{
		{"turn", `{"op":"start","prompt":"hi"}`, `{"op":"start","prompt":"hi"}`},
		{"respond", `{"id":"i","decision":"allow","scope":"turn"}`, `{"id":"i","decision":"allow","scope":"turn"}`},
		{"answer-question", `{"choices":[1,2]}`, `{"choices":[1,2]}`},
		{"plan-respond", `{"decision":"reject","feedback":"no"}`, `{"decision":"reject","feedback":"no"}`},
	}
	for _, c := range ok {
		got, err := normalizeProposalPayload(c.action, []byte(c.in))
		if err != nil || string(got) != c.want {
			t.Errorf("%s %s: got %s, %v", c.action, c.in, got, err)
		}
	}
	bad := []struct{ action, in string }{
		{"turn", `{"op":"start","prompt":"hi","attachments":["/home/dev/.ssh/id"]}`},
		{"turn", `{"op":"start","prompt":"hi","attachments":[]}`},
		{"turn", `{"op":"start","prompt":"hi","discard_queue":true}`},
		{"turn", `{"op":"interrupt","prompt":"hi"}`},
		{"turn", `{"op":"start","prompt":"  "}`},
		{"turn", `[]`},
		{"respond", `{"id":"i","decision":"allow","scope":"forever"}`},
		{"plan-respond", `{"decision":"approve","expect_plan":"p"}`},
		{"answer-question", `{"choices":[]}`},
	}
	for _, c := range bad {
		if got, err := normalizeProposalPayload(c.action, []byte(c.in)); err == nil {
			t.Errorf("%s %s accepted as %s", c.action, c.in, got)
		}
	}
}

func TestProposeAllowListsPayloadAndRefusesTerminalSessions(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ownerIdentity, _ := st.UpsertIdentity(ctx, "owner@example.com", "owner", "")
	recipientIdentity, _ := st.UpsertIdentity(ctx, "recipient@example.com", "recipient", "")
	owner, _ := st.EnsureMembership(ctx, ownerIdentity.ID, tenant.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, recipientIdentity.ID, tenant.ID, "member")
	recipientViews, _ := st.ListMemberships(ctx, recipientIdentity.ID)
	ownerViews, _ := st.ListMemberships(ctx, ownerIdentity.ID)
	workspace := store.Workspace{ID: "ws-owner", TenantID: tenant.ID, MembershipID: owner.ID,
		ContainerName: "owner", Network: "test", DataDir: "/data/owner", AgentPort: "1", AgentToken: "tok",
		State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	mgr := &manager{store: st, rts: map[string]cachedRT{owner.ID: {rt: stubRuntime{endpoint: "http://127.0.0.1:1", token: "tok"}, ws: workspace}}}
	api := newSessionShareAPI(mgr)
	api.syncedAt[owner.ID] = time.Now() // the inventory is not what is under test
	catalogs := []store.SharedSessionCatalog{
		{ID: "cat-agent", WorkspaceID: workspace.ID, OwnerMembershipID: owner.ID, Name: "s-agent", Kind: "codex", WorkingCopyID: "wc", LastSeen: store.NowTS()},
		{ID: "cat-shell", WorkspaceID: workspace.ID, OwnerMembershipID: owner.ID, Name: "s-shell", Kind: "shell", WorkingCopyID: "wc", LastSeen: store.NowTS()},
	}
	if err := st.ReplaceSharedSessionCatalog(ctx, workspace.ID, owner.ID, catalogs); err != nil {
		t.Fatal(err)
	}
	for _, c := range catalogs {
		if err := st.PutSessionShare(ctx, store.SessionShare{ID: "share-" + c.ID, TenantID: tenant.ID, OwnerMembershipID: owner.ID,
			RecipientMembershipID: recipient.ID, ScopeType: "session", ScopeKey: c.Name, Permission: "rw",
			CreatedAt: store.NowTS(), UpdatedAt: store.NowTS()}); err != nil {
			t.Fatal(err)
		}
	}
	post := func(id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/shared-sessions/"+id+"/proposals", strings.NewReader(body))
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		api.propose(rec, req, recipientIdentity, recipientViews[0])
		return rec
	}
	if rec := post("cat-agent", `{"action":"turn","payload":{"op":"start","prompt":"p","attachments":["/home/dev/.aws/credentials"]}}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "proposal_attachments_refused") {
		t.Fatalf("attachments: %d %s", rec.Code, rec.Body)
	}
	if rec := post("cat-agent", `{"action":"turn","payload":{"op":"start","prompt":"p","extra":1}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d %s", rec.Code, rec.Body)
	}
	if rec := post("cat-shell", `{"action":"turn","payload":{"op":"start","prompt":"ls"}}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "proposal_session_refused") {
		t.Fatalf("shell: %d %s", rec.Code, rec.Body)
	}
	if rec := post("cat-agent", `{"action":"turn","payload":{"op":"start","prompt":"hello"}}`); rec.Code != http.StatusCreated {
		t.Fatalf("plain prompt: %d %s", rec.Code, rec.Body)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/session-share-proposals", nil)
	rec := httptest.NewRecorder()
	api.listProposals(rec, req, ownerIdentity, ownerViews[0])
	var out struct {
		Proposals []map[string]any `json:"proposals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Proposals) != 1 {
		t.Fatalf("list: %v %s", err, rec.Body)
	}
	p := out.Proposals[0]
	if p["sessionName"] != "s-agent" || p["proposerEmail"] != "recipient@example.com" || p["refused"] != nil {
		t.Fatalf("card fields: %v", p)
	}
}
