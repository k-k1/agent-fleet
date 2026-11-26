package tmuxx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fakeTmux puts a "tmux" first on PATH that runs script.
func fakeTmux(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AF_TMUX_SOCKET", "")
}

// A delete decides on LiveSessionNamesChecked, so only tmux's own "nothing there" may read as an
// empty set; a server it cannot reach or any other failure is an error, not "nothing runs".
func TestLiveSessionNamesCheckedTellsAbsentFromFailed(t *testing.T) {
	for _, c := range []struct {
		name, script string
		want         []string
		fails        bool
	}{
		{"listed", "echo " + session.TmuxPrefix + "aa1\necho other\necho " + session.TmuxPrefix + "bb2\n", []string{"aa1", "bb2"}, false},
		{"no server", "echo 'no server running on /tmp/tmux-1/default' >&2\nexit 1\n", nil, false},
		{"no socket", "echo 'error connecting to /tmp/tmux-1/default (No such file or directory)' >&2\nexit 1\n", nil, false},
		{"permission denied", "echo 'error connecting to /tmp/tmux-1/default (Permission denied)' >&2\nexit 1\n", nil, true},
		{"other exit", "echo 'boom' >&2\nexit 2\n", nil, true},
		{"exit 1 without a word", "exit 1\n", nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeTmux(t, c.script)
			live, err := LiveSessionNamesChecked()
			if (err != nil) != c.fails {
				t.Fatalf("err = %v, want failure %v", err, c.fails)
			}
			if len(live) != len(c.want) {
				t.Fatalf("live = %v, want %v", live, c.want)
			}
			for _, n := range c.want {
				if !live[n] {
					t.Fatalf("live = %v, want %v", live, c.want)
				}
			}
			if got := LiveSessionNames(); len(got) != len(c.want) {
				t.Fatalf("LiveSessionNames = %v, want %v", got, c.want)
			}
		})
	}
}

// With no tmux binary no session can have been started, so the set is empty and no error.
func TestLiveSessionNamesCheckedWithoutTmux(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("AF_TMUX_SOCKET", "")
	if live, err := LiveSessionNamesChecked(); err != nil || len(live) != 0 {
		t.Fatalf("live = %v err = %v, want an empty set", live, err)
	}
}
