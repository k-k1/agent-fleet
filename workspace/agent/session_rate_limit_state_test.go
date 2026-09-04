package main

// A session stuck on the rate-limit menu (claude's /rate-limit-options) must read as blocked
// rather than "in progress" — a real pane is raised on an isolated tmux server and driveState
// runs against it unchanged (docs/log/47 §4-3).
//
// The decision itself (frame → boolean) is pinned by internal/tmuxx's golden corpus. What is
// checked here is the wiring: that capture → classify → self-heal → returned state hangs
// together against claude's real meta. The production breakage was "the marker stays working
// forever", so the marker is checked alongside.

import (
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// paneShowing starts an isolated tmux session whose pane displays frame's contents and
// then stays alive, and returns the session meta for it.
func paneShowing(t *testing.T, name, frame string) session.Meta {
	t.Helper()
	// Check that the frame exists, here. Without it only `cat` fails while `new-session` still
	// succeeds, so callers wave an EMPTY pane through as the thing under test — which is exactly
	// what happened when the move changed the depth of the relative paths. A check that lists
	// the callers by hand can only notice the list shrinking, so the call site itself is guarded.
	if _, err := os.Stat(frame); err != nil {
		t.Fatalf("frame %s is missing: %v (suspect the depth of the relative path; left alone the pane shows nothing and the check stays green)", frame, err)
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tn := session.TmuxName(name)
	// Take a real pane's width: different wrapping changes how the footer/choice lines look.
	out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "-x", "200", "-y", "50",
		"sh", "-c", fmt.Sprintf("cat %q; sleep 60", frame)).CombinedOutput()
	if err != nil {
		t.Fatalf("new-session %s: %v\n%s", tn, err, out)
	}
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	// Wait for cat to finish drawing into the pane (capture-pane reads the drawn screen).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tmuxx.CapturePane(tn) != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return m
}

// tmuxSocketSeq is the serial number that gives each isolated tmux server a different name.
var tmuxSocketSeq atomic.Int64

// isolatedTmuxSocket returns a tmux socket name shared with nobody.
//
// A fixed name for the isolated socket makes the tests that fire `kill-server` race each other
// (the reason is in isolateAgentState's note). The rule for building the name lives in this
// one place because writing the same rule twice leaves one copy stale — and it did:
// `shutdown_isolation_test.go` assembles the same name itself and was never fixed.
//
// This function sits in this file for ownership reasons; in meaning it is a shared piece of
// the tmux isolation.
func isolatedTmuxSocket() string {
	return fmt.Sprintf("af-test-%d-%d", os.Getpid(), tmuxSocketSeq.Add(1))
}

func isolateAgentState(t *testing.T) {
	t.Helper()
	// Vary the socket name per test. It used to be a fixed `af-test-<pid>`, so every test in
	// the four files that use this isolation (plus shutdown_isolation_test.go, which builds the
	// same name) shared ONE tmux server. Each test's Cleanup fires `kill-server`, but tmux
	// returns as soon as it has taken the command and the server's shutdown is asynchronous. So
	// the next test's `new-session` reaches a dying server and fails with
	// `server exited unexpectedly` — a red with no visible reason, unrelated to the test body.
	//
	// The window widens with load (measured 2026-09-02: 0 occurrences at `-count=30` with no
	// load, 7 at `-count=40` under 6 CPU hogs; the failures were TestDriveStateIdlePaneNotBlocked
	// and TestDriveStateAuthValid, the same shape as real CI run 33584943716).
	//
	// The serial number is there for `-count=N`: with the test name alone, a round races the
	// kill-server of the PREVIOUS round of the same name.
	t.Setenv("AF_TMUX_SOCKET", isolatedTmuxSocket())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	// The status store sits directly under HOME (paths.AgentConfigDir) — never write a marker
	// into the real fleet.
	t.Setenv("HOME", t.TempDir())
	// Isolate claude's config/credentials too. HOME alone is not enough: in this container
	// CLAUDE_CONFIG_DIR points at the real fleet's tree, so the state decision (auth expired,
	// docs/log/47 §4-8) would depend on the real login's expiry.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	// kill-server is allowed only against a dedicated socket (dev/04 §4.11).
	t.Cleanup(func() { _ = tmuxx.Cmd("kill-server").Run() })
}

// TestDriveStateRateLimitModalBlocks: a pane on the rate-limit menu returns blocked, and the
// working marker that caused the stall is cleared.
func TestDriveStateRateLimitModalBlocks(t *testing.T) {
	isolateAgentState(t)
	m := paneShowing(t, "ratelimit1", "internal/tmuxx/testdata/footers/modal_rate_limit.txt")
	sid := session.UUID(m.Dir, m.Name)
	// The same starting point as production: the turn has begun (working) and no Stop fired.
	status.Persist(sid, "working")

	if got := sessionx.DriveState(m, true, true); got != agents.StateBlocked {
		t.Fatalf("driveState = %q, want %q (the rate-limit menu is not in progress)", got, agents.StateBlocked)
	}
	// Self-heal must have run. Its not running was the original bug: a marker left at working
	// keeps the reaper counting the session as busy and the container is never released.
	if st, ok := status.Read(sid); ok && st.State == "working" {
		t.Error("status marker is still working — self-heal did not run (the original stall, unchanged)")
	}
	// While the menu is up, any number of polls stay blocked (the state does not oscillate).
	if got := sessionx.DriveState(m, true, true); got != agents.StateBlocked {
		t.Errorf("second driveState = %q, want %q", got, agents.StateBlocked)
	}
}

// TestDriveStateIdlePaneNotBlocked: an ordinary idle pane must not be misread as blocked. A
// false positive treats a running session as stopped and rejects injection, so it is pinned too.
func TestDriveStateIdlePaneNotBlocked(t *testing.T) {
	isolateAgentState(t)
	m := paneShowing(t, "ratelimit2", "internal/tmuxx/testdata/footers/idle_bypass_hint.txt")
	if got := sessionx.DriveState(m, true, true); got == agents.StateBlocked {
		t.Fatalf("driveState = %q — an ordinary idle pane is being read as the rate-limit menu", got)
	}
}
