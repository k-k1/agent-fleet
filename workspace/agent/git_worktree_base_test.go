package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A new worktree must start at origin/<base>'s tip, not at the parent's local base. Nothing in
// this product moves a local branch (auto-fetch only refreshes origin/*), so without this a
// session cut from an old clone silently starts weeks behind.
//
// The heart of the fix is not touching the parent: a pull --ff-only there swaps files out from
// under a session working in the parent (a FF succeeds even when unrelated files are dirty). So
// this function advances the new worktree only.
func TestFastForwardNewWorktreeToOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	// origin (bare) + the upstream working copy that pushed to it + the parent clone.
	origin := filepath.Join(home, "origin.git")
	runIntegrationGit(t, home, "init", "--quiet", "--bare", "-b", "main", origin)
	up := filepath.Join(home, "up")
	gitInit(t, up)
	runIntegrationGit(t, up, "remote", "add", "origin", origin)
	runIntegrationGit(t, up, "push", "--quiet", "origin", "main")

	parent := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
		t.Fatal(err)
	}
	runIntegrationGit(t, home, "clone", "--quiet", origin, parent)

	// origin advances after the clone (= the parent's local main stays stale).
	commitIntegrationFile(t, up, "newer")
	runIntegrationGit(t, up, "push", "--quiet", "origin", "main")
	tip := gitRev(t, up, "HEAD")
	stale := gitRev(t, parent, "main")
	if tip == stale {
		t.Fatal("fixture is wrong: origin did not advance past the parent's local main")
	}

	wt, err := gitx.EnsureWorktree(parent, "main", "temp/fresh", "wip-fresh")
	if err != nil {
		t.Fatalf("gitx.EnsureWorktree: %v", err)
	}
	if got := gitRev(t, wt, "HEAD"); got != stale {
		t.Fatalf("worktree started at %s, want the parent's local main %s (fixture)", got, stale)
	}

	gitx.FastForwardNewWorktreeToOrigin(wt, "main")

	if got := gitRev(t, wt, "HEAD"); got != tip {
		t.Errorf("worktree HEAD = %s, want origin's tip %s", got, tip)
	}
	// The parent must not move. That is the difference from the "fast-forward the parent"
	// alternative, and the line that keeps a session running in the parent from being advanced
	// underneath it.
	if got := gitRev(t, parent, "main"); got != stale {
		t.Errorf("the parent's local main moved to %s; it must stay at %s", got, stale)
	}
	if got := gitRev(t, parent, "HEAD"); got != stale {
		t.Errorf("the parent's HEAD moved to %s; it must stay at %s", got, stale)
	}
	// The new branch must gain no upstream (a pull with an explicit refspec sets no tracking).
	// With one, the ↑↓ badge changes meaning and every later pull pours the base into the
	// working branch.
	if out, err := gitx.Run(wt, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		t.Errorf("temp/fresh gained an upstream (%s); it must stay untracked", out)
	}
}

// When the local base has unpushed commits (= it has diverged), that local work is the base the
// user meant, so it must not be silently replaced with origin's.
func TestFastForwardNewWorktreeKeepsDivergedLocalBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	origin := filepath.Join(home, "origin.git")
	runIntegrationGit(t, home, "init", "--quiet", "--bare", "-b", "main", origin)
	up := filepath.Join(home, "up")
	gitInit(t, up)
	runIntegrationGit(t, up, "remote", "add", "origin", origin)
	runIntegrationGit(t, up, "push", "--quiet", "origin", "main")

	parent := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
		t.Fatal(err)
	}
	runIntegrationGit(t, home, "clone", "--quiet", origin, parent)

	commitIntegrationFile(t, up, "remote-side")
	runIntegrationGit(t, up, "push", "--quiet", "origin", "main")
	commitIntegrationFile(t, parent, "local-only") // work that exists only in the parent's local branch
	local := gitRev(t, parent, "main")

	wt, err := gitx.EnsureWorktree(parent, "main", "temp/diverged", "wip-diverged")
	if err != nil {
		t.Fatalf("gitx.EnsureWorktree: %v", err)
	}
	gitx.FastForwardNewWorktreeToOrigin(wt, "main")

	if got := gitRev(t, wt, "HEAD"); got != local {
		t.Errorf("worktree HEAD = %s, want the local base %s (a diverged base must not be replaced)", got, local)
	}
}

// When origin has no such branch (local-only), or there is no remote at all, nothing must
// happen and the launch must still work.
func TestFastForwardNewWorktreeWithoutOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent) // no remote
	wt, err := gitx.EnsureWorktree(parent, "main", "temp/local", "wip-local")
	if err != nil {
		t.Fatalf("gitx.EnsureWorktree: %v", err)
	}
	before := gitRev(t, wt, "HEAD")
	gitx.FastForwardNewWorktreeToOrigin(wt, "main")
	gitx.FastForwardNewWorktreeToOrigin(wt, "")                // unknown base
	gitx.FastForwardNewWorktreeToOrigin(wt, "--upload-pack=x") // a name that would turn into an argument
	if got := gitRev(t, wt, "HEAD"); got != before {
		t.Errorf("HEAD moved to %s without an origin; want %s", got, before)
	}
}

func gitRev(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := gitx.Run(dir, "rev-parse", ref)
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", ref, dir, err)
	}
	return out
}

// Checked through the wiring: POST /sessions' worktree-then-start really does begin at origin's
// tip. A correct helper means nothing if it is never called, so this goes through the path with
// no base given (= start from the parent's current branch).
func TestCreateSessionWorktreeStartsAtOriginTip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))

	origin := filepath.Join(home, "origin.git")
	runIntegrationGit(t, home, "init", "--quiet", "--bare", "-b", "main", origin)
	up := filepath.Join(home, "up")
	gitInit(t, up)
	runIntegrationGit(t, up, "remote", "add", "origin", origin)
	runIntegrationGit(t, up, "push", "--quiet", "origin", "main")

	parent := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
		t.Fatal(err)
	}
	runIntegrationGit(t, home, "clone", "--quiet", origin, parent)

	commitIntegrationFile(t, up, "landed-after-the-clone")
	runIntegrationGit(t, up, "push", "--quiet", "origin", "main")
	tip := gitRev(t, up, "HEAD")
	stale := gitRev(t, parent, "main")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", sessionx.HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{
		// no branch sent = "the default base"; the parent's current branch (main) is the start.
		"worktree": true, "dir": parent, "new_branch": "feat-fresh", "kind": "shell",
	}, http.StatusCreated, &created)
	defer exec.Command("tmux", "kill-session", "-t", session.TmuxName(created.Name)).Run()

	if got := gitRev(t, created.Dir, "HEAD"); got != tip {
		t.Errorf("session started at %s, want origin's tip %s", got, tip)
	}
	if got := gitRev(t, parent, "main"); got != stale {
		t.Errorf("the launch moved the parent's main to %s; it must stay at %s", got, stale)
	}
}
