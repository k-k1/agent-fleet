package mcpx

import (
	"os"
	"testing"
)

// TestMain clears AF_CP_INTERNAL_URL: a workspace that runs these tests may itself carry it,
// and cpurl.Request would then send the tests' requests past their fake CP.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("AF_CP_INTERNAL_URL")
	os.Exit(m.Run())
}
