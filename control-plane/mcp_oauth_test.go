package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	testBase     = "https://af.example.com"
	testResource = testBase + "/mcp"
	testIssuer   = testBase + "/mcp-oauth"
	loopbackCB   = "http://127.0.0.1:53682/callback"
)

type mcpOAuthEnv struct {
	t     *testing.T
	mgr   *manager
	api   *mcpOAuthAPI
	mux   *http.ServeMux
	ident store.Identity
	tenA  store.Tenant
	mu    sync.Mutex
	clock time.Time
	// fetches counts CIMD document fetches; docs answers them by URL.
	fetches atomic.Int32
	docs    map[string]string
}

func newMCPOAuthEnv(t *testing.T) *mcpOAuthEnv {
	t.Helper()
	t.Setenv("AF_MCP_ENABLED", "true")
	t.Setenv("AF_MCP_OAUTH", "1")
	t.Setenv("AF_MCP_OAUTH_TRUSTED_HOSTS", "")
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dflt, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ident, err := st.UpsertIdentity(ctx, "", "dev-user", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member"); err != nil {
		t.Fatal(err)
	}
	mgr := &manager{
		rts: map[string]cachedRT{}, store: st, authMode: "dev", devUser: "dev-user", emailHeader: "X-Forwarded-Email",
		provisionMode: "auto", defaultTenantID: dflt.ID, conns: newConnRegistry(), tenantLogin: newTenantLoginCache(st),
		publicBaseURL: testBase,
	}
	e := &mcpOAuthEnv{t: t, mgr: mgr, ident: ident, tenA: dflt, clock: time.Now(), docs: map[string]string{}}
	e.api = newMCPOAuthAPI(config{mgr: mgr, publicBaseURL: testBase, brand: newBrandResolver(brandConfig{}, nil)})
	e.api.now = e.now
	e.api.fetch = func(_ context.Context, u string) ([]byte, time.Duration, error) {
		e.fetches.Add(1)
		doc, ok := e.docs[u]
		if !ok {
			return nil, 0, errors.New("404")
		}
		return []byte(doc), time.Hour, nil
	}
	e.mux = http.NewServeMux()
	e.api.mount(e.mux)
	e.mux.HandleFunc("/mcp", e.api.withOAuth(mcpsrv.New(cpDeps{mgr})).HandleMCP)
	return e
}

func (e *mcpOAuthEnv) now() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.clock
}

func (e *mcpOAuthEnv) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clock = e.clock.Add(d)
}

func (e *mcpOAuthEnv) do(method, target string, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, testBase+target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func (e *mcpOAuthEnv) post(path string, form url.Values) *httptest.ResponseRecorder {
	return e.do(http.MethodPost, path, form.Encode(), nil)
}

// registerClient registers a DCR client with the given redirects and returns its id.
func (e *mcpOAuthEnv) registerClient(redirects ...string) string {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"redirect_uris": redirects, "client_name": "Test Client"})
	rec := e.do(http.MethodPost, mcpOAuthRegister, string(b), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	return jsonField(e.t, rec, "client_id")
}

func jsonField(t *testing.T, rec *httptest.ResponseRecorder, k string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON (%d): %s", rec.Code, rec.Body)
	}
	s, _ := m[k].(string)
	return s
}

func (e *mcpOAuthEnv) authQuery(clientID, redirect, challenge string) url.Values {
	return url.Values{
		"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "state": {"st-1"},
		"scope": {"read write"}, "resource": {testResource},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
}

func (e *mcpOAuthEnv) authorize(q url.Values) *httptest.ResponseRecorder {
	return e.do(http.MethodGet, mcpOAuthAuthorize+"?"+q.Encode(), "", nil)
}

var mcpTicketRE = regexp.MustCompile(`name="ticket" value="([^"]+)"`)

func (e *mcpOAuthEnv) ticketFrom(rec *httptest.ResponseRecorder) string {
	e.t.Helper()
	m := mcpTicketRE.FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || m == nil {
		e.t.Fatalf("expected a consent page, got %d %s", rec.Code, rec.Body)
	}
	return m[1]
}

func (e *mcpOAuthEnv) consent(ticket string, extra url.Values) *httptest.ResponseRecorder {
	f := url.Values{"ticket": {ticket}}
	for k, v := range extra {
		f[k] = v
	}
	return e.do(http.MethodPost, mcpOAuthConsent, f.Encode(), map[string]string{"Origin": testBase})
}

func (e *mcpOAuthEnv) membershipID() string {
	ms, _ := e.mgr.store.ListMemberships(context.Background(), e.ident.ID)
	return ms[0].MembershipID
}

func locationQuery(t *testing.T, rec *httptest.ResponseRecorder) url.Values {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("want a redirect, got %d %s", rec.Code, rec.Body)
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

type tokens struct{ Access, Refresh, Scope, Code, Verifier string }

// connect walks authorize, consent and the code exchange as the signed-in member.
func (e *mcpOAuthEnv) connect(clientID, redirect, scope string) tokens {
	e.t.Helper()
	verifier, challenge := pkcePair()
	q := e.authQuery(clientID, redirect, challenge)
	if scope != "" {
		q.Set("scope", scope)
	}
	ticket := e.ticketFrom(e.authorize(q))
	granted := "write"
	if scope == "read" {
		granted = "read"
	}
	lq := locationQuery(e.t, e.consent(ticket, url.Values{"membership": {e.membershipID()}, "scope": {granted}}))
	code := lq.Get("code")
	if code == "" {
		e.t.Fatalf("no code in %v", lq)
	}
	rec := e.post(mcpOAuthToken, url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID},
		"code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "resource": {testResource}})
	if rec.Code != http.StatusOK {
		e.t.Fatalf("token: %d %s", rec.Code, rec.Body)
	}
	return tokens{Access: jsonField(e.t, rec, "access_token"), Refresh: jsonField(e.t, rec, "refresh_token"),
		Scope: jsonField(e.t, rec, "scope"), Code: code, Verifier: verifier}
}

func (e *mcpOAuthEnv) mcpCall(token, method string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q}`, method)
	hdr := map[string]string{"Content-Type": "application/json"}
	if token != "" {
		hdr["Authorization"] = "Bearer " + token
	}
	return e.do(http.MethodPost, "/mcp", body, hdr)
}

func (e *mcpOAuthEnv) refresh(clientID, rt string) *httptest.ResponseRecorder {
	return e.post(mcpOAuthToken, url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {rt}})
}

func toolNames(t *testing.T, rec *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	var out struct {
		Result struct {
			Tools []struct{ Name string } `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("tools/list: %d %s", rec.Code, rec.Body)
	}
	names := map[string]bool{}
	for _, tl := range out.Result.Tools {
		names[tl.Name] = true
	}
	return names
}

// --- enablement and discovery -----------------------------------------------------

