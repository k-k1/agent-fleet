package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const trustedPath = "/api/admin/mcp-oauth/trusted-hosts"

// trustedAdminEnv adds the admin route to an OAuth environment and a super admin to call it.
type trustedAdminEnv struct {
	*mcpOAuthEnv
	admin *http.ServeMux
}

func newTrustedAdminEnv(t *testing.T) *trustedAdminEnv {
	t.Helper()
	e := newMCPOAuthEnv(t)
	if _, err := e.mgr.store.UpsertIdentity(context.Background(), "root@acme.co.jp", "root-acme-co-jp", "super_admin"); err != nil {
		t.Fatal(err)
	}
	// The OAuth environment runs in dev auth, where every caller is one person; the admin gate
	// gets its own manager that trusts the forwarded e-mail so callers can differ.
	gate := memberAuth{&manager{store: e.mgr.store, authMode: "proxy", emailHeader: "X-Forwarded-Email",
		defaultTenantID: e.mgr.defaultTenantID, provisionMode: "auto"}}
	mux := http.NewServeMux()
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		mux.HandleFunc(m+" "+trustedPath, gate.withSuperAdmin(e.api.trustedAdmin))
	}
	return &trustedAdminEnv{e, mux}
}

// call goes through the real gate: the caller is named by the forwarded e-mail header.
func (e *trustedAdminEnv) call(method, email, body string) (int, mcpOAuthTrustedView, string) {
	r := httptest.NewRequest(method, trustedPath, strings.NewReader(body))
	r.Header.Set("X-Forwarded-Email", email)
	rec := httptest.NewRecorder()
	e.admin.ServeHTTP(rec, r)
	var v mcpOAuthTrustedView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return rec.Code, v, rec.Body.String()
}

func (e *trustedAdminEnv) put(hosts string) int {
	e.t.Helper()
	code, _, body := e.call("PUT", "root@acme.co.jp", `{"hosts":`+hosts+`}`)
	if code != http.StatusOK {
		e.t.Logf("PUT %s: %s", hosts, body)
	}
	return code
}

func TestMCPOAuthTrustedHostsPrecedenceAndReset(t *testing.T) {
	e := newTrustedAdminEnv(t)
	get := func() mcpOAuthTrustedView {
		code, v, body := e.call("GET", "root@acme.co.jp", "")
		if code != http.StatusOK {
			t.Fatalf("GET %d %s", code, body)
		}
		return v
	}
	// Nothing saved, no env: the built-in default.
	if v := get(); v.Source != "default" || !slices.Equal(v.Hosts, []string{"chatgpt.com", "claude.ai", "claude.com"}) {
		t.Fatalf("default view = %+v", v)
	}
	// Removing a default vendor works, and the saved value wins.
	if e.put(`["claude.ai","Mcp.Corp.Example"]`) != http.StatusOK {
		t.Fatal("save refused")
	}
	if v := get(); v.Source != "admin" || !slices.Equal(v.Hosts, []string{"claude.ai", "mcp.corp.example"}) {
		t.Fatalf("saved view = %+v", v)
	}
	// Reset deletes the saved value: back to the fallback.
	if code, v, _ := e.call("DELETE", "root@acme.co.jp", ""); code != http.StatusOK || v.Source != "default" {
		t.Fatalf("reset = %d %+v", code, v)
	}
	if v, _ := e.mgr.store.GetSetting(context.Background(), mcpOAuthTrustedSetting); v != "" {
		t.Errorf("reset left %q stored", v)
	}
	// With the env var set, it is the fallback, and a saved value still beats it.
	t.Setenv("AF_MCP_OAUTH_TRUSTED_HOSTS", "claude.ai")
	e.api.fallback, e.api.fallbackSource = envTrustedHosts()
	if v := get(); v.Source != "env" || !slices.Equal(v.Hosts, []string{"claude.ai"}) {
		t.Fatalf("env view = %+v", v)
	}
	e.put(`["chatgpt.com"]`)
	if v := get(); v.Source != "admin" || !slices.Equal(v.Hosts, []string{"chatgpt.com"}) || v.FallbackSource != "env" {
		t.Fatalf("saved-over-env view = %+v", v)
	}
}

