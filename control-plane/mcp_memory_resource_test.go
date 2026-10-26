package main

// The memory-only MCP resource (#2764): /mcp/memory serves the external memory tools alone, and
// a token for it, OAuth or PAT, is refused at /mcp and the other way round.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const testMemResource = testBase + "/mcp/memory"

func (e *mcpOAuthEnv) mcpCallAt(path, token, method string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q}`, method)
	hdr := map[string]string{"Content-Type": "application/json"}
	if token != "" {
		hdr["Authorization"] = "Bearer " + token
	}
	return e.do(http.MethodPost, path, body, hdr)
}

// allowExternalMemory turns the tenant switch on, so the memory tools are listed at all.
func (e *mcpOAuthEnv) allowExternalMemory() {
	e.t.Helper()
	if err := e.mgr.store.SetTenantExternalMemory(context.Background(), e.tenA.ID, true); err != nil {
		e.t.Fatal(err)
	}
}

var memoryToolNames = []string{"memory_projects", "memory_index", "memory_search", "memory_read", "memory_save", "memory_forget"}

// onlyMemoryTools fails unless names is exactly the six memory tools.
func onlyMemoryTools(t *testing.T, names map[string]bool) {
	t.Helper()
	for _, n := range memoryToolNames {
		if !names[n] {
			t.Errorf("memory endpoint lacks %s: %v", n, names)
		}
	}
	if len(names) != len(memoryToolNames) {
		t.Errorf("memory endpoint lists %d tools, want only the %d memory tools: %v", len(names), len(memoryToolNames), names)
	}
}

func TestMCPMemoryResourceDiscovery(t *testing.T) {
	e := newMCPOAuthEnv(t)
	if !isAuthExempt(mcpOAuthMemPRMPath) {
		t.Fatal("the memory endpoint's metadata must be reachable without a session")
	}
	var prm map[string]any
	rec := e.do("GET", "/.well-known/oauth-protected-resource/mcp/memory", "", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &prm); err != nil || rec.Code != 200 {
		t.Fatalf("memory PRM: %d %s", rec.Code, rec.Body)
	}
	if prm["resource"] != testMemResource || fmt.Sprint(prm["authorization_servers"]) != "["+testIssuer+"]" {
		t.Errorf("memory protected resource metadata = %v", prm)
	}
	// /mcp's document is unchanged.
	_ = json.Unmarshal(e.do("GET", mcpOAuthPRMPath, "", nil).Body.Bytes(), &prm)
	if prm["resource"] != testResource {
		t.Errorf("/mcp protected resource metadata = %v", prm)
	}
	rec = e.mcpCallAt(mcpsrv.MemoryPath, "", "tools/list")
	want := `Bearer resource_metadata="` + testBase + `/.well-known/oauth-protected-resource/mcp/memory"`
	if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != want {
		t.Errorf("/mcp/memory 401 = %d %q, want %q", rec.Code, rec.Header().Get("WWW-Authenticate"), want)
	}
}

func TestMCPMemoryResourceOffIs404AndBare401(t *testing.T) {
	e := newMCPOAuthEnv(t)
	t.Setenv("AF_MCP_OAUTH", "")
	e.api = newMCPOAuthAPI(config{mgr: e.mgr, brand: newBrandResolver(brandConfig{}, nil)})
	e.mux = http.NewServeMux()
	e.api.mount(e.mux)
	e.api.mountMCP(e.mux, mcpsrv.New(cpDeps{e.mgr}))
	if rec := e.do("GET", mcpOAuthMemPRMPath, "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("memory PRM with OAuth off = %d, want 404", rec.Code)
	}
	if rec := e.mcpCallAt(mcpsrv.MemoryPath, "", "tools/list"); rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("/mcp/memory with OAuth off = %d %q, want a bare 401", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

// An OAuth connection made for /mcp/memory lists and calls only the memory tools there, is
// refused at /mcp, and a /mcp connection is refused at /mcp/memory.
func TestMCPMemoryResourceOAuthTokensAreSplit(t *testing.T) {
	e := newMCPOAuthEnv(t)
	e.allowExternalMemory()
	id := e.registerClient(loopbackCB)
	mem := e.connectTo(id, loopbackCB, "read write", testMemResource)
	full := e.connect(id, loopbackCB, "read write")

	onlyMemoryTools(t, toolNames(t, e.mcpCallAt(mcpsrv.MemoryPath, mem.Access, "tools/list")))
	if rec := e.mcpCall(mem.Access, "tools/list"); rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Errorf("memory token at /mcp = %d %q, want 401 invalid_token", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	if rec := e.mcpCallAt(mcpsrv.MemoryPath, full.Access, "tools/list"); rec.Code != http.StatusUnauthorized {
		t.Errorf("/mcp token at /mcp/memory = %d, want 401", rec.Code)
	}
	// /mcp itself is unchanged: sessions and memory for the /mcp token.
	names := toolNames(t, e.mcpCall(full.Access, "tools/list"))
	if !names["send_to_session"] || !names["memory_save"] {
		t.Errorf("/mcp token tools = %v", names)
	}
	// A session tool is refused by name at /mcp/memory, not only hidden.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_my_sessions","arguments":{}}}`
	rec := e.do(http.MethodPost, mcpsrv.MemoryPath, body, map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + mem.Access})
	if !strings.Contains(rec.Body.String(), "unknown or unauthorized tool") {
		t.Errorf("list_my_sessions at /mcp/memory = %d %s", rec.Code, rec.Body)
	}

	grants, _ := e.mgr.store.ListMCPOAuthGrantsByIdentity(context.Background(), e.ident.ID, e.now().Unix())
	got := map[string]int{}
	for _, g := range grants {
		got[g.Resource]++
	}
	if got[testMemResource] != 1 || got[testResource] != 1 {
		t.Errorf("grant resources = %v", got)
	}
	// The record says which endpoint was hit.
	var memGrant string
	for _, g := range grants {
		if g.Resource == testMemResource {
			memGrant = g.ID
		}
	}
	calls := e.mgr.mcpCalls.Recent(mcpsrv.CallKey(true, memGrant))
	if len(calls) == 0 || calls[0].Endpoint != mcpsrv.MemoryPath {
		t.Errorf("memory grant calls = %+v, want the newest at %s", calls, mcpsrv.MemoryPath)
	}
}

