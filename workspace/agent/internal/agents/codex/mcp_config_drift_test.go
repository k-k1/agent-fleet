//go:build drift

// The MCP configuration reload contract of codex app-server (docs/log/48 P3). This test runs
// against the real codex binary and is excluded from `go test ./...` by the `drift` build tag.
//
// Why it is needed: session materialize in the MCP registry works by rewriting
// `$CODEX_HOME/config.toml`, and a tui session relaunches codex every time, so the file is
// always re-read. A managed session, however, rides on the shared `codex app-server`
// (docs/log/27). If that daemon read its config only once at process start, a newly registered
// MCP would not reach managed sessions until app-server restarts - the registry UI would say
// "effective from the next session" while in reality nothing happened.
//
// Measured (codex-cli 0.145.0): it re-reads on every thread/start. materialize followed by
// thread/start is enough, and Supervisor.Restart (a heavy operation that drains every codex
// session in the workspace) is not needed. This goes red if that premise breaks.
//
// No authentication needed: the MCP server spawn happens inside thread/start and completes
// before the model is called, so the spawn is observable even when thread/start itself returns
// an authentication error.
//
// Note (docs/log/27 §9.3.1): a managed session overrides only the af entries through a
// per-thread config and inherits the rest from config.toml. So this reload contract still holds
// for managed, and whether an MCP registered in the registry shows up in a new thread is what
// this test guards. It is called with an empty slot (`threadStart(cl, home, "", "")`) to measure
// the bare inheritance path, with af's override out of the way.

package codex

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// waitFile polls for path up to d — the MCP child is spawned asynchronously.
func waitFile(path string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func TestDriftCodexAppServerRereadsMCPConfig(t *testing.T) {
	bin := codexBin(t)
	home := t.TempDir()
	marker := func(n string) string { return filepath.Join(home, n) }
	// The MCP "server" only has to be spawned, not to speak MCP: its side effect is
	// the observation. codex logs the handshake failure and moves on.
	serverBlock := func(name, out string) string {
		return "[mcp_servers." + name + "]\ncommand = \"/usr/bin/touch\"\nargs = [\"" + out + "\"]\n\n"
	}
	writeConfig := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(serverBlock("first", marker("first-spawned")))

	addr := "unix://" + filepath.Join(t.TempDir(), "app.sock")
	cmd := exec.Command(bin, "app-server", "--listen", addr)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	// `codex` is a Node shim that runs the vendored native binary as a CHILD, so killing
	// cmd.Process only reaps the shim: the native app-server is reparented to init and
	// keeps running (~115MB each, still holding its socket). That is the same trap
	// reapProcessGroup exists for, but this call site has no context to cancel — so put
	// the pair in its own process group and signal the group. Measured before this fix:
	// a single `-tags drift` run left two orphaned app-servers behind on the host.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("codex app-server: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = cmd.Wait()
	}()

	var cl *appClient
	deadline := time.Now().Add(15 * time.Second)
	for {
		var err error
		if cl, err = newAppClient(addr); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("could not connect to app-server: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	go cl.readLoop()

	// 1st thread: the server present when the daemon started.
	if _, err := threadStart(cl, home, "", ""); err != nil {
		t.Logf("thread/start 1 (expected error without authentication): %v", err)
	}
	if !waitFile(marker("first-spawned"), 15*time.Second) {
		t.Fatal("the MCP server from the startup config was never spawned - the premise of this test is broken")
	}

	// Now do what materialize does to a LIVE daemon: add a server to config.toml.
	writeConfig(serverBlock("first", marker("first-spawned")) + serverBlock("second", marker("second-spawned")))
	if _, err := threadStart(cl, home, "", ""); err != nil {
		t.Logf("thread/start 2 (expected error without authentication): %v", err)
	}
	if !waitFile(marker("second-spawned"), 15*time.Second) {
		t.Fatal("a running app-server no longer re-reads config.toml: " +
			"managed codex sessions stop seeing materialized MCP servers. " +
			"Update docs/log/48 §8.3 and wire Supervisor.Restart to registry changes")
	}
}
