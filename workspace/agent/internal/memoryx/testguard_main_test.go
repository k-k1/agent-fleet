package memoryx

import (
	"fmt"
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

// TestMain keeps every test of this package off the workspace's live tmux server, HOME and
// Agent (testguard).
//
// It also points git at an empty template directory: the stock one copies ~14 sample hooks into
// every `git init` (and the test's TempDir cleanup unlinks them again), and no test reads them.
// A test that needs a hook writes it itself.
func TestMain(m *testing.M) { os.Exit(testguard.Run(m, emptyGitTemplate)) }

func emptyGitTemplate() {
	// Inside the guard's scratch root (removed on exit) when there is one.
	if d, err := os.MkdirTemp(os.Getenv("AF_TESTGUARD_ROOT"), "memx-git-template-"); err == nil {
		os.Setenv("GIT_TEMPLATE_DIR", d)
	} else {
		fmt.Fprintln(os.Stderr, "memoryx tests: no empty git template dir, running with the stock one:", err)
	}
}
