package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// TestGitHTTPEndToEnd drives the real handler against a real git-http-backend:
// clone → commit → push from one workspace, then a fresh clone from a second
// workspace sees the pushed commit (goal A, team sharing). It also confirms a
// wrong-tenant URL is refused even with a valid token. Skips where the CGI backend
// is absent (it ships in the CP image, not necessarily every dev host).
func TestGitHTTPEndToEnd(t *testing.T) {
	if _, err := os.Stat(gitBackendPath()); err != nil {
		t.Skipf("git-http-backend not present (%s): skipping e2e", gitBackendPath())
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	tmp := t.TempDir()
	dataRoot := filepath.Join(tmp, "data")

	st, err := store.OpenSQLite(filepath.Join(tmp, "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dflt, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "u@x", "u-x", "")
	mem, _ := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member")

	master := []byte("master-key-e2e-000000000000000000")
	g := newGitServerAPI(&manager{store: st, master32: master, dataRoot: dataRoot}, "")
	token := mintGitToken(gitSignKey(master), mem.ID)

	// Create the bare + ledger row the way the API does.
	dir := filepath.Join(dataRoot, "git", "default", "shared.git")
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, tmp, nil, "init", "--bare", "--initial-branch=main", dir)
	if err := st.CreateGitRepo(ctx, store.GitRepo{ID: store.NewID(), TenantID: dflt.ID, Name: "shared", DefaultBranch: "main", CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(g.gitHTTP))
	defer srv.Close()

	// Clone URL with Basic creds embedded, as the cred helper supplies them.
	hostPart := srv.URL[len("http://"):]
	authURL := "http://x-access-token:" + token + "@" + hostPart + "/git/default/shared.git"

	env := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x",
	}

	// Workspace A: clone empty, commit, push.
	wa := filepath.Join(tmp, "wsA")
	gitRun(t, tmp, env, "clone", authURL, wa)
	if err := os.WriteFile(filepath.Join(wa, "hello.txt"), []byte("hi from A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wa, env, "add", ".")
	gitRun(t, wa, env, "commit", "-m", "first")
	gitRun(t, wa, env, "push", "origin", "HEAD:main")

	// Workspace B: a fresh clone must observe A's push.
	wb := filepath.Join(tmp, "wsB")
	gitRun(t, tmp, env, "clone", authURL, wb)
	got, err := os.ReadFile(filepath.Join(wb, "hello.txt"))
	if err != nil || string(got) != "hi from A\n" {
		t.Fatalf("workspace B missing pushed file: err=%v content=%q", err, string(got))
	}

	// Cross-tenant URL with a valid token must fail (git clone returns non-zero).
	badURL := "http://x-access-token:" + token + "@" + hostPart + "/git/security/shared.git"
	cmd := exec.Command("git", "clone", badURL, filepath.Join(tmp, "wsBad"))
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("cross-tenant clone unexpectedly succeeded: %s", out)
	}
}

func gitRun(t *testing.T, dir string, extraEnv []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out.String())
	}
}
