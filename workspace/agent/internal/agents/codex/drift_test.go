//go:build drift

// codex CLI ドリフト検知（Tier 1）。**実 codex バイナリに当てる**テストで、通常の
// `go test ./...` からは build tag `drift` で除外される（CI の専用ジョブと、codex が
// PATH に居る手元だけで走る）。
//
// なぜ要るか: 既存の codex テストは全て fixture/mock で、codex 自身が何かを変えても
// 緑のままになる。特に効くのが「**未知の -c キーは黙って無視される**」という実測挙動
// （--strict-config を付けない本番 buildProgram では、feature キーが改名/削除されても
// エラーにならず、質問あり状態が無言で壊れる）。claude の false-idle と同型の版数
// ドリフトを、壊れてから気付くのではなく CI で先に捕まえるのがこの層の役目。
//
// 設計上の約束: **アサート対象の文字列を手で書き写さない**。検証する -c フラグ・
// bypass フラグ・hook の入れ子は全て本番の buildProgram() の出力から取り出す。手写しの
// 期待値だと「テストとテストが一致するだけ」の同語反復になり、既存 mock と同じ穴に
// 落ちる（実際 driver_test の mock サーバはメソッド名を自前で持つため、codex が改名
// しても検知できない）。
//
// 認証は不要: ここで検証する経路（features list / config 検証 / UserPromptSubmit の
// 発火）は全て API 呼び出しの前に完結する。実測で UserPromptSubmit は 401 になる
// ターンでも発火する。実ターンを要する Stop hook・rollout イベント・turn/* 通知は
// この層では扱わない（Tier 2 / 要課金）。
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// codexBin resolves the real CLI. Absent codex is a skip locally but a hard failure
// under E2E_REQUIRE=1 (the CI convention used by e2e/ and console-e2e/) — a silent
// skip in CI would be a false green, which is exactly the failure mode this file exists
// to prevent.
func codexBin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("codex")
	if err != nil {
		if os.Getenv("E2E_REQUIRE") == "1" {
			t.Fatalf("codex not on PATH and E2E_REQUIRE=1: %v", err)
		}
		t.Skipf("codex not on PATH (set E2E_REQUIRE=1 to make this fatal): %v", err)
	}
	return p
}

// configOverrides pulls the values of every `-c '<value>'` out of a buildProgram
// string, undoing session.ShellQuote's '\” escaping. This is what keeps the drift
// tests honest: they feed the CLI exactly what production would.
func configOverrides(prog string) []string {
	var out []string
	for i := 0; ; {
		j := strings.Index(prog[i:], "-c '")
		if j < 0 {
			return out
		}
		k := i + j + len("-c '")
		var sb strings.Builder
		for k < len(prog) {
			if prog[k] == '\'' {
				if strings.HasPrefix(prog[k:], `'\''`) { // ShellQuote's escaped quote
					sb.WriteByte('\'')
					k += 4
					continue
				}
				break
			}
			sb.WriteByte(prog[k])
			k++
		}
		out = append(out, sb.String())
		i = k + 1
	}
}

// bypassFlags pulls the `--dangerously-…` flags out of a buildProgram string, so a
// rename upstream surfaces here rather than silently dropping our unattended-run
// policy.
func bypassFlags(prog string) []string {
	var out []string
	for _, f := range strings.Fields(prog) {
		if strings.HasPrefix(f, "--dangerously") {
			out = append(out, f)
		}
	}
	return out
}

// hookCommandRe rewrites the leaf command of a hook entry while preserving the
// surrounding nesting exactly as buildProgram emitted it — the nesting is the part
// under test (a flat [{type,command}] array parses fine but never fires).
var hookCommandRe = regexp.MustCompile(`command="[^"]*"`)

// featureOverrideRe reads the feature name out of production's `features.<name>=true`
// override, so the test never hard-codes the name it is supposed to be policing.
var featureOverrideRe = regexp.MustCompile(`^features\.([a-z0-9_]+)=true$`)

// prodProgram is the launch string under test (fresh slot: no resume/fork).
func prodProgram(t *testing.T) string {
	t.Helper()
	t.Setenv("AF_CODEX_APP_SERVER_ADDR", "") // TUI route, not --remote
	return buildProgram("", "", "slot-drift", "", "")
}

