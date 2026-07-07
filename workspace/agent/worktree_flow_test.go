package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
	mux.HandleFunc("GET /sessions", handleListSessions)
	mux.HandleFunc("POST /sessions", handleCreateSession)
	mux.HandleFunc("POST /sessions/{name}/stop", handleStopSession)
	mux.HandleFunc("GET /repos", handleListRepos)
	mux.HandleFunc("POST /repos/{name}/checkout", handleRepoCheckout)
	mux.HandleFunc("POST /repos/{name}/rename-branch", handleRepoRenameBranch)
	mux.HandleFunc("DELETE /repos/{name}", handleDeleteRepo)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// ② worktree-then-start: shell session (no claude needed) on a new branch off main.
	var created Session
	do(t, srv, "POST", "/sessions", map[string]any{
		"worktree": true, "dir": parent, "branch": "main", "new_branch": "feat-x", "kind": "shell",
	}, http.StatusCreated, &created)
	defer exec.Command("tmux", "kill-session", "-t", tmuxName(created.Name)).Run()

	wantDir := filepath.Join(home, "repos", "app@feat-x")
	if created.Dir != wantDir {
		t.Fatalf("session dir = %q, want %q", created.Dir, wantDir)
	}
	if created.Branch != "feat-x" {
		t.Fatalf("session start branch = %q, want feat-x", created.Branch)
	}
	if !isGitRepo(wantDir) {
		t.Fatalf("worktree not created at %s", wantDir)
	}

	// #2 badge: the repo list marks app@feat-x as a worktree of parent "app".
	var repoList struct{ Repos []Repo }
	do(t, srv, "GET", "/repos", nil, http.StatusOK, &repoList)
	var wtRepo *Repo
	for i := range repoList.Repos {
		if repoList.Repos[i].Name == "app@feat-x" {
			wtRepo = &repoList.Repos[i]
		}
	}
	if wtRepo == nil || !wtRepo.Worktree || wtRepo.Parent != "app" {
		t.Fatalf("repo list worktree flag = %+v, want worktree=true parent=app", wtRepo)
	}

	// ① checkout on that working copy is refused while the session runs.
	code := status(t, srv, "POST", "/repos/app@feat-x/checkout", map[string]any{"branch": "main"})
	if code != http.StatusConflict {
		t.Fatalf("checkout while live = %d, want 409", code)
	}
	// ① delete is likewise refused.
	if code := status(t, srv, "DELETE", "/repos/app@feat-x", nil); code != http.StatusConflict {
		t.Fatalf("delete while live = %d, want 409", code)
	}

	sessionByName := func(name string) *Session {
		var list struct{ Sessions []Session }
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
	if code := status(t, srv, "POST", "/repos/app@feat-x/rename-branch", map[string]any{"name": "renamed-x"}); code != http.StatusOK {
		t.Fatalf("rename-branch = %d, want 200", code)
	}
	if s := sessionByName(created.Name); s.Branch != "renamed-x" || s.BranchDrift {
		t.Fatalf("after rename: start=%q drift=%v, want renamed-x/false", s.Branch, s.BranchDrift)
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
	if code := status(t, srv, "POST", "/sessions/"+created.Name+"/stop", nil); code != http.StatusOK {
		t.Fatalf("stop = %d, want 200", code)
	}
	if _, err := os.Stat(wantDir); err == nil {
		t.Fatalf("worktree dir still exists after stopping its last session (auto-prune failed)")
	}
	// The parent is untouched by the auto-prune.
	if !isGitRepo(parent) {
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

func status(t *testing.T, srv *httptest.Server, method, path string, body any) int {
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
