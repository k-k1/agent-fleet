package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestEffectiveSharePermission(t *testing.T) {
	c := SharedSessionCatalog{OwnerMembershipID: "owner", Name: "s1", WorkingCopyID: "wc1"}
	shares := []SessionShare{
		{OwnerMembershipID: "owner", ScopeType: "session", ScopeKey: "s1", Permission: "ro"},
		{OwnerMembershipID: "owner", ScopeType: "worktree", ScopeKey: "wc1", Permission: "rw"},
		{OwnerMembershipID: "other", ScopeType: "session", ScopeKey: "s1", Permission: "rw"},
	}
	if got := effectivePermission(shares, c); got != "rw" {
		t.Fatalf("permission=%q", got)
	}
	if got := effectivePermission(shares[:1], c); got != "ro" {
		t.Fatalf("permission=%q", got)
	}
	if got := effectivePermission(shares[2:], c); got != "" {
		t.Fatalf("foreign owner permission=%q", got)
	}
}

func TestStripSharedPrivateFields(t *testing.T) {
	v := map[string]any{"jsonlPath": "/secret/log", "messages": []any{map[string]any{
		"cwd": "/home/dev/repos/private", "text": "visible", "parts": []any{map[string]any{"kind": "tool", "file": "secret.txt", "info": "edited"}},
	}}}
	stripSharedPrivateFields(v)
	if _, ok := v["jsonlPath"]; ok {
		t.Fatal("jsonlPath survived")
	}
	msg := v["messages"].([]any)[0].(map[string]any)
	if _, ok := msg["cwd"]; ok {
		t.Fatal("cwd survived")
	}
	part := msg["parts"].([]any)[0].(map[string]any)
	if _, ok := part["file"]; ok {
		t.Fatal("file survived")
	}
	if msg["text"] != "visible" || part["info"] != "edited" {
		t.Fatalf("visible fields changed: %#v", v)
	}
}

func TestSharedMessagesAuthorizeAndRemoveWorkspacePaths(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ownerIdentity, _ := st.UpsertIdentity(ctx, "owner@example.com", "owner", "")
	recipientIdentity, _ := st.UpsertIdentity(ctx, "recipient@example.com", "recipient", "")
	strangerIdentity, _ := st.UpsertIdentity(ctx, "stranger@example.com", "stranger", "")
	owner, _ := st.EnsureMembership(ctx, ownerIdentity.ID, tenant.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, recipientIdentity.ID, tenant.ID, "member")
	_, _ = st.EnsureMembership(ctx, strangerIdentity.ID, tenant.ID, "member")
	recipientViews, _ := st.ListMemberships(ctx, recipientIdentity.ID)
	strangerViews, _ := st.ListMemberships(ctx, strangerIdentity.ID)
	workspace := Workspace{ID: "ws-owner", TenantID: tenant.ID, MembershipID: owner.ID,
		ContainerName: "owner", Network: "test", DataDir: "/data/owner", AgentPort: "1", AgentToken: "tok",
		State: "running", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}

	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/session-1/messages" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonlPath": "/home/dev/.claude/session.jsonl",
			"messages": []any{map[string]any{
				"text": "visible", "cwd": "/home/dev/repos/private",
				"parts": []any{map[string]any{"info": "edited", "filePath": "/home/dev/repos/private/secret.go"}},
			}},
		})
	}))
	defer agent.Close()

	mgr := &manager{store: st, rts: map[string]cachedRT{owner.ID: {
		rt: stubRuntime{endpoint: agent.URL, token: "tok"}, ws: workspace,
	}}}
	api := newSessionShareAPI(mgr)
	catalog := SharedSessionCatalog{ID: "catalog-1", WorkspaceID: workspace.ID, OwnerMembershipID: owner.ID,
		Name: "session-1", Kind: "codex", WorkingCopyID: "wc-1", LastSeen: nowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, workspace.ID, owner.ID, []SharedSessionCatalog{catalog}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSessionShare(ctx, SessionShare{ID: "share-1", TenantID: tenant.ID, OwnerMembershipID: owner.ID,
		RecipientMembershipID: recipient.ID, ScopeType: "session", ScopeKey: "session-1", Permission: "ro",
		CreatedAt: nowTS(), UpdatedAt: nowTS()}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/shared-sessions/catalog-1/messages", nil)
	req.SetPathValue("id", catalog.ID)
	rec := httptest.NewRecorder()
	api.messages(rec, req, recipientIdentity, recipientViews[0])
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["jsonlPath"]; ok {
		t.Fatal("jsonlPath survived")
	}
	message := payload["messages"].([]any)[0].(map[string]any)
	if _, ok := message["cwd"]; ok {
		t.Fatal("cwd survived")
	}
	part := message["parts"].([]any)[0].(map[string]any)
	if _, ok := part["filePath"]; ok {
		t.Fatal("filePath survived")
	}

	deniedReq := httptest.NewRequest(http.MethodGet, "/api/shared-sessions/catalog-1/messages", nil)
	deniedReq.SetPathValue("id", catalog.ID)
	denied := httptest.NewRecorder()
	api.messages(denied, deniedReq, strangerIdentity, strangerViews[0])
	if denied.Code != http.StatusNotFound {
		t.Fatalf("unauthorized status=%d body=%s", denied.Code, denied.Body.String())
	}
}
