package mcpreg

// Google Workspace MCP servers (ADR 0146 decision 2), against the documents measured on
// 2026-11-26: Gmail's protected resource metadata and accounts.google.com's OIDC document, kept
// verbatim in testdata/google-workspace and rewritten to point at a local fake.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

const (
	gmailReadonly    = "https://www.googleapis.com/auth/gmail.readonly"
	googleIssuerLive = "https://accounts.google.com"
)

// googleFake serves the measured documents with Google's hosts replaced by its own URL. The MCP
// endpoint answers server/discover and tools/list without credentials, as Google's do.
type googleFake struct {
	srv *httptest.Server
	prm string // served at the path-inserted well-known URL; "" answers 404
	// rootPRM, when set, is served at the root well-known URL.
	rootPRM string
	oidc    string
	mu      sync.Mutex
	hits    []string
	tokens  []url.Values
}

func newGoogleFake(t *testing.T) *googleFake {
	t.Helper()
	f := &googleFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	read := func(name string) string {
		b, err := os.ReadFile("testdata/google-workspace/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return strings.NewReplacer(
			"https://gmailmcp.googleapis.com/mcp/v1", f.srv.URL+"/mcp",
			googleIssuerLive, f.srv.URL,
			"https://oauth2.googleapis.com", f.srv.URL,
		).Replace(string(b))
	}
	f.prm = read("gmail-prm.json")
	f.oidc = read("accounts-google-openid-configuration.json")
	// The rewrite must keep the measured difference: the PRM names the issuer with a "/",
	// the OIDC document without.
	if !strings.Contains(f.prm, `"`+f.srv.URL+`/"`) || !strings.Contains(f.oidc, `"issuer": "`+f.srv.URL+`"`) {
		t.Fatalf("fixtures lost the issuer shapes:\n%s\n%s", f.prm, f.oidc)
	}
	return f
}

func (f *googleFake) url(p string) string { return f.srv.URL + p }

func (f *googleFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits = append(f.hits, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	switch r.URL.Path {
	case "/mcp":
		var m struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		w.Header().Set("Content-Type", "application/json")
		switch m.Method {
		case "server/discover":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{
				"resultType": "complete", "supportedVersions": []string{ProtocolVersion},
				"_meta": map[string]any{metaServerInfo: map[string]any{"name": "gmail", "version": "1"}},
			}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{
				"resultType": "complete", "tools": []map[string]any{{"name": "search_threads"}},
			}})
		}
	case "/.well-known/oauth-protected-resource/mcp":
		if f.prm == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(f.prm))
	case "/.well-known/oauth-protected-resource":
		if f.rootPRM == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(f.rootPRM))
	case "/.well-known/openid-configuration":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(f.oidc))
	case "/token":
		_ = r.ParseForm()
		f.mu.Lock()
		f.tokens = append(f.tokens, r.PostForm)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(googleTokenBody(r.PostForm.Get("code"))))
	default:
		http.NotFound(w, r) // including /.well-known/oauth-authorization-server, as measured
	}
}

// googleTokenBody answers like Google: a refresh token only for an authorization the member
// consented to. A test passes googleCode(q), which says whether the URL asked for consent.
func googleTokenBody(code string) string {
	if strings.HasPrefix(code, "consented/") {
		return `{"access_token":"ya29.` + code + `","token_type":"Bearer","expires_in":3599,"refresh_token":"1//rt-` + code + `"}`
	}
	return `{"access_token":"ya29.` + code + `","token_type":"Bearer","expires_in":3599}`
}

func googleCode(q url.Values, n string) string {
	if q.Get("prompt") == "consent" && q.Get("access_type") == "offline" {
		return "consented/" + n
	}
	return "silent/" + n
}

func (f *googleFake) hitCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, h := range f.hits {
		if strings.HasPrefix(h, prefix) {
			n++
		}
	}
	return n
}

// useGoogleParamsFor gives the fake's issuer Google's row of the parameter table.
func useGoogleParamsFor(t *testing.T, issuer string) {
	t.Helper()
	authParamsByIssuer[issuer] = authParamsByIssuer[googleIssuerLive]
	t.Cleanup(func() { delete(authParamsByIssuer, issuer) })
}

