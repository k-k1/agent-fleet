package mcpsrv

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentkinds"
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

// The Agent's own list_models (mcpx/mcp_fleet_spawn.go) accepts these kinds; the CP copy
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

// bodyCP keeps the body of the last Agent request so a test can read the driver create_session
// asked for.
type bodyCP struct {
	testCP
	body *[]byte
}

func (c bodyCP) AgentText(_ context.Context, _ runtime.Runtime, _, _ string, body []byte) (string, error) {
	*c.body = body
	return "{}", nil
}

func createSessionDriver(t *testing.T, kind string) string {
	t.Helper()
	var raw []byte
	a := API{cp: bodyCP{body: &raw}}
	ctx := withMCPCall(context.Background(), mcpCallInfo{patID: "grant_k"})
	if _, err := memberTool(t, "create_session").run(ctx, a, &Resolved{}, map[string]any{"kind": kind}); err != nil {
		t.Fatalf("create_session(kind=%q) = %v", kind, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("create_session(kind=%q) sent %q: %v", kind, raw, err)
	}
	d, _ := m["driver"].(string)
	return d
}

// create_session asks for the managed driver exactly for the snapshot's managedDefault kinds
// (the old hand list plus the managed-only lcpp and muse, which the Agent defaults to managed
// anyway), and for no other spelling: the Agent matches kinds exactly and falls an unknown one
// back to claude, so "Codex" must not ride as a managed request.
func TestCreateSessionManagedDriverFollowsTheCatalog(t *testing.T) {
	for _, e := range agentkinds.All() {
		want := ""
		if e.ManagedDefault {
			want = "managed"
		}
		if got := createSessionDriver(t, e.Kind); got != want {
			t.Errorf("create_session(kind=%q): driver %q, want %q", e.Kind, got, want)
		}
	}
	for _, k := range []string{"codex", "opencode", "copilot", "cursor", "kiro", "lcpp", "muse"} {
		if got := createSessionDriver(t, k); got != "managed" {
			t.Errorf("create_session(kind=%q): driver %q, want managed", k, got)
		}
	}
	for _, k := range []string{"", "claude", "agy", "shell", "ssm"} {
		if got := createSessionDriver(t, k); got != "" {
			t.Errorf("create_session(kind=%q): driver %q, want none", k, got)
		}
	}
}

// create_session refuses a kind the snapshot does not list — the Agent would otherwise fold it
// to claude and start a different agent than the caller named (#2297) — and says which kinds
// are accepted. Nothing is sent to the Agent. The empty kind stays claude.
func TestCreateSessionRefusesUnregisteredKind(t *testing.T) {
	for _, k := range []string{"Codex", "CODEX", " codex", "codex ", "kiro-cli", "cursor-agent",
		"github-copilot", "llamacpp", "llama.cpp", "Muse", "open-code", "antigravity"} {
		var raw []byte
		a := API{cp: bodyCP{body: &raw}}
		_, err := memberTool(t, "create_session").run(context.Background(), a, &Resolved{}, map[string]any{"kind": k})
		if err == nil || !strings.Contains(err.Error(), "accepted kinds") || !strings.Contains(err.Error(), "codex") {
			t.Errorf("create_session(kind=%q) = %v, want a refusal naming the accepted kinds", k, err)
		}
		if raw != nil {
			t.Errorf("create_session(kind=%q) still reached the Agent: %s", k, raw)
		}
	}
	for _, k := range []string{"", "claude", "codex"} {
		var raw []byte
		a := API{cp: bodyCP{body: &raw}}
		ctx := withMCPCall(context.Background(), mcpCallInfo{patID: "grant_k"})
		if _, err := memberTool(t, "create_session").run(ctx, a, &Resolved{}, map[string]any{"kind": k}); err != nil || raw == nil {
			t.Errorf("create_session(kind=%q) = %v (sent %q), want it forwarded", k, err, raw)
		}
	}
}
