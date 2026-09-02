package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestWorktreeExistingBranchGuard drives the existing-branch launch over real HTTP +
// git: the first launch checks the branch out into its own worktree, and every later
// attempt to put that same branch somewhere else — a second worktree, or a checkout in
// the parent — is refused with branch_in_use BEFORE any side effect, naming the copy
// that holds it. Skips without tmux/git.
func TestWorktreeExistingBranchGuard(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent) // on "main", plus a "feature" branch

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", handleCreateSession)
	mux.HandleFunc("POST /repos/{name}/checkout", gitx.HandleRepoCheckout)
	mux.HandleFunc("GET /repos/{name}/branches", gitx.HandleRepoBranches)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Launch on the EXISTING "feature" branch — no new branch is minted, and the
	// worktree folder is named after the branch it checked out.
	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{
		"worktree": true, "dir": parent, "branch": "feature", "use_existing": true, "kind": "shell",
	}, http.StatusCreated, &created)
	defer exec.Command("tmux", "kill-session", "-t", session.TmuxName(created.Name)).Run()

	wtDir := filepath.Join(home, "repos", "app@feature")
	if created.Dir != wtDir {
		t.Fatalf("session dir = %q, want %q", created.Dir, wtDir)
	}
	if br, _ := gitx.Run(wtDir, "rev-parse", "--abbrev-ref", "HEAD"); br != "feature" {
		t.Fatalf("worktree is on %q, want feature", br)
	}

	// The branch list now reports feature as occupied by that worktree (and main, the
	// parent's own branch, as free — "current" is not "occupied").
	var list struct {
		Branches []gitx.BranchInfo `json:"branches"`
	}
	do(t, srv, "GET", "/repos/app/branches", nil, http.StatusOK, &list)
	for _, b := range list.Branches {
		switch b.Name {
		case "feature":
			if filepath.Base(b.WorktreePath) != "app@feature" {
				t.Errorf("feature worktree_path = %q, want …/app@feature", b.WorktreePath)
			}
		case "main":
			if b.WorktreePath != "" {
				t.Errorf("main should be free, got worktree_path %q", b.WorktreePath)
			}
		}
	}

	// A second worktree on the same branch is refused, and nothing is created for it.
	code, body := roundtrip(t, srv, "POST", "/sessions", map[string]any{
		"worktree": true, "dir": parent, "branch": "feature", "use_existing": true, "kind": "shell",
	})
	if code != http.StatusConflict {
		t.Fatalf("second worktree on a live branch = %d (%s), want 409", code, body)
	}
	if got := errPayload(t, body); got["code"] != errCodeBranchInUse || got["worktree"] != "app@feature" {
		t.Errorf("error payload = %v, want branch_in_use / app@feature", got)
	}

	// Same guard for a plain checkout in the parent (which has no running sessions, so
	// this is our branch guard talking, not the sessions_running one).
	code, body = roundtrip(t, srv, "POST", "/repos/app/checkout", map[string]any{"branch": "feature"})
	if code != http.StatusConflict {
		t.Fatalf("checkout of a branch live in a worktree = %d (%s), want 409", code, body)
	}
	if got := errPayload(t, body); got["code"] != errCodeBranchInUse || got["worktree"] != "app@feature" {
		t.Errorf("checkout error payload = %v, want branch_in_use / app@feature", got)
	}
	// The parent stayed on main — the refusal happened before git ran.
	if br, _ := gitx.Run(parent, "rev-parse", "--abbrev-ref", "HEAD"); br != "main" {
		t.Errorf("parent moved to %q despite the refusal", br)
	}
}

// TestWorktreeExistingBranchSync covers the freshness half: a branch pushed AFTER this
// copy's last fetch is still launchable (ensureBranchRef fetches it once), and a branch
// whose remote has moved on starts at the current tip rather than the stale one
// (fastForwardWorktree). No tmux needed — this exercises the git layer directly.
func TestWorktreeExistingBranchSync(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)

	// A bare origin, plus a second clone standing in for "someone else's push".
	origin := filepath.Join(root, "origin.git")
	gitAt(t, root, "init", "--bare", "-b", "main", origin)
	seed := filepath.Join(root, "seed")
	gitInit(t, seed)
	gitAt(t, seed, "remote", "add", "origin", origin)
	gitAt(t, seed, "push", "-u", "origin", "main")

	parent := filepath.Join(root, "repos", "app")
	if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, root, "clone", origin, parent)

	// Someone pushes a brand-new branch AFTER our clone finished.
	other := filepath.Join(root, "other")
	gitAt(t, root, "clone", origin, other)
	gitAt(t, other, "checkout", "-b", "hot")
	if err := os.WriteFile(filepath.Join(other, "hot.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, other, "add", "hot.txt")
	gitAt(t, other, "commit", "-m", "hot v1")
	gitAt(t, other, "push", "-u", "origin", "hot")

	// Our copy has never heard of it — that is exactly the "invalid reference" case.
	if local, remote := gitx.BranchNameStatus(parent, "hot"); local || remote {
		t.Fatalf("precondition: hot already known to the parent (local=%v remote=%v)", local, remote)
	}
	gitx.EnsureBranchRef(parent, "hot")
	if _, remote := gitx.BranchNameStatus(parent, "hot"); !remote {
		t.Fatal("ensureBranchRef did not fetch the newly pushed branch")
	}
	wt, err := gitx.EnsureWorktree(parent, "hot", "", "")
	if err != nil {
		t.Fatalf("ensureWorktree on a remote-only branch: %v", err)
	}
	if br, _ := gitx.Run(wt, "rev-parse", "--abbrev-ref", "HEAD"); br != "hot" {
		t.Fatalf("worktree is on %q, want hot", br)
	}

	// The remote moves on while we hold a stale checkout; the launch must catch up.
	if err := os.WriteFile(filepath.Join(other, "hot.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, other, "commit", "-am", "hot v2")
	gitAt(t, other, "push")
	tip, _ := gitx.Run(other, "rev-parse", "HEAD")

	gitx.FastForwardWorktree(wt)
	if got, _ := gitx.Run(wt, "rev-parse", "HEAD"); got != tip {
		t.Errorf("worktree HEAD = %s, want the upstream tip %s", got, tip)
	}

	// Divergence must NOT break the launch: an unpushable local commit makes --ff-only
	// fail, and the session still gets its working copy (just not fast-forwarded).
	if err := os.WriteFile(filepath.Join(other, "hot.txt"), []byte("v3-remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, other, "commit", "-am", "hot v3 remote")
	gitAt(t, other, "push")
	if err := os.WriteFile(filepath.Join(wt, "hot.txt"), []byte("v3-local"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, wt, "commit", "-am", "hot v3 local")
	local, _ := gitx.Run(wt, "rev-parse", "HEAD")
	gitx.FastForwardWorktree(wt) // must not panic, must not destroy the local commit
	if got, _ := gitx.Run(wt, "rev-parse", "HEAD"); got != local {
		t.Errorf("diverged worktree HEAD = %s, want it left alone at %s", got, local)
	}
}

// errPayload extracts the {code, message, worktree} error object from a response body.
func errPayload(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var env struct {
		Error map[string]string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode error body %s: %v", body, err)
	}
	return env.Error
}
