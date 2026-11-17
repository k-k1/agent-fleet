package mcpsrv

import (
	"strings"
	"testing"
)

// A tidy run's apply and undo are the Console's (ADR 0137 section 8): a PAT or an OAuth grant
// reaches neither, so no tool of /mcp names them.
func TestNoToolOfMCPTidiesOrUndoes(t *testing.T) {
	for _, set := range [][]mcpTool{memberTools(), adminTools()} {
		for _, tl := range set {
			if strings.Contains(strings.ToLower(tl.name), "tidy") {
				t.Fatalf("tool %s", tl.name)
			}
		}
	}
}
