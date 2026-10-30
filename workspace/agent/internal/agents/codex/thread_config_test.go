package codex

// threadConfig assembly. When this breaks, MCP disappears from managed sessions, or the
// session name never arrives and the handoff proposal falls back to guessing from cwd.

import (
	"errors"
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

func stubSessionMCPDefs(t *testing.T, defs []mcpreg.ServerDef, err error) {
	t.Helper()
	old := sessionMCPDefs
	sessionMCPDefs = func() ([]mcpreg.ServerDef, error) { return defs, err }
	t.Cleanup(func() { sessionMCPDefs = old })
}

func afDef() mcpreg.ServerDef {
	return mcpreg.ServerDef{
		ID: mcpreg.BuiltinAF, Name: mcpreg.BuiltinAF, Origin: mcpreg.OriginBuiltin,
		Transport: mcpreg.TransportStdio, Command: "/usr/local/bin/workspace-agent",
		Args: []string{"mcp-stdio", "--self-report", "--chromium-attach"}, Enabled: true,
	}
}

func TestThreadConfigCarriesSessionNameToTheMCPChild(t *testing.T) {
	stubSessionMCPDefs(t, []mcpreg.ServerDef{afDef()}, nil)

	cfg := threadConfig("slot01")
	servers, ok := cfg["mcp_servers"].(map[string]any)
	if !ok {
		t.Fatalf("thread config has no mcp_servers: %v", cfg)
	}
	af, _ := servers[mcpreg.BuiltinAF].(map[string]any)
	env, _ := af["env"].(map[string]any)
	if env["AF_SESSION_NAME"] != "slot01" {
		t.Fatalf("af env = %v, want AF_SESSION_NAME=slot01", af["env"])
	}
	// The feature flag the questions path depends on must survive the restructuring.
	f, _ := cfg["features"].(map[string]any)
	if f["default_mode_request_user_input"] != true {
		t.Fatalf("features = %v, want default_mode_request_user_input — questions break without it", cfg["features"])
	}
}

// Nothing to override must mean "send no key at all", so the thread inherits every
// file layer exactly as a TUI session would.
func TestThreadConfigOmitsServersWhenThereIsNothingToOverride(t *testing.T) {
	for _, tc := range []struct {
		name string
		defs []mcpreg.ServerDef
		err  error
		slot string
	}{
		{name: "registry unreadable", err: errors.New("store locked"), slot: "slot01"},
		{name: "no servers for codex", defs: nil, slot: "slot01"},
		{name: "session name unknown", defs: []mcpreg.ServerDef{afDef()}, slot: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubSessionMCPDefs(t, tc.defs, tc.err)
			cfg := threadConfig(tc.slot)
			if _, present := cfg["mcp_servers"]; present {
				t.Fatalf("mcp_servers = %v, want the key absent so the thread inherits config.toml "+
					"and any trusted project config unchanged", cfg["mcp_servers"])
			}
			if cfg["features"] == nil {
				t.Fatal("features dropped along with the servers")
			}
		})
	}
}

// threadFeatures is package state reused by every thread; building a config must not
// write through to it.
func TestThreadConfigDoesNotMutateSharedFeatures(t *testing.T) {
	stubSessionMCPDefs(t, []mcpreg.ServerDef{afDef()}, nil)
	before := len(threadFeatures)
	cfg := threadConfig("slot01")
	cfg["features"] = "clobbered"
	if len(threadFeatures) != before {
		t.Fatalf("threadFeatures mutated: %v", threadFeatures)
	}
	if again, _ := threadConfig("slot02")["features"].(map[string]any); again == nil {
		t.Fatal("a later thread lost its features")
	}
}

// A managed codex thread's shell calls get this session's throwaway directory through the
// thread config, since the shared daemon's environment cannot differ per session.
func TestThreadConfigSetsWorkDirForShellCalls(t *testing.T) {
	stubSessionMCPDefs(t, nil, nil)
	cfg := threadConfig("slot01")
	pol, _ := cfg["shell_environment_policy"].(map[string]any)
	set, _ := pol["set"].(map[string]any)
	want := paths.SessionWorkDir("slot01")
	if want == "" || set["AF_WORK_DIR"] != want {
		t.Fatalf("shell_environment_policy.set = %v, want AF_WORK_DIR=%q", set, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("the directory was not created: %v", err)
	}
	if other, _ := threadConfig("slot02")["shell_environment_policy"].(map[string]any); other["set"].(map[string]any)["AF_WORK_DIR"] == want {
		t.Fatal("two sessions share one work dir")
	}
	// A name that could leave ~/.af-work gets no work dir.
	if _, present := threadConfig("../x")["shell_environment_policy"]; present {
		t.Fatal("an escaping name got a work dir")
	}
}

// The thread's shell also learns which session it belongs to: the shared daemon's environment
// carries no per-session AF_SESSION_NAME, and each session (a fork included) passes its own slot.
func TestThreadConfigSetsSessionNameForShellCalls(t *testing.T) {
	stubSessionMCPDefs(t, nil, nil)
	t.Setenv("HOME", t.TempDir())
	for _, slot := range []string{"slot01", "slot02"} {
		pol, _ := threadConfig(slot)["shell_environment_policy"].(map[string]any)
		set, _ := pol["set"].(map[string]any)
		if set["AF_SESSION_NAME"] != slot || set["AF_WORK_DIR"] != paths.SessionWorkDir(slot) {
			t.Fatalf("shell_environment_policy.set = %v, want AF_SESSION_NAME=%q beside AF_WORK_DIR", set, slot)
		}
	}
	// An invalid identifier is never handed to the shell as a session name.
	for _, bad := range []string{"", "../x", "a b", "a/b", "a\nb"} {
		pol, _ := threadConfig(bad)["shell_environment_policy"].(map[string]any)
		set, _ := pol["set"].(map[string]any)
		if _, present := set["AF_SESSION_NAME"]; present {
			t.Fatalf("slot %q was handed to the shell as a session name", bad)
		}
	}
}
