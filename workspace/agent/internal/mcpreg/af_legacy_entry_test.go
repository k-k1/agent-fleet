package mcpreg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

func TestStaleLegacyAFEntryOnlyMatchesAfsOwnLaunchLine(t *testing.T) {
	keep := map[string]bool{"af_11111111": true}
	cases := []struct {
		name    string
		keep    map[string]bool
		command string
		args    []string
		want    bool
	}{
		{"af", keep, "/usr/local/bin/workspace-agent", []string{"mcp-stdio", "--self-report"}, true},
		{"af", keep, "workspace-agent", []string{"mcp-stdio"}, true},
		{"af", keep, "/home/dev/.local/bin/workspace-agent", []string{"mcp-stdio"}, true},
		{"af", keep, "/usr/bin/node", []string{"mcp-stdio"}, false},
		{"af", keep, "/usr/local/bin/workspace-agent", []string{"mcp-run", "aws"}, false},
		{"af", keep, "/usr/local/bin/workspace-agent", nil, false},
		{"af", keep, "", []string{"mcp-stdio"}, false},
		{"af", map[string]bool{"af": true}, "workspace-agent", []string{"mcp-stdio"}, false},
		{"af_11111111", keep, "workspace-agent", []string{"mcp-stdio"}, false},
		{"mine", keep, "workspace-agent", []string{"mcp-stdio"}, false},
	}
	for _, c := range cases {
		if got := StaleLegacyAFEntry(c.name, c.keep, c.command, c.args); got != c.want {
			t.Errorf("StaleLegacyAFEntry(%q, %q, %v) = %v, want %v", c.name, c.command, c.args, got, c.want)
		}
	}
}

func rotatedAFDef() ServerDef {
	return ServerDef{ID: BuiltinAF, Name: RotateAFServerName(), Origin: OriginBuiltin,
		Transport: TransportStdio, Command: paths.ConfigExePath(),
		Args: []string{"mcp-stdio", "--self-report"}, Enabled: true, Targets: Targets{Session: true}}
}

func TestMaterializeClaudeSweepsTheLegacyBareAFEntry(t *testing.T) {
	resetAFName(t)
	cfg := filepath.Join(os.Getenv("HOME"), "claude-cfg")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(cfg, ".claude.json")
	if err := os.WriteFile(file, []byte(`{"mcpServers":{`+
		`"af":{"type":"stdio","command":"/usr/local/bin/workspace-agent","args":["mcp-stdio","--self-report","--chromium-attach"]},`+
		`"mine":{"type":"stdio","command":"/bin/true"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	af := rotatedAFDef()
	_, removed, changed, err := materializeClaude([]ServerDef{af}, nil)
	if err != nil || !changed || len(removed) != 1 || removed[0] != "af" {
		t.Fatalf("removed=%v changed=%v err=%v, want the bare af entry swept", removed, changed, err)
	}
	b, _ := os.ReadFile(file)
	got := string(b)
	if strings.Contains(got, `"af":`) || !strings.Contains(got, `"mine"`) || !strings.Contains(got, af.Name) {
		t.Fatalf("unexpected result:\n%s", got)
	}
	// Nothing left to do: the next launch must not rewrite the file.
	if _, _, changed, err := materializeClaude([]ServerDef{af}, nil); err != nil || changed {
		t.Fatalf("second run changed=%v err=%v, want a no-op", changed, err)
	}
}

func TestMaterializeKeepsAUsersOwnServerNamedAF(t *testing.T) {
	resetAFName(t)
	cfg := filepath.Join(os.Getenv("HOME"), "claude-cfg")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(cfg, ".claude.json")
	if err := os.WriteFile(file, []byte(`{"mcpServers":{"af":{"type":"stdio","command":"/opt/my-af","args":["mcp-stdio"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, removed, _, err := materializeClaude([]ServerDef{rotatedAFDef()}, nil); err != nil || len(removed) != 0 {
		t.Fatalf("removed=%v err=%v, the user's own `af` must stay", removed, err)
	}
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "/opt/my-af") {
		t.Fatalf("user's af entry was lost:\n%s", b)
	}
}

func TestMaterializeOpencodeSweepsTheLegacyBareAFEntry(t *testing.T) {
	resetAFName(t)
	file := opencodeConfigPath()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"mcp":{`+
		`"af":{"type":"local","command":["workspace-agent","mcp-stdio","--self-report"],"enabled":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, removed, _, err := materializeOpencode([]ServerDef{rotatedAFDef()}, nil); err != nil || len(removed) != 1 {
		t.Fatalf("removed=%v err=%v, want opencode's array-form af entry swept", removed, err)
	}
	// A non-af command under the same name stays.
	if err := os.WriteFile(file, []byte(`{"mcp":{"af":{"type":"local","command":["/bin/true","mcp-stdio"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, removed, _, err := materializeOpencode([]ServerDef{rotatedAFDef()}, nil); err != nil || len(removed) != 0 {
		t.Fatalf("removed=%v err=%v, a foreign `af` must stay", removed, err)
	}
}

func TestMaterializeCodexSweepsTheLegacyBareAFTable(t *testing.T) {
	resetAFName(t)
	dir := paths.CodexHome()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(file, []byte("[mcp_servers.af]\ncommand = \"/usr/local/bin/workspace-agent\"\n"+
		"args = [\"mcp-stdio\", \"--self-report\"]\n\n[mcp_servers.af.env]\nK = \"v\"\n\n"+
		"[mcp_servers.mine]\ncommand = \"/bin/true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	af := rotatedAFDef()
	if _, _, _, err := materializeCodex([]ServerDef{af}, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	got := string(b)
	if strings.Contains(got, "[mcp_servers.af]") || strings.Contains(got, "[mcp_servers.af.env]") ||
		!strings.Contains(got, "[mcp_servers.mine]") || !strings.Contains(got, "[mcp_servers."+af.Name+"]") {
		t.Fatalf("unexpected result:\n%s", got)
	}
	if _, _, changed, err := materializeCodex([]ServerDef{af}, nil); err != nil || changed {
		t.Fatalf("second run changed=%v err=%v, want a no-op", changed, err)
	}
}

func TestMaterializeCodexKeepsAUsersOwnServerNamedAF(t *testing.T) {
	resetAFName(t)
	dir := paths.CodexHome()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(file, []byte("[mcp_servers.af]\ncommand = \"/opt/my-af\"\nargs = [\"mcp-stdio\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := materializeCodex([]ServerDef{rotatedAFDef()}, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "[mcp_servers.af]") || !strings.Contains(string(b), "/opt/my-af") {
		t.Fatalf("user's af table was lost:\n%s", b)
	}
}
