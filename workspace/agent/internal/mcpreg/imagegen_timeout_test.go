package mcpreg

import (
	"reflect"
	"strings"
	"testing"
)

func afDef() ServerDef {
	return ServerDef{
		ID: BuiltinAF, Name: "af-1a2b", Origin: OriginBuiltin, Transport: TransportStdio,
		Command: "/usr/local/bin/workspace-agent", Args: []string{"mcp-stdio", "--self-report"},
		Enabled: true, Targets: Targets{Session: true},
	}
}

// codex cuts a tools/call at its 300 s tool_timeout_sec default (measured 0.153.4: "timed out
// awaiting tools/call after 300s" while the server had replied at 300.0 s), and generate_image
// runs a whole image generation inside the call. All THREE codex serializations have to carry
// the wider budget: the thread config replaces the file entry whole-entry, so a value written
// only into config.toml is lost for a managed session rather than inherited.
func TestCodexToolTimeoutOnEveryPath(t *testing.T) {
	d := afDef()

	block := codexServerBlocks([]ServerDef{d})[0]
	if !strings.Contains(block, "tool_timeout_sec = 600.0") {
		t.Errorf("config.toml block has no tool_timeout_sec:\n%s", block)
	}

	args, _ := CodexOverrides([]ServerDef{d}, CodexOpts{})
	if got := strings.Join(args, " "); !strings.Contains(got, "mcp_servers.af-1a2b.tool_timeout_sec=600.0") {
		t.Errorf("codex exec overrides have no tool_timeout_sec: %v", args)
	}

	entry := codexAFThreadEntry(d, "slot01")
	if entry["tool_timeout_sec"] != float64(600) {
		t.Errorf("thread config entry = %v, want tool_timeout_sec 600", entry)
	}
}

// Nobody else is widened. codex's default is a real protection against a wedged server, and
// only af's own has a call that legitimately runs for minutes.
func TestCodexToolTimeoutOnlyForAF(t *testing.T) {
	for _, d := range []ServerDef{
		{ID: BuiltinGrafana, Name: "grafana", Origin: OriginBuiltin, Transport: TransportStdio, Command: "/bin/true"},
		{Name: "user-server", Origin: OriginUser, Transport: TransportStdio, Command: "/bin/true"},
	} {
		if sec := CodexToolTimeoutSec(d); sec != 0 {
			t.Errorf("%s: tool timeout = %v, want 0 (codex's own default)", d.Name, sec)
		}
		if block := codexServerBlocks([]ServerDef{d})[0]; strings.Contains(block, "tool_timeout_sec") {
			t.Errorf("%s was widened:\n%s", d.Name, block)
		}
	}
}

// The run-arg carries the opt-in only. It must stay off by default, and it must never grow to
// carry the agent kind — the af server's argv is one definition per boot.
func TestImageGenRunArg(t *testing.T) {
	old := ImageGenEnabled
	t.Cleanup(func() { ImageGenEnabled = old })

	ImageGenEnabled = nil
	args, _ := BuiltinRunArgs(BuiltinAF)
	if want := []string{"mcp-stdio", "--self-report", "--chromium-attach"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args with no hook = %v, want %v", args, want)
	}

	ImageGenEnabled = func() bool { return false }
	if args, _ := BuiltinRunArgs(BuiltinAF); contains(args, "--image-gen") {
		t.Fatalf("args with the preference off = %v", args)
	}

	ImageGenEnabled = func() bool { return true }
	args, _ = BuiltinRunArgs(BuiltinAF)
	if !contains(args, "--image-gen") {
		t.Fatalf("args with the preference on = %v", args)
	}
	for _, kind := range []string{"claude", "codex", "opencode"} {
		if contains(args, kind) {
			t.Fatalf("the agent kind %q leaked into the af server's argv: %v", kind, args)
		}
	}

	// Only af's own server takes it.
	if args, _ := BuiltinRunArgs(BuiltinAWS); contains(args, "--image-gen") {
		t.Fatalf("a non-af builtin took the flag: %v", args)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
