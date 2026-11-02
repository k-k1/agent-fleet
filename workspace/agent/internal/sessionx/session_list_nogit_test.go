package sessionx

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

// GET /sessions is polled every few seconds by every open Console tab; each git
// process it spawned read ~/.gitconfig (an EFS mount in production). The list must
// enrich rows from the repository files, spawning no git, on every poll.
func TestSessionsListSpawnsNoGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateAgentState(t)
	root := t.TempDir()
	main := filepath.Join(root, "main")
	mustGit(t, root, "init", "-q", "-b", "main", main)
	mustGit(t, main, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "i")
	const n = 6
	dirs := []string{main}
	for i := 0; i < n-1; i++ {
		wt := filepath.Join(root, "wt"+string(rune('a'+i)))
		mustGit(t, main, "worktree", "add", "-q", "-b", "temp/s"+string(rune('a'+i)), wt)
		dirs = append(dirs, wt)
	}
	for i, d := range dirs {
		session.WriteMeta(session.Meta{
			Name: "s" + string(rune('a'+i)), Kind: session.KindClaude, Dir: d, Branch: "stale-start-branch",
			CreatedAt: "2026-09-09T10:00:00+09:00",
		})
	}
	// One session in a subdirectory of the main checkout and one outside any repo.
	sub := filepath.Join(main, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "ssub", Kind: session.KindClaude, Dir: sub, CreatedAt: "2026-09-09T10:00:00+09:00"})
	session.WriteMeta(session.Meta{Name: "snogit", Kind: session.KindClaude, Dir: t.TempDir(), CreatedAt: "2026-09-09T10:00:00+09:00"})

	srv := httptest.NewServer(http.HandlerFunc(HandleListSessions))
	defer srv.Close()
	for poll := 1; poll <= 3; poll++ {
		// Poll 1 also fills the per-dir origin-URL cache (TTL-bound, a separate path);
		// every later poll is the steady state the EFS load came from.
		before := gitx.SpawnCount()
		res, err := http.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		var list struct{ Sessions []session.Session }
		err = json.NewDecoder(res.Body).Decode(&list)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got := gitx.SpawnCount() - before; poll > 1 && got != 0 {
			t.Fatalf("poll %d: GET /sessions spawned %d git processes, want 0 (steady state)", poll, got)
		}
		t.Logf("poll %d: %d git spawns", poll, gitx.SpawnCount()-before)
		wts, drift := 0, 0
		for _, s := range list.Sessions {
			if s.Worktree {
				wts++
			}
			if s.BranchDrift {
				drift++
			}
		}
		if wts != n-1 || drift != n {
			t.Fatalf("poll %d: worktree rows=%d (want %d), drift rows=%d (want %d)", poll, wts, n-1, drift, n)
		}
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
