package mcpsrv

// The confidential-client token bridge (ADR 0119 decision 9). Pinned here:
//   - the secret is added here and goes only to the token endpoint the issuer's own metadata names;
//   - the tenant comes from the membership: another tenant's row is not usable, nor a public one;
//   - only the two grants are relayed, for the row's own resource;
//   - the secret never comes back out: not from the admin list, not from the distribution;
//   - a masked secret survives an edit only while the issuer and client id stay the same.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

type asStub struct {
	srv       *httptest.Server
	mu        sync.Mutex
	issuer    string // what the metadata says; defaults to the server's own URL
	methods   []string
	revMeth   []string // revocation_endpoint_auth_methods_supported
	postOnly  bool     // the client is registered for client_secret_post: basic gets invalid_client
	forms     []url.Values
	basics    []string
	revokes   []url.Values
	revBasics []string
	tokenCode int
	tokenBody string
}

func newASStub(t *testing.T) *asStub {
	s := &asStub{tokenCode: 200, tokenBody: `{"access_token":"at","token_type":"Bearer","expires_in":3600,"refresh_token":"rt"}`}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			iss := s.issuer
			if iss == "" {
				iss = s.srv.URL
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": iss, "token_endpoint": s.srv.URL + "/token", "revocation_endpoint": s.srv.URL + "/revoke",
				"token_endpoint_auth_methods_supported": s.methods, "revocation_endpoint_auth_methods_supported": s.revMeth,
			})
		case "/token":
			_ = r.ParseForm()
			u, p, _ := r.BasicAuth()
			s.forms = append(s.forms, r.PostForm)
			s.basics = append(s.basics, u+":"+p)
			if s.postOnly && r.PostForm.Get("client_secret") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
				return
			}
			w.WriteHeader(s.tokenCode)
			_, _ = w.Write([]byte(s.tokenBody))
		case "/revoke":
			_ = r.ParseForm()
			u, p, _ := r.BasicAuth()
			s.revokes = append(s.revokes, r.PostForm)
			s.revBasics = append(s.revBasics, u+":"+p)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	old := bridgeHTTP
	bridgeHTTP = func() *http.Client {
		c := s.srv.Client()
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return c
	}
	t.Cleanup(func() { bridgeHTTP = old })
	bridgeMetaMu.Lock()
	bridgeMetaCache = map[string]asMetaEntry{}
	bridgeMetaMu.Unlock()
	return s
}

const bridgeResource = "https://m.example.com/mcp"

// bridgeRow stores an OAuth row for tenant-a with a confidential client registered at the stub.
func bridgeRow(t *testing.T, a ServerAPI, ctx context.Context, issuer, secret string) store.MCPServerRow {
	t.Helper()
	row := store.MCPServerRow{
		ID: "s1", TenantID: "tenant-a", Name: "hosted", Transport: "http", URL: bridgeResource, Targets: "session",
		Enabled: true, Auth: mcpAuthOAuth, CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
	}
	b := mcpServerBody{Auth: mcpAuthOAuth, OAuthClient: &mcpOAuthClientBody{Issuer: issuer, ClientID: "corp", ClientSecret: secret}}
	if err := a.resolveOAuthClient(ctx, "tenant-a", b, nil, &row); err != nil {
		t.Fatal(err)
	}
	if err := a.store.CreateMCPServer(ctx, row); err != nil {
		t.Fatal(err)
	}
	return row
}