// distributeGoogle puts a tenant definition for the fake into the tenant cache, the way the
// Workspace preset will write one: explicit scopes, and the tenant's client entered with the
// issuer as the PRM spells it.
func distributeGoogle(t *testing.T, f *googleFake, scopes []string) string {
	t.Helper()
	d := ServerDef{
		ID: "t-gmail", Name: "gmail", Origin: OriginTenant, Transport: TransportHTTP, URL: f.url("/mcp"),
		Auth: AuthOAuth, Enabled: true, Targets: Targets{Session: true}, Scopes: scopes,
		OAuthClient: &secrets.MCPTenantClient{Issuer: f.url("/"), ClientID: "agentic.apps.googleusercontent.com"},
	}
	kept, dropped := acceptTenant([]ServerDef{d})
	if dropped != 0 {
		t.Fatalf("the tenant definition was dropped: %+v", d)
	}
	if err := writeTenantCache(kept, 1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(tenantCachePath()) })
	return d.ID
}

func TestGoogleWorkspaceProbeReportsOAuthOnAPublicToolList(t *testing.T) {
	f := newGoogleFake(t)
	res := Probe(context.Background(), ServerDef{Transport: TransportHTTP, URL: f.url("/mcp"), Enabled: true})
	if !res.OK || res.ToolCount != 1 || res.Code != "" {
		t.Fatalf("the public tool list must still pass: %+v", res)
	}
	o := res.OAuth
	if o == nil || !o.Required || len(o.Problems) != 0 {
		t.Fatalf("want the OAuth findings next to the success, got %+v", o)
	}
	if o.Selected != f.url("") || len(o.Servers) != 1 || !o.Servers[0].Valid {
		t.Fatalf("the issuer with a root \"/\" must validate against its OIDC document: %+v", o)
	}
	if o.Servers[0].Issuer != f.url("") || o.Servers[0].MetadataURL != f.url("/.well-known/openid-configuration") {
		t.Errorf("server = %+v, want the issuer the metadata states", o.Servers[0])
	}
	if len(o.ScopesSupported) == 0 || o.ScopesSupported[0] != "https://mail.google.com/" {
		t.Errorf("scopes_supported = %v", o.ScopesSupported)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"ok":true`) || !strings.Contains(string(b), `"oauth":{"required":true`) {
		t.Errorf("wire JSON: %s", b)
	}
}

func TestPublicToolListWithoutMetadataKeepsItsPlainResult(t *testing.T) {
	f := newGoogleFake(t)
	f.prm = "" // both well-known URLs answer 404
	res := Probe(context.Background(), ServerDef{Transport: TransportHTTP, URL: f.url("/mcp"), Enabled: true})
	if !res.OK || res.OAuth != nil {
		t.Fatalf("a server without a PRM must report a plain success: %+v", res)
	}
}

// An origin whose root PRM describes another resource says nothing about this public server.
func TestPublicToolListOnASharedOriginKeepsItsPlainResult(t *testing.T) {
	f := newGoogleFake(t)
	other := strings.Replace(f.prm, `"resource": "`+f.url("/mcp")+`"`, `"resource": "`+f.url("/private")+`"`, 1)
	if other == f.prm {
		t.Fatal("fixture rewrite did not apply")
	}
	f.prm, f.rootPRM = "", other // the path-inserted URL misses, the root one is the other resource's
	res := Probe(context.Background(), ServerDef{Transport: TransportHTTP, URL: f.url("/mcp"), Enabled: true})
	if !res.OK || res.OAuth != nil {
		t.Fatalf("a PRM for another resource must not mark this one: %+v", res.OAuth)
	}
}

// A proxy that never answers the well-known paths costs the passed test its own short budget,
// not the probe's whole deadline.
func TestPublicToolListLookupIsBounded(t *testing.T) {
	old := publicOAuthBudget
	publicOAuthBudget = 200 * time.Millisecond
	t.Cleanup(func() { publicOAuthBudget = old })
	f := newGoogleFake(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			<-r.Context().Done()
			return
		}
		f.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	start := time.Now()
	res := Probe(context.Background(), ServerDef{Transport: TransportHTTP, URL: srv.URL + "/mcp", Enabled: true, TimeoutMS: 20000})
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("the test took %v behind a silent well-known path", el)
	}
	if !res.OK || res.ToolCount != 1 || res.OAuth != nil {
		t.Fatalf("got %+v", res)
	}
}

func TestProbeWithStaticHeadersLooksUpNoMetadata(t *testing.T) {
	f := newGoogleFake(t)
	res := Probe(context.Background(), ServerDef{Transport: TransportHTTP, URL: f.url("/mcp"), Enabled: true,
		Headers: map[string]string{"Authorization": "Bearer tok"}})
	if !res.OK || res.OAuth != nil {
		t.Fatalf("got %+v", res)
	}
	if n := f.hitCount("GET "); n != 0 {
		t.Errorf("%d metadata requests for a definition with static headers", n)
	}
}

func TestGoogleWorkspaceSignIn(t *testing.T) {
	resetOAuthState(t)
	f := newGoogleFake(t)
	useGoogleParamsFor(t, f.url(""))
	id := distributeGoogle(t, f, []string{gmailReadonly})
	req := startReq(nil)
	res, q := mustStart(t, id, req)

	if !strings.HasPrefix(res.AuthorizeURL, f.url("/o/oauth2/v2/auth?")) {
		t.Fatalf("authorize URL = %s", res.AuthorizeURL)
	}
	want := map[string]string{
		// The definition's list replaces the PRM's twenty scopes, https://mail.google.com/ among them.
		"scope":       gmailReadonly,
		"access_type": "offline",
		"prompt":      "consent",
		"client_id":   "agentic.apps.googleusercontent.com",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if res.Issuer != f.url("") || res.Registration != OAuthRegTenant {
		t.Errorf("result = %+v, want the metadata's issuer and the tenant's client", res)
	}

	done, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: googleCode(q, "1"), Iss: f.url("")})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if done.Issuer != f.url("") || done.Scope != gmailReadonly {
		t.Errorf("complete = %+v", done)
	}
	g := currentGrant(id)
	if g == nil || g.RefreshToken != "1//rt-consented/1" || g.Issuer != f.url("") {
		t.Fatalf("grant = %+v, want a refresh token under the metadata's issuer", g)
	}

	// A second sign-in while the grant holds a refresh token (a reconnect, a step-up) replaces
	// the grant. It must ask for consent again, or the new grant has no refresh token.
	req2 := startReq(func(r *StartRequest) { r.Scope = gmailReadonly + " https://www.googleapis.com/auth/gmail.compose" })
	_, q2 := mustStart(t, id, req2)
	if q2.Get("prompt") != "consent" || q2.Get("access_type") != "offline" {
		t.Fatalf("second sign-in: prompt=%q access_type=%q", q2.Get("prompt"), q2.Get("access_type"))
	}
	if _, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req2.State, Code: googleCode(q2, "2"), Iss: f.url("")}); err != nil {
		t.Fatalf("second complete: %v", err)
	}
	if g := currentGrant(id); g == nil || g.RefreshToken != "1//rt-consented/2" {
		t.Fatalf("after the second sign-in grant = %+v, want the new refresh token", g)
	}
}

// The tenant's confidential client (the agentic Web client of ADR 0146 decision 1), entered
// with the PRM's "/" spelling: exchange, refresh and revocation go through the CP and name the
// issuer the metadata states.
func TestGoogleWorkspaceConfidentialClientThroughTheCP(t *testing.T) {
	resetOAuthState(t)
	f := newGoogleFake(t)
	cp := newCPFake(t)
	useGoogleParamsFor(t, f.url(""))
	d := ServerDef{
		ID: "t-hosted", Name: "gmail", Origin: OriginTenant, Transport: TransportHTTP, URL: f.url("/mcp"),
		Auth: AuthOAuth, Enabled: true, Targets: Targets{Session: true}, Scopes: []string{gmailReadonly},
		OAuthClient: &secrets.MCPTenantClient{Issuer: f.url("/"), ClientID: "agentic", Confidential: true},
	}
	kept, dropped := acceptTenant([]ServerDef{d})
	if dropped != 0 {
		t.Fatalf("dropped: %+v", d)
	}
	if err := writeTenantCache(kept, 1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(tenantCachePath()) })

	req := startReq(nil)
	res, q := mustStart(t, d.ID, req)
	if res.Registration != OAuthRegTenant || q.Get("client_id") != "agentic" || q.Get("prompt") != "consent" {
		t.Fatalf("start = %+v %v", res, q)
	}
	if _, err := CompleteOAuth(context.Background(), d.ID, CompleteRequest{State: req.State, Code: "c", Iss: f.url("")}); err != nil {
		t.Fatal(err)
	}
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := AccessToken(context.Background(), d.ID); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := DisconnectOAuth(context.Background(), d.ID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if len(cp.forms) != 3 || cp.revoked != 1 || len(f.tokens) != 0 {
		t.Fatalf("CP saw %v (revoked %d), AS token requests %d", cp.paths, cp.revoked, len(f.tokens))
	}
	for i, form := range cp.forms {
		if form.Get("issuer") != f.url("") || form.Get("client_id") != "agentic" {
			t.Errorf("CP request %d (%s): issuer=%q client_id=%q", i, cp.paths[i], form.Get("issuer"), form.Get("client_id"))
		}
	}
}

// The iss of the authorization response is compared with the issuer the metadata states, byte
// for byte: the PRM's spelling with "/" is not accepted there.
func TestGoogleWorkspaceIssIsTheMetadataIssuer(t *testing.T) {
	resetOAuthState(t)
	f := newGoogleFake(t)
	id := distributeGoogle(t, f, []string{gmailReadonly})
	req := startReq(nil)
	mustStart(t, id, req)
	_, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c", Iss: f.url("/")})
	wantCode(t, err, OAuthErrIssMismatch)
	if len(f.tokens) != 0 {
		t.Error("a mismatched iss must not reach the token endpoint")
	}
}

// Without the definition's scopes the PRM's whole list is asked for, full mailbox control
// included: the case the definition's list exists for.
func TestGoogleWorkspaceWithoutDefinitionScopesAsksForThePRMList(t *testing.T) {
	resetOAuthState(t)
	f := newGoogleFake(t)
	id := distributeGoogle(t, f, nil)
	_, q := mustStart(t, id, startReq(nil))
	got := strings.Fields(q.Get("scope"))
	if len(got) != 20 || got[0] != "https://mail.google.com/" {
		t.Fatalf("scope = %q, want the PRM's twenty", q.Get("scope"))
	}
}

func TestIssuerEquivalent(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://accounts.google.com", "https://accounts.google.com", true},
		{"https://accounts.google.com/", "https://accounts.google.com", true},
		{"https://accounts.google.com", "https://accounts.google.com/", true},
		{"https://as.example.com:8443/", "https://as.example.com:8443", true},
		{"http://127.0.0.1:9/", "http://127.0.0.1:9", true},
		{"https://as.example.com/t/", "https://as.example.com/t", false},
		{"https://as.example.com/t", "https://as.example.com/t/", false},
		{"https://as.example.com//", "https://as.example.com/", false},
		{"https://as.example.com//", "https://as.example.com", false},
		{"https://AS.example.com/", "https://as.example.com", false},
		{"HTTPS://as.example.com/", "https://as.example.com", false},
		{"https://as.example.com:443/", "https://as.example.com", false},
		{"https://as.example.com?/", "https://as.example.com?", false},
		{"https://as.example.com#/", "https://as.example.com#", false},
		{"https://u@as.example.com/", "https://u@as.example.com", false},
		{"urn:as/", "urn:as", false},
		{"ftp://as.example.com/", "ftp://as.example.com", false},
		{"/", "", false},
		{"https://evil.example.com/", "https://as.example.com", false},
	}
	for _, c := range cases {
		if got := issuerEquivalent(c.a, c.b); got != c.want {
			t.Errorf("issuerEquivalent(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestIssuerAuthParams(t *testing.T) {
	for _, iss := range []string{"https://accounts.google.com", "https://accounts.google.com/"} {
		p := issuerAuthParams(iss)
		if p.Get("access_type") != "offline" || p.Get("prompt") != "consent" {
			t.Errorf("%s: params = %v", iss, p)
		}
	}
	for _, iss := range []string{"https://accounts.google.com/o", "https://accounts.google.com.evil.example", "https://as.example.com", ""} {
		if p := issuerAuthParams(iss); p != nil {
			t.Errorf("%s: params = %v, want none", iss, p)
		}
	}
}

// Only a Google authorization request carries the extra parameters.
func TestOtherIssuersGetNoExtraAuthParams(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, map[string]any{"client_id_metadata_document_supported": true})
	id := registerServer(t, f)
	_, q := mustStart(t, id, startReq(nil))
	for _, k := range []string{"access_type", "prompt"} {
		if q.Has(k) {
			t.Errorf("%s = %q on a non-Google authorization request", k, q.Get(k))
		}
	}
}

func TestValidateDefinitionScopes(t *testing.T) {
	base := ServerDef{Name: "gmail", Origin: OriginTenant, Transport: TransportHTTP, URL: "https://gmailmcp.example.com/mcp/v1",
		Auth: AuthOAuth, Scopes: []string{gmailReadonly, "https://www.googleapis.com/auth/gmail.compose"}}
	if err := Validate(base); err != nil {
		t.Fatalf("valid: %v", err)
	}
	empty := base
	empty.Scopes = []string{}
	if err := Validate(empty); err != nil {
		t.Fatalf("an empty list is no list: %v", err)
	}
	long := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		long = append(long, gmailReadonly)
	}
	// Room for " offline_access" (15 bytes) is kept unless the list already holds it.
	fit := []string{strings.Repeat("a", maxScopeLen-15)}
	over := []string{strings.Repeat("a", maxScopeLen-14)}
	fullWithOffline := []string{strings.Repeat("a", maxScopeLen-15), "offline_access"}
	for name, l := range map[string][]string{"fits with offline_access to come": fit, "full, offline_access included": fullWithOffline} {
		d := base
		d.Scopes = l
		if err := Validate(d); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	cases := map[string]struct {
		mod  func(*ServerDef)
		code string
	}{
		"user row":                   {func(d *ServerDef) { d.Origin = OriginUser }, CodeOAuthScopesUser},
		"not oauth":                  {func(d *ServerDef) { d.Auth = "" }, CodeOAuthScopes},
		"empty entry":                {func(d *ServerDef) { d.Scopes = []string{gmailReadonly, ""} }, CodeOAuthScopes},
		"two in one":                 {func(d *ServerDef) { d.Scopes = []string{"a b"} }, CodeOAuthScopes},
		"padded":                     {func(d *ServerDef) { d.Scopes = []string{" a"} }, CodeOAuthScopes},
		"quote":                      {func(d *ServerDef) { d.Scopes = []string{`a"b`} }, CodeOAuthScopes},
		"non-ASCII":                  {func(d *ServerDef) { d.Scopes = []string{"スコープ"} }, CodeOAuthScopes},
		"over the limit":             {func(d *ServerDef) { d.Scopes = long }, CodeOAuthScopes},
		"no room for offline_access": {func(d *ServerDef) { d.Scopes = over }, CodeOAuthScopes},
		"comma":                      {func(d *ServerDef) { d.Scopes = []string{"read,archive"} }, CodeOAuthScopes},
	}
	for name, c := range cases {
		d := base
		c.mod(&d)
		var ve *ValidationError
		if err := Validate(d); !errors.As(err, &ve) || ve.Code != c.code {
			t.Errorf("%s: %v, want %s", name, err, c.code)
		}
	}
}

// A definition list at the validation limit still starts a sign-in once offline_access, which
// the AS lists, is appended.
func TestDefinitionScopesAtTheLimitStillStart(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, map[string]any{"client_id_metadata_document_supported": true}) // lists offline_access
	scopes := []string{strings.Repeat("a", maxScopeLen-15)}
	d := ServerDef{
		ID: "t-hosted", Name: "hosted", Origin: OriginTenant, Transport: TransportHTTP, URL: f.url("/mcp"),
		Auth: AuthOAuth, Enabled: true, Targets: Targets{Session: true}, Scopes: scopes,
	}
	kept, dropped := acceptTenant([]ServerDef{d})
	if dropped != 0 {
		t.Fatalf("dropped: %+v", d)
	}
	if err := writeTenantCache(kept, 1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(tenantCachePath()) })
	_, q := mustStart(t, d.ID, startReq(nil))
	if got := q.Get("scope"); got != scopes[0]+" offline_access" || len(got) != maxScopeLen {
		t.Fatalf("scope has %d bytes: %q", len(got), got)
	}
}
