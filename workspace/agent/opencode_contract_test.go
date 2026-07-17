//go:build clicontract

// opencode TUI contract test — the drift alarm for the pane-scraping probes, which had
// ZERO test coverage: paneMode (session_io.go) and the footer string it anchors on.
//
// These are the most fragile opencode dependency left, because they read TUI text rather
// than the store: the status line's shape is not a contract anyone promised us. The blast
// radius is smaller than a false-idle (paneMode drives the Console's mode chip and the
// launch-seed readiness wait, so breakage means a missing chip and a ~30s seed delay, not
// a lost prompt), but nothing else would notice.
//
// MUST launch with `--auto`, exactly as buildProgram does. A bare `opencode` renders
// "Build · <model>" with no `auto` token, and opencodeStatusAgentRe requires " auto ·" —
// testing without the flag reproduces a bug that does not exist in the fleet (verified:
// 1.17.13 / 1.17.18 / 1.18.3 all render identically, with and without the flag).
//
//	go test -tags clicontract -run TestContractOpencodeTUI ./
package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// tmuxSession starts prog in a detached pane on the DEFAULT tmux server (paneMode shells
// out to plain `tmux`, so a private -L socket can't be used) under an isolated HOME. The
// name is test-specific and killed on cleanup, so a live fleet's sessions are untouched.
func tmuxSession(t *testing.T, name, dir, prog string) {
	t.Helper()
	_ = exec.Command("tmux", "kill-session", "-t", name).Run()
	cmd := exec.Command("tmux", "new-session", "-d", "-s", name, "-x", "120", "-y", "40", "-c", dir, prog)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })
}

func requireBins(t *testing.T, bins ...string) {
	t.Helper()
	for _, b := range bins {
		if _, err := exec.LookPath(b); err != nil {
			if os.Getenv("E2E_REQUIRE") == "1" {
				t.Fatalf("%s not on PATH and E2E_REQUIRE=1: %v", b, err)
			}
			t.Skipf("%s not on PATH — TUI contract test skipped (set E2E_REQUIRE=1 to demand it)", b)
		}
	}
}

func opencodeVer(t *testing.T) string {
	t.Helper()
	out, _ := exec.Command("opencode", "--version").Output()
	return strings.TrimSpace(string(out))
}

// TestContractOpencodeTUIPaneMode boots a real opencode TUI the way the fleet does and
// asserts paneMode still reads the composer status line — both that it resolves at all
// (the launch-seed readiness signal) and that it tracks the live agent (the mode chip).
func TestContractOpencodeTUIPaneMode(t *testing.T) {
	requireBins(t, "opencode", "tmux")
	home, dir := t.TempDir(), t.TempDir()
	name := "af-contract-opencode"
	// Prefix HOME onto the command itself: tmux -e sets the session environment, which
	// does NOT reach the pane's process (the same reason buildProgram prefixes env).
	tmuxSession(t, name, dir, "HOME="+home+" opencode --auto")

	ver := opencodeVer(t)
	// Boot is slow (splash → composer, ~30s cold); paneMode is exactly the readiness
	// signal seedPrompt polls, so waiting on it here mirrors production.
	var got string
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if got = paneMode(session.KindOpencode, name); got != "" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if got == "" {
		pane, _ := exec.Command("tmux", "capture-pane", "-p", "-t", name).Output()
		t.Fatalf("paneMode never resolved for opencode %s — opencodeStatusAgentRe no longer matches the composer "+
			"status line, so the Console shows no mode chip and the launch seed falls back to a fixed beat.\npane:\n%s", ver, pane)
	}
	if got != "Build" {
		t.Errorf("paneMode = %q on a default launch, want \"Build\" (opencode's non-plan agent) — opencode %s", got, ver)
	}

	// Tab cycles the agent; the chip must follow the TUI's own state.
	if err := exec.Command("tmux", "send-keys", "-t", name, "Tab").Run(); err != nil {
		t.Fatalf("send-keys Tab: %v", err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if got = paneMode(session.KindOpencode, name); got == "Plan" {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	pane, _ := exec.Command("tmux", "capture-pane", "-p", "-t", name).Output()
	t.Errorf("after Tab paneMode = %q, want \"Plan\" — the agent/mode readout moved in opencode %s.\npane:\n%s", got, ver, pane)
}
