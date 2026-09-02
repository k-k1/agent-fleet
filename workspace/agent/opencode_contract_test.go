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
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
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

// isolatedHomeEnv returns the env assignments that pin opencode's config and state roots
// inside home. Overriding HOME alone is NOT enough: opencode resolves its global config
// through XDG_CONFIG_HOME (paths.go OpencodeConfigDir), and a GitHub Actions runner
// **exports XDG_CONFIG_HOME=/home/runner/.config**. With only HOME replaced, the runner's
// real (empty) config root wins, the "global" source of a precedence test silently
// contributes nothing, and the test reports upstream drift that does not exist — exactly
// how TestContractOpencodeConfigPrecedence went red on CI while passing on every
// workstation (verified: setting XDG_CONFIG_HOME elsewhere reproduces it on 1.18.9).
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
	home, dir := t.TempDir(), t.TempDir()
	name := "af-contract-opencode"
	// Prefix the env onto the command itself: tmux -e sets the session environment, which
	// does NOT reach the pane's process (the same reason buildProgram prefixes env).
	tmuxSession(t, name, dir, strings.Join(isolatedHomeEnv(home), " ")+" opencode --auto")

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

// TestContractOpencodeEnvConfig is the drift alarm for OPENCODE_CONFIG, which the
// assistant chat's report wiring rests on: the af MCP server must carry `--conv <id>`
// (docs/log/30), the id is per conversation, and opencode's config is per FILE — so the
// per-conversation config is handed over as OPENCODE_CONFIG while --dir stays the
// per-grant project dir (that path IS the session's resume identity).
//
// If opencode ever stops honoring the env var, every opencode assistant silently goes
// back to running with no af tools at all, which is exactly the class of failure this
// test exists to catch loudly.
//
//	go test -tags clicontract -run TestContractOpencodeEnvConfig ./
func TestContractOpencodeEnvConfig(t *testing.T) {
	requireBins(t, "opencode")
	home, dir := t.TempDir(), t.TempDir()
	// The project config defines NO server: anything `mcp list` reports can only have
	// come from the env-pointed file.
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"),
		[]byte(`{"$schema":"https://opencode.ai/config.json","permission":{"edit":"deny","bash":"deny"}}`+"\n"),
		0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	cfg := filepath.Join(home, "conv.json")
	// /bin/echo is not an MCP server — it exits at once and `mcp list` reports the entry
	// as failed. That is fine: the contract under test is that the entry is READ.
	if err := os.WriteFile(cfg,
		[]byte(`{"$schema":"https://opencode.ai/config.json","mcp":{"afconvprobe":{"type":"local","command":["/bin/echo","probe"],"enabled":true}}}`+"\n"),
		0o600); err != nil {
		t.Fatalf("write conv config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "opencode", "mcp", "list")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append(isolatedHomeEnv(home), "OPENCODE_CONFIG="+cfg)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("opencode mcp list: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "afconvprobe") {
		t.Fatalf("OPENCODE_CONFIG no longer honored by opencode %s — the per-conversation af MCP config "+
			"(and with it --conv / セッション報告) is silently dropped.\nmcp list:\n%s", opencodeVer(t), out)
	}
}

// TestContractOpencodeConfigPrecedence pins HOW opencode combines its config sources,
// which is what decides where the af MCP server may live (chat_providers.go).
//
// Measured on 1.18.7 with `opencode debug config`:
//   - every source is MERGED (union of the `mcp` maps) — none replaces another;
//   - on a name collision the nearest project config WINS, over both OPENCODE_CONFIG
//     and the global ~/.config/opencode config.
//
// The second half is the dangerous one. The chat writes af ONLY into the per-conversation
// OPENCODE_CONFIG file because that is the only place `--conv <id>` can live; if a future
// opencode flipped precedence — or if someone "helpfully" added af to the project config —
// the --conv-less entry would win and every opencode assistant would stop reporting back
// (docs/log/30), with nothing else looking broken.
//
//	go test -tags clicontract -run TestContractOpencodeConfigPrecedence ./
func TestContractOpencodeConfigPrecedence(t *testing.T) {
	requireBins(t, "opencode")
	home, dir := t.TempDir(), t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	// The same name "dup" in all three sources, plus one name unique to each: the unique
	// ones prove the merge, "dup" proves the precedence. /bin/echo is not an MCP server —
	// the entries fail to connect, which is fine, the contract is that they are READ.
	write(filepath.Join(home, ".config", "opencode", "opencode.json"),
		`{"$schema":"https://opencode.ai/config.json","mcp":{`+
			`"globalonly":{"type":"local","command":["/bin/echo","g"],"enabled":true},`+
			`"dup":{"type":"local","command":["/bin/echo","FROM-GLOBAL"],"enabled":true}}}`)
	write(filepath.Join(dir, "opencode.json"),
		`{"$schema":"https://opencode.ai/config.json","permission":{"edit":"deny","bash":"deny"},"mcp":{`+
			`"projonly":{"type":"local","command":["/bin/echo","p"],"enabled":true},`+
			`"dup":{"type":"local","command":["/bin/echo","FROM-PROJECT"],"enabled":true}}}`)
	cfg := filepath.Join(home, "conv.json")
	write(cfg, `{"$schema":"https://opencode.ai/config.json","mcp":{`+
		`"envonly":{"type":"local","command":["/bin/echo","e"],"enabled":true},`+
		`"dup":{"type":"local","command":["/bin/echo","FROM-ENV"],"enabled":true}}}`)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "opencode", "debug", "config")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append(isolatedHomeEnv(home), "OPENCODE_CONFIG="+cfg)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("opencode debug config: %v\n%s", err, out)
	}
	got := string(out)
	// Merge: a source contributing a name found nowhere else must survive.
	for _, name := range []string{"globalonly", "projonly", "envonly"} {
		if !strings.Contains(got, name) {
			t.Fatalf("opencode %s no longer MERGES its config sources — %q is missing, so one source "+
				"is replacing the others. chat_providers.go assumes the union.\ndebug config:\n%s",
				opencodeVer(t), name, got)
		}
	}
	// Precedence: the project config must win the collision.
	if !strings.Contains(got, "FROM-PROJECT") {
		t.Fatalf("opencode %s no longer resolves a colliding MCP name to the PROJECT config — "+
			"config precedence moved. Re-check that af still belongs only in the per-conversation "+
			"OPENCODE_CONFIG file (chat_providers.go opencodeChatConfig).\ndebug config:\n%s",
			opencodeVer(t), got)
	}
	if strings.Contains(got, "FROM-ENV") || strings.Contains(got, "FROM-GLOBAL") {
		t.Fatalf("opencode %s resolved the colliding MCP name to something other than the project "+
			"config — precedence moved (see above).\ndebug config:\n%s", opencodeVer(t), got)
	}
}