// A saved empty list is "none", which is not the same as nothing saved.
func TestMCPOAuthTrustedHostsEmptyIsNoneNotUnset(t *testing.T) {
	e := newTrustedAdminEnv(t)
	if e.put(`[]`) != http.StatusOK {
		t.Fatal("saving an empty list was refused")
	}
	_, v, _ := e.call("GET", "root@acme.co.jp", "")
	if v.Source != "admin" || len(v.Hosts) != 0 {
		t.Fatalf("view after saving none = %+v", v)
	}
	if got, _, _ := e.api.trustedNow(context.Background()); len(got) != 0 {
		t.Fatalf("effective list = %v, want none", got.sorted())
	}
	if e.api.classifyRedirect(mustTrusted(e.api), "https://claude.ai/cb") != redirectRefused ||
		e.api.classifyRedirect(mustTrusted(e.api), "http://127.0.0.1:9/cb") != redirectLoopback {
		t.Error("none must refuse hosted clients and keep loopback")
	}
	// A body without "hosts" is an error, never a silent "none" or reset.
	if code, _, _ := e.call("PUT", "root@acme.co.jp", `{}`); code != http.StatusBadRequest {
		t.Errorf("PUT without hosts = %d, want 400", code)
	}
}

func mustTrusted(a *mcpOAuthAPI) trustedHosts {
	t, _, _ := a.trustedNow(context.Background())
	return t
}

func TestMCPOAuthTrustedHostsValidation(t *testing.T) {
	e := newTrustedAdminEnv(t)
	e.put(`["claude.ai"]`)
	for _, bad := range []string{
		`["https://claude.ai"]`, `["claude.ai/path"]`, `["claude.ai:443"]`, `["*.claude.ai"]`,
		`["user@claude.ai"]`, `["claude.ai."]`, `["none"]`, `["10.0.0.1"]`, `["[::1]"]`, `[""]`,
		`["a b.example"]`, `["claude.ai\\evil.example"]`, `["-x.example"]`, `["é.example"]`,
	} {
		if code := e.put(bad); code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", bad, code)
		}
	}
	// Whatever was refused left the saved value alone.
	if _, v, _ := e.call("GET", "root@acme.co.jp", ""); !slices.Equal(v.Hosts, []string{"claude.ai"}) {
		t.Errorf("a refused PUT changed the list: %v", v.Hosts)
	}
	// The validator is never laxer than the parser: whatever it accepts the parser keeps.
	for _, h := range []string{"claude.ai", "mcp.corp.example", "a-b.example", "localhost"} {
		ok, err := validateTrustedClientHosts([]string{h})
		if err != nil || !parseTrustedClientHosts(h)[ok[0]] {
			t.Errorf("%q: validator %v %v, parser %v", h, ok, err, parseTrustedClientHosts(h))
		}
	}
}

// A saved value that cannot be read must narrow trust to nothing, never fall back to the env or
// default list.
func TestMCPOAuthTrustedHostsCorruptSavedValueFailsClosed(t *testing.T) {
	e := newTrustedAdminEnv(t)
	ctx := context.Background()
	for _, raw := range []string{"not json", `["https://claude.ai"]`, `{"a":1}`} {
		if err := e.mgr.store.SetSetting(ctx, mcpOAuthTrustedSetting, raw); err != nil {
			t.Fatal(err)
		}
		if got, _, err := e.api.trustedNow(ctx); err != nil || len(got) != 0 {
			t.Errorf("saved %q: trusted %v, want none", raw, got.sorted())
		}
	}
}

func TestMCPOAuthTrustedHostsSuperAdminOnly(t *testing.T) {
	e := newTrustedAdminEnv(t)
	for _, email := range []string{"yamada@acme.co.jp", "boss@acme.co.jp"} {
		for _, m := range []string{"GET", "PUT", "DELETE"} {
			if code, _, _ := e.call(m, email, `{"hosts":["evil.example"]}`); code != http.StatusForbidden && code != http.StatusUnauthorized {
				t.Errorf("%s by %s = %d, want 403", m, email, code)
			}
		}
	}
	if v, _ := e.mgr.store.GetSetting(context.Background(), mcpOAuthTrustedSetting); v != "" {
		t.Fatalf("a refused caller stored %q", v)
	}
	if e.put(`["claude.ai"]`) != http.StatusOK {
		t.Fatal("the super admin was refused - the refusals above prove nothing")
	}
}

func TestMCPOAuthTrustedHostsAreAudited(t *testing.T) {
	e := newTrustedAdminEnv(t)
	e.put(`["claude.ai","mcp.corp.example"]`)
	e.call("DELETE", "root@acme.co.jp", "")
	rows, err := e.mgr.store.ListAuditByTenant(context.Background(), "", 20)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.AuditLog{}
	for _, r := range rows {
		got[r.Action] = r
	}
	set, reset := got["mcp_oauth.trusted_hosts.set"], got["mcp_oauth.trusted_hosts.reset"]
	if set.ID == "" || reset.ID == "" {
		t.Fatalf("audit rows: %+v", rows)
	}
	for _, want := range []string{`"old":["chatgpt.com","claude.ai","claude.com"]`, `"new":["claude.ai","mcp.corp.example"]`, `"new_source":"admin"`} {
		if !strings.Contains(set.Detail, want) {
			t.Errorf("set detail %s lacks %s", set.Detail, want)
		}
	}
	if !strings.Contains(reset.Detail, `"old":["claude.ai","mcp.corp.example"]`) || !strings.Contains(reset.Detail, `"new_source":"default"`) {
		t.Errorf("reset detail = %s", reset.Detail)
	}
}