// A refresh keeps the grant's resource, and asking to move it is invalid_target.
func TestMCPMemoryResourceRefreshKeepsTheBinding(t *testing.T) {
	e := newMCPOAuthEnv(t)
	e.allowExternalMemory()
	id := e.registerClient(loopbackCB)
	mem := e.connectTo(id, loopbackCB, "read write", testMemResource)

	moved := e.post(mcpOAuthToken, url.Values{"grant_type": {"refresh_token"}, "client_id": {id},
		"refresh_token": {mem.Refresh}, "resource": {testResource}})
	if moved.Code != http.StatusBadRequest || jsonField(t, moved, "error") != "invalid_target" {
		t.Fatalf("refresh into /mcp = %d %s, want invalid_target", moved.Code, moved.Body)
	}
	// The refused attempt did not spend the token.
	rec := e.post(mcpOAuthToken, url.Values{"grant_type": {"refresh_token"}, "client_id": {id},
		"refresh_token": {mem.Refresh}, "resource": {testMemResource}})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh at the grant's own resource: %d %s", rec.Code, rec.Body)
	}
	at := jsonField(t, rec, "access_token")
	onlyMemoryTools(t, toolNames(t, e.mcpCallAt(mcpsrv.MemoryPath, at, "tools/list")))
	if rec := e.mcpCall(at, "tools/list"); rec.Code != http.StatusUnauthorized {
		t.Errorf("refreshed memory token at /mcp = %d, want 401", rec.Code)
	}
	// Without a resource parameter the binding is the grant's as well.
	rec = e.refresh(id, jsonField(t, rec, "refresh_token"))
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh without resource: %d %s", rec.Code, rec.Body)
	}
	if rec := e.mcpCall(jsonField(t, rec, "access_token"), "tools/list"); rec.Code != http.StatusUnauthorized {
		t.Errorf("memory token refreshed without resource reached /mcp: %d", rec.Code)
	}
}

// A code is redeemed only for the resource it was issued for, and an unknown resource is refused
// at authorize.
func TestMCPMemoryResourceCodeIsBoundToItsResource(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	verifier, challenge := pkcePair()
	q := e.authQuery(id, loopbackCB, challenge)
	q.Set("resource", testMemResource)
	ticket := e.ticketFrom(e.authorize(q))
	code := locationQuery(t, e.consent(ticket, url.Values{"membership": {e.membershipID()}, "scope": {"read"}})).Get("code")
	rec := e.post(mcpOAuthToken, url.Values{"grant_type": {"authorization_code"}, "client_id": {id},
		"code": {code}, "redirect_uri": {loopbackCB}, "code_verifier": {verifier}, "resource": {testResource}})
	if rec.Code != http.StatusBadRequest || jsonField(t, rec, "error") != "invalid_target" {
		t.Errorf("memory code redeemed for /mcp = %d %s, want invalid_target", rec.Code, rec.Body)
	}
	q.Set("resource", testBase+"/mcp/other")
	if got := locationQuery(t, e.authorize(q)); got.Get("error") != "invalid_target" {
		t.Errorf("unknown resource at authorize: %v", got)
	}
}

