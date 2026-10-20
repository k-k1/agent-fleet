package mcpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// memRT is a runtime that only knows its state.
type memRT struct {
	runtime.Runtime
	state string
}

func (r memRT) State(context.Context) string { return r.state }

// memCP wires the three seams a memory tool call crosses: resolution, the Agent and the scope ladder.
type memCP struct {
	testCP
	state     string
	agentErr  error
	gotMethod string
	gotPath   string
	gotBody   string
	calls     int
	// prin and tenantID are the seeded member and tenant behind the PAT (newMemCP).
	prin     mcpPrincipal
	tenantID string
}

func (c *memCP) ResolveByMembership(context.Context, string, string) (*Resolved, *APIError) {
	return &Resolved{RT: memRT{state: c.state}, MV: store.MembershipView{MembershipID: c.prin.membershipID, TenantID: c.tenantID}}, nil
}

func (c *memCP) AgentText(_ context.Context, _ runtime.Runtime, method, path string, body []byte) (string, error) {
	c.calls++
	c.gotMethod, c.gotPath, c.gotBody = method, path, string(body)
	return `{"ok":true}`, c.agentErr
}

func memCall(t *testing.T, cp *memCP, scope, tool string, args map[string]any) (string, bool) {
	t.Helper()
	a := New(cp)
	params, _ := json.Marshal(map[string]any{"name": tool, "arguments": args})
	ctx := withMCPCall(context.Background(), mcpCallInfo{patID: "pat1", client: "claude-code/9.9"})
	prin := cp.prin
	prin.scope = scope
	resp := a.mcpToolCall(ctx, &prin,
		rpcRequest{ID: json.RawMessage(`1`), Params: params})
	if resp.Error != nil {
		return resp.Error.Message, true
	}
	r := resp.Result.(map[string]any)
	text := r["content"].([]map[string]any)[0]["text"].(string)
	isErr, _ := r["isError"].(bool)
	return text, isErr
}

func memStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

// newMemCP seeds a tenant with one member in the store, so the tenant-wide switch has a real row.
func newMemCP(t *testing.T, st store.Store, state string) *memCP {
	t.Helper()
	ctx := context.Background()
	tn, err := st.CreateTenant(ctx, "sales", "Sales")
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertIdentity(ctx, "member@example.com", "member-example-com", "")
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	return &memCP{testCP: testCP{store: st}, state: state, tenantID: tn.ID,
		prin: mcpPrincipal{patID: "pat1", identityID: id.ID, membershipID: m.ID}}
}

// Reads are listed for a read PAT, writes only from the write tier, and a read PAT cannot call a
// write tool by name either.
func TestMemoryToolsFollowTheScopeTier(t *testing.T) {
	cp := newMemCP(t, memStore(t), "running")
	list := func(scope string) map[string]bool {
		out := map[string]bool{}
		for _, tl := range New(cp).toolsFor(context.Background(), &mcpPrincipal{identityID: cp.prin.identityID, membershipID: cp.prin.membershipID, scope: scope}) {
			out[tl.name] = true
		}
		return out
	}
	r, w := list("read"), list("write")
	for _, n := range []string{"memory_projects", "memory_index", "memory_search", "memory_read"} {
		if !r[n] || !w[n] {
			t.Errorf("%s must be listed at read and write", n)
		}
	}
	for _, n := range []string{"memory_save", "memory_forget"} {
		if r[n] || !w[n] {
			t.Errorf("%s: read=%v write=%v", n, r[n], w[n])
		}
	}
	text, isErr := memCall(t, cp, "read", "memory_save", map[string]any{"name": "x"})
	if !strings.Contains(text, "unknown or unauthorized tool") || cp.calls != 0 {
		t.Fatalf("read PAT reached memory_save: %q isErr=%v calls=%d", text, isErr, cp.calls)
	}
}

