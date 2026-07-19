package agy

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// GracefulStop の tmux 側 plumbing: /exit（の行）を受けて pane が自死したら
// true。本物の agy は使わず、行を読んで /exit で終了する fake TUI で
// send-keys（C-u → literal → Enter）と HasSession ポーリングを検証する。
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

// pane が /exit に応じない（busy 相当）場合は猶予後 false — 呼び出し側が
// kill-session にフォールバックする契約。
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
