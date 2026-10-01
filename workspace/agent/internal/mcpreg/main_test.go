package mcpreg

import (
	"os"
	"testing"
)

// In a Workspace on ECS the Agent sets AWS_EC2_METADATA_DISABLED for everything it starts,
// this test binary included, and every stdio definition then forwards it. Start from a
// machine without it so the expected shapes do not depend on where the suite runs; the
// tests about that variable set it themselves.
func TestMain(m *testing.M) {
	os.Unsetenv("AWS_EC2_METADATA_DISABLED")
	os.Exit(m.Run())
}