// Arguments reach the Agent's external routes unchanged, with the client name and an explicit project.
func TestMemoryToolsProxyToTheAgent(t *testing.T) {
	cp := newMemCP(t, memStore(t), "running")
	if _, isErr := memCall(t, cp, "read", "memory_read", map[string]any{"name": "n1", "scope": "project", "project": "demo-0123456789ab"}); isErr {
		t.Fatal("read failed")
	}
	if cp.gotMethod != "GET" || cp.gotPath != "/agents/memory/external/read?name=n1&project=demo-0123456789ab&scope=project" {
		t.Fatalf("read proxied as %s %s", cp.gotMethod, cp.gotPath)
	}
	if _, isErr := memCall(t, cp, "write", "memory_save", map[string]any{
		"name": "n1", "description": "d", "type": "project", "body": "b", "project": "demo-0123456789ab", "scope": "project"}); isErr {
		t.Fatal("save failed")
	}
	var got map[string]any
	if cp.gotMethod != "POST" || cp.gotPath != "/agents/memory/external/entries" || json.Unmarshal([]byte(cp.gotBody), &got) != nil ||
		got["project"] != "demo-0123456789ab" || got["client"] != "claude-code/9.9" || got["name"] != "n1" {
		t.Fatalf("save proxied as %s %s %s", cp.gotMethod, cp.gotPath, cp.gotBody)
	}
}

// A stopped workspace is answered by the CP, with no call to the Agent.
func TestMemoryToolsSayWhenTheWorkspaceIsStopped(t *testing.T) {
	cp := newMemCP(t, memStore(t), "stopped")
	text, isErr := memCall(t, cp, "read", "memory_index", nil)
	if !isErr || !strings.Contains(text, "not running") || !strings.Contains(text, "start the workspace") || cp.calls != 0 {
		t.Fatalf("stopped: %q isErr=%v calls=%d", text, isErr, cp.calls)
	}
}

// The Agent's refusal is relayed as its own message, not as "agent 403: {json}".
func TestMemoryToolsRelayTheAgentsRefusal(t *testing.T) {
	cp := newMemCP(t, memStore(t), "running")
	cp.agentErr = errors.New(`agent 403: {"error":{"code":"memory_external_disabled","message":"turn it on in Settings > Agent memory"}}`)
	text, isErr := memCall(t, cp, "read", "memory_projects", nil)
	if !isErr || text != "memory_external_disabled: turn it on in Settings > Agent memory" {
		t.Fatalf("refusal: %q", text)
	}
}

// External writes are audited with an id-shaped target only; the memory's name never reaches the
// ledger, and a failed write is not recorded.
func TestMemoryWritesAreAudited(t *testing.T) {
	st := memStore(t)
	cp := newMemCP(t, st, "running")
	secretName := "ghp-looking-name"
	memCall(t, cp, "write", "memory_save", map[string]any{"name": secretName, "description": "d", "type": "user", "body": "b", "scope": "user"})
	memCall(t, cp, "write", "memory_forget", map[string]any{"name": secretName, "revision": 1, "scope": "project", "project": "demo-0123456789ab"})
	memCall(t, cp, "write", "memory_save", map[string]any{"name": "y", "description": "d", "type": "user", "body": "b", "project": "Not An ID/../x"})
	cp.agentErr = errors.New("agent 409: {}")
	memCall(t, cp, "write", "memory_save", map[string]any{"name": "z", "description": "d", "type": "user", "body": "b"})

	rows, err := st.ListAuditByTenant(context.Background(), cp.tenantID, 10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("audit rows: %d %v", len(rows), err)
	}
	byAction := map[string][]string{}
	for _, r := range rows {
		if r.ActorKind != "mcp" || r.ActorID != "pat1" || strings.Contains(r.Target+r.Detail, secretName) {
			t.Errorf("row %+v", r)
		}
		byAction[r.Action] = append(byAction[r.Action], r.Target)
	}
	if len(byAction["memory.entry.save"]) != 2 || len(byAction["memory.entry.forget"]) != 1 || byAction["memory.entry.forget"][0] != "demo-0123456789ab" {
		t.Errorf("actions %v", byAction)
	}
	for _, tgt := range byAction["memory.entry.save"] {
		if tgt != "" {
			t.Errorf("a non-id project reached the ledger: %q", tgt)
		}
	}
}

// The client's name comes from the stateless `_meta`, else the User-Agent.
func TestMCPClientName(t *testing.T) {
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("User-Agent", "ua/1")
	if got := mcpClientName(r, paramsMeta{}); got != "ua/1" {
		t.Errorf("fallback: %q", got)
	}
	p := paramsMeta{Meta: map[string]json.RawMessage{metaClientInfo: json.RawMessage(`{"name":"claude-code","version":"2.0"}`)}}
	if got := mcpClientName(r, p); got != "claude-code/2.0" {
		t.Errorf("meta: %q", got)
	}
}

