package branchrule

import (
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

// TestMain clears AF_CP_INTERNAL_URL: a workspace that runs these tests may itself carry it,
// and cpurl.Request would then send the tests' requests past their fake CP.
func TestMain(m *testing.M) {
	os.Exit(testguard.Run(m, func() {
		_ = os.Unsetenv("AF_CP_INTERNAL_URL")
	}))
}
