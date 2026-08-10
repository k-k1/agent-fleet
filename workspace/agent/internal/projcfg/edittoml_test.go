package projcfg

import (
	"strings"
	"testing"
)

func TestUpsertCodexBlockAppendsAndReplaces(t *testing.T) {
	src := `[projects."/home/dev/repos/x"]
trust_level = "trusted"

[mcp_servers.old]
command = "/bin/echo"
`
	out := UpsertCodexBlock(src, "srv", "[mcp_servers.srv]\ncommand = \"/bin/echo\"")
	if out == src {
		t.Fatal("expected a change")
	}
	// The unrelated trust table and the pre-existing "old" server must survive
	// byte-for-byte.
	for _, want := range []string{`[projects."/home/dev/repos/x"]`, `trust_level = "trusted"`, `[mcp_servers.old]`} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q to survive:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `[mcp_servers.srv]`) {
		t.Fatalf("new block missing:\n%s", out)
	}

	// Upserting the SAME name again must replace, not duplicate.
	out2 := UpsertCodexBlock(out, "srv", "[mcp_servers.srv]\ncommand = \"/bin/echo2\"")
	if strings.Count(out2, "[mcp_servers.srv]") != 1 {
		t.Fatalf("expected exactly one srv table, got:\n%s", out2)
	}
	if !strings.Contains(out2, "echo2") {
		t.Fatalf("replacement value missing:\n%s", out2)
	}
}

func TestDeleteCodexBlockRemovesOnlyNamed(t *testing.T) {
	src := UpsertCodexBlock("", "keep", "[mcp_servers.keep]\ncommand = \"/bin/echo\"")
	src = UpsertCodexBlock(src, "drop", "[mcp_servers.drop]\ncommand = \"/bin/true\"")
	out := DeleteCodexBlock(src, "drop")
	if strings.Contains(out, "mcp_servers.drop") {
		t.Fatalf("drop not removed:\n%s", out)
	}
	if !strings.Contains(out, "mcp_servers.keep") {
		t.Fatalf("keep removed too:\n%s", out)
	}
}

func TestDeleteCodexBlockMissingIsNoop(t *testing.T) {
	src := UpsertCodexBlock("", "keep", "[mcp_servers.keep]\ncommand = \"/bin/echo\"")
	out := DeleteCodexBlock(src, "nope")
	if out != src {
		t.Fatalf("expected no-op:\ngot:\n%s\nwant:\n%s", out, src)
	}
}
