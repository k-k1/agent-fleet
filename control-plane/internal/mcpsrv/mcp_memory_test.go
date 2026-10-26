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
	// answer is the Agent's reply; empty means {"ok":true}.
	answer string
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
	if c.answer != "" {
		return c.answer, c.agentErr
	}
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

// External writes are audited with the project id the Agent resolved (the CP cannot resolve a
// repository itself), an id-shaped target only; the memory's name never reaches the ledger, and a
// failed write is not recorded.
func TestMemoryWritesAreAudited(t *testing.T) {
	st := memStore(t)
	cp := newMemCP(t, st, "running")
	secretName := "ghp-looking-name"
	cp.answer = `{"name":"x","scope":"user","revision":1}`
	memCall(t, cp, "write", "memory_save", map[string]any{"name": secretName, "description": "d", "type": "user", "body": "b", "scope": "user"})
	cp.answer = `{"name":"x","scope":"project","revision":1,"project":"demo-0123456789ab"}`
	memCall(t, cp, "write", "memory_forget", map[string]any{"name": secretName, "revision": 1, "scope": "project", "repository": "https://github.com/k-k1/demo"})
	cp.answer = `{"scope":"project","project":"Not An ID/../x"}`
	memCall(t, cp, "write", "memory_save", map[string]any{"name": "y", "description": "d", "type": "user", "body": "b", "scope": "project", "project": "demo-0123456789ab"})
	cp.agentErr = errors.New("agent 400: {}")
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

// The audit detail follows the scope the Agent reports, and the target is the project it resolved
// from `repository`.
func TestMemoryAuditUsesTheAgentsScopeAndProject(t *testing.T) {
	st := memStore(t)
	cp := newMemCP(t, st, "running")
	cp.answer = `{"scope":"project","project":"demo-0123456789ab"}`
	memCall(t, cp, "write", "memory_save", map[string]any{"name": "n", "description": "d", "type": "user", "body": "b", "scope": "project", "repository": "git@github.com:k-k1/demo.git"})
	cp.answer = `{"scope":"user"}`
	memCall(t, cp, "write", "memory_save", map[string]any{"name": "n", "description": "d", "type": "user", "body": "b", "scope": "user"})
	rows, _ := st.ListAuditByTenant(context.Background(), cp.tenantID, 10)
	n := map[string]int{}
	for _, r := range rows {
		n[r.Action+" "+r.Detail+" "+r.Target]++
	}
	if n["memory.entry.save external scope=project demo-0123456789ab"] != 1 || n["memory.entry.save external scope=user "] != 1 {
		t.Fatalf("audit details: %v", n)
	}
}

// `repository` is relayed to the Agent as is (the Agent resolves it), on every tool that takes a
// project; the writes must declare scope, and the descriptions tell the model what to pass.
func TestMemoryToolsRelayRepositoryAndRequireScopeOnWrites(t *testing.T) {
	cp := newMemCP(t, memStore(t), "running")
	const repo = "git@github.com:k-k1/demo.git"
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"memory_index", map[string]any{"repository": repo}, "/agents/memory/external/entries?repository=git%40github.com%3Ak-k1%2Fdemo.git"},
		{"memory_search", map[string]any{"query": "q", "repository": repo}, "/agents/memory/external/search?q=q&repository=git%40github.com%3Ak-k1%2Fdemo.git"},
		{"memory_read", map[string]any{"name": "n", "scope": "project", "repository": repo}, "/agents/memory/external/read?name=n&repository=git%40github.com%3Ak-k1%2Fdemo.git&scope=project"},
	} {
		if text, isErr := memCall(t, cp, "read", c.tool, c.args); isErr || cp.gotPath != c.want {
			t.Errorf("%s: %q %v path %s", c.tool, text, isErr, cp.gotPath)
		}
	}
	memCall(t, cp, "write", "memory_save", map[string]any{"name": "n", "description": "d", "type": "project", "body": "b", "scope": "project", "repository": repo})
	var got map[string]any
	if json.Unmarshal([]byte(cp.gotBody), &got) != nil || got["repository"] != repo || got["scope"] != "project" {
		t.Errorf("save body: %s", cp.gotBody)
	}
	memCall(t, cp, "write", "memory_forget", map[string]any{"name": "n", "revision": 1, "scope": "project", "repository": repo})
	if json.Unmarshal([]byte(cp.gotBody), &got) != nil || got["repository"] != repo {
		t.Errorf("forget body: %s", cp.gotBody)
	}

	for _, tl := range memoryTools() {
		props, _ := tl.schema["properties"].(map[string]any)
		if tl.name == "memory_projects" {
			continue
		}
		if _, ok := props["repository"]; !ok {
			t.Errorf("%s has no repository argument", tl.name)
		}
		if tl.name == "memory_save" || tl.name == "memory_forget" {
			req, _ := tl.schema["required"].([]string)
			if !strings.Contains(strings.Join(req, ","), "scope") {
				t.Errorf("%s does not require scope: %v", tl.name, req)
			}
		}
		if !strings.Contains(tl.desc, "git remote get-url origin") {
			t.Errorf("%s description does not tell the model to pass the origin URL: %s", tl.name, tl.desc)
		}
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

// Every external memory tool ends with the note steering sessions inside the Workspace to the af
// server's own tools (#2766).
func TestMemoryToolsSteerSessionsToTheLocalTools(t *testing.T) {
	tools := memoryTools()
	if len(tools) != 6 {
		t.Fatalf("memory tools: %d", len(tools))
	}
	for _, tl := range tools {
		if !strings.HasSuffix(tl.desc, memoryOutsideNote) || !strings.Contains(tl.desc, "Inside an Agent Fleet session, use the af server's own memory tools instead") {
			t.Errorf("%s description lacks the note: %s", tl.name, tl.desc)
		}
	}
}
