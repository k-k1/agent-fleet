package mcpx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// No af MCP tool may reach the background memory review's queue (ADR 0117 decision 6): approving,
// rejecting and undoing a run are the member's, and the MCP server runs inside a session that holds
// the same AGENT_TOKEN as the shell. The Agent refuses them without the Control Plane's member-origin
// mark whatever this file says; this keeps a tool from being written that would only ever fail.
func TestNoMCPToolNamesTheMemoryReviewRoutes(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files read: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "memory-review") {
			t.Errorf("%s names the memory review's routes", f)
		}
	}
}
