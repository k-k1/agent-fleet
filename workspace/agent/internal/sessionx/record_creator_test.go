package sessionx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A session whose meta never reached disk must not become a worktree's creator: the copy would
// name a session that is listed nowhere.
func TestRecordCreatorNamesOnlyAPublishedSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	wt := filepath.Join(home, "wt")
	gitDir := filepath.Join(home, "gitdir")
	for _, d := range []string{wt, gitDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	(&createRun{name: "ghost01", createdWorktree: wt}).recordCreator()
	if got := gitx.ReadWorktreeCreator(wt); got != "" {
		t.Fatalf("creator = %q for a session with no meta, want none", got)
	}

	session.WriteMeta(session.Meta{Name: "real001", Dir: wt, Kind: session.KindClaude})
	(&createRun{name: "real001", createdWorktree: wt}).recordCreator()
	if got := gitx.ReadWorktreeCreator(wt); got != "real001" {
		t.Fatalf("creator = %q, want real001", got)
	}
}
