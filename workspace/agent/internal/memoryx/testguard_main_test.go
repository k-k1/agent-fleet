package memoryx

import (
	"flag"
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
//
// Unless shardable says otherwise, the tests run in child processes (shard_main_test.go); the
// guard is applied here, around the parent, and the children inherit it.
func TestMain(m *testing.M) {
	flag.Parse()
	var r testguard.Runner = m
	if shardable() {
		r = shardRunner{}
	}
	os.Exit(testguard.Run(r, emptyGitTemplate))
}

func emptyGitTemplate() {
	// Inside the guard's scratch root (removed on exit) when there is one.
	if d, err := os.MkdirTemp(os.Getenv("AF_TESTGUARD_ROOT"), "memx-git-template-"); err == nil {
		os.Setenv("GIT_TEMPLATE_DIR", d)
	} else {
		fmt.Fprintln(os.Stderr, "memoryx tests: no empty git template dir, running with the stock one:", err)
	}
}