// TestDriftCodexFeatureFlagsKnown asserts the feature gates our launch depends on still
// exist upstream. `codex features list` reports a stage per feature and marks dropped
// ones "removed" (observed: plugin_hooks), so this catches a rename/removal directly —
// the case that would otherwise break questions silently, since an unknown -c key is
// accepted without complaint (see TestDriftCodexConfigOverridesValidated).
func TestDriftCodexFeatureFlagsKnown(t *testing.T) {
	bin := codexBin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "features", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("codex features list: %v\n%s", err, out)
	}
	// name -> stage (columns: "<name>  <stage words>  <true|false>")
	stage := map[string]string{}
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.Fields(ln)
		if len(f) < 3 {
			continue
		}
		stage[f[0]] = strings.Join(f[1:len(f)-1], " ")
	}

	// The feature name comes from production's own -c override, not a literal here.
	want := ""
	for _, v := range configOverrides(prodProgram(t)) {
		if m := featureOverrideRe.FindStringSubmatch(v); m != nil {
			want = m[1]
		}
	}
	if want == "" {
		t.Fatal("buildProgram no longer passes a features.<name>=true override — " +
			"if that was deliberate, delete this test; otherwise the codex TUI just " +
			"silently lost its request_user_input opt-in")
	}
	st, ok := stage[want]
	if !ok {
		t.Fatalf("feature %q is gone from `codex features list` — our -c override is now a "+
			"silent no-op and the TUI route's 質問あり state will never light. Features seen: %v",
			want, keys(stage))
	}
	if st == "removed" {
		t.Fatalf("feature %q is stage=removed upstream — the -c override no longer does "+
			"anything; find its replacement (or, if request_user_input became default-on, "+
			"drop the override and this test)", want)
	}
	// `hooks` gates the whole status-hook mechanism (working/idle). Name is literal:
	// it is codex's feature id, not something production spells out.
	if st, ok := stage["hooks"]; !ok || st == "removed" {
		t.Fatalf("the `hooks` feature is gone/removed upstream (stage=%q, present=%v) — "+
			"UserPromptSubmit/Stop status hooks are the codex TUI route's only state source", st, ok)
	}
	t.Logf("ok: feature %q stage=%q, hooks stage=%q", want, stage[want], stage["hooks"])
}

// TestDriftCodexConfigOverridesValidated feeds production's real -c overrides to the
// real binary under --strict-config, which rejects unknown configuration fields.
// Production deliberately does NOT pass --strict-config (an unknown key must not break
// a user's session), which is precisely why this check has to exist somewhere.
func TestDriftCodexConfigOverridesValidated(t *testing.T) {
	bin := codexBin(t)
	overrides := configOverrides(prodProgram(t))
	if len(overrides) == 0 {
		t.Fatal("buildProgram emitted no -c overrides — parser or production changed")
	}

	// HOME is isolated so we validate OUR overrides only, never a stray local config.toml.
	run := func(t *testing.T, vals ...string) error {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		args := []string{"app-server", "--strict-config"}
		for _, v := range vals {
			args = append(args, "-c", v)
		}
		// app-server validates config, then serves; stdin at EOF makes it exit at once.
		// It needs no credentials (verified), so this stays free and fast.
		args = append(args, "--listen", "stdio://")
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
		cmd.Stdin = strings.NewReader("")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("codex said: %s", strings.TrimSpace(string(out)))
		}
		return err
	}

	if err := run(t, overrides...); err != nil {
		t.Fatalf("codex rejected production's own -c overrides %v: %v\n"+
			"A key we depend on was renamed/removed upstream. In production (no "+
			"--strict-config) this does NOT error — it is silently ignored, so the "+
			"feature it enables just stops working.", overrides, err)
	}

	// Negative control: if --strict-config ever stops rejecting unknown fields, the
	// check above degrades to a no-op that always passes. Guard the detector itself.
	if err := run(t, "features.af_drift_canary_not_a_real_key=true"); err == nil {
		t.Fatal("--strict-config accepted a bogus key: this detector is no longer able " +
			"to catch config drift, so TestDriftCodexConfigOverridesValidated is now vacuous")
	}
}