// The list applies on the next request at every place it is checked, without a restart, in both
// directions: authorize (CIMD fetch and redirect), DCR, and the token endpoint.
func TestMCPOAuthTrustedHostsApplyLive(t *testing.T) {
	e := newTrustedAdminEnv(t)
	const peer = "https://af.peer.example/oauth/client.json"
	const peerCB = "https://af.peer.example/mcp-oauth/callback"
	e.docs[peer] = `{"client_id":"` + peer + `","client_name":"Peer","redirect_uris":["` + peerCB + `"]}`
	const claudeCB = "https://claude.ai/api/mcp/auth_callback"
	_, challenge := pkcePair()

	// Before: the peer is refused at authorize without a fetch, and DCR refuses its redirect.
	if rec := e.authorize(e.authQuery(peer, peerCB, challenge)); rec.Code != http.StatusBadRequest {
		t.Fatalf("untrusted CIMD peer: %d", rec.Code)
	}
	if n := e.fetches.Load(); n != 0 {
		t.Fatalf("%d fetches for an untrusted host", n)
	}
	reg := func(cb string) int {
		b, _ := json.Marshal(map[string]any{"redirect_uris": []string{cb}})
		return e.do(http.MethodPost, mcpOAuthRegister, string(b), map[string]string{"Content-Type": "application/json"}).Code
	}
	if reg(peerCB) != http.StatusBadRequest {
		t.Fatal("DCR accepted an untrusted redirect host")
	}
	// A DCR client on claude.ai and a connected grant, to see the token endpoint follow later.
	dcr := e.registerClient(claudeCB)
	tok := e.connect(dcr, claudeCB, "")

	// Add the peer (keeping the defaults).
	e.put(`["claude.ai","claude.com","chatgpt.com","af.peer.example"]`)
	e.ticketFrom(e.authorize(e.authQuery(peer, peerCB, challenge)))
	if reg(peerCB) != http.StatusCreated {
		t.Error("DCR still refuses a host added at runtime")
	}
	// The CIMD document was cached while trusted: removal must still bite the next request.
	e.put(`["claude.ai","claude.com","chatgpt.com"]`)
	if rec := e.authorize(e.authQuery(peer, peerCB, challenge)); rec.Code != http.StatusBadRequest {
		t.Errorf("a removed host still authorizes from the CIMD cache: %d", rec.Code)
	}
	if reg(peerCB) != http.StatusBadRequest {
		t.Error("DCR still accepts a host removed at runtime")
	}
	// Re-adding it lifts the cache entry's narrowing too.
	e.put(`["af.peer.example"]`)
	e.ticketFrom(e.authorize(e.authQuery(peer, peerCB, challenge)))

	// Token endpoint: a CIMD client whose host left the list is invalid_client on refresh, and a
	// DCR client is checked against the redirect rule at authorize (claude.ai is now removed).
	verifier, ch2 := pkcePair()
	ticket := e.ticketFrom(e.authorize(e.authQuery(peer, peerCB, ch2)))
	code := locationQuery(t, e.consent(ticket, map[string][]string{"membership": {e.membershipID()}, "scope": {"write"}})).Get("code")
	if code == "" {
		t.Fatal("no code")
	}
	e.put(`[]`)
	rec := e.post(mcpOAuthToken, map[string][]string{"grant_type": {"authorization_code"}, "client_id": {peer},
		"code": {code}, "redirect_uri": {peerCB}, "code_verifier": {verifier}, "resource": {testResource}})
	if rec.Code != http.StatusUnauthorized || jsonField(t, rec, "error") != "invalid_client" {
		t.Errorf("token for a CIMD client whose host was removed: %d %s", rec.Code, rec.Body)
	}
	_ = tok
}

// The effective list is read while saves happen: no reader may see a half-written list (and
// under -race, no data race).
func TestMCPOAuthTrustedHostsConcurrentSaveAndRead(t *testing.T) {
	e := newTrustedAdminEnv(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				e.put(`["claude.ai"]`)
				e.call("DELETE", "root@acme.co.jp", "")
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				tr := mustTrusted(e.api)
				// Only ever one of the two valid lists, never a half-written one.
				if n := len(tr); n != 1 && n != 3 {
					t.Errorf("saw %d hosts: %v", n, tr.sorted())
					return
				}
			}
		}()
	}
	wg.Wait()
}

