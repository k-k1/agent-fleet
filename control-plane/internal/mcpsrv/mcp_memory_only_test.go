package mcpsrv

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// The memory-only handler lists the memory tools and nothing else, even for a tenant
// administrator at the write tier, and refuses a session or admin tool called by name (#2764).
func TestMemoryOnlyHandlerServesOnlyMemoryTools(t *testing.T) {
	st := memStore(t)
	cp := newMemCP(t, st, "running")
	ctx := context.Background()
	if err := st.SetTenantExternalMemory(ctx, cp.tenantID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureMembership(ctx, cp.prin.identityID, cp.tenantID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	prin := cp.prin
	prin.scope = scopeWrite
	names := func(a API) []string {
		var out []string
		for _, tl := range a.toolsFor(ctx, &prin) {
			out = append(out, tl.name)
		}
		sort.Strings(out)
		return out
	}
	full, mem := names(New(cp)), names(New(cp).MemoryOnly())
	if got := strings.Join(mem, ","); got != "memory_forget,memory_index,memory_projects,memory_read,memory_save,memory_search" {
		t.Fatalf("memory-only tools = %s", got)
	}
	if len(full) <= len(mem) {
		t.Fatalf("the full handler lost tools: %v", full)
	}
	for _, name := range []string{"list_my_sessions", "stop_session", "list_workspaces"} {
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": map[string]any{}})
		resp := New(cp).MemoryOnly().mcpToolCall(ctx, &prin, rpcRequest{ID: json.RawMessage(`1`), Params: params})
		if resp.Error == nil || !strings.Contains(resp.Error.Message, "unknown or unauthorized tool") {
			t.Errorf("%s at the memory endpoint = %+v", name, resp)
		}
	}
	// The tenant switch still gates it: off means an empty list, not the session tools.
	if err := st.SetTenantExternalMemory(ctx, cp.tenantID, false); err != nil {
		t.Fatal(err)
	}
	if got := names(New(cp).MemoryOnly()); len(got) != 0 {
		t.Fatalf("memory endpoint with the tenant switch off lists %v", got)
	}
}

// server/discover tells the client which endpoint it is talking to.
func TestMemoryOnlyHandlerInstructions(t *testing.T) {
	cp := newMemCP(t, memStore(t), "running")
	for _, c := range []struct {
		a    API
		want string
	}{{New(cp), mcpInstructions}, {New(cp).MemoryOnly(), mcpMemoryInstructions}} {
		resp := c.a.dispatchMCPMethod(context.Background(), &cp.prin, rpcRequest{ID: json.RawMessage(`1`), Method: "server/discover"})
		if got := resp.Result.(map[string]any)["instructions"]; got != c.want {
			t.Errorf("instructions = %v, want %q", got, c.want)
		}
	}
}
