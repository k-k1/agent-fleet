package sessionx

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestCreateSessionSubdir drives POST /sessions with a subdir over real HTTP + tmux + git:
// the session's Dir must stay the WORKING COPY (everything that reasons about a copy —
// the checkout guard, worktree pruning, the Console's grouping — keys off it) while the
// launched process starts in the subdir. A path that isn't there is refused up front
// rather than launching somewhere unintended.
func TestCreateSessionSubdir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	repo := filepath.Join(home, "repos", "app")
	gitInit(t, repo)
	if err := os.MkdirAll(filepath.Join(repo, "console", "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{
		"dir": repo, "kind": "shell", "subdir": "console/src",
	}, http.StatusCreated, &created)
	defer exec.Command("tmux", "kill-session", "-t", session.TmuxName(created.Name)).Run()

	if created.Dir != repo {
		t.Fatalf("session dir = %q, want the working copy %q", created.Dir, repo)
	}
	if created.Subdir != "console/src" {
		t.Fatalf("session subdir = %q, want console/src", created.Subdir)
	}
	m, ok := session.ReadMeta(created.Name)
	if !ok {
		t.Fatal("meta not persisted")
	}
	if want := filepath.Join(repo, "console", "src"); m.CWD() != want {
		t.Fatalf("launch CWD = %q, want %q", m.CWD(), want)
	}
	// Repo (the display/grouping key) still names the working copy, not the folder.
	if m.Repo != "app" {
		t.Fatalf("meta repo = %q, want app", m.Repo)
	}

	// A folder that does not exist is a 400 before anything is launched.
	if code := httpStatus(t, srv, "POST", "/sessions", map[string]any{
		"dir": repo, "kind": "shell", "subdir": "nope",
	}); code != http.StatusBadRequest {
		t.Fatalf("missing subdir status = %d, want 400", code)
	}
	// So is an escape out of the working copy.
	if code := httpStatus(t, srv, "POST", "/sessions", map[string]any{
		"dir": repo, "kind": "shell", "subdir": "../..",
	}); code != http.StatusBadRequest {
		t.Fatalf("escaping subdir status = %d, want 400", code)
	}
}

// TestCreateSessionSubdirInWorktree pins the composition that motivated placing the
// subdir check AFTER dir resolution: with worktree=true the path is resolved inside the
// FRESHLY CREATED worktree, not the parent copy.
func TestCreateSessionSubdirInWorktree(t *testing.T) {
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
	gitInit(t, parent)
	// Commit the folder so the worktree checkout materializes it (git tracks files, so
	// an empty dir would never appear in the new worktree).
	if err := os.MkdirAll(filepath.Join(parent, "console"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "console", "main.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIntegrationGit(t, parent, "add", "console/main.ts")
	runIntegrationGit(t, parent, "commit", "-m", "console")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{
		"worktree": true, "dir": parent, "branch": "main", "new_branch": "feat-sub",
		"kind": "shell", "subdir": "console",
	}, http.StatusCreated, &created)
	defer exec.Command("tmux", "kill-session", "-t", session.TmuxName(created.Name)).Run()

	wantDir := filepath.Join(home, "repos", "app@feat-sub")
	if created.Dir != wantDir {
		t.Fatalf("session dir = %q, want the new worktree %q", created.Dir, wantDir)
	}
	m, _ := session.ReadMeta(created.Name)
	if want := filepath.Join(wantDir, "console"); m.CWD() != want {
		t.Fatalf("launch CWD = %q, want %q (inside the worktree)", m.CWD(), want)
	}
}
