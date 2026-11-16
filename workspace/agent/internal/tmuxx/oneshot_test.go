package tmuxx

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
)

// fakeHungTmux puts a "tmux" first on PATH that never answers and returns the file its pids
// go to (the script's own and its child's, so the call is known to have started). The child
// inherits the output pipes, as a wedged client's might, so the deadline has to cut the pipes
// too. Cleanup kills exactly those pids.
func fakeHungTmux(t *testing.T) string {
	t.Helper()
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
	return pidFile
}

func started(t *testing.T, pidFile string) {
	t.Helper()
	if b, _ := os.ReadFile(pidFile); len(b) == 0 {
		t.Fatal("the fake tmux never ran: the call failed for another reason")
	}
}

// A tmux that stops answering must end the call with a timeout error at the deadline, not
// block its caller forever.
func TestOneShotCallsEndAtTheDeadline(t *testing.T) {
	pidFile := fakeHungTmux(t)
	old := OneShotTimeout
	OneShotTimeout = 200 * time.Millisecond
	t.Cleanup(func() { OneShotTimeout = old })

	calls := map[string]func() error{
		"Run":            func() error { return Run("send-keys", "x") },
		"Output":         func() error { _, err := Output("capture-pane", "-p"); return err },
		"CombinedOutput": func() error { _, err := CombinedOutput("send-keys", "x"); return err },
		"Stdin":          func() error { _, err := CombinedOutputStdin("hi", "load-buffer", "-"); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- call() }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("err = %v, want a deadline error", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("call still blocked on a hung tmux long after the deadline")
			}
			started(t, pidFile)
		})
	}
}

func TestHasSessionAnswersFalseOnAHungTmux(t *testing.T) {
	pidFile := fakeHungTmux(t)
	old := OneShotTimeout
	OneShotTimeout = 200 * time.Millisecond
	t.Cleanup(func() { OneShotTimeout = old })
	done := make(chan bool, 1)
	go func() { done <- HasSession("x") }()
	select {
	case got := <-done:
		if got {
			t.Fatal("HasSession true on a hung tmux")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("HasSession still blocked")
	}
	started(t, pidFile)
}
