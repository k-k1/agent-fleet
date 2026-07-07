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
	"time"
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
	mux.HandleFunc("POST /repos/{name}/checkout", handleRepoCheckout)
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

	// ① checkout on that working copy is refused while the session runs.
	code := status(t, srv, "POST", "/repos/app@feat-x/checkout", map[string]any{"branch": "main"})
	if code != http.StatusConflict {
		t.Fatalf("checkout while live = %d, want 409", code)
	}
	// ① delete is likewise refused.
	if code := status(t, srv, "DELETE", "/repos/app@feat-x", nil); code != http.StatusConflict {
		t.Fatalf("delete while live = %d, want 409", code)
	}

	// ③ a stray checkout inside the worktree (bypassing the guard) shows as drift.
	if out, err := exec.Command("git", "-C", wantDir, "checkout", "-b", "drifted").CombinedOutput(); err != nil {
		t.Fatalf("stray checkout: %v: %s", err, out)
	}
	var list struct{ Sessions []Session }
	do(t, srv, "GET", "/sessions", nil, http.StatusOK, &list)
	var found *Session
	for i := range list.Sessions {
		if list.Sessions[i].Name == created.Name {
			found = &list.Sessions[i]
		}
	}
	if found == nil {
		t.Fatalf("session %s not in list", created.Name)
	}
	if !found.BranchDrift || found.CurrentBranch != "drifted" {
		t.Fatalf("drift = %v cur=%q, want true/drifted", found.BranchDrift, found.CurrentBranch)
	}

	// Stop the session, then the worktree deletes cleanly (guard clears, git removes it).
	if out, err := exec.Command("tmux", "kill-session", "-t", tmuxName(created.Name)).CombinedOutput(); err != nil {
		t.Fatalf("kill session: %v: %s", err, out)
	}
	// tmux kill is async-ish; wait for liveness to clear.
	for i := 0; i < 50 && tmuxHasSession(tmuxName(created.Name)); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if code := status(t, srv, "DELETE", "/repos/app@feat-x", nil); code != http.StatusOK {
		t.Fatalf("delete after stop = %d, want 200", code)
	}
	if _, err := os.Stat(wantDir); err == nil {
		t.Fatalf("worktree dir still exists after delete")
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
