package mcpreg

import (
	"reflect"
	"strings"
	"testing"
)

func threadAFDef() ServerDef {
	return ServerDef{
		ID: BuiltinAF, Name: BuiltinAF, Origin: OriginBuiltin, Transport: TransportStdio,
		Command: "/usr/local/bin/workspace-agent",
		Args:    []string{"mcp-stdio", "--self-report", "--chromium-attach"},
		Enabled: true, Targets: Targets{Session: true},
	}
}

func TestCodexThreadServersOverridesOnlyTheAFEntry(t *testing.T) {
	got, ok := CodexThreadServers(
		[]ServerDef{threadAFDef(), attachStdioDef("user1"), attachHTTPDef("remote1")},
		CodexThreadOpts{SessionName: "slot01"})
	if !ok {
		t.Fatal("CodexThreadServers reported nothing to send for a set containing af")
	}

	// Thread config MERGES with the file layers, so restating anything else would put
	// the user's secrets into an RPC payload for no benefit — and would shadow servers
	// af does not manage (hand-added rows, trusted project config) with a stale copy.
	if len(got) != 1 || got[BuiltinAF] == nil {
		t.Fatalf("thread map = %v, want only the af entry", got)
	}
	af, _ := got[BuiltinAF].(map[string]any)
	if env, _ := af["env"].(map[string]any); env[sessionNameVar] != "slot01" {
		t.Fatalf("af env = %v, want %s=slot01 — without it a managed session cannot name itself",
			af["env"], sessionNameVar)
	}
}

// The override is whole-entry, not field-wise: a thread definition REPLACES the
// config.toml one for that name. An entry carrying only env would therefore launch a
// server with no command.
func TestCodexThreadServersRestatesTheWholeAFEntry(t *testing.T) {
	d := threadAFDef()
	d.TimeoutMS = 2500
	got, _ := CodexThreadServers([]ServerDef{d}, CodexThreadOpts{SessionName: "slot01"})
	af, _ := got[BuiltinAF].(map[string]any)

	if af["command"] != d.Command {
		t.Fatalf("command = %v, want %q", af["command"], d.Command)
	}
	if want := anySlice(d.Args); !reflect.DeepEqual(af["args"], want) {
		t.Fatalf("args = %v, want %v", af["args"], want)
	}
	if af["startup_timeout_sec"] != 2.5 {
		t.Fatalf("startup_timeout_sec = %v, want 2.5 from 2500ms", af["startup_timeout_sec"])
	}
	// Same fields config.toml carries, so managed and TUI launch the same server.
	blocks := strings.Join(codexServerBlocks([]ServerDef{d}), "\n")
	for _, want := range []string{`command = "` + d.Command + `"`, "startup_timeout_sec = 2.5"} {
		if !strings.Contains(blocks, want) {
			t.Fatalf("config.toml no longer carries %q — the two paths have diverged:\n%s", want, blocks)
		}
	}
}

// Credentials must keep travelling by name. A literal value here would ride the
// app-server RPC and whatever it persists.
func TestCodexThreadServersForwardsCredentialsByName(t *testing.T) {
	got, _ := CodexThreadServers([]ServerDef{threadAFDef()}, CodexThreadOpts{SessionName: "slot01"})
	af, _ := got[BuiltinAF].(map[string]any)

	if want := []any{"AGENT_ADDR", "AGENT_TOKEN"}; !reflect.DeepEqual(af["env_vars"], want) {
		t.Fatalf("af env_vars = %v, want %v", af["env_vars"], want)
	}
	// Forwarded AND literal would be two answers to one question; the daemon has no
	// AF_SESSION_NAME to forward, so the literal must be the only one.
	for _, v := range af["env_vars"].([]any) {
		if v == sessionNameVar {
			t.Fatalf("af env_vars still forwards %s alongside the literal value: %v",
				sessionNameVar, af["env_vars"])
		}
	}
}

func TestCodexThreadServersSendsNothingWithoutASessionNameOrAF(t *testing.T) {
	for _, tc := range []struct {
		name string
		defs []ServerDef
		opts CodexThreadOpts
	}{
		{"no session name", []ServerDef{threadAFDef()}, CodexThreadOpts{}},
		{"af not attached", []ServerDef{attachStdioDef("user1")}, CodexThreadOpts{SessionName: "slot01"}},
		{"empty registry", nil, CodexThreadOpts{SessionName: "slot01"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := CodexThreadServers(tc.defs, tc.opts); ok || got != nil {
				t.Fatalf("= %v, %v; want nil, false so the caller omits the key and inherits", got, ok)
			}
		})
	}
}
