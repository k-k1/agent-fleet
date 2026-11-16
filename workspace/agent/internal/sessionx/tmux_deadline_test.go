package sessionx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Typing into the pane goes through tmux: a tmux that never answers must fail the delivery's
// step at the deadline, with an error that still says why, rather than hold the session's
// input chain.
func TestTypeLineAndSubmitEndsWhenTmuxHangs(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pids")
	script := "#!/bin/sh\necho $$ >> " + pidFile + "\nsleep 600 &\necho $! >> " + pidFile + "\nwait\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AF_TMUX_SOCKET", "")
	t.Cleanup(func() {
		b, _ := os.ReadFile(pidFile)
		for _, f := range strings.Fields(string(b)) {
			if pid, err := strconv.Atoi(f); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	old := tmuxx.OneShotTimeout
	tmuxx.OneShotTimeout = 200 * time.Millisecond
	t.Cleanup(func() { tmuxx.OneShotTimeout = old })

	done := make(chan error, 1)
	go func() { done <- typeLineAndSubmit("deadline-test", "%0", "hello") }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want a deadline error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("typeLineAndSubmit still blocked on a hung tmux")
	}
	if b, _ := os.ReadFile(pidFile); len(b) == 0 {
		t.Fatal("the fake tmux never ran")
	}
}
