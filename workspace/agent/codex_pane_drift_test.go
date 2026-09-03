//go:build drift

// codex TUI ペインのドリフト検知（Tier 1）。build tag `drift` で通常の
// `go test ./...` から除外される（実 codex バイナリ＋実 tmux が要る）。
// 兄弟: internal/agents/codex/drift_test.go（features / config / hooks）。
//
// ここが埋めるのは paneMode の codex 分岐＝**テストが1つも無かった**箇所。判定は
// codex のフッタ表示という固定文字列に依存しており（`<model> <effort> · <cwd>` と
// "Plan mode"）、claude の false-idle と同じ壊れ方（版数でフッタ仕様が変わる）を
// する場所なのに、fixture すら無い状態だった。
//
// 認証不要: フッタは起動直後に描画され、モード切替（shift+tab）も TUI 内で完結する
// ためモデル呼び出しが発生しない。ただし**ダミー auth.json は必要**（実測: 認証情報が
// 無いと TUI はログイン選択画面を出し、composer もフッタも描画されない）。鍵はダミー
// なので課金は起きない。ディレクトリ trust ゲートは本番と同じ経路
// （BuildLaunch → ensureFolderTrusted）で解除される。
package main

import (
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
	// TUI route, not --remote. アドレスを空にするだけでは足りない: BuildLaunch は
	// 共有 app-server を需要で起こす（docs/log/27 §7.1）ので、このコンテナで daemon が
	// 生きていれば adopt して印を書き、--remote つきの起動になってしまう。
	// 無効化フラグが「直接起動」の唯一の確実なスイッチ。
	t.Setenv("AF_CODEX_APP_SERVER_DISABLE", "1")
	t.Setenv("AF_CODEX_APP_SERVER_ADDR", "")

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
	_ = exec.Command("tmux", "kill-session", "-t", tn).Run()
	launch := "env HOME=" + home + " " + plan.Program
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", tn,
		"-x", "200", "-y", "50", "-c", plan.Cwd, launch).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}
	defer func() { _ = exec.Command("tmux", "kill-session", "-t", tn).Run() }()

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
	if out, err := exec.Command("tmux", "send-keys", "-t", tn, "BTab").CombinedOutput(); err != nil {
		t.Fatalf("send-keys BTab: %v: %s", err, out)
	}
	if got, pane := awaitCodexPlanTransition(t, tn); got != "Plan" &&
		!strings.Contains(pane, "for Plan mode.") {
		diagnosePane(t, tn)
		t.Fatalf("after shift+tab neither the footer nor the TUI system message confirms "+
			"Plan mode.\npaneMode=%q\npane:\n%s", got, pane)
	}
	t.Log("ok: codex composer readiness and Plan transition survive the production launch plan")
}

func awaitCodexPlanTransition(t *testing.T, tn string) (string, string) {
	t.Helper()
	lastMode, lastPane := "", ""
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		lastMode = sessionx.PaneMode(session.KindCodex, tn)
		lastPane = capturePane(tn)
		if lastMode == "Plan" || strings.Contains(lastPane, "for Plan mode.") {
			return lastMode, lastPane
		}
		time.Sleep(250 * time.Millisecond)
	}
	return lastMode, lastPane
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
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", tn).Output()
	if err != nil {
		return "(capture failed: " + err.Error() + ")"
	}
	return strings.TrimRight(string(out), "\n")
}