// bridgeCall names the stored row's client as the grant's, as the Agent does, unless the form
// already names one.
func bridgeCall(a ServerAPI, op, tenant string, in url.Values) *httptest.ResponseRecorder {
	form := url.Values{}
	for k, v := range in {
		form[k] = v
	}
	if !form.Has("issuer") {
		if row, ok, _ := a.store.GetMCPServer(context.Background(), "tenant-a", "s1"); ok {
			form.Set("issuer", row.OAuthIssuer)
			form.Set("client_id", row.OAuthClientID)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/internal/mcp-servers/s1/oauth/"+op, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("id", "s1")
	w := httptest.NewRecorder()
	mv := store.MembershipView{MembershipID: "m1", TenantID: tenant}
	if op == "token" {
		a.OAuthToken(w, r, mv)
	} else {
		a.OAuthRevoke(w, r, mv)
	}
	return w
}

func codeForm() url.Values {
	return url.Values{
		"grant_type": {"authorization_code"}, "code": {"the-code"}, "code_verifier": {"v"},
		"redirect_uri": {"https://af.example.com/oauth2/mcp/callback"}, "resource": {bridgeResource},
	}
}

func TestBridgeExchangesWithTheTenantSecret(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	as := newASStub(t)
	bridgeRow(t, a, ctx, as.srv.URL, "s3cret")
	w := bridgeCall(a, "token", "tenant-a", codeForm())
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"access_token":"at"`) || !strings.Contains(w.Body.String(), `"refresh_token":"rt"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(as.forms) != 1 || as.basics[0] != "corp:s3cret" {
		t.Fatalf("AS saw %v / %v", as.forms, as.basics)
	}
	f := as.forms[0]
	if f.Get("code") != "the-code" || f.Get("code_verifier") != "v" || f.Get("resource") != bridgeResource || f.Has("client_secret") {
		t.Errorf("AS form %v", f)
	}

	// client_secret_post when that is all the AS lists.
	as.methods = []string{"client_secret_post"}
	bridgeMetaCache = map[string]asMetaEntry{}
	w = bridgeCall(a, "token", "tenant-a", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"rt"}, "resource": {bridgeResource}})
	if w.Code != 200 || as.forms[1].Get("client_secret") != "s3cret" || as.forms[1].Get("refresh_token") != "rt" || as.basics[1] != ":" {
		t.Fatalf("%d %s; AS form %v basic %q", w.Code, w.Body, as.forms[1], as.basics[1])
	}
}

