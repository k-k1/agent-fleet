//go:build drift

// Can a managed session's MCP child be told WHICH session it serves?
//
// AF_SESSION_NAME is how a session-side MCP server identifies its owner, and for TUI
// sessions the tmux launch env delivers it. Managed sessions have no such env: their
// MCP children are spawned by the ONE shared `codex app-server` the Agent started, so
// anything per-session has to ride on the thread instead. docs/27 §9.3 established
// that `thread/start`'s `config.mcp_servers` is thread-scoped and REPLACES the global
// set — but scoping the *inventory* is not the same as delivering a *value* to the
// spawned process, which is what an identity needs. These tests measure that.
package codex

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const identityProbeServer = "af_drift_identity"

// TestDriftCodexThreadMCPConfigDeliversPerThreadEnv is the load-bearing measurement:
// two concurrently loaded threads configure the SAME MCP server name with DIFFERENT
// env values, and each spawned child must observe its own thread's value. That is the
// whole prerequisite for injecting a session name into a managed session's MCP child.
func TestDriftCodexThreadMCPConfigDeliversPerThreadEnv(t *testing.T) {
	cl := startDriftAppServer(t)
	probes := t.TempDir()

	first := startIdentityProbeThread(t, cl, probes, "slot_aaa")
	second := startIdentityProbeThread(t, cl, probes, "slot_bbb")
	waitDriftMCPServer(t, cl, first, identityProbeServer)
	waitDriftMCPServer(t, cl, second, identityProbeServer)

	for _, slot := range []string{"slot_aaa", "slot_bbb"} {
		got := waitIdentityProbe(t, filepath.Join(probes, slot))
		if got == probeUnset {
			t.Fatalf("thread config env did not reach the MCP child (%s read %q).\n"+
				"docs/27 §9.3 covers per-thread MCP *inventory* only; without env delivery a "+
				"managed session cannot tell its MCP child which session it serves, and the "+
				"handoff tool is stuck guessing from cwd.", slot, got)
		}
		if got != slot {
			t.Fatalf("MCP child for %s saw AF_SESSION_NAME=%q — thread configs are bleeding "+
				"into each other, so a per-session identity would be misattributed", slot, got)
		}
	}
}

// TestDriftCodexThreadMCPConfigProbeReportsMissingEnv is the control. Without it, a
// probe that never runs and a probe that runs with no env are indistinguishable, and
// the test above could pass for the wrong reason.
func TestDriftCodexThreadMCPConfigProbeReportsMissingEnv(t *testing.T) {
	cl := startDriftAppServer(t)
	probes := t.TempDir()
	out := filepath.Join(probes, "no_env")

	thread := startDriftThread(t, cl, map[string]any{
		"mcp_servers": map[string]any{
			identityProbeServer: map[string]any{
				"command": "/bin/sh",
				"args":    identityProbeArgs(out),
			},
		},
	})
	waitDriftMCPServer(t, cl, thread, identityProbeServer)

	if got := waitIdentityProbe(t, out); got != probeUnset {
		t.Fatalf("probe with no configured env read %q, want %q — the probe is picking the "+
			"value up from somewhere other than the thread config, so the sibling test proves nothing",
			got, probeUnset)
	}
}

// TestDriftCodexThreadMCPConfigForwardsEnvVarsFromDaemon settles how the SECRETS
// travel. A thread-local map replaces the global one (docs/27 §9.3), so the af server
// would lose the `env_vars` forward that today hands it AGENT_TOKEN — and re-supplying
// the token as a literal `env` value would push it through the app-server RPC and into
// whatever that persists. This measures whether `env_vars` still forwards from the
// daemon's own environment at thread scope, which is what keeps the token out of the
// config payload.
func TestDriftCodexThreadMCPConfigForwardsEnvVarsFromDaemon(t *testing.T) {
	// Set before the daemon starts: startDriftAppServer hands it os.Environ().
	t.Setenv("AF_DRIFT_FORWARDED", "from_daemon_env")
	cl := startDriftAppServer(t)
	out := filepath.Join(t.TempDir(), "forwarded")

	thread := startDriftThread(t, cl, map[string]any{
		"mcp_servers": map[string]any{
			identityProbeServer: map[string]any{
				"command": "/bin/sh",
				"args": []any{"-c",
					`printf '%s' "${AF_DRIFT_FORWARDED-` + probeUnset + `}" > "$0"`, out},
				"env_vars": []any{"AF_DRIFT_FORWARDED"},
			},
		},
	})
	waitDriftMCPServer(t, cl, thread, identityProbeServer)

	if got := waitIdentityProbe(t, out); got != "from_daemon_env" {
		t.Fatalf("env_vars forward at thread scope read %q, want the daemon's value.\n"+
			"If this no longer forwards, a thread-scoped af server must carry AGENT_TOKEN as a "+
			"literal config value — decide that deliberately before relying on it.", got)
	}
}

const probeUnset = "<unset>"

// startIdentityProbeThread configures one MCP server whose "command" is a probe: it
// records the AF_SESSION_NAME it was spawned with and exits. The MCP handshake fails,
// which is irrelevant — app-server still spawns the process and still lists the
// server, and spawning is the whole measurement (same trick as the /bin/true threads).
func startIdentityProbeThread(t *testing.T, cl *appClient, probeDir, slot string) string {
	t.Helper()
	return startDriftThread(t, cl, map[string]any{
		"mcp_servers": map[string]any{
			identityProbeServer: map[string]any{
				"command": "/bin/sh",
				"args":    identityProbeArgs(filepath.Join(probeDir, slot)),
				"env":     map[string]any{"AF_SESSION_NAME": slot},
			},
		},
	})
}

// identityProbeArgs writes the inherited AF_SESSION_NAME to out. The destination
// rides in $0 rather than the script text so no path escaping is involved.
func identityProbeArgs(out string) []any {
	return []any{"-c", `printf '%s' "${AF_SESSION_NAME-` + probeUnset + `}" > "$0"`, out}
}

func waitIdentityProbe(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return string(b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("MCP probe %s never ran: app-server listed the server but spawned nothing", path)
	return ""
}
