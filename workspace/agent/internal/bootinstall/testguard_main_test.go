package bootinstall

import (
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

// TestMain keeps every test of this package off the workspace's live tmux server, HOME and
// Agent (testguard).
func TestMain(m *testing.M) {
	// The supervisor tests shorten its grace through superviseGraceEnv, make its kill fail
	// through superviseNoKillEnv and pad its report through supervisePadEnv; only this re-exec
	// entry reads them, so a production supervisor cannot inherit any.
	grace := superviseGrace
	if d, err := time.ParseDuration(os.Getenv(superviseGraceEnv)); err == nil {
		grace = d
	}
	kill := killPid
	if os.Getenv(superviseNoKillEnv) != "" {
		kill = func(int) {} // stands in for a child in uninterruptible I/O, which no test can make
	}
	var report io.Writer = os.Stdout
	if os.Getenv(supervisePadEnv) != "" {
		report = padAfter{os.Stdout}
	}
	maybeSupervise(os.Args[1:], grace, kill, report)
	if os.Getenv(npmCallerHome) != "" { // after the supervisor check: the supervisor inherits the env
		os.Exit(npmCallerHelper())
	}
	os.Exit(testguard.Run(m, nil))
}

// padAfter follows every write of the supervisor's report with 64 KiB of lines naming no
// process, so the real entries come first in a report far past any tail a reader might keep.
type padAfter struct{ w io.Writer }

func (p padAfter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	for i := 0; i < 4096 && err == nil; i++ {
		_, err = fmt.Fprintf(p.w, "stuck %d 1\n", 999000000+i)
	}
	return n, err
}