func TestMCPOAuthOffEverythingIs404AndMCPKeepsBare401(t *testing.T) {
	for name, set := range map[string]func(*testing.T){
		"flag unset":    func(t *testing.T) { t.Setenv("AF_MCP_OAUTH", "") },
		"mcp disabled":  func(t *testing.T) { t.Setenv("AF_MCP_ENABLED", "") },
		"http base url": nil,
	} {
		t.Run(name, func(t *testing.T) {
			e := newMCPOAuthEnv(t)
			if set != nil {
				set(t)
			} else {
				e.mgr.publicBaseURL = "http://af.example.com"
			}
			e.api = newMCPOAuthAPI(config{mgr: e.mgr, brand: newBrandResolver(brandConfig{}, nil)})
			mux := http.NewServeMux()
			e.api.mount(mux)
			mux.HandleFunc("/mcp", e.api.withOAuth(mcpsrv.New(cpDeps{e.mgr})).HandleMCP)
			e.mux = mux
			for _, c := range []struct{ method, path string }{
				{"GET", mcpOAuthPRMPath}, {"GET", mcpOAuthMetadataPath}, {"GET", mcpOAuthAuthorize}, {"POST", mcpOAuthConsent},
				{"POST", mcpOAuthToken}, {"POST", mcpOAuthRegister}, {"POST", mcpOAuthRevoke}, {"GET", mcpOAuthToken},
			} {
				if rec := e.do(c.method, c.path, "", nil); rec.Code != http.StatusNotFound {
					t.Errorf("%s %s = %d, want 404", c.method, c.path, rec.Code)
				}
			}
			rec := e.mcpCall("", "tools/list")
			if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("/mcp = %d %q, want a bare 401", rec.Code, rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

func TestMCPOAuthMetadataAndChallenge(t *testing.T) {
	e := newMCPOAuthEnv(t)
	rec := e.do("GET", mcpOAuthMetadataPath, "", nil)
	var md map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &md); err != nil || rec.Code != 200 {
		t.Fatalf("metadata: %d %s", rec.Code, rec.Body)
	}
	for k, want := range map[string]string{
		"issuer": testIssuer, "authorization_endpoint": testIssuer + "/authorize", "token_endpoint": testIssuer + "/token",
		"registration_endpoint": testIssuer + "/register", "revocation_endpoint": testIssuer + "/revoke",
		"code_challenge_methods_supported": `["S256"]`, "token_endpoint_auth_methods_supported": `["none"]`,
		"scopes_supported": `["read","write"]`,
	} {
		got, _ := json.Marshal(md[k])
		if s, isStr := md[k].(string); isStr {
			got = []byte(s)
		}
		if string(got) != want {
			t.Errorf("metadata %s = %s, want %s", k, got, want)
		}
	}
	if md["authorization_response_iss_parameter_supported"] != true || md["client_id_metadata_document_supported"] != true {
		t.Errorf("metadata must advertise iss and CIMD support: %v", md)
	}
	var prm map[string]any
	_ = json.Unmarshal(e.do("GET", mcpOAuthPRMPath, "", nil).Body.Bytes(), &prm)
	if prm["resource"] != testResource || fmt.Sprint(prm["authorization_servers"]) != "["+testIssuer+"]" {
		t.Errorf("protected resource metadata = %v", prm)
	}
	// The 401 points at the metadata.
	rec = e.mcpCall("", "tools/list")
	want := `Bearer resource_metadata="` + testBase + `/.well-known/oauth-protected-resource/mcp"`
	if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != want {
		t.Errorf("/mcp 401 = %d %q, want %q", rec.Code, rec.Header().Get("WWW-Authenticate"), want)
	}
	rec = e.mcpCall("expired-token", "tools/list")
	if wantInvalid := want + `, error="invalid_token"`; rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != wantInvalid {
		t.Errorf("invalid token challenge = %d %q, want %q", rec.Code, rec.Header().Get("WWW-Authenticate"), wantInvalid)
	}
}

func TestMCPOAuthAuthGateExemptions(t *testing.T) {
	newMCPOAuthEnv(t)
	for _, p := range []string{mcpOAuthPRMPath, mcpOAuthMetadataPath, mcpOAuthToken, mcpOAuthRegister, mcpOAuthRevoke} {
		if !isAuthExempt(p) {
			t.Errorf("%s must be reachable without a session", p)
		}
	}
	for _, p := range []string{mcpOAuthAuthorize, mcpOAuthConsent} {
		if isAuthExempt(p) {
			t.Errorf("%s must stay behind authGate", p)
		}
	}
}

// --- redirect rule ---------------------------------------------------------------

func TestMCPOAuthRedirectRule(t *testing.T) {
	e := newMCPOAuthEnv(t)
	for raw, want := range map[string]redirectClass{
		"http://127.0.0.1:53682/callback":                       redirectLoopback,
		"http://127.0.0.1/callback":                             redirectLoopback,
		"http://localhost:8080/cb":                              redirectLoopback,
		"http://[::1]:9/cb":                                     redirectLoopback,
		"https://claude.ai/api/mcp/auth_callback":               redirectHosted,
		"https://claude.com/api/mcp/auth_callback":              redirectHosted,
		"https://chatgpt.com/connector_platform_oauth_redirect": redirectHosted,
		// refused
		"https://evil.example/cb":               redirectRefused,
		"https://claude.ai.evil.example/cb":     redirectRefused,
		"https://sub.claude.ai/cb":              redirectRefused,
		"https://Claude.ai/cb":                  redirectRefused,
		"https://claude.ai./cb":                 redirectRefused,
		"https://claude.ai:443/cb":              redirectRefused,
		"https://claude.ai:8443/cb":             redirectRefused,
		"https://user@claude.ai/cb":             redirectRefused,
		"https://claude.ai/cb#frag":             redirectRefused,
		"https://claude.ai\\@evil.example/cb":   redirectRefused,
		"http://claude.ai/cb":                   redirectRefused,
		"http://127.0.0.1:0/cb":                 redirectRefused,
		"http://127.0.0.1:080/cb":               redirectRefused,
		"http://LOCALHOST:8080/cb":              redirectRefused,
		"http://127.0.0.2:8080/cb":              redirectRefused,
		"http://localhost.evil.example:8080/cb": redirectRefused,
		"myapp://callback":                      redirectRefused,
		"javascript:alert(1)":                   redirectRefused,
		"":                                      redirectRefused,
	} {
		if got := e.api.classifyRedirect(e.api.fallback, raw); got != want {
			t.Errorf("classifyRedirect(%q) = %v, want %v", raw, got, want)
		}
	}
	// A loopback registration matches on any port, with the path exact; hosted is exact.
	reg := []string{"http://127.0.0.1/callback", "https://claude.ai/api/mcp/auth_callback"}
	for raw, want := range map[string]bool{
		"http://127.0.0.1:61234/callback":         true,
		"http://127.0.0.1:61234/other":            false,
		"http://localhost:61234/callback":         false,
		"https://claude.ai/api/mcp/auth_callback": true,
		"https://claude.ai/api/mcp/other":         false,
	} {
		if got := e.api.redirectRegistered(e.api.fallback, reg, raw); got != want {
			t.Errorf("redirectRegistered(%q) = %v, want %v", raw, got, want)
		}
	}
	// The operator list is honoured, in both directions.
	t.Setenv("AF_MCP_OAUTH_TRUSTED_HOSTS", "claude.ai, mcp.corp.example")
	api := newMCPOAuthAPI(config{mgr: e.mgr})
	if api.classifyRedirect(api.fallback, "https://chatgpt.com/cb") != redirectRefused || api.classifyRedirect(api.fallback, "https://mcp.corp.example/cb") != redirectHosted {
		t.Error("the configured trusted-host list was not applied")
	}
}

func TestMCPOAuthUntrustedRedirectIsAPageNeverARedirect(t *testing.T) {
	e := newMCPOAuthEnv(t)
	// A client registered earlier whose host has since left the trusted list.
	id := e.registerClient("https://claude.ai/api/mcp/auth_callback", loopbackCB)
	_, challenge := pkcePair()
	for _, redirect := range []string{"https://evil.example/cb", "http://127.0.0.1:1/other", "https://claude.ai/api/mcp/auth_callback#x"} {
		rec := e.authorize(e.authQuery(id, redirect, challenge))
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Errorf("redirect %q: %d Location=%q, want a 400 page", redirect, rec.Code, rec.Header().Get("Location"))
		}
	}
	rec := e.authorize(e.authQuery("afmcp-nonesuch", loopbackCB, challenge))
	if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
		t.Errorf("unknown client: %d", rec.Code)
	}
	t.Setenv("AF_MCP_OAUTH_TRUSTED_HOSTS", "chatgpt.com")
	e.api.fallback = parseTrustedClientHosts("chatgpt.com")
	if rec := e.authorize(e.authQuery(id, "https://claude.ai/api/mcp/auth_callback", challenge)); rec.Code != http.StatusBadRequest {
		t.Errorf("a host dropped from the trusted list still worked: %d", rec.Code)
	}
}

// --- client registration ---------------------------------------------------------

func TestMCPOAuthDCRRules(t *testing.T) {
	e := newMCPOAuthEnv(t)
	reg := func(body string) *httptest.ResponseRecorder {
		return e.do("POST", mcpOAuthRegister, body, map[string]string{"Content-Type": "application/json"})
	}
	for name, body := range map[string]string{
		"hostile redirect":   `{"redirect_uris":["https://evil.example/cb"]}`,
		"mixed in a hostile": `{"redirect_uris":["http://127.0.0.1/cb","https://evil.example/cb"]}`,
		"no redirects":       `{"redirect_uris":[]}`,
		"duplicate":          `{"redirect_uris":["http://127.0.0.1/cb","http://127.0.0.1/cb"]}`,
		"secret auth method": `{"redirect_uris":["http://127.0.0.1/cb"],"token_endpoint_auth_method":"client_secret_basic"}`,
		"implicit":           `{"redirect_uris":["http://127.0.0.1/cb"],"grant_types":["implicit"]}`,
		"not json":           `nope`,
	} {
		if rec := reg(body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
	rec := reg(`{"redirect_uris":["http://127.0.0.1/cb"],"client_name":"CLI‮"}`)
	if rec.Code != 201 || !strings.HasPrefix(jsonField(t, rec, "client_id"), "afmcp-") || jsonField(t, rec, "token_endpoint_auth_method") != "none" {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	if jsonField(t, rec, "client_name") != "CLI" {
		t.Errorf("a bidi control survived in the stored name: %q", jsonField(t, rec, "client_name"))
	}
}

func TestMCPOAuthDCRRateLimitAndCap(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ok := 0
	for i := 0; i < mcpOAuthRegPerHour+3; i++ {
		rec := e.do("POST", mcpOAuthRegister, `{"redirect_uris":["http://127.0.0.1/cb"]}`, nil)
		if rec.Code == 201 {
			ok++
		} else if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("register %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	if ok != mcpOAuthRegPerHour {
		t.Errorf("%d registrations accepted from one source, want %d", ok, mcpOAuthRegPerHour)
	}
	// The window slides: an hour later the source may register again.
	e.advance(61 * time.Minute)
	if rec := e.do("POST", mcpOAuthRegister, `{"redirect_uris":["http://127.0.0.1/cb"]}`, nil); rec.Code != 201 {
		t.Errorf("after the window: %d", rec.Code)
	}
	// The total cap (store level, here forced small).
	st := e.mgr.store
	if err := st.CreateMCPOAuthClient(context.Background(), store.MCPOAuthClient{ClientID: "x", RedirectURIs: "[]"}, 1); !errors.Is(err, store.ErrMCPOAuthClientCap) {
		t.Errorf("cap: %v", err)
	}
}

func TestMCPOAuthDCRClientWithoutGrantIsDeletedAndTokenSaysInvalidClient(t *testing.T) {
	e := newMCPOAuthEnv(t)
	stale := e.registerClient(loopbackCB)
	kept := e.registerClient(loopbackCB)
	e.connect(kept, loopbackCB, "read")
	e.advance(31 * 24 * time.Hour)
	e.registerClient(loopbackCB) // pruning runs on registration
	ctx := context.Background()
	if _, ok, _ := e.mgr.store.GetMCPOAuthClient(ctx, stale); ok {
		t.Error("a client with no grant survived 30 days")
	}
	if _, ok, _ := e.mgr.store.GetMCPOAuthClient(ctx, kept); !ok {
		t.Error("a client holding a grant was deleted")
	}
	rec := e.post(mcpOAuthToken, url.Values{"grant_type": {"authorization_code"}, "client_id": {stale}, "code": {"x"}})
	if rec.Code != 401 || jsonField(t, rec, "error") != "invalid_client" {
		t.Errorf("deleted client at /token: %d %s, want invalid_client", rec.Code, rec.Body)
	}
}

// --- CIMD (the SSRF guard) -------------------------------------------------------

func TestMCPOAuthCIMDOnlyFetchesTrustedHosts(t *testing.T) {
	e := newMCPOAuthEnv(t)
	const good = "https://claude.ai/oauth/client.json"
	e.docs[good] = `{"client_id":"` + good + `","client_name":"Claude","redirect_uris":["https://claude.ai/api/mcp/auth_callback","https://evil.example/cb"]}`
	e.docs["https://evil.example/client.json"] = `{"client_id":"https://evil.example/client.json","redirect_uris":["http://127.0.0.1/cb"]}`
	_, challenge := pkcePair()
	cb := "https://claude.ai/api/mcp/auth_callback"

	// Not on the list: refused without a single outbound request.
	for _, id := range []string{
		"https://evil.example/client.json", "http://claude.ai/oauth/client.json", "https://claude.ai:8443/oauth/client.json",
		"https://169.254.169.254/latest/meta-data", "https://claude.ai/", "https://claude.ai/a/../client.json",
		"https://claude.ai/oauth/client.json?x=1", "https://user@claude.ai/oauth/client.json", "https://CLAUDE.ai/oauth/client.json",
	} {
		if rec := e.authorize(e.authQuery(id, cb, challenge)); rec.Code != http.StatusBadRequest {
			t.Errorf("client_id %q: %d, want 400", id, rec.Code)
		}
	}
	if n := e.fetches.Load(); n != 0 {
		t.Fatalf("%d fetches for client_ids that are not on a trusted host", n)
	}

	// A trusted one is fetched once, then served from the cache.
	for i := 0; i < 3; i++ {
		e.ticketFrom(e.authorize(e.authQuery(good, cb, challenge)))
	}
	if n := e.fetches.Load(); n != 1 {
		t.Errorf("%d fetches for three authorizations, want 1 (cache)", n)
	}
	// Redirects in the document that the rule refuses are dropped, not honoured.
	if rec := e.authorize(e.authQuery(good, "https://evil.example/cb", challenge)); rec.Code != 400 || rec.Header().Get("Location") != "" {
		t.Errorf("a refused redirect listed in the document was accepted: %d", rec.Code)
	}
	// The cache expires.
	e.advance(2 * time.Hour)
	e.ticketFrom(e.authorize(e.authQuery(good, cb, challenge)))
	if n := e.fetches.Load(); n != 2 {
		t.Errorf("%d fetches after expiry, want 2", n)
	}
}

func TestMCPOAuthCIMDDocumentMustNameItself(t *testing.T) {
	e := newMCPOAuthEnv(t)
	const id = "https://chatgpt.com/oauth/client.json"
	_, challenge := pkcePair()
	cb := "https://chatgpt.com/connector_platform_oauth_redirect"
	for name, doc := range map[string]string{
		"other client_id":   `{"client_id":"https://chatgpt.com/other.json","redirect_uris":["` + cb + `"]}`,
		"has a secret":      `{"client_id":"` + id + `","client_secret":"s","redirect_uris":["` + cb + `"]}`,
		"secret auth":       `{"client_id":"` + id + `","token_endpoint_auth_method":"client_secret_post","redirect_uris":["` + cb + `"]}`,
		"only bad redirect": `{"client_id":"` + id + `","redirect_uris":["https://evil.example/cb"]}`,
		"not json":          `<html>`,
	} {
		e.docs[id] = doc
		e.api.cimd = newCIMDCache(8)
		if rec := e.authorize(e.authQuery(id, cb, challenge)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	e.docs[id] = `{"client_id":"` + id + `","client_name":"ChatGPT","redirect_uris":["` + cb + `"],"token_endpoint_auth_method":"none"}`
	e.api.cimd = newCIMDCache(8)
	if rec := e.authorize(e.authQuery(id, cb, challenge)); rec.Code != http.StatusOK {
		t.Errorf("a well-formed document: %d %s", rec.Code, rec.Body)
	}
}

// The fixture preserves ChatGPT's published document, including its preferred method
// and its supported-method list: the preference alone does not rule out a public client.
func TestMCPOAuthCIMDChatGPT(t *testing.T) {
	body, err := os.ReadFile("testdata/chatgpt-oauth-client.json")
	if err != nil {
		t.Fatal(err)
	}
	const id = "https://chatgpt.com/oauth/client.json"
	const cb = "https://chatgpt.com/connector_platform_oauth_redirect"
	for _, tc := range []struct {
		name     string
		mutate   func(map[string]any)
		accepted bool
	}{
		{"published document", nil, true},
		{"none default", func(d map[string]any) { d["token_endpoint_auth_method"] = "none" }, true},
		{"omitted default", func(d map[string]any) { delete(d, "token_endpoint_auth_method") }, true},
		{"empty default", func(d map[string]any) { d["token_endpoint_auth_method"] = "" }, true},
		{"private key only", func(d map[string]any) { d["token_endpoint_auth_methods_supported"] = []string{"private_key_jwt"} }, false},
		{"no supported list", func(d map[string]any) { delete(d, "token_endpoint_auth_methods_supported") }, false},
		{"secret despite none support", func(d map[string]any) { d["client_secret"] = "s" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newMCPOAuthEnv(t)
			doc := body
			if tc.mutate != nil {
				var d map[string]any
				if err := json.Unmarshal(body, &d); err != nil {
					t.Fatal(err)
				}
				tc.mutate(d)
				doc, err = json.Marshal(d)
				if err != nil {
					t.Fatal(err)
				}
			}
			e.docs[id] = string(doc)
			client, err := e.api.loadCIMD(context.Background(), e.api.fallback, id)
			if !tc.accepted {
				if !errors.Is(err, errMCPClientUnknown) {
					t.Fatalf("loadCIMD: %v, want unknown client", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !client.CIMD || client.ID != id || client.Name != "ChatGPT" || len(client.Redirects) != 1 || client.Redirects[0] != cb {
				t.Fatalf("unexpected client: %+v", client)
			}
			_, challenge := pkcePair()
			q := e.authQuery(id, cb, challenge)
			q.Del("code_challenge")
			if rec := e.authorize(q); rec.Code != http.StatusFound || locationQuery(t, rec).Get("error") != "invalid_request" {
				t.Fatalf("missing PKCE: %d %s", rec.Code, rec.Body)
			}
			tk := e.connect(id, cb, "read")
			for _, assertion := range []string{"signed.assertion.jwt", ""} {
				rec := e.post(mcpOAuthToken, url.Values{
					"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {tk.Refresh},
					"client_assertion":      {assertion},
					"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
				})
				if rec.Code != http.StatusUnauthorized || jsonField(t, rec, "error") != "invalid_client" {
					t.Fatalf("client assertion: %d %s", rec.Code, rec.Body)
				}
			}
			if rec := e.refresh(id, tk.Refresh); rec.Code != http.StatusOK {
				t.Fatalf("public refresh after rejected assertions: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestCIMDCacheIsBounded(t *testing.T) {
	c := newCIMDCache(3)
	now := time.Now()
	for i := 0; i < 20; i++ {
		c.put(fmt.Sprint(i), mcpClient{ID: fmt.Sprint(i)}, now.Add(time.Duration(i+1)*time.Minute), now)
	}
	if len(c.m) > 3 {
		t.Errorf("cache holds %d entries, cap is 3", len(c.m))
	}
}

func TestFetchCIMDDocumentGuards(t *testing.T) {
	// The real dial guard refuses a loopback server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()
	if _, _, err := fetchCIMDDocument(context.Background(), srv.URL); err == nil {
		t.Error("the default fetch reached a loopback address")
	}
	for ip, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true, "127.0.0.1": false, "10.0.0.1": false, "192.168.1.1": false,
		"172.16.0.1": false, "169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "::1": false,
		"fe80::1": false, "fd00::1": false, "::ffff:10.0.0.1": false, "224.0.0.1": false,
	} {
		if got := isPublicIP(netip.MustParseAddr(ip)); got != want {
			t.Errorf("isPublicIP(%s) = %v, want %v", ip, got, want)
		}
	}
	allow := func(string, string, syscall.RawConn) error { return nil }
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/final", http.StatusFound) })
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = w.Write([]byte(`{}`)) })
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("a"), cimdMaxBody+10))
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) })
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=120")
		_, _ = w.Write([]byte(`{"a":1}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	if _, _, err := fetchCIMDWith(context.Background(), ts.URL+"/redir", allow); err == nil || hits.Load() != 0 {
		t.Errorf("a redirect was followed (err=%v hits=%d)", err, hits.Load())
	}
	if _, _, err := fetchCIMDWith(context.Background(), ts.URL+"/big", allow); err == nil {
		t.Error("an oversized document was accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := fetchCIMDWith(ctx, ts.URL+"/slow", allow); err == nil {
		t.Error("a slow server was waited for")
	}
	body, ttl, err := fetchCIMDWith(context.Background(), ts.URL+"/ok", allow)
	if err != nil || string(body) != `{"a":1}` || ttl != 120*time.Second {
		t.Errorf("ok fetch: %q %v %v", body, ttl, err)
	}
}

// --- authorize -------------------------------------------------------------------

func TestMCPOAuthAuthorizeErrorsCarryIssAndBindResource(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	_, challenge := pkcePair()
	for name, mut := range map[string]func(url.Values){
		"wrong resource":      func(q url.Values) { q.Set("resource", testBase+"/other") },
		"no resource":         func(q url.Values) { q.Del("resource") },
		"resource with slash": func(q url.Values) { q.Set("resource", testResource+"/") },
		"plain PKCE":          func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"no PKCE":             func(q url.Values) { q.Del("code_challenge"); q.Del("code_challenge_method") },
		"no method":           func(q url.Values) { q.Del("code_challenge_method") },
		"short challenge":     func(q url.Values) { q.Set("code_challenge", "abc") },
		"token response":      func(q url.Values) { q.Set("response_type", "token") },
		"dangerous scope":     func(q url.Values) { q.Set("scope", "read admin:dangerous") },
		"prompt login":        func(q url.Values) { q.Set("prompt", "login") },
		"prompt none":         func(q url.Values) { q.Set("prompt", "none") },
	} {
		q := e.authQuery(id, loopbackCB, challenge)
		mut(q)
		rec := e.authorize(q)
		lq := locationQuery(t, rec)
		if lq.Get("error") == "" || lq.Get("code") != "" {
			t.Errorf("%s: %v, want an error and no code", name, lq)
		}
		if lq.Get("iss") != testIssuer {
			t.Errorf("%s: iss = %q, want %q", name, lq.Get("iss"), testIssuer)
		}
		if lq.Get("state") != "st-1" {
			t.Errorf("%s: state = %q", name, lq.Get("state"))
		}
	}
	// The specific codes.
	check := func(mut func(url.Values), want string) {
		q := e.authQuery(id, loopbackCB, challenge)
		mut(q)
		if got := locationQuery(t, e.authorize(q)).Get("error"); got != want {
			t.Errorf("error = %q, want %q", got, want)
		}
	}
	check(func(q url.Values) { q.Set("resource", testBase+"/other") }, "invalid_target")
	check(func(q url.Values) { q.Set("code_challenge_method", "plain") }, "invalid_request")
	check(func(q url.Values) { q.Set("scope", "admin:dangerous") }, "invalid_scope")
	check(func(q url.Values) { q.Set("prompt", "none") }, "consent_required")
	// Success redirects carry iss too.
	ticket := e.ticketFrom(e.authorize(e.authQuery(id, loopbackCB, challenge)))
	lq := locationQuery(t, e.consent(ticket, url.Values{"membership": {e.membershipID()}, "scope": {"read"}}))
	if lq.Get("iss") != testIssuer || lq.Get("code") == "" || lq.Get("state") != "st-1" {
		t.Errorf("success redirect = %v", lq)
	}
}

func TestMCPOAuthAuthorizeAppliesTheTenantNetworkRestriction(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	_, challenge := pkcePair()
	// A non-admin member behind the gateway (a deployment super admin is exempt from the rule).
	e.mgr.authMode = "proxy"
	if err := e.mgr.store.SetTenantAllowedCIDRs(context.Background(), e.tenA.ID, "203.0.113.0/24"); err != nil {
		t.Fatal(err)
	}
	e.mgr.tenantLogin.invalidate()
	get := func(ip *clientIPInfo) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", testBase+mcpOAuthAuthorize+"?"+e.authQuery(id, loopbackCB, challenge).Encode(), nil)
		req.Header.Set("X-Forwarded-Email", "member@example.com")
		if ip != nil {
			req = req.WithContext(context.WithValue(req.Context(), clientIPKey{}, *ip))
		}
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, req)
		return rec
	}
	// An address outside the range, and one nobody could name, are both denied.
	for _, ip := range []*clientIPInfo{{IP: netip.MustParseAddr("198.51.100.9"), OK: true}, nil} {
		lq := locationQuery(t, get(ip))
		if lq.Get("error") != "access_denied" || lq.Get("iss") != testIssuer {
			t.Errorf("restricted tenant from %v: %v, want access_denied", ip, lq)
		}
	}
	e.ticketFrom(get(&clientIPInfo{IP: netip.MustParseAddr("203.0.113.7"), OK: true}))
}

func TestMCPOAuthConsentPageContent(t *testing.T) {
	e := newMCPOAuthEnv(t)
	other, err := e.mgr.store.CreateTenant(context.Background(), "acme", "Acme Corp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.store.EnsureMembership(context.Background(), e.ident.ID, other.ID, "member"); err != nil {
		t.Fatal(err)
	}
	id := e.registerClient("https://claude.ai/api/mcp/auth_callback")
	_, challenge := pkcePair()
	rec := e.do(http.MethodGet, mcpOAuthAuthorize+"?"+e.authQuery(id, "https://claude.ai/api/mcp/auth_callback", challenge).Encode(), "",
		map[string]string{"Accept-Language": "en"})
	body := rec.Body.String()
	for _, want := range []string{
		`class="host">claude.ai<`, "Test Client", "declared by the app itself", "Acme Corp",
		`name="membership"`, `value="read"`, `value="write"`, `value="cancel"`,
		"Agent Fleet memory (list, search, read)", "saves and forgets memories",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("consent page lacks %q", want)
		}
	}
	if n := strings.Count(body, `name="membership"`); n != 2 {
		t.Errorf("%d tenant choices, want one per membership (2)", n)
	}
	if strings.Contains(body, "admin:dangerous\"") && strings.Contains(body, `value="admin:dangerous"`) {
		t.Error("the page offers admin:dangerous")
	}
	h := rec.Header()
	if h.Get("Referrer-Policy") != "same-origin" || h.Get("X-Frame-Options") != "DENY" || h.Get("Cache-Control") != "no-store" ||
		strings.Contains(h.Get("Content-Security-Policy"), "form-action") || !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("page headers = %v", h)
	}
	// A read-only request offers no write choice.
	q := e.authQuery(id, "https://claude.ai/api/mcp/auth_callback", challenge)
	q.Set("scope", "read")
	if rec := e.authorize(q); strings.Contains(rec.Body.String(), `value="write"`) {
		t.Error("write offered although only read was requested")
	}
}

func TestMCPOAuthConsentDefaultsToRead(t *testing.T) {
	for _, tc := range []struct {
		name, requested, chosen, wantScope string
		canWrite                           bool
	}{
		{"narrow to read", "read write", "read", "read", true},
		{"choose write", "read write", "write", "read write", true},
		{"read only", "read", "read", "read", false},
		{"cannot widen read", "read", "write", "read", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newMCPOAuthEnv(t)
			id := e.registerClient(loopbackCB)
			verifier, challenge := pkcePair()
			q := e.authQuery(id, loopbackCB, challenge)
			q.Set("scope", tc.requested)
			rec := e.authorize(q)
			ticket := e.ticketFrom(rec)
			body := rec.Body.String()
			if !strings.Contains(body, `name="scope" value="read" checked`) {
				t.Error("read must be selected by default")
			}
			if strings.Contains(body, `name="scope" value="write" checked`) {
				t.Error("write must require an explicit choice")
			}
			if got := strings.Contains(body, `name="scope" value="write"`); got != tc.canWrite {
				t.Errorf("write offered = %v, want %v", got, tc.canWrite)
			}
			if strings.Contains(body, `value="admin:dangerous"`) {
				t.Error("dangerous scope must never be offered")
			}
			lq := locationQuery(t, e.consent(ticket, url.Values{"membership": {e.membershipID()}, "scope": {tc.chosen}}))
			rec = e.post(mcpOAuthToken, url.Values{
				"grant_type": {"authorization_code"}, "client_id": {id}, "code": {lq.Get("code")},
				"redirect_uri": {loopbackCB}, "code_verifier": {verifier}, "resource": {testResource},
			})
			if rec.Code != http.StatusOK || jsonField(t, rec, "scope") != tc.wantScope {
				t.Fatalf("token scope: %d %s, want %q", rec.Code, rec.Body, tc.wantScope)
			}
			tools := toolNames(t, e.mcpCall(jsonField(t, rec, "access_token"), "tools/list"))
			if got := tools["send_to_session"]; got != (tc.wantScope == "read write") {
				t.Errorf("write tool advertised = %v for scope %q", got, tc.wantScope)
			}
		})
	}
}

func TestMCPOAuthConsentPOSTChecks(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	_, challenge := pkcePair()
	mem := url.Values{"membership": {e.membershipID()}, "scope": {"write"}}
	fresh := func() string { return e.ticketFrom(e.authorize(e.authQuery(id, loopbackCB, challenge))) }

	// Cross-site POST.
	f := url.Values{"ticket": {fresh()}, "membership": mem["membership"], "scope": mem["scope"]}
	for _, hdr := range []map[string]string{{"Origin": "https://evil.example"}, {"Sec-Fetch-Site": "cross-site"}} {
		if rec := e.do("POST", mcpOAuthConsent, f.Encode(), hdr); rec.Code != http.StatusForbidden {
			t.Errorf("cross-site POST %v = %d, want 403", hdr, rec.Code)
		}
	}
	// A ticket is single use.
	tk := fresh()
	if rec := e.consent(tk, mem); rec.Code != http.StatusFound {
		t.Fatalf("first use: %d", rec.Code)
	}
	if rec := e.consent(tk, mem); rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
		t.Errorf("second use: %d, want a 400 page", rec.Code)
	}
	// A ticket belongs to the member it was shown to.
	tk = fresh()
	e.mgr.devUser = "someone-else"
	if rec := e.consent(tk, mem); rec.Code != http.StatusForbidden {
		t.Errorf("another member's POST = %d, want 403", rec.Code)
	}
	e.mgr.devUser = "dev-user"
	// The membership has to be one of the member's own.
	tk = fresh()
	if rec := e.consent(tk, url.Values{"membership": {"not-mine"}, "scope": {"read"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("foreign membership = %d, want 400", rec.Code)
	}
	// Scope cannot be widened past the request or to the dangerous tier.
	for _, sc := range []string{"admin:dangerous", "read write admin:dangerous", ""} {
		tk = fresh()
		if rec := e.consent(tk, url.Values{"membership": mem["membership"], "scope": {sc}}); rec.Code != http.StatusBadRequest {
			t.Errorf("scope %q = %d, want 400", sc, rec.Code)
		}
	}
	// Cancel answers access_denied to the parked redirect with iss and state.
	tk = fresh()
	lq := locationQuery(t, e.consent(tk, url.Values{"action": {"cancel"}, "redirect_uri": {"https://evil.example/"}}))
	if lq.Get("error") != "access_denied" || lq.Get("iss") != testIssuer || lq.Get("state") != "st-1" {
		t.Errorf("cancel = %v", lq)
	}
	// An expired ticket.
	tk = fresh()
	e.advance(6 * time.Minute)
	if rec := e.consent(tk, mem); rec.Code != http.StatusBadRequest {
		t.Errorf("expired ticket = %d", rec.Code)
	}
}

func TestMCPOAuthPromptNoneNeedsAnExistingWiderGrant(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	_, challenge := pkcePair()
	none := func(scope string) url.Values {
		q := e.authQuery(id, loopbackCB, challenge)
		q.Set("prompt", "none")
		q.Set("scope", scope)
		return locationQuery(t, e.authorize(q))
	}
	if got := none("read"); got.Get("error") != "consent_required" {
		t.Fatalf("no grant yet: %v", got)
	}
	e.connect(id, loopbackCB, "read")
	if got := none("read"); got.Get("code") == "" || got.Get("iss") != testIssuer {
		t.Errorf("equal scope: %v, want a code", got)
	}
	if got := none("read write"); got.Get("error") != "consent_required" {
		t.Errorf("wider scope than the grant: %v, want consent_required", got)
	}
	e.connect(id, loopbackCB, "read write")
	if got := none("read write"); got.Get("code") == "" || got.Get("iss") != testIssuer {
		t.Errorf("covering write grant: %v, want a code", got)
	}
	// Another client does not inherit it.
	other := e.registerClient(loopbackCB)
	q := e.authQuery(other, loopbackCB, challenge)
	q.Set("prompt", "none")
	if got := locationQuery(t, e.authorize(q)); got.Get("error") != "consent_required" {
		t.Errorf("another client: %v", got)
	}
}

// --- token endpoint --------------------------------------------------------------

func TestMCPOAuthFullFlowAndPrincipal(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	tk := e.connect(id, loopbackCB, "read write")
	if tk.Scope != "read write" || !strings.HasPrefix(tk.Access, "afmcp_at_") || !strings.HasPrefix(tk.Refresh, "afmcp_rt_") {
		t.Fatalf("tokens = %+v", tk)
	}
	names := toolNames(t, e.mcpCall(tk.Access, "tools/list"))
	if !names["list_my_sessions"] || !names["send_to_session"] {
		t.Errorf("a write token should see read and write tools: %v", names)
	}
	// The grant row is the principal's id, and nothing was stored as a PAT.
	grants, _ := e.mgr.store.ListMCPOAuthGrantsByIdentity(context.Background(), e.ident.ID, e.now().Unix())
	if len(grants) != 1 || grants[0].ClientID != id || grants[0].RedirectHost != "127.0.0.1:53682" || grants[0].Resource != testResource {
		t.Fatalf("grants = %+v", grants)
	}
	if pats, _ := e.mgr.store.ListPATsByIdentity(context.Background(), e.ident.ID); len(pats) != 0 {
		t.Errorf("an OAuth connection created %d PAT rows", len(pats))
	}
	// Narrowed at consent: a read grant sees no write tool.
	rd := e.connect(id, loopbackCB, "read")
	if rd.Scope != "read" {
		t.Errorf("narrowed token scope = %q", rd.Scope)
	}
	if names := toolNames(t, e.mcpCall(rd.Access, "tools/list")); names["send_to_session"] {
		t.Errorf("a read token sees the write tool")
	}
	// The token endpoint response is not cacheable.
	rec := e.refresh(id, tk.Refresh)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("token response headers = %v", rec.Header())
	}
	// A dropped access token, a PAT-looking one and garbage are all 401 with the challenge.
	for _, bad := range []string{"afmcp_at_nonesuch", "af_pat_nonesuch", "junk"} {
		rec := e.mcpCall(bad, "tools/list")
		if rec.Code != 401 || !strings.Contains(rec.Header().Get("WWW-Authenticate"), `resource_metadata="`) {
			t.Errorf("token %q: %d %q", bad, rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
	}
}

func TestMCPOAuthMCPRejectsATokenBoundElsewhere(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	now := e.now().Unix()
	g := store.MCPOAuthGrant{ID: "g-other", ClientID: "c", IdentityID: e.ident.ID, MembershipID: e.membershipID(), Scope: "read",
		Resource: testBase + "/not-mcp", CreatedAt: now, IdleExpiresAt: now + 1000, AbsExpiresAt: now + 1000}
	if err := e.mgr.store.CreateMCPOAuthGrant(ctx, g); err != nil {
		t.Fatal(err)
	}
	put := func(tok, res string) {
		if err := e.mgr.store.InsertMCPOAuthToken(ctx, store.MCPOAuthToken{Hash: devIdPHash(tok), Kind: store.MCPOAuthAccessKind,
			GrantID: g.ID, ClientID: "c", IdentityID: e.ident.ID, MembershipID: g.MembershipID, Scope: "read", Resource: res,
			ExpiresAt: time.Now().Unix() + 1000}); err != nil {
			t.Fatal(err)
		}
	}
	// time.Now is what /mcp reads, so the expiry above is real time.
	put("afmcp_at_elsewhere", testBase+"/not-mcp")
	put("afmcp_at_here", testResource)
	if rec := e.mcpCall("afmcp_at_elsewhere", "tools/list"); rec.Code != 401 {
		t.Errorf("a token bound to another resource = %d, want 401", rec.Code)
	}
	if rec := e.mcpCall("afmcp_at_here", "tools/list"); rec.Code != 200 {
		t.Errorf("a token bound to /mcp = %d %s, want 200", rec.Code, rec.Body)
	}
}

func TestMCPOAuthTokenEndpointRules(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	// authorize helper that stops at a code.
	code := func() (string, string) {
		verifier, challenge := pkcePair()
		tk := e.ticketFrom(e.authorize(e.authQuery(id, loopbackCB, challenge)))
		lq := locationQuery(t, e.consent(tk, url.Values{"membership": {e.membershipID()}, "scope": {"write"}}))
		return lq.Get("code"), verifier
	}
	exch := func(code, verifier string, mut func(url.Values)) *httptest.ResponseRecorder {
		f := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code},
			"redirect_uri": {loopbackCB}, "code_verifier": {verifier}, "resource": {testResource}}
		if mut != nil {
			mut(f)
		}
		return e.post(mcpOAuthToken, f)
	}
	wantErr := func(name string, rec *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if rec.Code != status || jsonField(t, rec, "error") != code {
			t.Errorf("%s: %d %s, want %d %s", name, rec.Code, rec.Body, status, code)
		}
	}
	c, v := code()
	wantErr("wrong resource", exch(c, v, func(f url.Values) { f.Set("resource", testBase+"/other") }), 400, "invalid_target")
	wantErr("no resource", exch(c, v, func(f url.Values) { f.Del("resource") }), 400, "invalid_target")
	// The failed attempts above did not spend the code.
	wantErr("wrong verifier", exch(c, "x"+v[1:], nil), 400, "invalid_grant")
	c, v = code()
	wantErr("other redirect", exch(c, v, func(f url.Values) { f.Set("redirect_uri", "http://127.0.0.1:1/callback") }), 400, "invalid_grant")
	c, v = code()
	wantErr("no verifier", exch(c, "", nil), 400, "invalid_grant")
	c, v = code()
	wantErr("client secret offered", exch(c, v, func(f url.Values) { f.Set("client_secret", "s") }), 401, "invalid_client")
	wantErr("basic auth offered", func() *httptest.ResponseRecorder {
		return e.do("POST", mcpOAuthToken, url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {c}}.Encode(),
			map[string]string{"Authorization": "Basic Zm9vOmJhcg=="})
	}(), 401, "invalid_client")
	other := e.registerClient(loopbackCB)
	c, v = code()
	wantErr("code of another client", exch(c, v, func(f url.Values) { f.Set("client_id", other) }), 400, "invalid_grant")
	wantErr("unknown grant type", e.post(mcpOAuthToken, url.Values{"grant_type": {"password"}, "client_id": {id}}), 400, "unsupported_grant_type")
	wantErr("no client_id", e.post(mcpOAuthToken, url.Values{"grant_type": {"authorization_code"}}), 400, "invalid_request")
	// Expiry: a code lives 60 seconds.
	c, v = code()
	e.advance(61 * time.Second)
	wantErr("expired code", exch(c, v, nil), 400, "invalid_grant")
	// GET is not a token request.
	if rec := e.do("GET", mcpOAuthToken, "", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /token = %d", rec.Code)
	}
}

func TestMCPOAuthReplayedCodeRevokesTheGrant(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	tk := e.connect(id, loopbackCB, "read write")
	if e.mcpCall(tk.Access, "tools/list").Code != 200 {
		t.Fatal("token should work before the replay")
	}
	rec := e.post(mcpOAuthToken, url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {tk.Code},
		"redirect_uri": {loopbackCB}, "code_verifier": {tk.Verifier}, "resource": {testResource}})
	if rec.Code != 400 || jsonField(t, rec, "error") != "invalid_grant" {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body)
	}
	if e.mcpCall(tk.Access, "tools/list").Code != 401 {
		t.Error("the access token minted from a replayed code still works")
	}
	if rec := e.refresh(id, tk.Refresh); rec.Code != 400 {
		t.Errorf("the refresh token minted from a replayed code still works: %d", rec.Code)
	}
}

func TestMCPOAuthRefreshRotatesAndReuseRevokesTheGrant(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	tk := e.connect(id, loopbackCB, "read write")
	rec := e.refresh(id, tk.Refresh)
	if rec.Code != 200 {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body)
	}
	a2, r2 := jsonField(t, rec, "access_token"), jsonField(t, rec, "refresh_token")
	if r2 == tk.Refresh || a2 == tk.Access {
		t.Error("a refresh did not rotate the tokens")
	}
	if e.mcpCall(a2, "tools/list").Code != 200 {
		t.Error("the refreshed access token does not work")
	}
	// Reuse of the spent token: refused, and the grant and its newest tokens die with it.
	rec = e.refresh(id, tk.Refresh)
	if rec.Code != 400 || jsonField(t, rec, "error") != "invalid_grant" {
		t.Fatalf("reuse: %d %s", rec.Code, rec.Body)
	}
	if e.mcpCall(a2, "tools/list").Code != 401 {
		t.Error("the newest access token survived a refresh-token reuse")
	}
	if rec := e.refresh(id, r2); rec.Code != 400 {
		t.Errorf("the newest refresh token survived a reuse: %d", rec.Code)
	}
	// Another client presenting the token neither works nor burns it.
	tk2 := e.connect(id, loopbackCB, "read")
	other := e.registerClient(loopbackCB)
	if rec := e.refresh(other, tk2.Refresh); rec.Code != 400 {
		t.Errorf("another client's refresh = %d", rec.Code)
	}
	if rec := e.refresh(id, tk2.Refresh); rec.Code != 200 {
		t.Errorf("the rightful client was locked out by a stranger: %d", rec.Code)
	}
}

func TestMCPOAuthConcurrentRefreshesHaveOneWinner(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	tk := e.connect(id, loopbackCB, "read")
	var wins, losses atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch e.refresh(id, tk.Refresh).Code {
			case 200:
				wins.Add(1)
			case 400:
				losses.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || losses.Load() != 7 {
		t.Errorf("wins=%d losses=%d, want exactly one winner", wins.Load(), losses.Load())
	}
}

func TestMCPOAuthRefreshNarrowsNeverWidens(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	tk := e.connect(id, loopbackCB, "read")
	f := url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {tk.Refresh}, "scope": {"read write"}}
	if rec := e.post(mcpOAuthToken, f); rec.Code != 400 || jsonField(t, rec, "error") != "invalid_scope" {
		t.Errorf("widening refresh: %d %s", rec.Code, rec.Body)
	}
	tk = e.connect(id, loopbackCB, "read write")
	f.Set("refresh_token", tk.Refresh)
	f.Set("scope", "read")
	rec := e.post(mcpOAuthToken, f)
	if rec.Code != 200 || jsonField(t, rec, "scope") != "read" {
		t.Fatalf("narrowing refresh: %d %s", rec.Code, rec.Body)
	}
	if names := toolNames(t, e.mcpCall(jsonField(t, rec, "access_token"), "tools/list")); names["send_to_session"] {
		t.Error("the narrowed access token still sees write tools")
	}
	f.Set("resource", testBase+"/other")
	f.Set("refresh_token", jsonField(t, rec, "refresh_token"))
	if rec := e.post(mcpOAuthToken, f); jsonField(t, rec, "error") != "invalid_target" {
		t.Errorf("refresh with a wrong resource: %s", rec.Body)
	}
}

func TestMCPOAuthGrantLifetimes(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	// Idle: 30 days without use ends the grant.
	tk := e.connect(id, loopbackCB, "read")
	e.advance(31 * 24 * time.Hour)
	if rec := e.refresh(id, tk.Refresh); rec.Code != 400 {
		t.Errorf("a grant idle for 31 days refreshed: %d", rec.Code)
	}
	// Active use keeps it alive, but never past 90 days.
	tk = e.connect(id, loopbackCB, "read")
	for i := 0; i < 8; i++ {
		e.advance(20 * 24 * time.Hour)
		rec := e.refresh(id, tk.Refresh)
		if i < 4 {
			if rec.Code != 200 {
				t.Fatalf("refresh at day %d: %d %s", (i+1)*20, rec.Code, rec.Body)
			}
			tk.Refresh = jsonField(t, rec, "refresh_token")
			continue
		}
		if rec.Code != 400 { // day 100 > 90
			t.Fatalf("refresh at day %d = %d, want the 90-day cap to end it", (i+1)*20, rec.Code)
		}
		break
	}
}

func TestMCPOAuthRevoke(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	other := e.registerClient(loopbackCB)
	tk := e.connect(id, loopbackCB, "read write")
	// Another client's attempt, and an unknown token, are 200 and change nothing.
	for _, f := range []url.Values{{"client_id": {other}, "token": {tk.Access}}, {"client_id": {id}, "token": {"afmcp_at_nonesuch"}}} {
		if rec := e.post(mcpOAuthRevoke, f); rec.Code != 200 {
			t.Errorf("revoke %v = %d", f, rec.Code)
		}
	}
	if e.mcpCall(tk.Access, "tools/list").Code != 200 {
		t.Fatal("a stranger's revoke took the token down")
	}
	// The refresh token revokes the whole connection, access token included.
	if rec := e.post(mcpOAuthRevoke, url.Values{"client_id": {id}, "token": {tk.Refresh}, "token_type_hint": {"refresh_token"}}); rec.Code != 200 {
		t.Fatalf("revoke = %d", rec.Code)
	}
	if e.mcpCall(tk.Access, "tools/list").Code != 401 {
		t.Error("the access token outlived its revoked grant")
	}
	if rec := e.refresh(id, tk.Refresh); rec.Code != 400 {
		t.Errorf("a revoked refresh token worked: %d", rec.Code)
	}
	if rec := e.post(mcpOAuthRevoke, url.Values{"token": {"x"}}); rec.Code != 400 {
		t.Errorf("revoke without client_id = %d", rec.Code)
	}
}

func TestMCPOAuthGrantFollowsMembership(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	tk := e.connect(id, loopbackCB, "read")
	if err := e.mgr.store.DeleteMembership(context.Background(), e.membershipID()); err != nil {
		t.Fatal(err)
	}
	if e.mcpCall(tk.Access, "tools/list").Code != 401 {
		t.Error("a removed member's OAuth token still works")
	}
}

// --- audit and secrets -----------------------------------------------------------

func TestMCPOAuthAuditsAndNeverLogsATokenValue(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	tk := e.connect(id, loopbackCB, "read write")
	rec := e.refresh(id, tk.Refresh)
	r2 := jsonField(t, rec, "refresh_token")
	_ = e.refresh(id, tk.Refresh) // reuse: revokes
	_ = e.refresh(id, r2)
	_ = e.post(mcpOAuthToken, url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {tk.Code},
		"redirect_uri": {loopbackCB}, "code_verifier": {tk.Verifier}, "resource": {testResource}})

	audit, err := e.mgr.store.ListAuditByTenant(context.Background(), e.tenA.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	var grants, toks int
	var all strings.Builder
	for _, a := range audit {
		all.WriteString(a.Action + " " + a.Target + " " + a.Detail + "\n")
		switch a.Action {
		case "mcp.oauth.grant":
			grants++
		case "mcp.oauth.token":
			toks++
		}
	}
	if grants < 1 || toks < 2 {
		t.Errorf("audit rows: grant=%d token=%d\n%s", grants, toks, all.String())
	}
	secrets := []string{tk.Access, tk.Refresh, tk.Code, r2, tk.Verifier}
	for _, s := range secrets {
		for where, text := range map[string]string{"log": logs.String(), "audit": all.String()} {
			if strings.Contains(text, s) {
				t.Errorf("a token value appears in the %s", where)
			}
			if strings.Contains(text, devIdPHash(s)) {
				t.Errorf("a token hash appears in the %s", where)
			}
		}
	}
	// The searcher itself works: it would have found a leaked value.
	if !strings.Contains("x "+tk.Access, tk.Access) {
		t.Fatal("unreachable")
	}
}

func TestMCPOAuthTokenIssuanceNeedsTheAuditRow(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	verifier, challenge := pkcePair()
	tkt := e.ticketFrom(e.authorize(e.authQuery(id, loopbackCB, challenge)))
	code := locationQuery(t, e.consent(tkt, url.Values{"membership": {e.membershipID()}, "scope": {"read"}})).Get("code")
	// Break the audit ledger: no row, no token.
	e.mgr.store = noAuditStore{e.mgr.store}
	rec := e.post(mcpOAuthToken, url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code},
		"redirect_uri": {loopbackCB}, "code_verifier": {verifier}, "resource": {testResource}})
	if rec.Code != http.StatusInternalServerError || jsonField(t, rec, "access_token") != "" {
		t.Errorf("token issued without an audit row: %d %s", rec.Code, rec.Body)
	}
}

func TestMCPOAuthRefreshNeedsTheAuditRow(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	tk := e.connect(id, loopbackCB, "read")
	e.mgr.store = noAuditStore{e.mgr.store}
	rec := e.refresh(id, tk.Refresh)
	if rec.Code != http.StatusInternalServerError || jsonField(t, rec, "access_token") != "" {
		t.Errorf("tokens issued on refresh without an audit row: %d %s", rec.Code, rec.Body)
	}
}

// noAuditStore is a store whose audit ledger is unavailable.
type noAuditStore struct{ store.Store }

func (noAuditStore) InsertAudit(context.Context, store.AuditLog) error {
	return errors.New("ledger down")
}

// grantHold runs a hook just before the grant row is written, to interleave a second request.
type grantHold struct {
	store.Store
	once sync.Once
	hook func()
}

func (g *grantHold) CreateMCPOAuthGrant(ctx context.Context, gr store.MCPOAuthGrant) error {
	g.once.Do(g.hook)
	return g.Store.CreateMCPOAuthGrant(ctx, gr)
}

func TestMCPOAuthReplayDuringTheFirstRedemptionLeavesNoLiveToken(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	verifier, challenge := pkcePair()
	tkt := e.ticketFrom(e.authorize(e.authQuery(id, loopbackCB, challenge)))
	code := locationQuery(t, e.consent(tkt, url.Values{"membership": {e.membershipID()}, "scope": {"write"}})).Get("code")
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code},
		"redirect_uri": {loopbackCB}, "code_verifier": {verifier}, "resource": {testResource}}
	// The replay arrives after the first redemption spent the code and before its grant exists.
	e.mgr.store = &grantHold{Store: e.mgr.store, hook: func() {
		if rec := e.post(mcpOAuthToken, form); rec.Code != http.StatusBadRequest {
			t.Errorf("the replay: %d %s", rec.Code, rec.Body)
		}
	}}
	rec := e.post(mcpOAuthToken, form)
	if rec.Code == http.StatusOK {
		if e.mcpCall(jsonField(t, rec, "access_token"), "tools/list").Code == http.StatusOK {
			t.Fatal("a token issued by the first redemption is live after the code was replayed")
		}
	}
}

