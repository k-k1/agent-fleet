//go:build contract

// Drift detection for the codex TUI pane (Tier 1). The `contract` build tag keeps it out of
// an ordinary `go test ./...`, since it needs the real codex binary and a real tmux.
// Sibling: internal/agents/codex/drift_test.go (features / config / hooks).
//
// What this covers is paneMode's codex branch, which had no test at all. The decision
// depends on fixed strings in codex's footer (`<model> <effort> · <cwd>` and "Plan mode"),
// i.e. it breaks the same way claude's false-idle did — the footer spec changes between
// versions — and there was not even a fixture for it.
//
// No authentication needed: the footer is drawn right after launch and the mode switch
// (shift+tab) is handled entirely inside the TUI, so no model call happens. A dummy
// auth.json IS required though (measured: with no credential the TUI shows the login
// chooser and draws neither the composer nor the footer). The key is a dummy, so nothing is
// billed. The directory-trust gate is cleared through the production path
// (BuildLaunch → ensureFolderTrusted).
package main

import (
	"errors"
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

func needBin(t *testing.T, bin string) {
	t.Helper()
	if _, err := exec.LookPath(bin); err != nil {
		if os.Getenv("E2E_REQUIRE") == "1" {
			t.Fatalf("%s not on PATH and E2E_REQUIRE=1: %v", bin, err)
		}
		t.Skipf("%s not on PATH (set E2E_REQUIRE=1 to make this fatal): %v", bin, err)
	}
}

// TestDriftCodexPaneMode drives the REAL production launch plan (codex.BuildLaunch —
// same bypass flags, same -c overrides, same directory-trust preparation) in a real
// tmux pane and asserts paneMode still reads the footer correctly in both modes.
//
// A footer change upstream is exactly the class of drift that broke claude's busy
// detection; for codex it would silently strip the Console's mode chip instead.
func TestDriftCodexPaneMode(t *testing.T) {
	needBin(t, "codex")
	needBin(t, "tmux")
	testguard.IsolateTmux(t)

	home := t.TempDir()
	work := t.TempDir()

	// Dummy key: skips the login screen so the composer (and its footer) renders.
	// It is not a real credential — any actual turn would 401, and we never start one.
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"),
		[]byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-af-drift-test-not-a-real-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// HOME must be redirected before BuildLaunch: ensureFolderTrusted writes the
	// directory-trust section into $HOME/.codex/config.toml.
	t.Setenv("HOME", home)

	m := session.Meta{Name: "drift-pane", Dir: work, Kind: session.KindCodex}
	plan, err := codex.New().BuildLaunch(m, agents.LaunchOpts{})
	if err != nil {
		t.Fatalf("BuildLaunch: %v", err)
	}

	// tmux panes inherit the tmux SERVER's env, not this process's, so HOME is pinned
	// on the command itself.
	// pid-scoped so a concurrent run (or a leftover from a killed one) can't collide.
	// The name is outside the fleet's own claude_*/codex_* namespace by construction.
	tn := fmt.Sprintf("af-drift-codex-%d", os.Getpid())
	_ = tmuxx.Cmd("kill-session", "-t", tn).Run()
	launch := "env HOME=" + home + " " + plan.Program
	if out, err := tmuxx.Cmd("new-session", "-d", "-s", tn,
		"-x", "200", "-y", "50", "-c", plan.Cwd, launch).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}
	// Deferred, so it runs before t.TempDir's cleanup: codex keeps writing into
	// $HOME/.codex/.tmp (a background plugin clone) after kill-session returns, and
	// RemoveAll racing it fails a test whose assertions all passed (measured: 2 of 11 runs).
	pgid := panePID(tn)
	defer func() {
		_ = tmuxx.Cmd("kill-session", "-t", tn).Run()
		awaitProcessGroupGone(t, pgid)
	}()

	// Default mode: footer is "<model> <effort> · <cwd>" with no "Plan mode" label.
	if got := awaitPaneMode(t, tn, "Default"); got != "Default" {
		diagnosePane(t, tn) // never blame the footer for a TUI that never drew
		t.Fatalf("paneMode = %q, want \"Default\".\ncodex's composer footer no longer matches "+
			"codexFooterEffortRe — the Console's mode chip is now blank for every codex "+
			"session.\npane:\n%s", got, capturePane(tn))
	}

	// Plan mode: shift+tab cycles. Up through 0.144 the footer gained a "Plan mode"
	// label; 0.145 removed that label but prints a trusted TUI system message confirming
	// the transition. Production persists mirror-driven mode changes in session meta.
	before := capturePane(tn)
	got, pane, presses := awaitCodexPlanTransition(t, tn)
	if got != "Plan" && !strings.Contains(pane, "for Plan mode.") {
		diagnosePane(t, tn)
		t.Fatalf("after %d shift+tab presses neither the footer nor the TUI system message "+
			"confirms Plan mode.\npaneMode=%q\npane before the first press:\n%s\npane:\n%s\n"+
			"warnings (f2):\n%s", presses, got, before, pane, codexWarnings(tn))
	}
	if presses > 1 {
		// Not a failure, but the evidence for why a single press is not enough.
		t.Logf("Plan mode took %d shift+tab presses; pane before the first:\n%s", presses, before)
	}
	t.Log("ok: codex composer readiness and Plan transition survive the production launch plan")
}

