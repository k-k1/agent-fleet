package afmemory

import (
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

// TestMain keeps every test of this package (the command reaches the Agent only through the fake server it starts) off the workspace's live tmux server, HOME and
// Agent (testguard).
func TestMain(m *testing.M) { os.Exit(testguard.Run(m, nil)) }