// A grant made through DCR is cut off at the token endpoint when its redirect host leaves the
// list; a loopback grant is not affected.
func TestMCPOAuthTrustedHostsRemovalEndsExistingDCRGrants(t *testing.T) {
	e := newTrustedAdminEnv(t)
	const cb = "https://claude.ai/api/mcp/auth_callback"
	hosted := e.connect(e.registerClient(cb), cb, "")
	loop := e.connect(e.registerClient(loopbackCB), loopbackCB, "")

	// A code issued while claude.ai was trusted, not yet redeemed.
	id := e.registerClient(cb)
	verifier, challenge := pkcePair()
	ticket := e.ticketFrom(e.authorize(e.authQuery(id, cb, challenge)))
	code := locationQuery(t, e.consent(ticket, map[string][]string{"membership": {e.membershipID()}, "scope": {"write"}})).Get("code")

	e.put(`["chatgpt.com"]`)
	refresh := func(client, rt string) *httptest.ResponseRecorder { return e.refresh(client, rt) }
	clientOf := func(tk tokens) string { return clientOfRefresh(t, e.mcpOAuthEnv, tk) }
	if rec := refresh(clientOf(hosted), hosted.Refresh); rec.Code != http.StatusBadRequest || jsonField(t, rec, "error") != "invalid_grant" {
		t.Errorf("refresh of a grant on a removed host: %d %s", rec.Code, rec.Body)
	}
	if rec := refresh(clientOf(loop), loop.Refresh); rec.Code != http.StatusOK {
		t.Errorf("a loopback grant must survive the removal: %d %s", rec.Code, rec.Body)
	}
	rec := e.post(mcpOAuthToken, map[string][]string{"grant_type": {"authorization_code"}, "client_id": {id},
		"code": {code}, "redirect_uri": {cb}, "code_verifier": {verifier}, "resource": {testResource}})
	if rec.Code != http.StatusBadRequest || jsonField(t, rec, "error") != "invalid_grant" {
		t.Errorf("code exchange for a removed host: %d %s", rec.Code, rec.Body)
	}
}

func clientOfRefresh(t *testing.T, e *mcpOAuthEnv, tk tokens) string {
	t.Helper()
	g, ok, err := e.mgr.store.GetMCPOAuthTokenByHash(context.Background(), devIdPHash(tk.Refresh), e.now().Unix())
	if err != nil || !ok {
		t.Fatalf("refresh token lookup: %v %v", ok, err)
	}
	return g.ClientID
}

func TestMCPOAuthTrustedHostsNoOpResetAndTrailingGarbage(t *testing.T) {
	e := newTrustedAdminEnv(t)
	if code, _, _ := e.call("DELETE", "root@acme.co.jp", ""); code != http.StatusOK {
		t.Fatalf("reset with nothing saved = %d", code)
	}
	rows, _ := e.mgr.store.ListAuditByTenant(context.Background(), "", 20)
	for _, r := range rows {
		if strings.HasPrefix(r.Action, "mcp_oauth.trusted_hosts") {
			t.Errorf("a reset that changed nothing was audited: %s", r.Action)
		}
	}
	if code, _, _ := e.call("PUT", "root@acme.co.jp", `{"hosts":[]} junk`); code != http.StatusBadRequest {
		t.Errorf("PUT with trailing data = %d, want 400", code)
	}
}

// An unreadable store is a 5xx, never an empty saved list, and a write is not attempted or
// audited blind. A corrupt value is reported as such and an admin can still overwrite it.
func TestMCPOAuthTrustedHostsStoreFailureIsNotAnEmptyList(t *testing.T) {
	e := newTrustedAdminEnv(t)
	ctx := context.Background()
	e.mgr.store.SetSetting(ctx, mcpOAuthTrustedSetting, "garbage")
	if code, v, _ := e.call("GET", "root@acme.co.jp", ""); code != http.StatusOK || !v.Unreadable {
		t.Errorf("GET over a corrupt value = %d %+v, want 200 unreadable", code, v)
	}
	if e.put(`["claude.ai"]`) != http.StatusOK {
		t.Error("a corrupt value could not be overwritten")
	}
	e.mgr.store.Close()
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		if code, _, _ := e.call(m, "root@acme.co.jp", `{"hosts":[]}`); code != http.StatusInternalServerError {
			t.Errorf("%s with the store down = %d, want 500", m, code)
		}
	}
}
