package bootinstall

import (
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

// TestMain keeps every test of this package off the workspace's live tmux server, HOME and
// Agent (testguard).
func TestMain(m *testing.M) {
	// The supervisor tests shorten its grace through superviseGraceEnv; only this re-exec entry
	// reads it, so a production supervisor cannot inherit a shorter one.
	grace := superviseGrace
	if d, err := time.ParseDuration(os.Getenv(superviseGraceEnv)); err == nil {
		grace = d
	}
	maybeSupervise(os.Args[1:], grace)
	if os.Getenv(npmCallerHome) != "" { // after the supervisor check: the supervisor inherits the env
		os.Exit(npmCallerHelper())
	}
	os.Exit(testguard.Run(m, nil))
}
