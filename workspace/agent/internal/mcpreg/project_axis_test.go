package mcpreg

// docs/log/56 §3 / §13 "separation of axes": internal/mcpreg.Materialize* writes ONLY each
// CLI's own user/global config (paths.ClaudeConfigDir / CodexHome / …), never a repo's
// project-scope files (.mcp.json, opencode.json, .codex/config.toml, …) — those are
// internal/mcpproj's read-only territory in P0, and any future P1 write path there
// is a distinct, explicitly user-triggered operation (docs/log/56 §5's "pure one-shot",
// never an automatic materialize trigger). This test pins that boundary: a
// project-scope-shaped file sitting in an arbitrary directory must survive
// MaterializeAll byte-for-byte, even though MaterializeAll genuinely has a def to
// write (proving it did real work elsewhere, not a no-op skip).

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializeAllNeverTouchesRepoFiles(t *testing.T) {
	withTempCLIHomes(t)
	if _, err := Create(ServerDef{
		Name: "srv", Transport: TransportStdio, Command: "/bin/echo",
		Enabled: true, Targets: Targets{Session: true},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	repoDir := t.TempDir()
	mcpJSON := filepath.Join(repoDir, ".mcp.json")
	const handWritten = `{"mcpServers":{"hand-written":{"command":"/bin/echo","args":["hi"]}}}`
	writeFile(t, mcpJSON, handWritten)
	codexToml := filepath.Join(repoDir, ".codex", "config.toml")
	const codexBody = "[mcp_servers.hand]\ncommand = \"/bin/echo\"\n"
	writeFile(t, codexToml, codexBody)

	res := MaterializeAll()
	for _, r := range res {
		if r.Err != "" {
			t.Fatalf("%s: %s", r.Kind, r.Err)
		}
	}
	// Confirm MaterializeAll actually wrote somewhere (a no-op run would make the
	// "untouched repo file" assertion below vacuous).
	if b, err := os.ReadFile(claudeJSONPath()); err != nil || len(b) == 0 {
		t.Fatalf("expected claude's OWN config to be written: %v", err)
	}

	after, err := os.ReadFile(mcpJSON)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != handWritten {
		t.Fatalf("MaterializeAll touched a repo .mcp.json:\n got: %s\nwant: %s", after, handWritten)
	}
	afterCodex, err := os.ReadFile(codexToml)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterCodex) != codexBody {
		t.Fatalf("MaterializeAll touched a repo .codex/config.toml:\n got: %s\nwant: %s", afterCodex, codexBody)
	}
}
