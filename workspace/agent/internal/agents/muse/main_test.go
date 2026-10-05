package muse

import (
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

// TestMain points HOME at a throwaway directory for the whole package. accept records every
// send in the ClientMessageID ledger under HOME, so any test that sends without setting its own
// HOME would otherwise write into the real ~/.local/state/agent-fleet/muse-msgledger/. Tests
// that need a HOME of their own still take one with t.Setenv.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "muse-test-home-")
	if err != nil {
		panic(err)
	}
	code := testguard.Run(m, func() { os.Setenv("HOME", home) })
	os.RemoveAll(home)
	os.Exit(code)
}
