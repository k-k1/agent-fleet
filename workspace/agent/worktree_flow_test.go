package main

import (
	"bytes"
	"encoding/json"
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

// TestWorktreeGuardDriftFlow drives the ①②③ feature set end-to-end over real HTTP +
// tmux + git in an isolated HOME: worktree-then-start creates the worktree and a live
// session (②); a branch switch and a delete of that working copy are both refused while
// the session runs (①); after a stray checkout, the session list flags branch drift
// (③); once the session is stopped the worktree deletes cleanly. Skips without tmux/git.
func TestWorktreeGuardDriftFlow(t *testing.T) {
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
	mux.HandleFunc("GET /sessions", sessionx.HandleListSessions)
	mux.HandleFunc("POST /sessions", sessionx.HandleCreateSession)
	mux.HandleFunc("POST /sessions/{name}/stop", sessionx.HandleStopSession)
	mux.HandleFunc("GET /repos", gitx.HandleListRepos)
	mux.HandleFunc("POST /repos/{name}/checkout", gitx.HandleRepoCheckout)
	mux.HandleFunc("POST /sessions/{name}/rename-branch", sessionx.HandleSessionRenameBranch)
	mux.HandleFunc("DELETE /repos/{name}", gitx.HandleDeleteRepo)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// ② worktree-then-start: shell session (no claude needed) on a new branch off main.
	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{
		"worktree": true, "dir": parent, "branch": "main", "new_branch": "feat-x", "kind": "shell",
	}, http.StatusCreated, &created)
	defer exec.Command("tmux", "kill-session", "-t", session.TmuxName(created.Name)).Run()

	wantDir := filepath.Join(home, "repos", "app@feat-x")
	if created.Dir != wantDir {
		t.Fatalf("session dir = %q, want %q", created.Dir, wantDir)
	}
	if created.Branch != "feat-x" {
		t.Fatalf("session start branch = %q, want feat-x", created.Branch)
	}
	if !gitx.IsGitRepo(wantDir) {
		t.Fatalf("worktree not created at %s", wantDir)
	}

	// #2 badge: the repo list marks app@feat-x as a worktree of parent "app".
	var repoList struct{ Repos []gitx.Repo }
	do(t, srv, "GET", "/repos", nil, http.StatusOK, &repoList)
	var wtRepo *gitx.Repo
	for i := range repoList.Repos {
		if repoList.Repos[i].Name == "app@feat-x" {
			wtRepo = &repoList.Repos[i]
		}
	}
	if wtRepo == nil || !wtRepo.Worktree || wtRepo.Parent != "app" {
		t.Fatalf("repo list worktree flag = %+v, want worktree=true parent=app", wtRepo)
	}
	if wtRepo.Integration == nil || wtRepo.Integration.Relation != "same" || wtRepo.Integration.TargetBranch != "main" {
		t.Fatalf("repo list integration = %+v, want same against main", wtRepo.Integration)
	}

	// Name-collision guard: a second worktree whose new branch clashes with the now-
	// existing local feat-x is refused (409) before anything is created — no silently
	// divergent branch.
	if code := httpStatus(t, srv, "POST", "/sessions", map[string]any{
		"worktree": true, "dir": parent, "branch": "main", "new_branch": "feat-x", "kind": "shell",
	}); code != http.StatusConflict {
		t.Fatalf("colliding worktree create = %d, want 409", code)
	}

	// ① checkout on that working copy is refused while the session runs.
	code := httpStatus(t, srv, "POST", "/repos/app@feat-x/checkout", map[string]any{"branch": "main"})
	if code != http.StatusConflict {
		t.Fatalf("checkout while live = %d, want 409", code)
	}
	// ① delete is likewise refused.
	if code := httpStatus(t, srv, "DELETE", "/repos/app@feat-x", nil); code != http.StatusConflict {
		t.Fatalf("delete while live = %d, want 409", code)
	}

	sessionByName := func(name string) *session.Session {
		var list struct{ Sessions []session.Session }
		do(t, srv, "GET", "/sessions", nil, http.StatusOK, &list)
		for i := range list.Sessions {
			if list.Sessions[i].Name == name {
				return &list.Sessions[i]
			}
		}
		t.Fatalf("session %s not in list", name)
		return nil
	}

	// Deferred naming: rename the provisional branch in place. The working tree is
	// untouched, the session's start-branch meta follows the rename, so it is NOT
	// mistaken for drift.
	if code := httpStatus(t, srv, "POST", "/sessions/"+created.Name+"/rename-branch", map[string]any{"name": "renamed-x"}); code != http.StatusOK {
		t.Fatalf("rename-branch = %d, want 200", code)
	}
	if s := sessionByName(created.Name); s.Branch != "renamed-x" || s.BranchDrift || !s.Worktree {
		t.Fatalf("after rename: start=%q drift=%v worktree=%v, want renamed-x/false/true", s.Branch, s.BranchDrift, s.Worktree)
	}

	// ③ a stray checkout inside the worktree (bypassing the guard) DOES show as drift.
	if out, err := exec.Command("git", "-C", wantDir, "checkout", "-b", "drifted").CombinedOutput(); err != nil {
		t.Fatalf("stray checkout: %v: %s", err, out)
	}
	if s := sessionByName(created.Name); !s.BranchDrift || s.CurrentBranch != "drifted" {
		t.Fatalf("drift = %v cur=%q, want true/drifted", s.BranchDrift, s.CurrentBranch)
	}

	// #1 auto-cleanup: stopping the last (clean) session in the worktree forgets its
	// meta and auto-removes the worktree — no manual delete needed. The stray branch
	// above left no uncommitted/unpushed work, so it qualifies.
	if code := httpStatus(t, srv, "POST", "/sessions/"+created.Name+"/stop", nil); code != http.StatusOK {
		t.Fatalf("stop = %d, want 200", code)
	}
	if _, err := os.Stat(wantDir); err == nil {
		t.Fatalf("worktree dir still exists after stopping its last session (auto-prune failed)")
	}
	// The parent is untouched by the auto-prune.
	if !gitx.IsGitRepo(parent) {
		t.Fatalf("parent working copy damaged by worktree prune")
	}
}

// do sends a JSON request, asserts the status, and decodes the body into out (if any).
func do(t *testing.T, srv *httptest.Server, method, path string, body any, want int, out any) {
	t.Helper()
	code, raw := roundtrip(t, srv, method, path, body)
	if code != want {
		t.Fatalf("%s %s = %d (%s), want %d", method, path, code, raw, want)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s: %v (%s)", path, err, raw)
		}
	}
}

func httpStatus(t *testing.T, srv *httptest.Server, method, path string, body any) int {
	t.Helper()
	code, _ := roundtrip(t, srv, method, path, body)
	return code
}

func roundtrip(t *testing.T, srv *httptest.Server, method, path string, body any) (int, []byte) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(res.Body)
	return res.StatusCode, buf.Bytes()
}