// A memory grant does not make a silent (prompt=none) /mcp grant, nor the reverse.
func TestMCPMemoryResourcePromptNoneIsPerResource(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	_, challenge := pkcePair()
	none := func(resource string) url.Values {
		q := e.authQuery(id, loopbackCB, challenge)
		q.Set("prompt", "none")
		q.Set("scope", "read")
		q.Set("resource", resource)
		return locationQuery(t, e.authorize(q))
	}
	e.connectTo(id, loopbackCB, "read write", testMemResource)
	if got := none(testResource); got.Get("error") != "consent_required" {
		t.Errorf("/mcp after a memory grant: %v, want consent_required", got)
	}
	if got := none(testMemResource); got.Get("code") == "" {
		t.Errorf("memory after a memory grant: %v, want a code", got)
	}
}

func TestMCPMemoryResourceConsentPageSaysMemoryOnly(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient("https://claude.ai/api/mcp/auth_callback")
	_, challenge := pkcePair()
	q := e.authQuery(id, "https://claude.ai/api/mcp/auth_callback", challenge)
	q.Set("resource", testMemResource)
	body := e.do(http.MethodGet, mcpOAuthAuthorize+"?"+q.Encode(), "", map[string]string{"Accept-Language": "en"}).Body.String()
	for _, want := range []string{"connect to your Agent Fleet memory", "cannot reach sessions or admin tools", "save and forget memories"} {
		if !strings.Contains(body, want) {
			t.Errorf("memory consent page lacks %q", want)
		}
	}
	if strings.Contains(body, "send to sessions") {
		t.Error("memory consent page describes session tools")
	}
}

// PATs get the same split: a memory PAT works only at /mcp/memory, a /mcp PAT (every token
// issued before the split included) only at /mcp.
func TestMCPMemoryResourcePATsAreSplit(t *testing.T) {
	e := newMCPOAuthEnv(t)
	e.allowExternalMemory()
	pats := newPATAPI(e.mgr)
	mint := func(body string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		pats.create(w, httptest.NewRequest(http.MethodPost, "/api/pat", strings.NewReader(body)), e.ident)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("create: %d %s", w.Code, w.Body)
		}
		out["status"] = w.Code
		return out
	}
	mem := mint(`{"name":"claude.ai","scope":"write","endpoint":"memory"}`)
	full := mint(`{"name":"desktop","scope":"write"}`)
	if mem["endpoint"] != "memory" || full["endpoint"] != "mcp" {
		t.Fatalf("minted %v and %v", mem, full)
	}
	if bad := mint(`{"name":"x","endpoint":"admin"}`); bad["status"] != http.StatusBadRequest || !strings.Contains(fmt.Sprint(bad), "invalid_endpoint") {
		t.Errorf("unknown resource: %v", bad)
	}
	memTok, fullTok := mem["token"].(string), full["token"].(string)

	onlyMemoryTools(t, toolNames(t, e.mcpCallAt(mcpsrv.MemoryPath, memTok, "tools/list")))
	if rec := e.mcpCall(memTok, "tools/list"); rec.Code != http.StatusUnauthorized {
		t.Errorf("memory PAT at /mcp = %d, want 401", rec.Code)
	}
	if rec := e.mcpCallAt(mcpsrv.MemoryPath, fullTok, "tools/list"); rec.Code != http.StatusUnauthorized {
		t.Errorf("/mcp PAT at /mcp/memory = %d, want 401", rec.Code)
	}
	if names := toolNames(t, e.mcpCall(fullTok, "tools/list")); !names["send_to_session"] {
		t.Errorf("/mcp PAT tools = %v", names)
	}
	// A super admin's memory PAT stops at write: nothing above it means anything there.
	e.ident.Role = "super_admin"
	if top := mint(`{"name":"m","scope":"admin:dangerous","endpoint":"memory"}`); top["scope"] != scopeWrite {
		t.Errorf("memory PAT scope = %v, want write", top["scope"])
	}
	listed, _ := e.mgr.store.ListPATsByIdentity(context.Background(), e.ident.ID)
	kinds := map[string]int{}
	for _, p := range listed {
		kinds[p.Resource]++
	}
	if kinds[store.PATResourceMemory] != 2 || kinds[store.PATResourceMCP] != 1 {
		t.Errorf("stored PAT resources = %v", kinds)
	}
}

// "MCP tokens & apps" reads the binding of a connection from the list.
func TestMCPMemoryResourceConnectedListNamesTheResource(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	e.connectTo(id, loopbackCB, "read", testMemResource)
	e.connect(id, loopbackCB, "read")
	w := httptest.NewRecorder()
	mcpConnectedAPI{memberAuth{e.mgr}}.list(w, httptest.NewRequest(http.MethodGet, "/api/mcp-oauth/grants", nil), e.ident)
	var out mcpConnectedList
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	got := map[string]int{}
	for _, g := range out.Grants {
		got[g.Endpoint]++
	}
	if got["memory"] != 1 || got["mcp"] != 1 {
		t.Errorf("listed resources = %v", got)
	}
}