// codexPlanRepress is how long a shift+tab may go unanswered before it is sent again. A
// press codex dropped (one that arrived while something drawn at startup held the input)
// otherwise fails the whole test. A re-press cannot toggle a slow first one back out of
// Plan unseen: the "for Plan mode." system message stays in the pane once printed.
const codexPlanRepress = 10 * time.Second

// awaitCodexPlanTransition presses shift+tab and polls until the pane confirms Plan mode,
// pressing again every codexPlanRepress. It returns the last mode and pane seen and the
// number of presses sent.
func awaitCodexPlanTransition(t *testing.T, tn string) (string, string, int) {
	t.Helper()
	lastMode, lastPane, presses := "", "", 0
	var pressed time.Time
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if presses == 0 || time.Since(pressed) >= codexPlanRepress {
			if out, err := tmuxx.Cmd("send-keys", "-t", tn, "BTab").CombinedOutput(); err != nil {
				t.Fatalf("send-keys BTab: %v: %s", err, out)
			}
			presses++
			pressed = time.Now()
		}
		time.Sleep(250 * time.Millisecond)
		lastMode = sessionx.PaneMode(session.KindCodex, tn)
		lastPane = capturePane(tn)
		if lastMode == "Plan" || strings.Contains(lastPane, "for Plan mode.") {
			return lastMode, lastPane, presses
		}
	}
	return lastMode, lastPane, presses
}

// codexWarnings opens the warnings view the footer advertises ("f2 to view") and returns
// it. Only called on the way to a failure: it changes what the pane shows.
func codexWarnings(tn string) string {
	if err := tmuxx.Cmd("send-keys", "-t", tn, "F2").Run(); err != nil {
		return "(send-keys F2 failed: " + err.Error() + ")"
	}
	time.Sleep(time.Second)
	return capturePane(tn)
}

// panePID is the pid of the process tmux started in the session's pane, which is also
// its process group: tmux makes each pane a session leader.
func panePID(tn string) int {
	out, err := tmuxx.Cmd("display-message", "-p", "-t", tn, "#{pane_pid}").Output()
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return pid
}

// awaitProcessGroupGone waits until no process is left in pgid, escalating to SIGKILL
// when the group outlives kill-session's SIGHUP. It never fails the test: a straggler
// costs a noisy cleanup at worst, not a wrong verdict.
func awaitProcessGroupGone(t *testing.T, pgid int) {
	t.Helper()
	if pgid <= 1 {
		return
	}
	for _, sig := range []syscall.Signal{0, syscall.SIGKILL} {
		if sig != 0 {
			_ = syscall.Kill(-pgid, sig)
		}
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	t.Logf("process group %d still alive after SIGKILL", pgid)
}

// diagnosePane fails with the REAL reason when the pane never reached the composer, so
// a slow start or a launch gate is never misreported as "codex changed its footer".
// (Measured: the footer normally draws in well under a second, but a loaded host has
// been seen to blow past a 45s budget — hence the generous deadline plus this triage.)
func diagnosePane(t *testing.T, tn string) {
	t.Helper()
	s := capturePane(tn)
	switch {
	case strings.TrimSpace(s) == "":
		t.Fatalf("the codex pane is still empty: the TUI never drew (slow host, or it died at " +
			"launch). This says nothing about the footer format — re-run before suspecting drift.")
	case strings.Contains(s, "Do you trust"):
		t.Fatalf("codex is parked on the directory-trust gate — production's ensureFolderTrusted "+
			"no longer satisfies it, so every codex TUI session would stall at launch.\n%s", s)
	case strings.Contains(s, "Sign in with ChatGPT"), strings.Contains(s, "Provide your own API key"):
		t.Fatalf("codex is showing its login screen: the dummy credential no longer skips "+
			"onboarding (auth.json format changed?). This is a harness break, not footer drift.\n%s", s)
	}
}

// awaitPaneMode polls paneMode until it reports want (the TUI needs a moment to draw,
// and again to redraw after a mode switch). Returns the last value seen. The deadline is
// deliberately generous: a flaky drift detector gets ignored, which defeats the purpose.
func awaitPaneMode(t *testing.T, tn, want string) string {
	t.Helper()
	last := ""
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if last = sessionx.PaneMode(session.KindCodex, tn); last == want {
			return last
		}
		time.Sleep(250 * time.Millisecond)
	}
	return last
}

func capturePane(tn string) string {
	out, err := tmuxx.Cmd("capture-pane", "-p", "-t", tn).Output()
	if err != nil {
		return "(capture failed: " + err.Error() + ")"
	}
	return strings.TrimRight(string(out), "\n")
}
