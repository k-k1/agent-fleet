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

func TestCodexThreadServersStampsSessionNameOnAFOnly(t *testing.T) {
	got, ok := CodexThreadServers(
		[]ServerDef{threadAFDef(), attachStdioDef("user1"), attachHTTPDef("remote1")},
		CodexThreadOpts{SessionName: "slot01"})
	if !ok {
		t.Fatal("CodexThreadServers reported nothing to send for a non-empty set")
	}

	af, _ := got[BuiltinAF].(map[string]any)
	env, _ := af["env"].(map[string]any)
	if env[sessionNameVar] != "slot01" {
		t.Fatalf("af env = %v, want %s=slot01 — without it a managed session cannot name itself",
			af["env"], sessionNameVar)
	}
	// Forwarded AND literal would be two answers to one question; the daemon has no
	// AF_SESSION_NAME to forward, so the literal must be the only one.
	for _, v := range af["env_vars"].([]any) {
		if v == sessionNameVar {
			t.Fatalf("af env_vars still forwards %s alongside the literal value: %v",
				sessionNameVar, af["env_vars"])
		}
	}
	// The credentials keep travelling by name, not by value.
	if want := []any{"AGENT_ADDR", "AGENT_TOKEN"}; !reflect.DeepEqual(af["env_vars"], want) {
		t.Fatalf("af env_vars = %v, want %v", af["env_vars"], want)
	}

	for _, name := range []string{"user1", "remote1"} {
		e, _ := got[name].(map[string]any)
		if e == nil {
			t.Fatalf("%q missing: a thread-local map REPLACES config.toml, so dropping a "+
				"user server here removes it from every managed codex session", name)
		}
		if env, _ := e["env"].(map[string]any); env[sessionNameVar] != nil {
			t.Fatalf("%q was handed the session name; only the af server has a reason to "+
				"know it", name)
		}
	}
}

// The thread map is the JSON twin of the config.toml blocks. If the two ever describe
// different servers, a managed session and a TUI session stop seeing the same tools.
func TestCodexThreadServersMatchConfigTOMLBlocks(t *testing.T) {
	defs := []ServerDef{threadAFDef(), attachStdioDef("user1"), attachHTTPDef("remote1")}
	got, _ := CodexThreadServers(defs, CodexThreadOpts{})
	blocks := strings.Join(codexServerBlocks(defs), "\n")

	if len(got) != len(defs) {
		t.Fatalf("thread map has %d servers, config.toml has %d", len(got), len(defs))
	}
	stdio, _ := got["user1"].(map[string]any)
	if stdio["command"] != "/usr/bin/thing" {
		t.Fatalf("stdio command = %v", stdio["command"])
	}
	if env, _ := stdio["env"].(map[string]any); env["THING_TOKEN"] != "s3cr3t" {
		t.Fatalf("stdio env = %v, want the same literal config.toml already carries", stdio["env"])
	}
	if !strings.Contains(blocks, `THING_TOKEN = "s3cr3t"`) {
		t.Fatalf("config.toml no longer carries the value literally — the two paths have "+
			"diverged and this test's premise is stale:\n%s", blocks)
	}
	remote, _ := got["remote1"].(map[string]any)
	if remote["url"] != "https://mcp.example.com/mcp" {
		t.Fatalf("remote url = %v", remote["url"])
	}
	if h, _ := remote["http_headers"].(map[string]any); h["Authorization"] != "Bearer tok" {
		t.Fatalf("remote headers = %v", remote["http_headers"])
	}
}

// An empty map is a working DENY (docs/27 §9.3), so "nothing to say" must mean "send
// no key" — otherwise a registry that yields no codex servers would silently strip
// the ones config.toml provides.
func TestCodexThreadServersRefusesToEmitAnEmptyMap(t *testing.T) {
	if got, ok := CodexThreadServers(nil, CodexThreadOpts{SessionName: "slot01"}); ok || got != nil {
		t.Fatalf("CodexThreadServers(nil) = %v, %v; want nil, false so the caller omits the key", got, ok)
	}
}

func TestCodexThreadServersOmitsSessionNameWhenUnknown(t *testing.T) {
	got, _ := CodexThreadServers([]ServerDef{threadAFDef()}, CodexThreadOpts{})
	af, _ := got[BuiltinAF].(map[string]any)
	if af["env"] != nil {
		t.Fatalf("af env = %v, want none when there is no session name to stamp", af["env"])
	}
	// The forward stays in place: it is what the TUI route relies on.
	if got, want := af["env_vars"], []any{"AF_SESSION_NAME", "AGENT_ADDR", "AGENT_TOKEN"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("af env_vars = %v, want %v", got, want)
	}
}

func TestCodexThreadServersCarriesStartupTimeout(t *testing.T) {
	d := attachStdioDef("slow")
	d.TimeoutMS = 2500
	got, _ := CodexThreadServers([]ServerDef{d}, CodexThreadOpts{})
	e, _ := got["slow"].(map[string]any)
	if e["startup_timeout_sec"] != 2.5 {
		t.Fatalf("startup_timeout_sec = %v, want 2.5 seconds from 2500ms", e["startup_timeout_sec"])
	}
}
