package main

// Regression test for what shutdown selects (docs/log/32 M1, the permanent fix after the E2E
// incident): shutdown may touch only "own meta ∩ live", never another instance's sessions
// sharing the same tmux server (live sessions with no meta of ours). Checked against a real
// tmux on a dedicated server isolated with AF_TMUX_SOCKET.

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// TestTmuxCmdSocketScope: AF_TMUX_SOCKET rides along as -L on every tmux call (an argv
// check, no server needed).
func TestTmuxCmdSocketScope(t *testing.T) {
	t.Setenv("AF_TMUX_SOCKET", "af-test-sock")
	got := tmuxx.Cmd("list-sessions").Args
	want := []string{"tmux", "-L", "af-test-sock", "list-sessions"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("Cmd args = %v, want %v", got, want)
	}
	t.Setenv("AF_TMUX_SOCKET", "")
	if got := tmuxx.Cmd("list-sessions").Args; strings.Join(got, " ") != "tmux list-sessions" {
		t.Fatalf("Cmd args (no socket) = %v", got)
	}
}

// TestOwnedLiveSessionsScopedToOwnMetas puts two sessions on the isolated socket, one with
// our own meta and one without (standing in for another instance), and checks that
// shutdown's selection returns only the former.
func TestOwnedLiveSessionsScopedToOwnMetas(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	// Never pin the socket name. This used to build `af-test-<pid>` itself, and a pid is
	// constant within a process, so it shared one tmux server with the previous iteration
	// of `-count=N` (and with any other test using the same name). Each iteration's
	// Cleanup fires `kill-server`, but tmux returns as soon as it accepts the command and
	// the server exits asynchronously, so the next `new-session` reaches a dying server
	// and fails with `server exited unexpectedly`. Measured 2026-09-02 (idle, `-count=30`):
	// 11/30 failures → 0/30. Name construction is centralized in isolatedTmuxSocket
	// (session_rate_limit_state_test.go).
	sock := isolatedTmuxSocket()
	t.Setenv("AF_TMUX_SOCKET", sock)
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	// kill-server is allowed only against a dedicated socket (dev/04 §4.11).
	t.Cleanup(func() { _ = tmuxx.Cmd("kill-server").Run() })

	for _, name := range []string{"owned1", "foreign1"} {
		tn := session.TmuxName(name)
		if out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "sleep", "60").CombinedOutput(); err != nil {
			t.Fatalf("new-session %s: %v\n%s", tn, err, out)
		}
	}
	session.WriteMeta(session.Meta{Name: "owned1", Dir: t.TempDir(), Kind: session.KindShell})
	// Also mix in a stopped one: a meta with no pane is out of scope too.
	session.WriteMeta(session.Meta{Name: "stopped1", Dir: t.TempDir(), Kind: session.KindShell})

	owned := ownedLiveSessions()
	if len(owned) != 1 || owned[0] != session.TmuxName("owned1") {
		t.Fatalf("ownedLiveSessions = %v, want [claude_owned1] (foreign/live-without-meta and meta-without-live must be excluded)", owned)
	}
}