func TestMCPOAuthLoopbackMatchIgnoresOnlyThePort(t *testing.T) {
	e := newMCPOAuthEnv(t)
	for _, c := range []struct {
		reg, req string
		want     bool
	}{
		{"http://127.0.0.1/a%2Fb", "http://127.0.0.1:4567/a%2Fb", true},
		{"http://127.0.0.1/a%2Fb", "http://127.0.0.1:4567/a/b", false},
		{"http://127.0.0.1/a", "http://127.0.0.1:4567/%61", false},
		{"http://127.0.0.1/a", "http://127.0.0.1:4567/a?", false},
		{"http://127.0.0.1/a", "http://127.0.0.1:4567/a?x=1", false},
		{"http://127.0.0.1/a?x=1", "http://127.0.0.1:4567/a?x=1", true},
	} {
		if got := e.api.redirectRegistered(e.api.fallback, []string{c.reg}, c.req); got != c.want {
			t.Errorf("registered %q, requested %q = %v, want %v", c.reg, c.req, got, c.want)
		}
	}
}

func TestMCPOAuthRevocationsAreAuditedUnderTheGrantsTenant(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(loopbackCB)
	ctx := context.Background()
	count := func(detail string) int {
		rows, _ := e.mgr.store.ListAuditByTenant(ctx, e.tenA.ID, 200)
		n := 0
		for _, a := range rows {
			if a.Action == "mcp.oauth.grant" && strings.Contains(a.Detail, detail) {
				n++
			}
		}
		return n
	}
	tk := e.connect(id, loopbackCB, "read")
	e.post(mcpOAuthRevoke, url.Values{"client_id": {id}, "token": {tk.Access}})
	tk = e.connect(id, loopbackCB, "read")
	e.post(mcpOAuthToken, url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {tk.Code},
		"redirect_uri": {loopbackCB}, "code_verifier": {tk.Verifier}, "resource": {testResource}})
	tk = e.connect(id, loopbackCB, "read")
	e.refresh(id, tk.Refresh)
	e.refresh(id, tk.Refresh)
	for _, d := range []string{"revoked by client", "reason=code-replay", "reason=refresh-reuse"} {
		if n := count(d); n != 1 {
			t.Errorf("%d %q rows in the tenant's audit, want 1", n, d)
		}
	}
}

