package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Issue #1200: deleting an internal repository takes its bare and LFS objects from everyone
// in the tenant, so delete and rename belong to the repository's creator and to a
// tenant_admin, and creating one needs a role that may push.

// addMember adds email to the p2Env tenant with role and returns the membership id.
func (e *p2Env) addMember(t *testing.T, email, role string) string {
	t.Helper()
	ctx := context.Background()
	ident, err := e.st.UpsertIdentity(ctx, email, strings.NewReplacer("@", "-", ".", "-").Replace(email), "")
	if err != nil {
		t.Fatal(err)
	}
	mem, err := e.st.EnsureMembership(ctx, ident.ID, e.tenantID, role)
	if err != nil {
		t.Fatal(err)
	}
	return mem.ID
}

func (e *p2Env) callAs(email, method, path, name string, body any, h func(http.ResponseWriter, *http.Request, store.Identity, store.MembershipView)) *httptest.ResponseRecorder {
	w, r := e.req(method, path, body)
	r.Header.Set("X-Forwarded-Email", email)
	if name != "" {
		r.SetPathValue("name", name)
	}
	e.g.withMembership(h)(w, r)
	return w
}

func TestInternalGitDeleteAndRenameNeedTheCreatorOrATenantAdmin(t *testing.T) {
	ctx := context.Background()
	e := newP2Env(t) // u@x is a member and the creator of every seeded repo
	e.addMember(t, "other@x", "member")
	e.addMember(t, "admin@x", "tenant_admin")
	e.seedRepo(t, "theirs")
	dir := filepath.Join(e.g.dataRoot, "git", "default", "theirs.git")
	rename := func(email, from, to string) *httptest.ResponseRecorder {
		return e.callAs(email, http.MethodPost, "/api/internal-git/repos/"+from+"/rename", from, map[string]string{"new_name": to}, e.g.repoRename)
	}
	del := func(email, name string) *httptest.ResponseRecorder {
		return e.callAs(email, http.MethodDelete, "/api/internal-git/repos/"+name, name, nil, e.g.repoDelete)
	}

	for what, w := range map[string]*httptest.ResponseRecorder{
		"rename": rename("other@x", "theirs", "mine"),
		"delete": del("other@x", "theirs"),
	} {
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"`+errCodeGitRepoManageForbidden+`"`) {
			t.Errorf("%s by a member who did not create it = %d %s, want 403 %s", what, w.Code, w.Body.String(), errCodeGitRepoManageForbidden)
		}
	}
	if _, ok, _ := e.st.GetGitRepo(ctx, e.tenantID, "theirs"); !ok {
		t.Fatal("the repo row is gone after the refusals")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the bare is gone after the refusals: %v", err)
	}
	if logs, _ := e.st.ListAuditByTenant(ctx, e.tenantID, 10); len(logs) != 0 {
		t.Errorf("a refused request left audit rows %+v; the role check comes before the intent", logs)
	}

	// The list tells each caller which rows they may manage.
	for email, want := range map[string]bool{"u@x": true, "other@x": false, "admin@x": true} {
		w := e.callAs(email, http.MethodGet, "/api/internal-git/repos", "", nil, e.g.reposList)
		if got := strings.Contains(w.Body.String(), `"can_manage":true`); got != want {
			t.Errorf("list for %s: can_manage=%v, want %v (%s)", email, got, want, w.Body.String())
		}
	}

	if w := rename("u@x", "theirs", "renamed"); w.Code != http.StatusOK {
		t.Fatalf("rename by the creator = %d %s, want 200", w.Code, w.Body.String())
	}
	if w := rename("admin@x", "renamed", "again"); w.Code != http.StatusOK {
		t.Fatalf("rename by a tenant_admin = %d %s, want 200", w.Code, w.Body.String())
	}
	if w := del("admin@x", "again"); w.Code != http.StatusOK {
		t.Fatalf("delete by a tenant_admin = %d %s, want 200", w.Code, w.Body.String())
	}
	e.seedRepo(t, "own")
	if w := del("u@x", "own"); w.Code != http.StatusOK {
		t.Fatalf("delete by the creator = %d %s, want 200", w.Code, w.Body.String())
	}
}

// A role that may not push may not create, rename or delete either, even as the creator.
func TestInternalGitRoleWithoutPushCannotManageRepos(t *testing.T) {
	ctx := context.Background()
	e := newP2Env(t)
	readerID := e.addMember(t, "reader@x", "viewer")
	if canPush("viewer") {
		t.Fatal("the fixture's read-only role may push; pick another")
	}
	w := e.callAs("reader@x", http.MethodPost, "/api/internal-git/repos", "", map[string]string{"name": "nope"}, e.g.repoCreate)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"`+errCodeGitRepoCreateForbidden+`"`) {
		t.Fatalf("create by a role without push = %d %s, want 403 %s", w.Code, w.Body.String(), errCodeGitRepoCreateForbidden)
	}
	if _, ok, _ := e.st.GetGitRepo(ctx, e.tenantID, "nope"); ok {
		t.Fatal("the refused create left a repo row")
	}
	if _, err := os.Stat(filepath.Join(e.g.dataRoot, "git", "default", "nope.git")); !os.IsNotExist(err) {
		t.Fatalf("the refused create left a bare: %v", err)
	}

	// Recorded as its creator (say, from before a demotion): still refused.
	if err := e.st.CreateGitRepo(ctx, store.GitRepo{ID: store.NewID(), TenantID: e.tenantID, Name: "old",
		DefaultBranch: "main", CreatedBy: readerID, CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	if w := e.callAs("reader@x", http.MethodDelete, "/api/internal-git/repos/old", "old", nil, e.g.repoDelete); w.Code != http.StatusForbidden {
		t.Fatalf("delete by a creator whose role may not push = %d %s, want 403", w.Code, w.Body.String())
	}
}
