package modelfallback

import (
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

// TestMain keeps every test of this package off the workspace's live tmux server, HOME and
// Agent (testguard).
func TestMain(m *testing.M) { os.Exit(testguard.Run(m, nil)) }
