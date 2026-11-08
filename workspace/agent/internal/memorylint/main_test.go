package memorylint

import (
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

// TestMain runs the package's tests under the test guard like every other test binary.
func TestMain(m *testing.M) { os.Exit(testguard.Run(m, nil)) }
