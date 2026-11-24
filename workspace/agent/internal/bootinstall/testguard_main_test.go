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
	// The supervisor tests shorten its grace through superviseGraceEnv and make its kill fail
	// through superviseNoKillEnv; only this re-exec entry reads them, so a production supervisor
	// cannot inherit either.
	grace := superviseGrace
	if d, err := time.ParseDuration(os.Getenv(superviseGraceEnv)); err == nil {
		grace = d
	}
	kill := killPid
	if os.Getenv(superviseNoKillEnv) != "" {
		kill = func(int) {} // stands in for a child in uninterruptible I/O, which no test can make
	}
	maybeSupervise(os.Args[1:], grace, kill)
	if os.Getenv(npmCallerHome) != "" { // after the supervisor check: the supervisor inherits the env
		os.Exit(npmCallerHelper())
	}
	os.Exit(testguard.Run(m, nil))
}
