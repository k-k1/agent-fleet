package bootinstall

import (
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

// TestMain keeps every test of this package off the workspace's live tmux server, HOME and
// Agent (testguard).
func TestMain(m *testing.M) {
	MaybeSupervise(os.Args[1:])
	if os.Getenv(npmCallerHome) != "" { // after the supervisor check: the supervisor inherits the env
		os.Exit(npmCallerHelper())
	}
	os.Exit(testguard.Run(m, nil))
}
