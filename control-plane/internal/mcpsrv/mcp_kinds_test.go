package mcpsrv

import (
	"context"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// pathCP records the Agent paths a tool asks for, so a tool can be run without a
// Workspace behind it.
type pathCP struct {
	testCP
	paths *[]string
}

func (c pathCP) AgentText(_ context.Context, _ runtime.Runtime, method, path string, _ []byte) (string, error) {
	*c.paths = append(*c.paths, method+" "+path)
	return "[]", nil
}

func memberTool(t *testing.T, name string) mcpTool {
	t.Helper()
	for _, tool := range memberTools() {
		if tool.name == name {
			return tool
		}
	}
	t.Fatalf("%s tool not found", name)
	return mcpTool{}
}

// The Agent's own list_models (mcpx/mcp_stdio.go) accepts these kinds; the CP copy
// must not refuse any of them (#1075: lcpp was rejected here only).
func TestListModelsAcceptsEveryAgentKind(t *testing.T) {
	tool := memberTool(t, "list_models")
	var paths []string
	a := API{cp: pathCP{paths: &paths}}
	kinds := []string{"claude", "codex", "opencode", "agy", "copilot", "cursor", "kiro", "lcpp", "muse"}
	for _, kind := range kinds {
		if _, err := tool.run(context.Background(), a, &Resolved{}, map[string]any{"kind": kind}); err != nil {
			t.Errorf("list_models(kind=%q) = %v, want accepted", kind, err)
		}
	}
	if len(paths) != len(kinds) || paths[len(paths)-1] != "GET /agents/muse/models" {
		t.Fatalf("Agent paths = %v", paths)
	}
	desc, _ := tool.schema["properties"].(map[string]any)["kind"].(map[string]any)["description"].(string)
	for _, kind := range kinds {
		if !strings.Contains(desc, kind) {
			t.Errorf("list_models kind description %q does not name %q", desc, kind)
		}
	}
	for _, kind := range []string{"shell", "ssm", ""} {
		if _, err := tool.run(context.Background(), a, &Resolved{}, map[string]any{"kind": kind}); err == nil {
			t.Errorf("list_models(kind=%q) accepted, want refused", kind)
		}
	}
}

func TestCreateSessionKindDescriptionNamesManagedOnlyKinds(t *testing.T) {
	tool := memberTool(t, "create_session")
	desc, _ := tool.schema["properties"].(map[string]any)["kind"].(map[string]any)["description"].(string)
	for _, kind := range []string{"| lcpp", "| muse"} {
		if !strings.Contains(desc, kind) {
			t.Errorf("create_session kind description does not list %q: %q", kind, desc)
		}
	}
}
