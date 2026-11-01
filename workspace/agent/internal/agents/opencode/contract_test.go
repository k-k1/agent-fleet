//go:build contract

// opencode CLI contract tests — the drift alarm for the store and app-server contracts
// this package reads. Unlike the rest of the package's tests (synthetic `CREATE TABLE`
// schemas and httptest mocks, which keep passing no matter what the real CLI does),
// these run against the REAL opencode binary, its REAL SQLite store and a REAL
// `opencode serve`, so an upstream change that moves a contract fails here.
//
// Why this exists: the fleet tracks opencode@latest by design (entrypoint.sh's opt-in
// self-update), so a new CLI arrives unannounced at any container restart — the image's
// ARG pin is the fallback, not what runs. The existing L1 smoke asserts "image version ==
// ARG pin", which is a pin-conformance check: it stays green when a NEW opencode breaks
// our parsing. That gap is how claude's false-idle reached the live fleet.
//
// Credential-free by design: opencode ships free OpenCode Zen models and boots its
// composer without a login, so Tier A needs no API key and no spend (unlike claude's L4).
//
// Tier A (this file, default): deterministic, no LLM turn.
//
//	go test -tags contract ./internal/agents/opencode/
package opencode

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// requireOpencode skips (or fails under E2E_REQUIRE=1 or AF_REQUIRE_OPENCODE2=1) when the CLI is absent,
// so a dev box without opencode doesn't fail while CI insists on a real run.
func requireOpencode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("opencode"); err != nil {
		if os.Getenv("E2E_REQUIRE") == "1" || os.Getenv("AF_REQUIRE_OPENCODE2") == "1" {
			t.Fatalf("opencode not on PATH and a run was demanded (E2E_REQUIRE / AF_REQUIRE_OPENCODE2): %v", err)
		}
		t.Skip("opencode not on PATH — contract test skipped (set E2E_REQUIRE=1 to demand it)")
	}
}

func opencodeVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("opencode", "--version").Output()
	if err != nil {
		t.Fatalf("opencode --version: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// envWithHome replaces HOME so a contract run never reads or writes the user's real
// opencode store (~/.local/share/opencode) — the tests must be isolated and repeatable.
//
// The XDG_* roots have to go with it, or the isolation is only nominal: opencode resolves
// its config/state through XDG_CONFIG_HOME / XDG_DATA_HOME when those are set, and a
// GitHub Actions runner exports them pointing at the real /home/runner. Replacing HOME
// alone therefore leaves the run reading someone else's roots on CI while looking
// hermetic on a workstation (that gap is what made the precedence contract in
// ../../opencode_contract_test.go red on CI only).
func envWithHome(home string) []string {
	out := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
	}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "XDG_") {
			out = append(out, kv)
		}
	}
	return out
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// startServe boots a real `opencode serve` against an isolated HOME and returns its addr
// plus that HOME. The store it writes is the one dbPath() resolves once HOME is set.
func startServe(t *testing.T) (addr, home string) {
	t.Helper()
	return startServeIn(t, t.TempDir())
}

// startServeIn is startServe against a HOME the caller prepared, for tests that must
// seed config (e.g. an `mcp` entry) BEFORE the daemon reads it at boot.
func startServeIn(t *testing.T, home string) (addr, _ string) {
	t.Helper()
	requireOpencode(t)
	port := freePort(t)
	addr = "http://127.0.0.1:" + port
	cmd := exec.Command("opencode", "serve", "--hostname", "127.0.0.1", "--port", port)
	cmd.Env = envWithHome(home)
	cmd.Dir = home
	if err := cmd.Start(); err != nil {
		t.Fatalf("opencode serve start: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if healthy(addr) { // the production health probe, not a test-local one
			return addr, home
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("opencode serve (%s) never became healthy at %s", opencodeVersion(t), addr)
	return "", ""
}

// TestContractProgramFlagsAccepted guards the launch flags buildProgram emits. `--auto`
// is load-bearing twice over: it auto-approves permissions AND it is why the composer
// renders "<Agent> auto ·", the token opencodeStatusAgentRe anchors on. A rename would
// break the mode chip and the launch-seed readiness signal at once.
func TestContractProgramFlagsAccepted(t *testing.T) {
	requireOpencode(t)
	home := t.TempDir()
	cmd := exec.Command("opencode", "--auto", "--help")
	cmd.Env = envWithHome(home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("opencode %s rejected `--auto` (buildProgram's default AGENT_OPENCODE_FLAGS): %v\n%s",
			opencodeVersion(t), err, out)
	}
}