// TestDriftCodexThreadMCPConfigIsScoped verifies the prerequisite recorded in
// docs/log/27 §9.3: app-server accepts an MCP configuration on a thread and does
// not leak that configuration into another concurrently loaded thread.
//
// This is deliberately a drift (not live) test. It starts a credential-free,
// isolated app-server and only launches /bin/true as a deliberately invalid
// MCP stdio endpoint; no model turn is started. The useful contract is the
// per-thread MCP inventory, not successful MCP tool execution.
func TestDriftCodexThreadMCPConfigIsScoped(t *testing.T) {
	cl := startDriftAppServer(t)
	const (
		readServer  = "af_drift_read"
		writeServer = "af_drift_write"
	)
	readThread := startDriftMCPThread(t, cl, readServer)
	waitDriftMCPServer(t, cl, readThread, readServer)
	writeThread := startDriftMCPThread(t, cl, writeServer)
	waitDriftMCPServer(t, cl, writeThread, writeServer)

	if names := driftMCPServerNames(t, cl, readThread); names[writeServer] {
		t.Fatalf("thread %s inherited %q from another thread: %v", readThread, writeServer, names)
	}
	if names := driftMCPServerNames(t, cl, writeThread); names[readServer] {
		t.Fatalf("thread %s inherited %q from another thread: %v", writeThread, readServer, names)
	}
}

