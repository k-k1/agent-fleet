//go:build drift

// Drift detection for the agy TUI pane (Tier 1), sibling of codex_pane_drift_test.go.
// The `drift` build tag keeps it out of a plain `go test ./...` because it needs a real agy
// binary and a real tmux.
//
// What it covers is paneMode's agy branch, which depends on the composer footer's fixed
// strings ("? for shortcuts" / "esc to cancel" on the left, "<model>" or "plan · <model>"
// on the right). That footer IS the chat mirror's launch-seed readiness gate: if it
// changes, the Console can no longer detect that agy has drawn its composer, and first
// prompts get eaten by the "Signing in..." boot screen again.
//
// Unlike codex, a dummy credential cannot reach the composer: agy runs a real
// "Signing in..." at startup and stops on the login chooser when unauthenticated. So a
// really signed-in agy is a precondition, and the test skips without one. HOME stays the
// real home (agy is hard-wired to ~/.gemini). The working dir is the same fixed path every
// run, so BuildLaunch's pre-trust does not grow trustedWorkspaces in the real settings.json
// once per run. tmux is isolated onto a dedicated socket as dev/04 §4.11 requires: `-L` for
// the calls this test makes itself, AF_TMUX_SOCKET for paneMode (product code, via tmuxx).
package main

import (
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestDriftAgyPaneMode drives the REAL production launch plan (agy.BuildLaunch —
// same flags, same pre-trust path) in a real tmux pane for both launch modes and
// asserts paneMode reads the composer footer: "Default" on a plain launch,
// "Plan" with mode=plan ("plan · <model>" footer, measured on v1.1.4).
func TestDriftAgyPaneMode(t *testing.T) {
	needBin(t, "agy")
	needBin(t, "tmux")
	if !agy.SignedIn() {
		if os.Getenv("E2E_REQUIRE") == "1" {
			t.Fatal("agy is not signed in (E2E_REQUIRE=1 requires the real TUI credential)")
		}
		t.Skip("agy is not signed in (needs a real token — the boot-time sign-in can't be faked)")
	}

	// Dedicated tmux server (dev/04 §4.11): both the test's own tmux calls (-L)
	// and paneMode's tmuxx path (AF_TMUX_SOCKET) target it, so nothing touches
	// the shared default socket. kill-server is allowed on this socket only.
	sock := fmt.Sprintf("af-drift-agy-%d", os.Getpid())
	t.Setenv("AF_TMUX_SOCKET", sock)
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", sock, "kill-server").Run() })

	work := "/tmp/af-agy-drift"
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, mode, want string }{
		{"default", "", "Default"},
		{"plan", "plan", "Plan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := session.Meta{Name: "drift-agy-" + tc.name, Dir: work, Kind: session.KindAgy, Mode: tc.mode}
			plan, err := agy.New().BuildLaunch(m, agents.LaunchOpts{})
			if err != nil {
				t.Fatalf("BuildLaunch: %v", err)
			}
			tn := "drift-agy-" + tc.name
			if out, err := exec.Command("tmux", "-L", sock, "new-session", "-d", "-s", tn,
				"-x", "200", "-y", "50", "-c", plan.Cwd, plan.Program).CombinedOutput(); err != nil {
				t.Fatalf("tmux new-session: %v: %s", err, out)
			}
			defer func() { _ = exec.Command("tmux", "-L", sock, "kill-session", "-t", tn).Run() }()

			if got := awaitPaneModeKind(t, session.KindAgy, tn, tc.want); got != tc.want {
				out, _ := exec.Command("tmux", "-L", sock, "capture-pane", "-p", "-t", tn).Output()
				t.Fatalf("paneMode = %q, want %q.\nagy's composer footer no longer matches the "+
					"agy branch in paneMode — the mirror's launch-seed readiness gate is blind "+
					"again and first prompts get eaten by the boot screen.\npane:\n%s",
					got, tc.want, out)
			}
		})
	}
}

// awaitPaneModeKind polls paneMode until it reports want (boot includes a real
// network sign-in, so the deadline stays generous like codex's).
func awaitPaneModeKind(t *testing.T, kind, tn, want string) string {
	t.Helper()
	last := ""
	advancedAgyTheme := false
	advancedAgyToS := false
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		// agy 1.1.7 adds a one-time color-scheme chooser before its signed-in
		// composer.  It is not an auth or readiness state: accept its selected
		// default once, then keep this contract focused on the production
		// composer footer.  The setting persists in the real HOME, so the plan
		// subtest (and later launches) must not see it again.
		if kind == session.KindAgy && (!advancedAgyTheme || !advancedAgyToS) {
			out, _ := exec.Command("tmux", "-L", os.Getenv("AF_TMUX_SOCKET"),
				"capture-pane", "-p", "-t", tn).Output()
			pane := string(out)
			if !advancedAgyTheme && strings.Contains(pane, "Choose your color scheme:") {
				if err := exec.Command("tmux", "-L", os.Getenv("AF_TMUX_SOCKET"),
					"send-keys", "-t", tn, "Enter").Run(); err != nil {
					t.Fatalf("confirm agy color scheme: %v", err)
				}
				advancedAgyTheme = true
				time.Sleep(500 * time.Millisecond)
				continue
			}
			// Token-only CI restores no agy settings.  Complete the same initial
			// ToS step the production connection flow drives: toggle Interactions
			// data collection OFF, then select Done.  A normal AF login has already
			// done this, so the screen simply never appears there.
			if !advancedAgyToS && strings.Contains(pane, "Terms of Service & Data Use") {
				for _, key := range []string{"Enter", "Down", "Right", "Enter"} {
					if err := exec.Command("tmux", "-L", os.Getenv("AF_TMUX_SOCKET"),
						"send-keys", "-t", tn, key).Run(); err != nil {
						t.Fatalf("confirm agy terms: %v", err)
					}
					time.Sleep(250 * time.Millisecond)
				}
				advancedAgyToS = true
				continue
			}
		}
		if last = sessionx.PaneMode(kind, tn); last == want {
			return last
		}
		time.Sleep(250 * time.Millisecond)
	}
	return last
}
