//go:build drift

// Same server NAME in config.toml and in the thread config: which definition wins?
// This decides whether af must re-emit every server (replacement model) or may send
// only its own entry and let every other layer merge through (override model).
package codex

import (
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
