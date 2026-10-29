package workitems

import (
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

// TestMain keeps every test of this package off the workspace's live tmux server, HOME and
// Agent (testguard). AF_CP_INTERNAL_URL is cleared as package main's TestMain does, where these
// tests ran before the move: a workspace's own value would send requests past a fake CP.
func TestMain(m *testing.M) {
	os.Exit(testguard.Run(m, func() { _ = os.Unsetenv("AF_CP_INTERNAL_URL") }))
}
