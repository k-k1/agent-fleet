package sessionx

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// typing into the pane goes through tmux: a tmux that never answers must fail the delivery's
// step at the deadline rather than hold the session's input chain.
func TestTypePromptTextEndsWhenTmuxHangs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\nsleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AF_TMUX_SOCKET", "")
	old := tmuxx.OneShotTimeout
	tmuxx.OneShotTimeout = 200 * time.Millisecond
	t.Cleanup(func() { tmuxx.OneShotTimeout = old })

	done := make(chan error, 1)
	go func() { done <- typeLineAndSubmit("deadline-test", "%0", "hello") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("typeLineAndSubmit returned no error against a hung tmux")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("typeLineAndSubmit still blocked on a hung tmux")
	}
}
