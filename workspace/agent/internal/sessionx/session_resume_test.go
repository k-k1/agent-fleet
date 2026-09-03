package sessionx

import (
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Resume must REPORT its failures. ensureSessionTmux used to discard the launch
// error and return success unconditionally, so POST /sessions/{name}/start answered
// {"ok":true} for a session that never started — and the Console, which has no other
// signal, then sat waiting on a session nobody had launched, with no error to show
// and (before the fix on its side) no way to retry.
func TestEnsureSessionTmuxReportsFailures(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// HOME alone is not enough — a CI runner exports XDG_CONFIG_HOME, and the meta
	// store resolves under it, which would read and WRITE the developer's real one.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	t.Run("no meta", func(t *testing.T) {
		if err := ensureSessionTmux("afnosuchsession", false); err == nil {
			t.Fatal("ensureSessionTmux = nil for a session with no meta")
		}
	})

	t.Run("launch cannot be built", func(t *testing.T) {
		// An ssm session with no target cannot produce a launch plan — a deterministic
		// stand-in for every way a relaunch can fail before tmux is ever reached.
		session.WriteMeta(session.Meta{Name: "aftestssm", Kind: session.KindSSM, Dir: home})
		if err := ensureSessionTmux("aftestssm", false); err == nil {
			t.Fatal("ensureSessionTmux = nil for a session that cannot launch; " +
				"/start would answer ok:true for a session that never started")
		}
	})
}