// With scope omitted the Agent works in the project when one is named, so the audit says project;
// an explicit user scope stays user.
func TestMemoryAuditUsesTheAgentsDefaultScope(t *testing.T) {
	st := memStore(t)
	cp := newMemCP(t, st, "running")
	base := map[string]any{"name": "n", "description": "d", "type": "user", "body": "b"}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	memCall(t, cp, "write", "memory_save", with(map[string]any{"project": "demo-0123456789ab"}))
	memCall(t, cp, "write", "memory_save", with(map[string]any{"project": "demo-0123456789ab", "scope": "user"}))
	memCall(t, cp, "write", "memory_save", with(nil))
	memCall(t, cp, "write", "memory_forget", map[string]any{"name": "n", "revision": 1, "project": "demo-0123456789ab"})
	rows, _ := st.ListAuditByTenant(context.Background(), cp.tenantID, 10)
	n := map[string]int{}
	for _, r := range rows {
		n[r.Action+" "+r.Detail]++
	}
	if n["memory.entry.save external scope=project"] != 1 || n["memory.entry.save external scope=user"] != 2 ||
		n["memory.entry.forget external scope=project"] != 1 {
		t.Fatalf("audit details: %v", n)
	}
}

// With the tenant-wide switch off (#2318) the memory tools are neither listed nor callable, at any
// scope, and the Agent is never reached; turning it back on restores them at once.
func TestTenantSwitchWithholdsTheMemoryTools(t *testing.T) {
	st := memStore(t)
	cp := newMemCP(t, st, "running")
	ctx := context.Background()
	listed := func() []string {
		var out []string
		prin := cp.prin
		prin.scope = "write"
		for _, tl := range New(cp).toolsFor(ctx, &prin) {
			if strings.HasPrefix(tl.name, "memory_") {
				out = append(out, tl.name)
			}
		}
		return out
	}
	if got := listed(); len(got) != 6 {
		t.Fatalf("a new tenant must keep the tools (default on): %v", got)
	}
	if err := st.SetTenantExternalMemory(ctx, cp.tenantID, false); err != nil {
		t.Fatal(err)
	}
	if got := listed(); len(got) != 0 {
		t.Fatalf("switch off, still listed: %v", got)
	}
	for _, c := range []struct{ scope, tool string }{{"read", "memory_index"}, {"write", "memory_projects"}, {"write", "memory_save"}, {"write", "memory_forget"}} {
		text, isErr := memCall(t, cp, c.scope, c.tool, map[string]any{"name": "n"})
		if !strings.Contains(text, "unknown or unauthorized tool") || cp.calls != 0 {
			t.Fatalf("%s reached past the switch: %q isErr=%v calls=%d", c.tool, text, isErr, cp.calls)
		}
	}
	// Other tools are untouched.
	prin := cp.prin
	prin.scope = "read"
	found := false
	for _, tl := range New(cp).toolsFor(ctx, &prin) {
		found = found || tl.name == "list_my_sessions"
	}
	if !found {
		t.Error("the switch must only withhold memory tools")
	}
	if err := st.SetTenantExternalMemory(ctx, cp.tenantID, true); err != nil {
		t.Fatal(err)
	}
	if got := listed(); len(got) != 6 {
		t.Fatalf("switch back on: %v", got)
	}
	if _, isErr := memCall(t, cp, "read", "memory_index", nil); isErr || cp.calls != 1 {
		t.Fatalf("call after re-enable: isErr=%v calls=%d", isErr, cp.calls)
	}
}

// A membership the store does not know (revoked, or a store error) gets no memory tools.
func TestMemoryToolsFailClosedWithoutAMembership(t *testing.T) {
	cp := newMemCP(t, memStore(t), "running")
	prin := cp.prin
	prin.membershipID = "gone"
	prin.scope = "write"
	for _, tl := range New(cp).toolsFor(context.Background(), &prin) {
		if strings.HasPrefix(tl.name, "memory_") {
			t.Fatalf("%s listed for an unknown membership", tl.name)
		}
	}
}
