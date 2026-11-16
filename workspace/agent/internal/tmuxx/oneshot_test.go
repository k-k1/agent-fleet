package tmuxx

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeHungTmux puts a "tmux" first on PATH that never answers, and returns the file its pid
// is written to. The script also forks a child that inherits the output pipes, as a real
// wedged client might, so the test covers a deadline that has to cut the pipes too.
func fakeHungTmux(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	script := "#!/bin/sh\necho $$ > " + pidFile + "\nsleep 600 &\nsleep 600\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AF_TMUX_SOCKET", "")
	return pidFile
}

// A tmux that stops answering must end the call with an error at the deadline, not block
// its caller forever.
func TestOneShotCallsEndAtTheDeadline(t *testing.T) {
	fakeHungTmux(t)
	old := OneShotTimeout
	OneShotTimeout = 200 * time.Millisecond
	t.Cleanup(func() { OneShotTimeout = old })

	calls := map[string]func() error{
		"Run":                 func() error { return Run("send-keys", "x") },
		"Output":              func() error { _, err := Output("capture-pane", "-p"); return err },
		"CombinedOutput":      func() error { _, err := CombinedOutput("send-keys", "x"); return err },
		"CombinedOutputStdin": func() error { _, err := CombinedOutputStdin("hi", "load-buffer", "-"); return err },
		"HasSession":          func() error { return errIf(HasSession("x")) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- call() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("a hung tmux call returned no error")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("call still blocked on a hung tmux long after the deadline")
			}
		})
	}
}

func errIf(ok bool) error {
	if ok {
		return nil
	}
	return os.ErrDeadlineExceeded
}
