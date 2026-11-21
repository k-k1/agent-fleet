package sessionx

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// A session whose launch makes a worktree is recorded as its creator.
func TestCreateSessionRecordsWorktreeCreator(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	testguard.IsolateTmux(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var first session.Session
	do(t, srv, "POST", "/sessions", map[string]any{
		"worktree": true, "dir": parent, "branch": "main", "new_branch": "feat-c", "kind": "shell",
	}, http.StatusCreated, &first)
	defer tmuxx.Cmd("kill-session", "-t", session.TmuxName(first.Name)).Run()
	if got := gitx.ReadWorktreeCreator(first.Dir); got != first.Name {
		t.Fatalf("creator = %q, want %q", got, first.Name)
	}
}