func TestMCPOAuthDisabledAuthorizeIs404BehindTheAuthGateToo(t *testing.T) {
	for _, on := range []bool{false, true} {
		cfg := oauthTestConfig(t)
		cfg.mgr.publicBaseURL = testBase
		cfg.mgr.store = brandTestStore(t)
		if !on {
			t.Setenv("AF_MCP_OAUTH", "")
		} else {
			t.Setenv("AF_MCP_OAUTH", "1")
			t.Setenv("AF_MCP_ENABLED", "true")
		}
		mux := http.NewServeMux()
		newMCPOAuthAPI(cfg).mount(mux)
		gate := cfg.authGate(mux)
		for _, path := range []string{mcpOAuthAuthorize, mcpOAuthConsent} {
			req := httptest.NewRequest("GET", testBase+path, nil)
			req.Header.Set("Accept", "text/html")
			rec := httptest.NewRecorder()
			gate.ServeHTTP(rec, req)
			want := http.StatusFound // served: a signed-out visitor is sent to log in
			if !on {
				want = http.StatusNotFound
			}
			if rec.Code != want {
				t.Errorf("enabled=%v GET %s through authGate = %d, want %d", on, path, rec.Code, want)
			}
		}
	}
}

func TestFetchCIMDDocumentLeavesNoConnectionBehind(t *testing.T) {
	var open atomic.Int32
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	ts.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		switch s {
		case http.StateNew:
			open.Add(1)
		case http.StateClosed, http.StateHijacked:
			open.Add(-1)
		}
	}
	ts.Start()
	defer ts.Close()
	allow := func(string, string, syscall.RawConn) error { return nil }
	for i := 0; i < 4; i++ {
		if _, _, err := fetchCIMDWith(context.Background(), ts.URL, allow); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for open.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := open.Load(); n != 0 {
		t.Errorf("%d connections left open after four fetches", n)
	}
}

