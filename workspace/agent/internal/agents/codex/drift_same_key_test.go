//go:build drift

// Same server NAME in config.toml and in the thread config: which definition wins?
// This decides whether af must re-emit every server (replacement model) or may send
// only its own entry and let every other layer merge through (override model).
package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDriftCodexThreadConfigOverridesSameNamedFileServer(t *testing.T) {
	probes := t.TempDir()
	const name = "af_drift_samekey"
	fileProbe := filepath.Join(probes, "from_file")
	threadProbe := filepath.Join(probes, "from_thread")

	cl := startDriftAppServerSeeded(t, func(home string) {
		codexHome := filepath.Join(home, ".codex")
		if err := os.MkdirAll(codexHome, 0o700); err != nil {
			t.Fatal(err)
		}
		body := "[mcp_servers." + name + "]\ncommand = \"/bin/sh\"\n" +
			"args = [\"-c\", \"printf x > \\\"$0\\\"; exec sleep 45\", \"" + fileProbe + "\"]\n"
		if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	})

	tid := driftStartThreadParams(t, cl, map[string]any{
		"cwd": driftProjectDir(t),
		"config": map[string]any{"mcp_servers": map[string]any{
			name: map[string]any{
				"command": "/bin/sh",
				"args":    []any{"-c", `printf x > "$0"`, threadProbe},
			},
		}},
	})
	// Poll the markers directly: a probe that fails its handshake can wedge
	// mcpServerStatus/list, and the markers are the actual evidence anyway.
	for i := 0; i < 60; i++ {
		if _, err := os.Stat(fileProbe); err == nil {
			break
		}
		if _, err := os.Stat(threadProbe); err == nil {
			break
		}
		driftSleep()
	}
	driftSleep()
	driftSleep()
	_ = tid

	_, fileRan := os.Stat(fileProbe)
	_, threadRan := os.Stat(threadProbe)
	switch {
	case threadRan == nil && fileRan != nil:
		t.Logf("thread definition WINS for a shared name: af may send only its own entry and " +
			"let config.toml (global + trusted project) merge through untouched")
	case fileRan == nil && threadRan != nil:
		t.Fatalf("the config.toml definition won: af cannot override its own entry per thread, " +
			"so the session name cannot be injected this way at all")
	default:
		t.Fatalf("both definitions spawned (file=%v thread=%v): a shared name is duplicated "+
			"rather than overridden, so af re-emitting its entry would run two copies of the "+
			"same server", fileRan == nil, threadRan == nil)
	}
}

// TestDriftCodexProjectConfigWinsOverUserConfig settles the other half of the layering
// question for codex: when $CODEX_HOME/config.toml and a TRUSTED project's
// .codex/config.toml define the same server name, which one spawns?
//
// It decides whether a repository can shadow af's own `af` entry — the server that
// carries self-report, the handoff proposal and Chromium attach. Every other kind was
// measured for the same collision (mcpreg's materialize_scope_drift_test.go); codex
// needs the app-server, so it lives here.
func TestDriftCodexProjectConfigWinsOverUserConfig(t *testing.T) {
	probes := t.TempDir()
	userProbe := filepath.Join(probes, "user_scope")
	projProbe := filepath.Join(probes, "project_scope")
	const name = "af_drift_scope"

	proj := driftProjectDir(t)
	writeProbeServer(t, filepath.Join(proj, ".codex", "config.toml"), name, projProbe)

	cl := startDriftAppServerSeeded(t, func(home string) {
		codexHome := filepath.Join(home, ".codex")
		writeProbeServer(t, filepath.Join(codexHome, "config.toml"), name, userProbe)
		// Project config only applies to a trusted project.
		f, err := os.OpenFile(filepath.Join(codexHome, "config.toml"), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := fmt.Fprintf(f, "\n[projects.%q]\ntrust_level = \"trusted\"\n", proj); err != nil {
			t.Fatal(err)
		}
	})

	driftStartThreadParams(t, cl, map[string]any{"cwd": proj})
	for i := 0; i < 60; i++ {
		if _, err := os.Stat(userProbe); err == nil {
			break
		}
		if _, err := os.Stat(projProbe); err == nil {
			break
		}
		driftSleep()
	}
	driftSleep()
	driftSleep()

	_, userRan := os.Stat(userProbe)
	_, projRan := os.Stat(projProbe)
	switch {
	case projRan == nil && userRan != nil:
		t.Logf("codex: collision winner = project — a repository's .codex/config.toml CAN " +
			"shadow af's own `af` entry once the project is trusted")
	case userRan == nil && projRan != nil:
		t.Fatalf("codex: collision winner = user, not project. docs/48 §8.4 records project " +
			"as the winner; af's `af` entry is safer than documented, and the warning about " +
			"repositories shadowing it can be narrowed")
	default:
		t.Fatalf("codex: neither or both scopes spawned (user=%v project=%v) — the layering "+
			"changed shape entirely", userRan == nil, projRan == nil)
	}
}

// writeProbeServer writes a one-server config.toml whose "server" records that it was
// the definition codex chose to launch.
func writeProbeServer(t *testing.T, path, name, marker string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("[mcp_servers.%s]\ncommand = \"/bin/sh\"\nargs = [\"-c\", \"printf x > \\\"$0\\\"\", %q]\n",
		name, marker)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDriftCodexThreadConfigOverridesProjectConfig checks the last cell of the
// layering table: a trusted project's .codex/config.toml beats $CODEX_HOME
// (TestDriftCodexProjectConfigWinsOverUserConfig), so a repository can shadow af's `af`
// entry — unless the thread config sits above the project layer too. docs/48 §8.4
// claims managed codex is therefore immune to that shadowing; this is the claim.
func TestDriftCodexThreadConfigOverridesProjectConfig(t *testing.T) {
	probes := t.TempDir()
	projProbe := filepath.Join(probes, "project_scope")
	threadProbe := filepath.Join(probes, "thread_scope")
	const name = "af_drift_layered"

	proj := driftProjectDir(t)
	writeProbeServer(t, filepath.Join(proj, ".codex", "config.toml"), name, projProbe)

	cl := startDriftAppServerSeeded(t, func(home string) {
		codexHome := filepath.Join(home, ".codex")
		if err := os.MkdirAll(codexHome, 0o700); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("[projects.%q]\ntrust_level = \"trusted\"\n", proj)
		if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	})

	driftStartThreadParams(t, cl, map[string]any{
		"cwd": proj,
		"config": map[string]any{"mcp_servers": map[string]any{
			name: map[string]any{
				"command": "/bin/sh",
				"args":    []any{"-c", `printf x > "$0"`, threadProbe},
			},
		}},
	})
	for i := 0; i < 60; i++ {
		if _, err := os.Stat(projProbe); err == nil {
			break
		}
		if _, err := os.Stat(threadProbe); err == nil {
			break
		}
		driftSleep()
	}
	driftSleep()
	driftSleep()

	_, projRan := os.Stat(projProbe)
	_, threadRan := os.Stat(threadProbe)
	if threadRan != nil || projRan == nil {
		t.Fatalf("thread config no longer sits above the project layer (project=%v thread=%v): "+
			"a repository's .codex/config.toml can now shadow af's `af` entry in MANAGED codex "+
			"sessions too, which breaks self-report, the handoff proposal and Chromium attach "+
			"there. docs/48 §8.4's immunity note is wrong and must be removed",
			projRan == nil, threadRan == nil)
	}
	t.Logf("codex: thread > project > user — managed sessions keep af's own entry")
}
