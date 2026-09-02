package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// p2Env is a gitServerAPI + store wired for the CP-native management handlers,
// with a member of the default tenant resolvable via proxy-auth headers.
type p2Env struct {
	g        gitServerAPI
	st       *store.SQL
	tenantID string
}

func newP2Env(t *testing.T) *p2Env {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dflt, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "u@x", "u-x", "")
	if _, err := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	return &p2Env{
		st:       st,
		tenantID: dflt.ID,
		g: newGitServerAPI(&manager{store: st, authMode: "proxy", emailHeader: "X-Forwarded-Email", dataRoot: t.TempDir()},
			"https://fleet.example.com"),
	}
}

func (e *p2Env) req(method, path string, body any) (*httptest.ResponseRecorder, *http.Request) {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("X-Forwarded-Email", "u@x")
	r.Header.Set("X-AF-Tenant", "default")
	return httptest.NewRecorder(), r
}

func (e *p2Env) setLimits(t *testing.T, l tenantLimits) {
	t.Helper()
	lj, _ := json.Marshal(l)
	if err := e.st.SetTenantLimits(context.Background(), e.tenantID, string(lj)); err != nil {
		t.Fatalf("set limits: %v", err)
	}
}

func (e *p2Env) seedRepo(t *testing.T, name string) {
	t.Helper()
	if err := e.st.CreateGitRepo(context.Background(), store.GitRepo{
		ID: store.NewID(), TenantID: e.tenantID, Name: name, DefaultBranch: "main", CreatedAt: store.NowTS(),
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	dir := filepath.Join(e.g.dataRoot, "git", "default", name+".git")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
}

func TestInternalGitQuota(t *testing.T) {
	e := newP2Env(t)
	// Unlimited by default: enforce passes.
	if aerr := e.g.enforceGitRepoQuota(context.Background(), e.tenantID); aerr != nil {
		t.Fatalf("unlimited should pass: %v", aerr)
	}
	// Cap at 1 and seed 1 → at the cap, creation blocked.
	e.setLimits(t, tenantLimits{MaxGitRepos: 1})
	e.seedRepo(t, "one")
	if aerr := e.g.enforceGitRepoQuota(context.Background(), e.tenantID); aerr == nil || aerr.code != "quota_exceeded" {
		t.Fatalf("want quota_exceeded, got %v", aerr)
	}
	// The create handler surfaces it as 409 before touching disk/git.
	w, r := e.req("POST", "/api/internal-git/repos", map[string]string{"name": "two"})
	e.g.withMembership(e.g.repoCreate)(w, r)
	if w.Code != 409 {
		t.Fatalf("create over quota: want 409 got %d (%s)", w.Code, w.Body.String())
	}
}

func TestInternalGitRename(t *testing.T) {
	e := newP2Env(t)
	e.seedRepo(t, "old")
	oldDir := filepath.Join(e.g.dataRoot, "git", "default", "old.git")
	newDir := filepath.Join(e.g.dataRoot, "git", "default", "new.git")

	// Happy path: row + bare move together, clone_url reflects the new name.
	w, r := e.req("POST", "/api/internal-git/repos/old/rename", map[string]string{"new_name": "new"})
	r.SetPathValue("name", "old")
	e.g.withMembership(e.g.repoRename)(w, r)
	if w.Code != 200 {
		t.Fatalf("rename: want 200 got %d (%s)", w.Code, w.Body.String())
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Fatalf("new bare missing: %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old bare still present: %v", err)
	}
	if _, ok, _ := e.st.GetGitRepo(context.Background(), e.tenantID, "new"); !ok {
		t.Fatal("row not renamed")
	}
	var dto map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &dto)
	if dto["clone_url"] != "https://fleet.example.com/git/default/new.git" {
		t.Fatalf("clone_url = %v", dto["clone_url"])
	}

	// Collision: renaming onto an existing name → 409, no move.
	e.seedRepo(t, "taken")
	w, r = e.req("POST", "/api/internal-git/repos/new/rename", map[string]string{"new_name": "taken"})
	r.SetPathValue("name", "new")
	e.g.withMembership(e.g.repoRename)(w, r)
	if w.Code != 409 {
		t.Fatalf("collision: want 409 got %d", w.Code)
	}

	// Nonexistent source → 404.
	w, r = e.req("POST", "/api/internal-git/repos/ghost/rename", map[string]string{"new_name": "x"})
	r.SetPathValue("name", "ghost")
	e.g.withMembership(e.g.repoRename)(w, r)
	if w.Code != 404 {
		t.Fatalf("missing source: want 404 got %d", w.Code)
	}
}

func TestGitGCSweep(t *testing.T) {
	// No git tree yet → sweep is a safe no-op.
	newGitGC(nil, filepath.Join(t.TempDir(), "empty"), 0, 0).sweep(context.Background())

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dataRoot := t.TempDir()
	bare := filepath.Join(dataRoot, "git", "default", "r.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v: %s", err, out)
	}
	// Sweeping a real bare completes without error (gc --auto is a no-op on a fresh
	// repo, but the walk + exec path is exercised). No LFS dir → prune is skipped.
	newGitGC(nil, dataRoot, 0, 0).sweep(context.Background())
	if _, err := os.Stat(bare); err != nil {
		t.Fatalf("bare gone after gc: %v", err)
	}
}
