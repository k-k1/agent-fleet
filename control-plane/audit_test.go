package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
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
		{"POST", "/api/sessions/s1/move", "s1", "session.move", "s1", true},
		{"POST", "/api/sessions/s1/move/retry", "s1", "session.move.retry", "s1", true},
		{"POST", "/api/sessions/s1/move/cancel", "s1", "session.move.cancel", "s1", true},
		{"POST", "/api/sessions/s1/move/reopen", "s1", "session.move.reopen", "s1", true},
		{"POST", "/api/sessions/s1/move/deliver", "s1", "session.move.deliver", "s1", true},
		{"GET", "/api/sessions/s1/move/preview", "s1", "", "", false},
		{"POST", "/api/sessions/s1/stop", "s1", "session.delete", "s1", true},
		{"DELETE", "/api/sessions/s1", "s1", "session.delete", "s1", true},
		{"DELETE", "/api/sessions/s1?stop=1", "s1", "session.delete", "s1", true},
		{"DELETE", "/api/sessions/s1/marks", "s1", "", "", false},
		// ADR 0102: the press and the cancel are audited with the Console's profile hint;
		// the polls are reads.
		{"POST", "/api/aws-login/0123456789abcdef01234567/start?profile=prod", "", "aws.login.start", "0123456789abcdef01234567 (profile hint: prod)", true},
		{"POST", "/api/aws-login/0123456789abcdef01234567/cancel?profile=prod", "", "aws.login.cancel", "0123456789abcdef01234567 (profile hint: prod)", true},
		{"GET", "/api/aws-login/0123456789abcdef01234567/attempts/abc", "", "", "", false},
		// #1028: the Settings row's press, keyed by the profile name in the path.
		{"POST", "/api/aws-login/profiles/prod/start", "prod", "aws.login.start", "profile: prod", true},
		{"GET", "/api/aws-login/profiles/prod/attempts/abc", "prod", "", "", false},
		{"POST", "/api/aws-login/profiles/prod/logout", "prod", "aws.logout", "profile: prod", true},
		{"GET", "/api/aws-login/profiles", "", "", "", false},
		{"GET", "/api/aws-runs", "", "", "", false},
		// ADR 0126 decision 5: the member's answer to a confirmation request is audited; the list
		// is a read.
		{"POST", "/api/aws-confirm/0123456789abcdef01234567/approve?profile=prod-admin&session=s1", "", "aws.use.approve",
			"0123456789abcdef01234567 (profile hint: prod-admin, session hint: s1)", true},
		{"POST", "/api/aws-confirm/0123456789abcdef01234567/deny?profile=prod-admin&session=s1", "", "aws.use.deny",
			"0123456789abcdef01234567 (profile hint: prod-admin, session hint: s1)", true},
		{"GET", "/api/aws-confirm", "", "", "", false},
		{"GET", "/api/aws-login", "", "", "", false},
		// ADR 0107 decision 3: the presses, the cancel and the code; the code route names the
		// attempt by its reference only. Polls and lists are reads.
		{"POST", "/api/gcp-login/0123456789abcdef01234567/start?profile=prod", "", "gcp.login.start", "0123456789abcdef01234567 (profile hint: prod)", true},
		{"POST", "/api/gcp-login/0123456789abcdef01234567/cancel?profile=prod", "", "gcp.login.cancel", "0123456789abcdef01234567 (profile hint: prod)", true},
		{"POST", "/api/gcp-login/profiles/prod/start", "prod", "gcp.login.start", "profile: prod", true},
		{"POST", "/api/gcp-login/profiles/prod/start?force=1", "prod", "gcp.login.start", "profile: prod (log in again)", true},
		{"POST", "/api/gcp-login/profiles/prod/logout", "prod", "gcp.logout", "profile: prod", true},
		{"POST", "/api/gcp-login/profiles/prod/attempts/abc/code", "prod", "gcp.login.code", "profile: prod, attempt: " + gcpAttemptRef("abc"), true},
		{"GET", "/api/gcp-login/profiles/prod/attempts/abc", "prod", "", "", false},
		{"GET", "/api/gcp-login/profiles", "", "", "", false},
		{"GET", "/api/gcp-login", "", "", "", false},
		// #2883: fs, git and session changes the relays used to leave unrecorded.
		{"POST", "/api/sessions/s1/archive", "s1", "session.archive", "s1", true},
		{"POST", "/api/sessions/s1/restore", "s1", "session.restore", "s1", true},
		{"POST", "/api/sessions/s1/recreate", "s1", "session.recreate", "s1", true},
		{"POST", "/api/sessions/s1/start", "s1", "session.start", "s1", true},
		{"POST", "/api/sessions/s1/rename-branch", "s1", "session.rename_branch", "s1", true},
		{"DELETE", "/api/cleanup/archives?older_than_days=30", "", "cleanup.archive.purge_old", "older_than_days: 30", true},
		{"DELETE", "/api/cleanup/archives?older_than_days=../../x", "", "cleanup.archive.purge_old", "", true},
		{"POST", "/api/repos/init", "", "repo.init", "", true},
		{"POST", "/api/repos/foo/recreate", "foo", "repo.recreate", "foo", true},
		{"POST", "/api/repos/foo/stage", "foo", "git.stage", "foo", true},
		{"POST", "/api/repos/foo/unstage", "foo", "git.unstage", "foo", true},
		{"DELETE", "/api/repos/foo/branch?branch=feature/x", "foo", "repo.branch.delete", "foo: feature/x", true},
		{"DELETE", "/api/repos/foo/branch?branch=a%20b%0Ac", "foo", "repo.branch.delete", "foo", true},
		{"POST", "/api/repos/foo/gitflow/init", "foo", "repo.gitflow.init", "foo", true},
		{"POST", "/api/repos/foo/mcp/apply", "foo", "repo.mcp.apply", "foo", true},
		// Not auditable (reads, non-change mutations, unlisted ops):
		{"GET", "/api/fs/file?path=a", "", "", "", false},
		{"GET", "/api/fs/tree", "", "", "", false},
		{"GET", "/api/sessions", "", "", "", false},
		{"POST", "/api/sessions/s1/input", "s1", "", "", false},
		{"GET", "/api/repos/foo/log", "foo", "", "", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.target, nil)
		if c.name != "" {
			r.SetPathValue("name", c.name)
		}
		if strings.Contains(c.target, "/attempts/abc") {
			r.SetPathValue("attempt", "abc")
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
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ins := func(tenant, action string) {
		if err := st.InsertAudit(ctx, store.AuditLog{
			ID: store.NewID(), TenantID: tenant, ActorKind: "user", ActorID: "id1",
			Action: action, HTTPStatus: http.StatusAccepted, At: store.NowTS(),
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
