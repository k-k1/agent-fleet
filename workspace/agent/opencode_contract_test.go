//go:build contract

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
//	go test -tags contract -run TestContractOpencodeTUI ./
package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// tmuxSession starts prog in a detached pane under an isolated HOME. The caller isolates the
// tmux server first (testguard.IsolateTmux): a send-keys on the default socket would reach the
// workspace's live sessions, and the test guard fails the run for it.
func tmuxSession(t *testing.T, name, dir, prog string) {
	t.Helper()
	_ = tmuxx.Cmd("kill-session", "-t", name).Run()
	cmd := tmuxx.Cmd("new-session", "-d", "-s", name, "-x", "120", "-y", "40", "-c", dir, prog)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = tmuxx.Cmd("kill-session", "-t", name).Run() })
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

// opencodeIs2 reports whether the opencode on PATH is 2.x ("opencode v2.0.29"; 1.x prints "1.19.0").
func opencodeIs2(t *testing.T) bool {
	t.Helper()
	return strings.HasPrefix(strings.TrimPrefix(opencodeVer(t), "opencode v"), "2.")
}

func opencodeVer(t *testing.T) string {
	t.Helper()
	out, _ := exec.Command("opencode", "--version").Output()
	return strings.TrimSpace(string(out))
}

// isolatedHomeEnv returns the env assignments that pin opencode's config and state roots
// inside home. Overriding HOME alone is NOT enough: opencode resolves its global config
// through XDG_CONFIG_HOME (paths.go OpencodeConfigDir), and a GitHub Actions runner
// **exports XDG_CONFIG_HOME=/home/runner/.config**. With only HOME replaced, the runner's
// real (empty) config root wins, the "global" source of a config test silently contributes
// nothing, and the test reports upstream drift that does not exist (verified: setting
// XDG_CONFIG_HOME elsewhere reproduces it on 1.18.9).
// The data/cache roots get the same treatment so the isolation the tests claim is real.
func isolatedHomeEnv(home string) []string {
	return []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
	}
}

// TestContractOpencodeTUIPaneMode boots a real opencode TUI the way the fleet does and
// asserts paneMode still reads the composer status line — both that it resolves at all
// (the launch-seed readiness signal) and that it tracks the live agent (the mode chip).
func TestContractOpencodeTUIPaneMode(t *testing.T) {
	requireBins(t, "opencode", "tmux")
	testguard.IsolateTmux(t)
	home, dir := t.TempDir(), t.TempDir()
	name := "af-contract-opencode"
	// Prefix the env onto the command itself: tmux -e sets the session environment, which
	// does NOT reach the pane's process (the same reason buildProgram prefixes env).
	launch := " opencode --auto"
	if opencodeIs2(t) {
		// buildProgram launches a 2.x TUI with --standalone; a bare one would start the per-user
		// background service on its fixed port.
		launch = " opencode --standalone --auto"
	}
	tmuxSession(t, name, dir, strings.Join(isolatedHomeEnv(home), " ")+launch)

	ver := opencodeVer(t)
	// Boot is slow (splash → composer, ~30s cold); paneMode is exactly the readiness
	// signal seedPrompt polls, so waiting on it here mirrors production.
	var got string
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if got = sessionx.PaneMode(session.KindOpencode, name); got != "" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if got == "" {
		pane, _ := tmuxx.Cmd("capture-pane", "-p", "-t", name).Output()
		t.Fatalf("paneMode never resolved for opencode %s — opencodeStatusAgentRe no longer matches the composer "+
			"status line, so the Console shows no mode chip and the launch seed falls back to a fixed beat.\npane:\n%s", ver, pane)
	}
	if got != "Build" {
		t.Errorf("paneMode = %q on a default launch, want \"Build\" (opencode's non-plan agent) — opencode %s", got, ver)
	}

	// Tab cycles the agent on 1.x; the chip must follow the TUI's own state. On 2.0.29 Tab does
	// nothing at the start screen (measured), so there is no key to drive and only the first
	// reading above applies.
	if opencodeIs2(t) {
		return
	}
	if err := tmuxx.Cmd("send-keys", "-t", name, "Tab").Run(); err != nil {
		t.Fatalf("send-keys Tab: %v", err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if got = sessionx.PaneMode(session.KindOpencode, name); got == "Plan" {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	pane, _ := tmuxx.Cmd("capture-pane", "-p", "-t", name).Output()
	t.Errorf("after Tab paneMode = %q, want \"Plan\" — the agent/mode readout moved in opencode %s.\npane:\n%s", got, ver, pane)
}
