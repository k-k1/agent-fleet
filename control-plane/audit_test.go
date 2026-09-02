package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// auditActionTarget must classify exactly the M1 change operations and nothing else.
func TestAuditActionTarget(t *testing.T) {
	cases := []struct {
		method, target      string // request line
		name                string // {name} path value ("" if none)
		wantAction, wantTgt string
		wantOK              bool
	}{
		{"DELETE", "/api/fs/delete?path=repos/foo/a.txt", "", "fs.delete", "repos/foo/a.txt", true},
		{"POST", "/api/fs/mkdir?path=repos/foo/dir", "", "fs.mkdir", "repos/foo/dir", true},
		{"POST", "/api/fs/newfile?path=a.txt", "", "fs.newfile", "a.txt", true},
		{"POST", "/api/fs/rename?from=a&to=b", "", "fs.rename", "a → b", true},
		{"POST", "/api/fs/upload?path=dir", "", "fs.upload", "dir", true},
		{"POST", "/api/repos", "", "repo.clone", "", true},
		{"DELETE", "/api/repos/foo", "foo", "repo.delete", "foo", true},
		{"POST", "/api/repos/foo/commit", "foo", "git.commit", "foo", true},
		{"POST", "/api/repos/foo/discard", "foo", "git.discard", "foo", true},
		{"POST", "/api/repos/foo/checkout", "foo", "git.checkout", "foo", true},
		{"POST", "/api/repos/foo/fetch", "foo", "git.fetch", "foo", true},
		{"POST", "/api/repos/foo/ff", "foo", "git.ff", "foo", true},
		{"POST", "/api/repos/foo/parent-ff", "foo", "git.parent_ff", "foo", true},
		{"POST", "/api/sessions", "", "session.create", "", true},
		{"POST", "/api/sessions/s1/fork", "s1", "session.fork", "s1", true},
		{"POST", "/api/sessions/s1/stop", "s1", "session.stop", "s1", true},
		// Not auditable (reads, non-change mutations, unlisted ops):
		{"GET", "/api/fs/file?path=a", "", "", "", false},
		{"GET", "/api/fs/tree", "", "", "", false},
		{"GET", "/api/sessions", "", "", "", false},
		{"POST", "/api/sessions/s1/input", "s1", "", "", false},
		{"POST", "/api/repos/foo/stage", "foo", "", "", false},
		{"GET", "/api/repos/foo/log", "foo", "", "", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.target, nil)
		if c.name != "" {
			r.SetPathValue("name", c.name)
		}
		a, tg, ok := auditActionTarget(r)
		if a != c.wantAction || tg != c.wantTgt || ok != c.wantOK {
			t.Errorf("%s %s: got (%q,%q,%v) want (%q,%q,%v)",
				c.method, c.target, a, tg, ok, c.wantAction, c.wantTgt, c.wantOK)
		}
	}
	put := httptest.NewRequest(http.MethodPut, "/api/fs/file", nil)
	put = put.WithContext(context.WithValue(put.Context(), fsPutAuditTargetContextKey{}, "repos/foo/a.txt"))
	if action, target, ok := auditActionTarget(put); !ok || action != "fs.file.put" || target != "repos/foo/a.txt" {
		t.Fatalf("PUT /api/fs/file: got (%q,%q,%v)", action, target, ok)
	}
}

// InsertAudit + ListAuditByTenant: "" spans every tenant, a set id scopes to it.
func TestAuditStoreScope(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ins := func(tenant, action string) {
		if err := st.InsertAudit(ctx, AuditLog{
			ID: newID(), TenantID: tenant, ActorKind: "user", ActorID: "id1",
			Action: action, HTTPStatus: http.StatusAccepted, At: nowTS(),
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	ins("t1", "fs.delete")
	ins("t2", "git.commit")
	ins("", "session.create") // deployment-wide

	all, err := st.ListAuditByTenant(ctx, "", 100)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("deployment-wide list: want 3 got %d", len(all))
	}
	t1, err := st.ListAuditByTenant(ctx, "t1", 100)
	if err != nil {
		t.Fatalf("list t1: %v", err)
	}
	if len(t1) != 1 || t1[0].Action != "fs.delete" {
		t.Fatalf("tenant scope: want [fs.delete] got %+v", t1)
	}
	if t1[0].HTTPStatus != http.StatusAccepted {
		t.Fatalf("audit http_status=%d want=%d", t1[0].HTTPStatus, http.StatusAccepted)
	}
}
