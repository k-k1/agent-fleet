package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// docsTestEnv wires an in-memory store plus the docs bridge over a miniature docs tree.
type docsTestEnv struct {
	a       docsAPI
	st      *sqlStore
	signKey []byte
}

func newDocsTestEnv(t *testing.T) *docsTestEnv {
	t.Helper()
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	master := []byte("master-key-for-docs-bridge-tests0")
	t.Setenv("AF_DOCS_DIR", buildDocsSrc(t))
	return &docsTestEnv{
		a:       newDocsAPI(&manager{store: st, master32: master, dataRoot: t.TempDir()}),
		st:      st,
		signKey: docsSignKey(master),
	}
}

// addMembership mirrors the git bridge's helper: identity + tenant + membership row.
func (e *docsTestEnv) addMembership(t *testing.T, tenantSlug, role string) string {
	t.Helper()
	ctx := context.Background()
	tn, err := e.st.CreateTenant(ctx, tenantSlug, tenantSlug)
	if err != nil {
		got, ok, gerr := e.st.GetTenantBySlug(ctx, tenantSlug)
		if gerr != nil || !ok {
			t.Fatalf("tenant %s: %v / %v", tenantSlug, err, gerr)
		}
		tn = got
	}
	ident, err := e.st.UpsertIdentity(ctx, tenantSlug+"-"+role+"@x", "key-"+tenantSlug+"-"+role, "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	mid := newID()
	if _, err := e.st.db.ExecContext(ctx,
		`INSERT INTO membership(id, identity_id, tenant_id, role, status, created_at) VALUES(?,?,?,?, 'active', ?)`,
		mid, ident.ID, tn.ID, role, nowTS()); err != nil {
		t.Fatalf("membership: %v", err)
	}
	return mid
}

func (e *docsTestEnv) do(token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/internal/docs", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.a.download(w, r)
	return w
}

// tarNames lists the file paths in a gzipped tar body.
func tarNames(t *testing.T, body []byte) map[string]bool {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	names := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		names[hdr.Name] = true
	}
	return names
}

// The bridge must hand a container exactly what the bind-mount would have staged for
// that role — the leak this whole mechanism exists to prevent is a member pulling the
// internal decision/history docs.
func TestDocsBridgeRoleScoping(t *testing.T) {
	e := newDocsTestEnv(t)
	cases := []struct {
		role       string
		wantHave   []string
		wantAbsent []string
	}{
		{
			role:     "member",
			wantHave: []string{"use/02-sessions.md", "ref/agents.md"},
			wantAbsent: []string{
				"admin/02-limits.md", "operate/01-install.md", "build/04-workspace-agent.md",
				"dev/04-workspace-agent.md",
				"decisions/0011-console.md", "log/p3-10.md",
			},
		},
		{
			role:     "tenant_admin",
			wantHave: []string{"use/02-sessions.md", "admin/02-limits.md"},
			wantAbsent: []string{
				"operate/01-install.md", "build/04-workspace-agent.md",
				"decisions/0011-console.md", "log/p3-10.md",
			},
		},
		{
			role:     "super_admin",
			wantHave: []string{"use/02-sessions.md", "operate/01-install.md", "build/04-workspace-agent.md"},
			// Even the highest role is an allowlist: the decision records and the
			// frozen journals are served to nobody.
			wantAbsent: []string{
				"decisions/0011-console.md", "log/p3-10.md", "dev/04-workspace-agent.md",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.role, func(t *testing.T) {
			mid := e.addMembership(t, "t-"+c.role, c.role)
			w := e.do(mintDocsToken(e.signKey, mid))
			if w.Code != http.StatusOK {
				t.Fatalf("want 200 got %d (%s)", w.Code, w.Body.String())
			}
			names := tarNames(t, w.Body.Bytes())
			for _, f := range c.wantHave {
				if !names[f] {
					t.Errorf("role %s: expected %s present, got %v", c.role, f, names)
				}
			}
			for _, f := range c.wantAbsent {
				if names[f] {
					t.Errorf("role %s: LEAK — %s must not be served, got %v", c.role, f, names)
				}
			}
		})
	}
}

func TestDocsBridgeAuth(t *testing.T) {
	e := newDocsTestEnv(t)
	mid := e.addMembership(t, "default", "member")
	tok := mintDocsToken(e.signKey, mid)

	if w := e.do(""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: want 401 got %d", w.Code)
	}
	if w := e.do("afd_bogus.tag"); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: want 401 got %d", w.Code)
	}
	// A token minted with someone else's key must not pass (tag is an HMAC, not an id).
	if w := e.do(mintDocsToken(docsSignKey([]byte("some-other-master-key-000000000x")), mid)); w.Code != http.StatusUnauthorized {
		t.Fatalf("foreign key: want 401 got %d", w.Code)
	}
	if w := e.do(tok); w.Code != http.StatusOK {
		t.Fatalf("valid token: want 200 got %d", w.Code)
	}
	// Deactivating the membership stops the same deterministic token immediately —
	// role/scope is resolved live, never carried in the token.
	if _, err := e.st.db.ExecContext(context.Background(),
		`UPDATE membership SET status='disabled' WHERE id=?`, mid); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if w := e.do(tok); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked: want 401 got %d", w.Code)
	}
}

// Which adapters stage <dataDir>/docs is a fact the start path reads off the type. If an
// ECS adapter ever claimed the marker it would copy megabytes onto the CP's disk that no
// task can read; if docker/native lost it, their bind mount would go empty.
func TestDocsMounterMarker(t *testing.T) {
	var _ runtimeDocsMounter = (*dockerRuntime)(nil)
	var _ runtimeDocsMounter = (*nativeRuntime)(nil)
	if _, ok := any((*ecsRuntime)(nil)).(runtimeDocsMounter); ok {
		t.Error("ecsRuntime has no host seam to mount from — it must not claim staged docs")
	}
	if _, ok := any((*ecsEC2Runtime)(nil)).(runtimeDocsMounter); ok {
		t.Error("ecsEC2Runtime has no host seam to mount from — it must not claim staged docs")
	}
}

// A deployment with no baked docs must say so, rather than serving an empty archive the
// agent would happily "install" (leaving the guide broken with no trace of why).
func TestDocsBridgeNoBakedDocs(t *testing.T) {
	e := newDocsTestEnv(t)
	mid := e.addMembership(t, "default", "member")
	t.Setenv("AF_DOCS_DIR", filepath.Join(t.TempDir(), "does-not-exist"))
	w := e.do(mintDocsToken(e.signKey, mid))
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d (%s)", w.Code, w.Body.String())
	}
}