func TestBridgeRefusesWhatItMustNotRelay(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	as := newASStub(t)
	bridgeRow(t, a, ctx, as.srv.URL, "s3cret")
	other := codeForm()
	other.Set("resource", "https://evil.example.com/mcp")
	creds := codeForm()
	creds.Set("grant_type", "client_credentials")
	noVerifier := codeForm()
	noVerifier.Del("code_verifier")
	cases := map[string]struct {
		tenant string
		form   url.Values
		code   string
	}{
		"another tenant":   {"tenant-b", codeForm(), "unauthorized_client"},
		"another resource": {"tenant-a", other, "invalid_target"},
		"other grant":      {"tenant-a", creds, "unsupported_grant_type"},
		"no verifier":      {"tenant-a", noVerifier, "invalid_request"},
	}
	for name, c := range cases {
		w := bridgeCall(a, "token", c.tenant, c.form)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"`+c.code+`"`) {
			t.Errorf("%s: %d %s, want %s", name, w.Code, w.Body, c.code)
		}
	}
	if len(as.forms) != 0 {
		t.Fatalf("the AS was called %d time(s)", len(as.forms))
	}
}

func TestBridgeNeedsAConfidentialClient(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	as := newASStub(t)
	bridgeRow(t, a, ctx, as.srv.URL, "") // public: nothing to add here
	if w := bridgeCall(a, "token", "tenant-a", codeForm()); !strings.Contains(w.Body.String(), "unauthorized_client") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestBridgeTrustsOnlyMetadataForTheSameIssuer(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	as := newASStub(t)
	as.issuer = "https://someone-else.example.com"
	bridgeRow(t, a, ctx, as.srv.URL, "s3cret")
	if w := bridgeCall(a, "token", "tenant-a", codeForm()); w.Code != http.StatusBadGateway {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(as.forms) != 0 {
		t.Fatal("the secret was sent to a token endpoint of metadata naming another issuer")
	}
}

func TestBridgePassesOnlyAnErrorCode(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	as := newASStub(t)
	bridgeRow(t, a, ctx, as.srv.URL, "s3cret")
	as.tokenCode, as.tokenBody = 400, `{"error":"invalid_grant","error_description":"secret detail"}`
	w := bridgeCall(a, "token", "tenant-a", codeForm())
	if w.Code != 400 || strings.TrimSpace(w.Body.String()) != `{"error":"invalid_grant"}` {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	as.tokenCode, as.tokenBody = 500, `{"error":"<b>Oops</b>"}`
	if w := bridgeCall(a, "token", "tenant-a", codeForm()); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "server_error") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// Well-formed but not a token endpoint code: the AS's own words do not pass.
	as.tokenCode, as.tokenBody = 400, `{"error":"client_secret_s_cret"}`
	if w := bridgeCall(a, "token", "tenant-a", codeForm()); strings.TrimSpace(w.Body.String()) != `{"error":"server_error"}` {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestBridgeRevokes(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	as := newASStub(t)
	bridgeRow(t, a, ctx, as.srv.URL, "s3cret")
	w := bridgeCall(a, "revoke", "tenant-a", url.Values{"token": {"rt"}, "token_type_hint": {"refresh_token"}})
	if w.Code != 200 || len(as.revokes) != 1 || as.revokes[0].Get("token") != "rt" {
		t.Fatalf("%d %s; revokes %v", w.Code, w.Body, as.revokes)
	}
}

func TestBridgeDialsPublicAddressesOnly(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:443", "[::]:443", "224.0.0.1:443", "127.0.0.1:443", "10.0.0.5:443", "169.254.169.254:443", "[::1]:443", "100.64.1.1:443", "192.168.1.1:443"} {
		if bridgePublicDial("tcp", addr, syscall.RawConn(nil)) == nil {
			t.Errorf("%s was allowed", addr)
		}
	}
	if err := bridgePublicDial("tcp", "8.8.8.8:443", syscall.RawConn(nil)); err != nil {
		t.Errorf("a public address was refused: %v", err)
	}
}

// The secret is write-only: masked in the admin list, absent from the distribution, kept by a
// masked round trip, and dropped when the client it belongs to changes.
func TestAdminOAuthClientSecretIsWriteOnly(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	tn := store.Tenant{ID: "tenant-a", Slug: "a"}
	cp := a.cp.(testCP)
	cp.admin = &tn
	a = NewServerAPI(cp)
	send := func(method, id string, c *mcpOAuthClientBody) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(mcpServerBody{TenantSlug: "a", Name: "hosted", URL: bridgeResource, Auth: mcpAuthOAuth,
			OAuthClient: c, Targets: mcpTargets{Session: true}, Enabled: true})
		r := httptest.NewRequest(method, "/api/admin/mcp-servers", strings.NewReader(string(raw)))
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		a.AdminUpsert(w, r)
		return w
	}
	put := func(method, id string, c *mcpOAuthClientBody) mcpServerBody {
		t.Helper()
		w := send(method, id, c)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "s3cret") {
			t.Fatalf("the secret came back: %s", w.Body)
		}
		var out mcpServerBody
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	// A masked secret with nothing stored behind it is a client bug: refused, not stored as "***".
	if w := send(http.MethodPost, "", &mcpOAuthClientBody{Issuer: "https://as.example.com", ClientID: "corp", ClientSecret: MaskedValue}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), codeMCPOAuthReenter) {
		t.Fatalf("masked secret on a new row: %d %s", w.Code, w.Body)
	}
	created := put(http.MethodPost, "", &mcpOAuthClientBody{Issuer: "https://as.example.com", ClientID: "corp", ClientSecret: "s3cret"})
	if created.OAuthClient == nil || created.OAuthClient.ClientSecret != MaskedValue {
		t.Fatalf("created: %+v", created.OAuthClient)
	}
	secretOf := func() string {
		row, _, _ := a.store.GetMCPServer(ctx, tn.ID, created.ID)
		if row.OAuthSecretEnc == "" {
			return ""
		}
		h, err := a.openHeaders(ctx, row.OAuthSecretEnc, row.OAuthKeyRef)
		if err != nil {
			t.Fatal(err)
		}
		return h[oauthSecretKey]
	}
	if secretOf() != "s3cret" {
		t.Fatal("the secret was not stored")
	}
	if row, _, _ := a.store.GetMCPServer(ctx, tn.ID, created.ID); strings.Contains(row.OAuthSecretEnc, "s3cret") {
		t.Fatal("the secret is stored unsealed")
	}

	put(http.MethodPut, created.ID, &mcpOAuthClientBody{Issuer: "https://as.example.com", ClientID: "corp", ClientSecret: MaskedValue})
	if secretOf() != "s3cret" {
		t.Fatal("a masked round trip lost the secret")
	}

	rec := httptest.NewRecorder()
	a.Distribute(rec, httptest.NewRequest(http.MethodGet, "/internal/mcp-servers", nil), store.MembershipView{TenantID: tn.ID})
	if strings.Contains(rec.Body.String(), "s3cret") || !strings.Contains(rec.Body.String(), `"oauthClient":{"issuer":"https://as.example.com","clientId":"corp","confidential":true}`) {
		t.Fatalf("distribution: %s", rec.Body)
	}

	// Another issuer or client id with the secret still masked: refused, so the secret neither
	// follows the client to another AS nor is dropped without the admin knowing.
	for _, c := range []*mcpOAuthClientBody{
		{Issuer: "https://attacker.example.com", ClientID: "corp", ClientSecret: MaskedValue},
		{Issuer: "https://as.example.com", ClientID: "other", ClientSecret: MaskedValue},
	} {
		if w := send(http.MethodPut, created.ID, c); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), codeMCPOAuthReenter) {
			t.Fatalf("masked secret with %+v: %d %s", c, w.Code, w.Body)
		}
	}
	if row, _, _ := a.store.GetMCPServer(ctx, tn.ID, created.ID); row.OAuthIssuer != "https://as.example.com" || secretOf() != "s3cret" {
		t.Fatalf("a refused edit changed the row: %+v", row)
	}
	put(http.MethodPut, created.ID, &mcpOAuthClientBody{Issuer: "https://attacker.example.com", ClientID: "corp", ClientSecret: ""})
	if secretOf() != "" {
		t.Fatal("an explicitly emptied secret was kept")
	}
	put(http.MethodPut, created.ID, nil)
	if row, _, _ := a.store.GetMCPServer(ctx, tn.ID, created.ID); row.OAuthClientID != "" || row.OAuthIssuer != "" {
		t.Fatalf("removing the client left %+v", row)
	}
}

func TestValidateOAuthClient(t *testing.T) {
	ok := mcpServerBody{Name: "x", URL: "https://x/y", Auth: mcpAuthOAuth,
		OAuthClient: &mcpOAuthClientBody{Issuer: "https://as.example.com", ClientID: "corp"}}
	if aerr := validateMCPBody(ok); aerr != nil {
		t.Fatalf("%+v", aerr)
	}
	for name, mod := range map[string]func(*mcpServerBody){
		"not oauth":    func(b *mcpServerBody) { b.Auth = "" },
		"http issuer":  func(b *mcpServerBody) { b.OAuthClient.Issuer = "http://as.example.com" },
		"no client id": func(b *mcpServerBody) { b.OAuthClient.ClientID = " " },
		"secret ctrl":  func(b *mcpServerBody) { b.OAuthClient.ClientSecret = "a\nb" },
		"issuer query": func(b *mcpServerBody) { b.OAuthClient.Issuer = "https://as.example.com/?a=b" },
	} {
		b := ok
		c := *ok.OAuthClient
		b.OAuthClient = &c
		mod(&b)
		if aerr := validateMCPBody(b); aerr == nil || aerr.Code != codeMCPOAuthClient {
			t.Errorf("%s: %+v", name, aerr)
		}
	}
}

// The admin's issuer may differ from the metadata's by a trailing "/" only; a development Console
// on loopback http may be the redirect URI.
func TestBridgeToleratesTrailingSlashAndLoopbackRedirect(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	as := newASStub(t)
	bridgeRow(t, a, ctx, as.srv.URL+"/", "s3cret")
	f := codeForm()
	f.Set("redirect_uri", "http://127.0.0.1:5173/oauth2/mcp/callback")
	if w := bridgeCall(a, "token", "tenant-a", f); w.Code != 200 || len(as.forms) != 1 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	f.Set("redirect_uri", "http://af.example.com/oauth2/mcp/callback")
	if w := bridgeCall(a, "token", "tenant-a", f); w.Code != http.StatusBadRequest {
		t.Fatalf("a non-loopback http redirect URI was relayed: %d %s", w.Code, w.Body)
	}
}

// A grant made with a client the admin has since replaced is refused before anything is sent:
// its tokens belong to the old client's AS, not the new one's.
func TestBridgeRefusesAGrantOfAReplacedClient(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	as := newASStub(t)
	bridgeRow(t, a, ctx, as.srv.URL, "s3cret")
	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"old-rt"}, "resource": {bridgeResource}}
	cases := map[string]url.Values{
		"another issuer":    {"issuer": {"https://old-as.example.com"}, "client_id": {"corp"}},
		"another client id": {"issuer": {as.srv.URL}, "client_id": {"old-corp"}},
		"no client named":   {"issuer": {""}},
	}
	for name, grant := range cases {
		for _, op := range []string{"token", "revoke"} {
			f := url.Values{"token": {"old-rt"}}
			if op == "token" {
				f = url.Values{}
				for k, v := range refresh {
					f[k] = v
				}
			}
			for k, v := range grant {
				f[k] = v
			}
			if w := bridgeCall(a, op, "tenant-a", f); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unauthorized_client") {
				t.Errorf("%s %s: %d %s", name, op, w.Code, w.Body)
			}
		}
	}
	if len(as.forms) != 0 || len(as.revokes) != 0 {
		t.Fatalf("the AS was sent %d token and %d revocation request(s)", len(as.forms), len(as.revokes))
	}
}

// The metadata lists what the AS supports, not what the client was registered with: with both
// listed, a client registered for client_secret_post still signs in.
func TestBridgeRetriesWithPostForAPostRegisteredClient(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	as := newASStub(t)
	as.methods, as.postOnly = []string{"client_secret_basic", "client_secret_post"}, true
	bridgeRow(t, a, ctx, as.srv.URL, "s3cret")
	if w := bridgeCall(a, "token", "tenant-a", codeForm()); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(as.forms) != 2 || as.basics[0] != "corp:s3cret" || as.forms[1].Get("client_secret") != "s3cret" || as.basics[1] != ":" {
		t.Fatalf("AS saw %v / %v", as.forms, as.basics)
	}
	// A refusal that is not about the client is not retried.
	as.postOnly, as.forms, as.basics = false, nil, nil
	as.tokenCode, as.tokenBody = 400, `{"error":"invalid_grant"}`
	if w := bridgeCall(a, "token", "tenant-a", codeForm()); len(as.forms) != 1 || !strings.Contains(w.Body.String(), "invalid_grant") {
		t.Fatalf("%d %s after %d request(s)", w.Code, w.Body, len(as.forms))
	}
}

// Revocation authenticates with the revocation endpoint's own methods (RFC 8414 §2).
func TestBridgeRevokeUsesItsOwnAuthMethods(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	as := newASStub(t)
	as.methods, as.revMeth = []string{"client_secret_basic"}, []string{"client_secret_post"}
	bridgeRow(t, a, ctx, as.srv.URL, "s3cret")
	w := bridgeCall(a, "revoke", "tenant-a", url.Values{"token": {"rt"}})
	if w.Code != 200 || len(as.revokes) != 1 || as.revokes[0].Get("client_secret") != "s3cret" || as.revBasics[0] != ":" {
		t.Fatalf("%d %s; revokes %v basic %v", w.Code, w.Body, as.revokes, as.revBasics)
	}
	if as.revokes[0].Has("issuer") {
		t.Errorf("the grant's issuer was forwarded to the AS: %v", as.revokes[0])
	}
}
