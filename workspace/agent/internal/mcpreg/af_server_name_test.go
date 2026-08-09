package mcpreg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// resetAFName isolates the per-process cache and the file behind it.
func resetAFName(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	afNameOnce.Lock()
	old := afNameOnce.value
	afNameOnce.value = ""
	afNameOnce.Unlock()
	t.Cleanup(func() {
		afNameOnce.Lock()
		afNameOnce.value = old
		afNameOnce.Unlock()
	})
}

func TestRotateAFServerNameChangesEveryBoot(t *testing.T) {
	resetAFName(t)
	first := RotateAFServerName()
	if !afNameRE.MatchString(first) {
		t.Fatalf("rotated name = %q, want the af_<8 hex> shape the sweep recognises", first)
	}
	// Same process, same file: every later reader must agree with the rotation.
	if got := AFServerName(); got != first {
		t.Fatalf("AFServerName() = %q right after rotating to %q — a session launched from "+
			"another path would be configured under a different name than the config on disk",
			got, first)
	}

	// A second boot: fresh process cache, same home.
	afNameOnce.Lock()
	afNameOnce.value = ""
	afNameOnce.Unlock()
	second := RotateAFServerName()
	if second == first {
		t.Fatalf("both boots minted %q — rotation is not happening", first)
	}
	if got := AFServerName(); got != second {
		t.Fatalf("AFServerName() = %q, want the new boot's %q", got, second)
	}
}

// A process that has not rotated (and finds no file) must NOT invent a name: two
// processes disagreeing about the server name is worse than not rotating at all.
func TestAFServerNameFallsBackToTheHistoricalName(t *testing.T) {
	resetAFName(t)
	if got := AFServerName(); got != BuiltinAF {
		t.Fatalf("AFServerName() = %q with no rotation on disk, want %q", got, BuiltinAF)
	}
}

func TestAFServerNameReadsAnotherProcessRotation(t *testing.T) {
	resetAFName(t)
	rotated := RotateAFServerName()

	// Simulate a different process: cache cleared, file kept.
	afNameOnce.Lock()
	afNameOnce.value = ""
	afNameOnce.Unlock()
	if got := AFServerName(); got != rotated {
		t.Fatalf("AFServerName() = %q, want %q from the file another process wrote", got, rotated)
	}
}

func TestBuiltinDefsCarryTheRotatedNameButKeepTheID(t *testing.T) {
	resetAFName(t)
	rotated := RotateAFServerName()

	var af ServerDef
	for _, d := range builtinDefs(&secrets.Data{}) {
		if d.ID == BuiltinAF {
			af = d
		}
	}
	if af.Name != rotated {
		t.Fatalf("af builtin Name = %q, want the rotated %q", af.Name, rotated)
	}
	// Everything that branches on af — the thread config stamp, the Console's note,
	// extraEnvVars — keys off the ID, which must not move.
	if af.ID != BuiltinAF {
		t.Fatalf("af builtin ID = %q, want it pinned to %q", af.ID, BuiltinAF)
	}
	servers, ok := CodexThreadServers([]ServerDef{af}, CodexThreadOpts{SessionName: "slot01"})
	if !ok || servers[rotated] == nil {
		t.Fatalf("CodexThreadServers = %v, %v — it stopped recognising the af builtin once the "+
			"server was renamed, so managed codex sessions lose their session name", servers, ok)
	}
}

// The generated shape is af's; a user row wearing it would either shadow af or be
// swept as one of af's leftovers.
func TestValidateRejectsTheRotatedNameShape(t *testing.T) {
	err := Validate(ServerDef{
		Name: "af_deadbeef", Origin: OriginUser, Transport: TransportStdio,
		Command: "/bin/true", Targets: Targets{Session: true},
	})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("Validate(af_deadbeef) = %v, want a reserved-name refusal", err)
	}
}

// The point of the sweep: rotation would otherwise leave one live server per boot,
// because the ownership ledger is the only thing that normally authorizes a deletion.
func TestStaleAFServerNameRecognisesEarlierBoots(t *testing.T) {
	keep := map[string]bool{"af_11111111": true}
	if !StaleAFServerName("af_22222222", keep) {
		t.Fatal("an earlier boot's af name was not recognised as sweepable")
	}
	if StaleAFServerName("af_11111111", keep) {
		t.Fatal("this boot's own name must never be swept")
	}
	for _, other := range []string{"af", "affinity", "af_short", "af_1234567890", "my-af_deadbeef"} {
		if StaleAFServerName(other, keep) {
			t.Fatalf("%q is not one of af's generated names and must not be deleted from a "+
				"user's config", other)
		}
	}
}

// End to end through a materializer: last boot's entry goes even when the ledger has
// forgotten it (prev is empty), and the user's own servers are untouched.
func TestMaterializeSweepsThePreviousBootsAFEntry(t *testing.T) {
	resetAFName(t)
	home := os.Getenv("HOME")
	cfg := filepath.Join(home, "claude-cfg")
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, ".claude.json"), []byte(
		`{"mcpServers":{"af_aaaaaaaa":{"type":"stdio","command":"/old"},`+
			`"mine":{"type":"stdio","command":"/bin/true"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	rotated := RotateAFServerName()
	af := ServerDef{ID: BuiltinAF, Name: rotated, Origin: OriginBuiltin, Transport: TransportStdio,
		Command: "/new", Enabled: true, Targets: Targets{Session: true}}
	// prev is empty: the ledger has no memory of af_aaaaaaaa.
	if _, removed, _, err := materializeClaude([]ServerDef{af}, nil); err != nil {
		t.Fatalf("materializeClaude: %v", err)
	} else if len(removed) != 1 || removed[0] != "af_aaaaaaaa" {
		t.Fatalf("removed = %v, want the previous boot's af entry", removed)
	}

	b, err := os.ReadFile(filepath.Join(cfg, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Contains(got, "af_aaaaaaaa") {
		t.Fatalf("the previous boot's server survived — every boot would add another live "+
			"af child:\n%s", got)
	}
	if !strings.Contains(got, `"mine"`) {
		t.Fatalf("the user's own server was swept along with af's:\n%s", got)
	}
	if !strings.Contains(got, rotated) {
		t.Fatalf("this boot's af server was not written:\n%s", got)
	}
}

// The codex writer is a separate, line-based code path (config.toml is the user's file
// and is edited by text, not parsed), so the sweep needs its own coverage there.
func TestMaterializeCodexSweepsThePreviousBootsAFEntry(t *testing.T) {
	resetAFName(t)
	codexHome := filepath.Join(os.Getenv("HOME"), ".codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(
		"# keep me\n[mcp_servers.af_aaaaaaaa]\ncommand = \"/old\"\n\n"+
			"[mcp_servers.mine]\ncommand = \"/bin/true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rotated := RotateAFServerName()
	af := ServerDef{ID: BuiltinAF, Name: rotated, Origin: OriginBuiltin, Transport: TransportStdio,
		Command: "/new", Enabled: true, Targets: Targets{Session: true}}
	if _, _, _, err := materializeCodex([]ServerDef{af}, nil); err != nil {
		t.Fatalf("materializeCodex: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Contains(got, "af_aaaaaaaa") {
		t.Fatalf("the previous boot's table survived:\n%s", got)
	}
	for _, want := range []string{"[mcp_servers.mine]", "# keep me", "[mcp_servers." + rotated + "]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing — the sweep took more than af's own leftovers:\n%s", want, got)
		}
	}
}
