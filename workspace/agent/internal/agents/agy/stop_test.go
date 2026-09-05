package agy

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The tmux-side plumbing of GracefulStop: true once the pane exits on its own after receiving
// the /exit line. Instead of the real agy this drives a fake TUI that reads lines and quits on
// /exit, exercising send-keys (C-u -> literal -> Enter) and the HasSession polling.
func TestGracefulStopEndsPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	t.Setenv("HOME", t.TempDir())
	name := fmt.Sprintf("agystop%d", os.Getpid())
	tn := session.TmuxName(name)
	_ = exec.Command("tmux", "kill-session", "-t", tn).Run()
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", tn, "sh", "-c",
		`while read line; do case "$line" in */exit) exit 0;; esac; done`).CombinedOutput(); err != nil {
		t.Skipf("tmux new-session failed (no server?): %v %s", err, out)
	}
	defer func() { _ = exec.Command("tmux", "kill-session", "-t", tn).Run() }()

	if !(agentImpl{}).GracefulStop(session.Meta{Name: name, Dir: t.TempDir()}) {
		t.Fatal("GracefulStop = false; want the fake TUI to exit on /exit")
	}
}

// A pane that does not act on /exit (the busy case) returns false after the grace period; the
// contract is that the caller then falls back to kill-session.
func TestGracefulStopTimesOutOnStuckPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	t.Setenv("HOME", t.TempDir())
	name := fmt.Sprintf("agystuck%d", os.Getpid())
	tn := session.TmuxName(name)
	_ = exec.Command("tmux", "kill-session", "-t", tn).Run()
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", tn, "sh", "-c",
		"while :; do sleep 1; done").CombinedOutput(); err != nil {
		t.Skipf("tmux new-session failed (no server?): %v %s", err, out)
	}
	defer func() { _ = exec.Command("tmux", "kill-session", "-t", tn).Run() }()

	if (agentImpl{}).GracefulStop(session.Meta{Name: name, Dir: t.TempDir()}) {
		t.Fatal("GracefulStop = true on a pane that never exits")
	}
}
