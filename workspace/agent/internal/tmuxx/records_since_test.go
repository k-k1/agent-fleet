package tmuxx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// startSleeper starts a process that stands in for a pane's process and returns it with the
// window its start fell in.
func startSleeper(t *testing.T) (pid int, after, before time.Time) {
	t.Helper()
	bin, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("no sleep binary: %v", err)
	}
	after = time.Now()
	cmd := exec.Command(bin, "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	before = time.Now()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid, after, before
}

// /proc reads a start to the clock tick (10 ms), and uptime moves in the same steps.
const tickSlack = 50 * time.Millisecond

func TestProcessStartIsWhenTheProcessStarted(t *testing.T) {
	pid, after, before := startSleeper(t)
	start, ok := ProcessStart(pid)
	if !ok {
		t.Fatal("ProcessStart: no answer for a live process")
	}
	if start.Before(after.Add(-tickSlack)) || start.After(before.Add(tickSlack)) {
		t.Errorf("ProcessStart = %v, want within [%v, %v]", start, after, before)
	}
	if _, ok := ProcessStart(-1); ok {
		t.Error("ProcessStart answered for a process that does not exist")
	}
}

// fakeServer puts a tmux on PATH whose one session reports line for its created stamp and pane
// process ("" = no such session).
func fakeServer(t *testing.T, line string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\n[ -n \"" + line + "\" ] || exit 1\nprintf '%s\\n' \"" + line + "\"\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("AF_TMUX_SOCKET", "")
}

// The bound is the pane process's start, not tmux's second-resolution stamp: a question the new
// CLI asks within the second the session was created is its own, and one a process killed in that
// second left behind is not. Without a readable pane process it falls back to the second after
// the stamp, and without a session there is no bound at all.
func TestCLIRecordsSinceIsThePaneProcessStart(t *testing.T) {
	pid, after, before := startSleeper(t)
	stamp := after.Truncate(time.Second)

	fakeServer(t, fmt.Sprintf("%d %d", stamp.Unix(), pid))
	got, ok := CLIRecordsSince("claude_rs")
	if !ok || got.Before(after.Add(-tickSlack)) || got.After(before.Add(tickSlack)) || got.Equal(stamp.Add(time.Second)) {
		t.Errorf("CLIRecordsSince = %v, %v; want the pane process start within [%v, %v]", got, ok, after, before)
	}

	fakeServer(t, fmt.Sprint(stamp.Unix()))
	if got, ok := CLIRecordsSince("claude_rs"); !ok || !got.Equal(stamp.Add(time.Second)) {
		t.Errorf("no pane process: CLIRecordsSince = %v, %v; want %v", got, ok, stamp.Add(time.Second))
	}

	fakeServer(t, "")
	if _, ok := CLIRecordsSince("claude_rs"); ok {
		t.Error("no session: CLIRecordsSince answered")
	}
}