// TestDriftCodexThreadMCPConfigReplacesGlobalServers pins the docs/log/27 §9.3
// security contract: a thread-local mcp_servers map REPLACES servers the daemon was
// given via `-c` overrides rather than merging with them, so an empty map is a
// working deny for that layer.
//
// SCOPE — measured 2026-08-09, 0.147.0: this holds for `-c`-supplied servers ONLY.
// FILE layers behave the opposite way: $CODEX_HOME/config.toml and a trusted
// project's .codex/config.toml both merge through a thread map in every combination
// of ephemeral/persistent and empty/non-empty (TestDriftCodexThreadConfigMergeMatrix).
// The startDriftAppServer call below passes the global server as `-c`, which is why
// this test sees replacement — do not read it as a general contract.
//
// This assertion was inverted on 2026-07-20. It previously asserted the opposite
// ("an empty map is not an allowlist; the global server leaks in") and carried a
// note not to invert it until a replacement/deny mechanism existed. That note is
// now discharged: the mechanism was always there. The old assertion had never
// actually executed — the preceding test in this package hung in cleanup and took
// the whole package down by timeout (fixed by reapProcessGroup), so this test was
// unreachable from the commit that introduced it. Measured identically on codex
// 0.144.5 and 0.144.6, i.e. this is a corrected premise, not CLI drift.
//
// The no-config control matters as much as the deny itself: it proves the global
// -c config is live, so "no servers" cannot be a false pass from a global config
// that never took effect.
func TestDriftCodexThreadMCPConfigReplacesGlobalServers(t *testing.T) {
	const globalServer = "af_drift_global"
	cl := startDriftAppServer(t, "-c", "mcp_servers."+globalServer+`.command="/bin/true"`)

	// Control: without a thread-local map the global server IS inherited.
	inherited := startDriftThread(t, cl, nil)
	waitDriftMCPServer(t, cl, inherited, globalServer)

	// Deny: an empty map replaces the global set, leaving nothing.
	cleared := startDriftThread(t, cl, map[string]any{"mcp_servers": map[string]any{}})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if names := driftMCPServerNames(t, cl, cleared); len(names) > 0 {
			t.Fatalf("thread-local empty mcp_servers no longer denies `-c`-supplied servers: "+
				"got %v.\ndocs/log/27 §9.3 assumes thread config REPLACES that layer; if codex has "+
				"switched to merging it too, the none/af_read/af_write permission boundary for "+
				"the managed assistant chat is gone and must not be relied on. (File-configured "+
				"servers already merge — that is expected, see the scope note above.)", names)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("ok: global %q inherited without thread config, denied by empty map", globalServer)
}

// startDriftAppServer launches an isolated, credential-free app-server over a
// Unix socket. newAppClient performs the real initialize handshake, so this
// exercises the production JSON-RPC wire rather than a schema fixture.
func startDriftAppServer(t *testing.T, configArgs ...string) *appClient {
	t.Helper()
	return startDriftAppServerSeeded(t, nil, configArgs...)
}

// startDriftAppServerSeeded is startDriftAppServer with a hook that writes into the
// isolated HOME before the daemon boots — for the config layers ($CODEX_HOME/config.toml
// and its projects table) that only take effect if they are already on disk.
func startDriftAppServerSeeded(t *testing.T, seed func(home string), configArgs ...string) *appClient {
	t.Helper()
	// Codex intentionally refuses to create its helper aliases under /tmp. Use a
	// short-lived directory beneath the real home instead; HOME is still fully
	// isolated, so no user config/MCP/auth state is visible to this process.
	base, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home for isolated app-server: %v", err)
	}
	home, err := os.MkdirTemp(filepath.Join(base, ".cache"), "af-codex-drift-")
	if err != nil {
		t.Fatalf("make isolated app-server home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if seed != nil {
		seed(home)
	}
	socket := filepath.Join(t.TempDir(), "app-server.sock")
	ctx, cancel := context.WithCancel(context.Background())
	var output bytes.Buffer
	args := append([]string{"app-server"}, configArgs...)
	args = append(args, "--listen", "unix://"+socket)
	cmd := exec.CommandContext(ctx, codexBin(t), args...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdout = &output
	cmd.Stderr = &output
	reapProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start isolated app-server: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cl, err := newAppClient("unix://" + socket)
		if err == nil {
			go cl.readLoop()
			t.Cleanup(cl.close)
			return cl
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("isolated app-server did not become ready:\n%s", output.String())
	return nil
}

// reapProcessGroup makes killing a codex command actually kill codex. `codex` on
// PATH is a Node shim that spawns the vendored native binary as a child, so the
// default cancel (SIGKILL to the shim alone) leaves that child running: it is
// reparented to init and keeps the inherited stdout/stderr pipe open, so the
// io.Copy feeding cmd.Stdout never sees EOF and cmd.Wait() blocks forever. Both
// codex 0.144.5 and 0.144.6 behave this way — this is a harness bug, not CLI drift.
// Run the shim in its own process group and signal the whole group instead;
// WaitDelay is the backstop so Wait() can never outlive a stuck pipe.
func reapProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
}

// startDriftMCPThread creates an ephemeral thread with exactly one unique MCP
// server. The command intentionally fails its MCP handshake; app-server still
// records it in mcpServerStatus/list, which is enough to prove configuration
// placement without invoking a model or a real integration.
func startDriftMCPThread(t *testing.T, cl *appClient, name string) string {
	t.Helper()
	return startDriftThread(t, cl, map[string]any{
		"mcp_servers": map[string]any{name: map[string]any{"command": "/bin/true"}},
	})
}

func startDriftThread(t *testing.T, cl *appClient, config map[string]any) string {
	t.Helper()
	params := map[string]any{"cwd": t.TempDir(), "ephemeral": true}
	if config != nil {
		params["config"] = config
	}
	res, err := cl.call("thread/start", params, 10*time.Second)
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	st, err := parseThreadResult(res)
	if err != nil || st.threadID == "" {
		t.Fatalf("thread/start returned no thread id: %v", err)
	}
	return st.threadID
}

func waitDriftMCPServer(t *testing.T, cl *appClient, threadID, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if driftMCPServerNames(t, cl, threadID)[name] {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("MCP %q did not appear in thread %s", name, threadID)
}

func driftMCPServerNames(t *testing.T, cl *appClient, threadID string) map[string]bool {
	t.Helper()
	res, err := cl.call("mcpServerStatus/list", map[string]any{"threadId": threadID}, 5*time.Second)
	if err != nil {
		t.Fatalf("mcpServerStatus/list for %s: %v", threadID, err)
	}
	var body struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res, &body); err != nil {
		t.Fatalf("decode mcpServerStatus/list for %s: %v", threadID, err)
	}
	names := make(map[string]bool, len(body.Data))
	for _, server := range body.Data {
		names[server.Name] = true
	}
	return names
}

// TestDriftCodexUserPromptSubmitHookFires runs the real CLI with production's own hook
// override and asserts the hook actually executes. This is the load-bearing one: the
// TUI route's entire working/idle state comes from these hooks, and the nesting they
// require (`[{hooks=[{type,command}]}]`) is undocumented — a flat array still parses
// but never fires, so a schema change would be invisible to any fixture test.
//
// Credential-free: UserPromptSubmit fires before the model call (verified — it fires
// even when the turn 401s). We poll for the probe and kill codex the moment it lands,
// so the test costs ~seconds and never reaches the API. Stop can't be covered here (it
// needs a turn that actually completes) — that's Tier 2.
func TestDriftCodexUserPromptSubmitHookFires(t *testing.T) {
	bin := codexBin(t)
	prog := prodProgram(t)

	probe := filepath.Join(t.TempDir(), "fired")
	script := filepath.Join(t.TempDir(), "probe.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+probe+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Keep production's nesting byte-for-byte; swap only the leaf command so the hook is
	// observable (and so Stop can't invoke the test binary).
	var args []string
	args = append(args, bypassFlags(prog)...)
	sawHook := false
	for _, v := range configOverrides(prog) {
		switch {
		case strings.HasPrefix(v, "hooks.UserPromptSubmit="):
			v = hookCommandRe.ReplaceAllString(v, `command="`+script+`"`)
			sawHook = true
		case strings.HasPrefix(v, "hooks."):
			v = hookCommandRe.ReplaceAllString(v, `command="/bin/true"`)
		}
		args = append(args, "-c", v)
	}
	if !sawHook {
		t.Fatal("buildProgram no longer injects a hooks.UserPromptSubmit override — the TUI " +
			"route has lost its 進行中 signal")
	}
	if len(bypassFlags(prog)) == 0 {
		t.Fatal("buildProgram no longer passes --dangerously-… flags; hook trust would block the hooks")
	}
	args = append(args, "exec", "--skip-git-repo-check", "af drift probe")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	// Empty HOME => no credentials. The turn will fail to reach the API; we only care
	// that the hook ran first.
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	cmd.Dir = t.TempDir()
	reapProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start codex: %v", err)
	}
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	deadline := time.Now().Add(75 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(probe); err == nil {
			t.Log("ok: UserPromptSubmit hook fired with production's nesting")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("UserPromptSubmit hook never fired for override shape emitted by buildProgram.\n"+
		"codex's hook schema likely changed: a flat [{type,command}] array parses without "+
		"error but never fires, so the working/idle state would silently stop updating.\nargs: %v", args)
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestDriftCodexMemoriesFeatureExists guards the P4 有効化配線 (docs/log/39): our toggle
// writes `features.memories` into config.toml, which only means something while codex
// still ships that gate. A rename/removal upstream turns the Console toggle into a
// switch that writes a dead key — the memories workspace never appears and the memory
// root silently never shows up.
func TestDriftCodexMemoriesFeatureExists(t *testing.T) {
	bin := codexBin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "features", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("codex features list: %v\n%s", err, out)
	}
	stage := ""
	found := false
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.Fields(ln)
		if len(f) < 3 || f[0] != "memories" {
			continue
		}
		stage, found = strings.Join(f[1:len(f)-1], " "), true
	}
	if !found || stage == "removed" {
		t.Fatalf("the `memories` feature is gone/removed upstream (stage=%q, present=%v) — "+
			"the Console memories toggle now writes a dead config key, so enabling it can "+
			"never produce ~/.codex/memories and the codex memory root stays invisible", stage, found)
	}
	t.Logf("ok: memories stage=%q", stage)
}

// TestDriftCodexMemoriesTuningKeysValid feeds the tuning table we seed on enable to the
// real binary under --strict-config, which rejects unknown configuration fields.
// Production runs codex WITHOUT --strict-config, so a renamed key is silently ignored:
// the seed would stop capping how much background extract/consolidation work runs, and
// nothing would report an error. The keys come from memoriesTuning(), never a literal.
func TestDriftCodexMemoriesTuningKeysValid(t *testing.T) {
	bin := codexBin(t)
	prev := cheapModelFn
	cheapModelFn = func() string { return "gpt-5.4-mini" } // pin: the catalog must not decide coverage
	defer func() { cheapModelFn = prev }()

	run := func(t *testing.T, vals ...string) error {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		args := []string{"app-server", "--strict-config"}
		for _, v := range vals {
			args = append(args, "-c", v)
		}
		args = append(args, "--listen", "stdio://")
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
		cmd.Stdin = strings.NewReader("")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("codex said: %s", strings.TrimSpace(string(out)))
		}
		return err
	}

	overrides := []string{"features.memories=true"}
	for _, kv := range memoriesTuning() {
		overrides = append(overrides, "memories."+kv[0]+"="+kv[1])
	}
	if len(overrides) < 4 {
		t.Fatalf("memoriesTuning() emitted almost nothing (%v) — the seed lost its content", overrides)
	}
	if err := run(t, overrides...); err != nil {
		t.Fatalf("codex rejected the memories tuning we seed %v: %v\n"+
			"A key was renamed/removed upstream. In production (no --strict-config) this "+
			"does NOT error — the cap is silently ignored and background extract/"+
			"consolidation runs at codex's own, more expensive, defaults.", overrides, err)
	}

	// Negative control: without this, a --strict-config that stopped validating would
	// make the check above pass forever while detecting nothing.
	if err := run(t, "memories.af_drift_canary_not_a_real_key=true"); err == nil {
		t.Fatal("--strict-config accepted a bogus memories key: this detector is now vacuous")
	}
}