func TestMCPOAuthAuthorizeNamesAnUntrustedClientHost(t *testing.T) {
	e := newMCPOAuthEnv(t)
	_, challenge := pkcePair()
	cb := "https://claude.ai/api/mcp/auth_callback"
	get := func(id, lang string) *httptest.ResponseRecorder {
		return e.do(http.MethodGet, mcpOAuthAuthorize+"?"+e.authQuery(id, cb, challenge).Encode(), "", map[string]string{"Accept-Language": lang})
	}

	// A well-formed https client_id on a host off the list: the page names the host, nothing more.
	for _, id := range []string{
		"https://app.example.org/oauth/client.json",
		"https://app.example.org/oauth/client.json?token=s3cret",
		"https://app.example.org/oauth/client.json?x=%3Cscript%3E",
	} {
		rec := get(id, "en")
		body := rec.Body.String()
		if rec.Code != http.StatusBadRequest || !strings.Contains(body, "not trusted here") ||
			!strings.Contains(body, "<code>app.example.org</code>") || !strings.Contains(body, "trusted client hosts") {
			t.Errorf("%q: want the untrusted-host page naming app.example.org, got %d %s", id, rec.Code, body)
		}
		for _, leak := range []string{"oauth/client.json", "s3cret", "token=", "<script>"} {
			if strings.Contains(body, leak) {
				t.Errorf("%q: page leaks %q", id, leak)
			}
		}
	}
	// The setting is named as the Console and the guides spell it, route included.
	if rec := get("https://app.example.org/c.json", "ja"); !strings.Contains(rec.Body.String(), "信頼されていません") ||
		!strings.Contains(rec.Body.String(), "管理 → 配備 → MCP ログイン許可ホスト") {
		t.Errorf("ja page missing: %s", rec.Body)
	}
	if rec := get("https://app.example.org/c.json", "en"); !strings.Contains(rec.Body.String(), "Admin → Deployment → MCP sign-in hosts") {
		t.Errorf("en page lacks the setting route: %s", rec.Body)
	}

	// Markup in the host never reaches the page unescaped.
	if rec := get("https://<b>x</b>.example.org/c.json", "en"); strings.Contains(rec.Body.String(), "<b>x") {
		t.Errorf("markup in the host was echoed: %s", rec.Body)
	}

	// Everything else stays generic: userinfo, a port, plain http, an unknown DCR id, and a
	// trusted host whose document cannot be fetched.
	for _, id := range []string{
		"https://user:pw@app.example.org/c.json", "https://app.example.org:8443/c.json",
		"http://app.example.org/c.json", "dcr-unknown", "https://claude.ai/missing.json",
	} {
		rec := get(id, "en")
		body := rec.Body.String()
		if rec.Code != http.StatusBadRequest || !strings.Contains(body, "This connection request was refused.") ||
			strings.Contains(body, "app.example.org") || strings.Contains(body, "pw") {
			t.Errorf("%q: want the generic page without the host, got %d %s", id, rec.Code, body)
		}
	}
}
