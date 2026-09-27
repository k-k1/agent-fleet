package muse

import (
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A stopped muse session is resumable exactly when its folder exists: Resume refuses a gone
// folder (DirGoneErr), and the Console offers to recreate a deleted worktree only for rows
// that say they cannot resume.
func TestWireLiveStoppedFollowsTheFolder(t *testing.T) {
	a := New()
	if li := a.WireLive(session.Meta{Name: "s", Kind: session.KindMuse, Dir: t.TempDir()}, false); !li.Resumable {
		t.Errorf("folder present: %+v, want resumable", li)
	}
	if li := a.WireLive(session.Meta{Name: "s", Kind: session.KindMuse, Dir: filepath.Join(t.TempDir(), "gone")}, false); li.Resumable {
		t.Errorf("folder gone: %+v, want not resumable", li)
	}
}
