package main

// Tripwire against destructive tmux operations. Measured (docs/log/32 M1 E2E): a test
// agent instance's shutdown ran `tmux kill-server` against the shared default socket and
// killed every session in the Workspace, four times.
//
//  1. `kill-server` takes down the whole server, including panes this instance never
//     created, so product code may never use it. Stopping is always a `kill-session`
//     against a session this instance's meta owns (shutdown.go / halt).
//  2. Every tmux exec goes through tmuxx.Cmd. A direct exec.Command("tmux", …) escapes
//     the AF_TMUX_SOCKET socket isolation that keeps a second instance (dev / E2E)
//     apart, so it is banned too.
//
// _test.go files are out of scope: tests may drive their own socket and sessions, but
// must isolate with `tmux -L` (see e2e-tmux-socket-isolation).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func scanAgentSources(t *testing.T, visit func(path, src string)) {
	t.Helper()
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		visit(path, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestNoKillServer fails as soon as "kill-server" appears on a code line of product code.
// Comment lines are allowed: a reintroduction always shows up on a code line.
func TestNoKillServer(t *testing.T) {
	scanAgentSources(t, func(path, src string) {
		for i, line := range strings.Split(src, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if strings.Contains(line, "kill-server") {
				t.Errorf("%s:%d: kill-server is banned (kills sessions this instance does not own; use kill-session against owned sessions — see shutdown.go)", path, i+1)
			}
		}
	})
}

// TestTmuxExecFunnel bans calling exec.Command on tmux directly: tmuxx.Cmd is the only
// entry point that honours AF_TMUX_SOCKET.
func TestTmuxExecFunnel(t *testing.T) {
	scanAgentSources(t, func(path, src string) {
		if filepath.ToSlash(path) == "internal/tmuxx/tmuxx.go" {
			return // the funnel itself
		}
		for i, line := range strings.Split(src, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if strings.Contains(line, `exec.Command("tmux"`) {
				t.Errorf(`%s:%d: exec.Command("tmux", …) bypasses AF_TMUX_SOCKET scoping — use tmuxx.Cmd`, path, i+1)
			}
		}
	})
}
