package main

import (
	"encoding/base64"
	"encoding/json"
	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// docs/log/61 P0. The real IdP is unreachable from CI, so discovery / token /
// userinfo are stubbed by an httptest server; what these tests pin down is the
// half we own: the provider abstraction, the signed state/session cookies, the
// per-request re-check, and — the point of the whole exercise — the fail-closed
// startup rules (a provider that doesn't declare how it justifies an email, and
// the multi-tenant Entra endpoint that would otherwise put every Microsoft
// account in front of an email allowlist).

// --- stub IdP --------------------------------------------------------------

type stubIdP struct {
	*httptest.Server
	idTokenClaims  map[string]any
	userinfoClaims map[string]any
	userinfoStatus int  // 0 => 200
	omitIDToken    bool // token response carries only an access_token
	tokenError     string
	tokenHits      int
	userinfoHits   int
}

func newStubIdP(t *testing.T, s *stubIdP) *stubIdP {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.Server = srv
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
			"userinfo_endpoint":      srv.URL + "/userinfo",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		s.tokenHits++
		if s.tokenError != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": s.tokenError})
			return
		}
		body := map[string]any{"access_token": "stub-access-token"}
		if !s.omitIDToken {
			body["id_token"] = stubIDToken(s.idTokenClaims)
		}
		writeJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		s.userinfoHits++
		if s.userinfoStatus != 0 && s.userinfoStatus != http.StatusOK {
			w.WriteHeader(s.userinfoStatus)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer stub-access-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, s.userinfoClaims)
	})
	return s
}

// stubIDToken builds an unsigned-in-practice JWS: CP reads the payload without
// verifying the signature (ADR0043 決定 9), so the signature segment is filler.
func stubIDToken(claims map[string]any) string {
	if claims == nil {
		claims = map[string]any{}
	}
	payload, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc(payload) + ".not-verified"
}

// --- config helpers --------------------------------------------------------

func oauthTestConfig(t *testing.T, ps ...auth.LoginProvider) config {
	t.Helper()
	cfg := config{
		publicBaseURL: "https://af.example.com",
		cookieSecret:  []byte("0123456789abcdef0123456789abcdef"),
		cookieSecure:  true,
		sessionTTL:    time.Hour,
		allowEmails:   emailSet(""),
		allowDomains:  domainSet("acme.co.jp"),
		mgr:           &manager{emailHeader: "X-Forwarded-Email"},
	}
	for _, p := range ps {
		if op, ok := p.(*auth.OIDCProvider); ok && op.DeployAllowed == nil {
			op.DeployAllowed = cfg.emailAllowed
		}
	}
	cfg.setProviders(ps)
	return cfg
}

func stubProvider(id string, idp *stubIdP, trust string) *auth.OIDCProvider {
	return &auth.OIDCProvider{
		ProviderID: id, Issuer: idp.URL, ClientID: "client-id", ClientSecret: "client-secret",
		Trust: trust, Scope: "openid email profile", Prompt: "select_account",
		HTTPClient: idp.Client(),
	}
}

// startLogin drives GET /oauth2/login and returns the state cookie plus the
// authorize URL the browser was redirected to.
func startLogin(t *testing.T, cfg config, query string) (*http.Cookie, *url.URL) {
	t.Helper()
	w := httptest.NewRecorder()
	cfg.handleOAuthLogin(w, httptest.NewRequest(http.MethodGet, "/oauth2/login"+query, nil))
	if w.Code != http.StatusFound {
		t.Fatalf("login: want 302 got %d (%s)", w.Code, w.Body.String())
	}
	var state *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == stateCookie && ck.Value != "" {
			state = ck
		}
	}
	if state == nil {
		t.Fatal("login: no state cookie")
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("login: bad Location %q: %v", w.Header().Get("Location"), err)
	}
	return state, u
}

// callback replays the IdP's redirect back to CP with the state cookie set.
func callback(t *testing.T, cfg config, state *http.Cookie, code, stateParam string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/oauth2/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(stateParam), nil)
	if state != nil {
		r.AddCookie(state)
	}
	w := httptest.NewRecorder()
	cfg.handleOAuthCallback(w, r)
	return w
}

func sessionCookieOf(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, ck := range w.Result().Cookies() {
		if ck.Name == sessionCookie && ck.Value != "" {
			return ck
		}
	}
	return nil
}

func loginErrorOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Code != http.StatusFound {
		t.Fatalf("want 302 got %d", w.Code)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("bad Location: %v", err)
	}
	return u.Query().Get("error")
}

// --- Google regression (受入条件 6) ----------------------------------------

// A deployment that only ever set GOOGLE_OAUTH_* must be byte-for-byte unchanged:
// same single button, same authorize URL, same parameters — no discovery request
// (the endpoints stay seeded), so restricted egress can't break login either.
func TestGoogleOnlyDeploymentIsUnchanged(t *testing.T) {
	t.Setenv("AF_OIDC_PROVIDERS", "")
	base := config{
		googleClientID: "g-client", googleClientSecret: "g-secret",
		allowDomains: domainSet("acme.co.jp"),
	}
	ps, err := buildLoginProviders(base)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(ps) != 1 || ps[0].ID() != auth.GoogleProviderID {
		t.Fatalf("providers: %#v", ps)
	}
	cfg := oauthTestConfig(t, ps...)

	_, au := startLogin(t, cfg, "?next=%2Fsessions")
	if au.Scheme+"://"+au.Host+au.Path != auth.GoogleAuthorizeURL {
		t.Fatalf("authorize endpoint = %q, want %q", au.String(), auth.GoogleAuthorizeURL)
	}
	q := au.Query()
	for k, want := range map[string]string{
		"client_id":     "g-client",
		"redirect_uri":  "https://af.example.com/oauth2/callback",
		"response_type": "code",
		"scope":         "openid email",
		"access_type":   "online",
		"prompt":        "select_account",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("authorize %s = %q, want %q", k, got, want)
		}
	}
	if q.Get("state") == "" {
		t.Error("authorize: no state")
	}
	// One button, and it still says "Google でサインイン".
	w := httptest.NewRecorder()
	cfg.handleLogin(w, httptest.NewRequest(http.MethodGet, "/login", nil))
	page := w.Body.String()
	if n := strings.Count(page, `class="gbtn"`); n != 1 {
		t.Fatalf("login page: %d buttons, want 1", n)
	}
	if !strings.Contains(page, "Google でサインイン") || !strings.Contains(page, "provider=google") {
		t.Fatalf("login page missing the Google button:\n%s", page)
	}
}

// Google's own login still runs through the generic client end to end: the
// email_verified claim gates it, and the session records provider + subject.
func TestGoogleTrustEmailVerified(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verified any
		wantErr  string
	}{
		{"verified", true, ""},
		{"verified as string", "true", ""},
		{"unverified", false, "denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idp := newStubIdP(t, &stubIdP{
				idTokenClaims:  map[string]any{"sub": "g-sub-1", "email": "yamada@acme.co.jp", "email_verified": tc.verified},
				userinfoClaims: map[string]any{"sub": "g-sub-1", "email": "yamada@acme.co.jp", "email_verified": tc.verified},
			})
			p := stubProvider(auth.GoogleProviderID, idp, auth.TrustEmailVerified)
			cfg := oauthTestConfig(t, p)
			st, au := startLogin(t, cfg, "?next=%2Fmemos")
			w := callback(t, cfg, st, "the-code", au.Query().Get("state"))
			if tc.wantErr != "" {
				if got := loginErrorOf(t, w); got != tc.wantErr {
					t.Fatalf("error = %q, want %q", got, tc.wantErr)
				}
				if sessionCookieOf(t, w) != nil {
					t.Fatal("a denied login must not issue a session")
				}
				return
			}
			if got := w.Header().Get("Location"); got != "/memos" {
				t.Fatalf("post-login redirect = %q, want /memos", got)
			}
			ck := sessionCookieOf(t, w)
			if ck == nil {
				t.Fatal("no session cookie")
			}
			payload, ok := cfg.verifyCookie(ck.Value)
			if !ok {
				t.Fatal("session cookie does not verify")
			}
			var claims sessionClaims
			if err := json.Unmarshal(payload, &claims); err != nil {
				t.Fatalf("claims: %v", err)
			}
			if claims.Email != "yamada@acme.co.jp" || claims.Prov != auth.GoogleProviderID || claims.Sub != "g-sub-1" {
				t.Fatalf("claims = %+v", claims)
			}
		})
	}
}

// --- Entra / generic OIDC ---------------------------------------------------

// Entra emits no email_verified at all and its userinfo returns no email — the
// address comes from the id_token's preferred_username, and trust=issuer is what
// makes it acceptable (docs/log/61 §61.4).
func TestEntraTrustIssuerUsesIDTokenClaims(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{
		idTokenClaims: map[string]any{
			"sub": "entra-sub-1", "tid": "TENANT-GUID", "preferred_username": "yamada@acme.co.jp",
		},
		userinfoClaims: map[string]any{"sub": "entra-sub-1"},
	})
	p := stubProvider("entra", idp, auth.TrustIssuer)
	p.AllowedTIDs = map[string]bool{"tenant-guid": true}
	cfg := oauthTestConfig(t, p)

	st, au := startLogin(t, cfg, "?provider=entra")
	if !strings.HasPrefix(au.String(), idp.URL+"/authorize") {
		t.Fatalf("authorize URL = %q", au.String())
	}
	w := callback(t, cfg, st, "code", au.Query().Get("state"))
	if ck := sessionCookieOf(t, w); ck == nil {
		t.Fatalf("no session issued (error=%q)", loginErrorOf(t, w))
	}
	if idp.tokenHits != 1 {
		t.Fatalf("token endpoint hits = %d", idp.tokenHits)
	}
}

// ★ The tenant pin is the whole reason the multi-tenant endpoint is allowed at
// all: a token from another Microsoft tenant must be refused even though its
// email would satisfy the allowlist.
func TestEntraForeignTenantIsRefused(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{
		idTokenClaims:  map[string]any{"sub": "s", "tid": "someone-elses-tenant", "preferred_username": "yamada@acme.co.jp"},
		userinfoClaims: map[string]any{"sub": "s"},
	})
	p := stubProvider("entra", idp, auth.TrustIssuer)
	p.AllowedTIDs = map[string]bool{"tenant-guid": true}
	cfg := oauthTestConfig(t, p)
	st, au := startLogin(t, cfg, "?provider=entra")
	w := callback(t, cfg, st, "code", au.Query().Get("state"))
	if got := loginErrorOf(t, w); got != "forbidden" {
		t.Fatalf("error = %q, want forbidden", got)
	}
	if sessionCookieOf(t, w) != nil {
		t.Fatal("a foreign tenant must not get a session")
	}
	// Same for an id_token with no tid at all (fail closed, not "unknown = fine").
	idp.idTokenClaims = map[string]any{"sub": "s", "preferred_username": "yamada@acme.co.jp"}
	st, au = startLogin(t, cfg, "?provider=entra")
	if got := loginErrorOf(t, callback(t, cfg, st, "code", au.Query().Get("state"))); got != "forbidden" {
		t.Fatalf("missing tid: error = %q, want forbidden", got)
	}
}

// A userinfo blip must not fail an otherwise complete sign-in: the id_token from
// the same TLS response already carries the claims we need.
func TestUserinfoFailureFallsBackToIDToken(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{
		idTokenClaims:  map[string]any{"sub": "s1", "email": "yamada@acme.co.jp", "email_verified": true},
		userinfoStatus: http.StatusInternalServerError,
	})
	cfg := oauthTestConfig(t, stubProvider("okta", idp, auth.TrustEmailVerified))
	st, au := startLogin(t, cfg, "?provider=okta")
	w := callback(t, cfg, st, "code", au.Query().Get("state"))
	if ck := sessionCookieOf(t, w); ck == nil {
		t.Fatalf("no session issued (error=%q)", loginErrorOf(t, w))
	}
}

func TestTokenEndpointErrorShowsExchangeError(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{tokenError: "invalid_grant"})
	cfg := oauthTestConfig(t, stubProvider("okta", idp, auth.TrustEmailVerified))
	st, au := startLogin(t, cfg, "?provider=okta")
	if got := loginErrorOf(t, callback(t, cfg, st, "code", au.Query().Get("state"))); got != "exchange" {
		t.Fatalf("error = %q, want exchange", got)
	}
}

// --- state / provider resolution -------------------------------------------

// The state cookie is signed, but the provider id it carries is still resolved
// against the configured set before anything branches on it (決定 8).
func TestCallbackRejectsStateForUnconfiguredProvider(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{idTokenClaims: map[string]any{"sub": "s", "email": "yamada@acme.co.jp", "email_verified": true}})
	cfg := oauthTestConfig(t, stubProvider("okta", idp, auth.TrustEmailVerified))
	// A validly signed state naming a provider this deployment doesn't have (it
	// was removed from AF_OIDC_PROVIDERS, say).
	b, _ := json.Marshal(oauthState{Nonce: "nonce123", Next: "/", Exp: time.Now().Add(time.Minute).Unix(), Prov: "gone"})
	st := &http.Cookie{Name: stateCookie, Value: cfg.signCookie(b)}
	if got := loginErrorOf(t, callback(t, cfg, st, "code", "nonce123")); got != "provider" {
		t.Fatalf("error = %q, want provider", got)
	}
	if idp.tokenHits != 0 {
		t.Fatal("an unknown provider id must not reach a token exchange")
	}
}

// A tampered / unsigned state is still rejected before anything else, and the
// nonce must match the query parameter (CSRF).
func TestCallbackRejectsBadState(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{})
	cfg := oauthTestConfig(t, stubProvider("okta", idp, auth.TrustEmailVerified))
	if got := loginErrorOf(t, callback(t, cfg, nil, "code", "n")); got != "session" {
		t.Fatalf("no cookie: error = %q, want session", got)
	}
	if got := loginErrorOf(t, callback(t, cfg, &http.Cookie{Name: stateCookie, Value: "forged.value"}, "code", "n")); got != "session" {
		t.Fatalf("forged cookie: error = %q, want session", got)
	}
	st, _ := startLogin(t, cfg, "?provider=okta")
	if got := loginErrorOf(t, callback(t, cfg, st, "code", "wrong-nonce")); got != "session" {
		t.Fatalf("nonce mismatch: error = %q, want session", got)
	}
	if idp.tokenHits != 0 {
		t.Fatal("a bad state must not reach a token exchange")
	}
}

// A state cookie minted by the previous version carries no provider id; it must
// resolve to the deployment's first button rather than 500 or dead-end.
func TestCallbackAcceptsPreP0StateCookie(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{
		idTokenClaims:  map[string]any{"sub": "g1", "email": "yamada@acme.co.jp", "email_verified": true},
		userinfoClaims: map[string]any{"sub": "g1", "email": "yamada@acme.co.jp", "email_verified": true},
	})
	cfg := oauthTestConfig(t, stubProvider(auth.GoogleProviderID, idp, auth.TrustEmailVerified))
	b, _ := json.Marshal(map[string]any{"n": "nonce123", "x": "/", "e": time.Now().Add(time.Minute).Unix()})
	st := &http.Cookie{Name: stateCookie, Value: cfg.signCookie(b)}
	w := callback(t, cfg, st, "code", "nonce123")
	if ck := sessionCookieOf(t, w); ck == nil {
		t.Fatalf("no session issued (error=%q)", loginErrorOf(t, w))
	}
}

func TestSanitizeNextRejectsOffSiteRedirects(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/sessions", "/sessions"},
		{"/a?b=c#d", "/a?b=c#d"},
		{"", "/"},
		{"//evil.com", "/"},
		{`/\evil.com`, "/"},
		{"https://evil.com", "/"},
		{"evil.com", "/"},
	} {
		if got := sanitizeNext(tc.in); got != tc.want {
			t.Errorf("sanitizeNext(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- session claims / authGate ---------------------------------------------

// A session cookie written by the previous version has neither prov nor sub.
// It must keep working (no forced logout on upgrade) — read as the Google
// session it was — and it must still be re-checked against the allowlist.
func TestLegacySessionCookieKeepsWorkingAndIsStillRechecked(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{})
	cfg := oauthTestConfig(t, stubProvider(auth.GoogleProviderID, idp, auth.TrustEmailVerified))
	gate := cfg.authGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Header.Get(cfg.mgr.emailHeader)))
	}))
	legacy := func(email string) *http.Cookie {
		b, _ := json.Marshal(map[string]any{"email": email, "exp": time.Now().Add(time.Hour).Unix()})
		return &http.Cookie{Name: sessionCookie, Value: cfg.signCookie(b)}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/tenants", nil)
	r.AddCookie(legacy("yamada@acme.co.jp"))
	w := httptest.NewRecorder()
	gate.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "yamada@acme.co.jp" {
		t.Fatalf("legacy session: %d %q", w.Code, w.Body.String())
	}
	// Offboarded (no longer on the allowlist) => 403 on the very next request.
	r = httptest.NewRequest(http.MethodGet, "/api/tenants", nil)
	r.AddCookie(legacy("gone@other.example"))
	w = httptest.NewRecorder()
	gate.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("offboarded session: want 403 got %d", w.Code)
	}
}

// The per-request re-check reads the live allowlist file, so deleting a line is
// still the offboarding path (docs/log/61 受入条件 5) — no restart, no TTL wait.
func TestAuthGateRereadsAllowlistFileEveryRequest(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{})
	file := filepath.Join(t.TempDir(), "allowed-emails.txt")
	if err := os.WriteFile(file, []byte("# people\nyamada@acme.co.jp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := oauthTestConfig(t, stubProvider(auth.GoogleProviderID, idp, auth.TrustEmailVerified))
	cfg.allowDomains = domainSet("") // only the file decides
	cfg.allowEmailsFile = file
	// The provider captured the pre-file config's method value, so rebind it.
	cfg.providers[0].(*auth.OIDCProvider).DeployAllowed = cfg.emailAllowed
	gate := cfg.authGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	call := func() int {
		b, _ := json.Marshal(sessionClaims{Email: "yamada@acme.co.jp", Exp: time.Now().Add(time.Hour).Unix(), Prov: auth.GoogleProviderID, Sub: "s"})
		r := httptest.NewRequest(http.MethodGet, "/api/tenants", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cfg.signCookie(b)})
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		return w.Code
	}
	if got := call(); got != http.StatusOK {
		t.Fatalf("allowed: %d", got)
	}
	if err := os.WriteFile(file, []byte("# emptied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := call(); got != http.StatusForbidden {
		t.Fatalf("after removal: want 403 got %d", got)
	}
}

// A provider-specific allowlist replaces the deployment-wide one for that
// provider only (docs/log/61 §61.8).
func TestProviderAllowlistOverridesTheDeploymentOne(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{})
	p := stubProvider("entra", idp, auth.TrustIssuer)
	p.AllowDomains = domainSet("sales.acme.co.jp")
	oauthTestConfig(t, p) // binds the deployment-wide fallback list (@acme.co.jp)
	for _, tc := range []struct {
		email string
		want  bool
	}{
		{"yamada@sales.acme.co.jp", true},
		{"yamada@acme.co.jp", false}, // allowed deployment-wide, not for this provider
	} {
		got, err := p.Allowed(t.Context(), auth.Principal{Provider: "entra", Email: tc.email})
		if err != nil || got != tc.want {
			t.Errorf("Allowed(%s) = %v (err %v), want %v", tc.email, got, err, tc.want)
		}
	}
}

// --- login page -------------------------------------------------------------

func TestLoginPageRendersOneButtonPerProvider(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{})
	entra := stubProvider("entra", idp, auth.TrustIssuer)
	entra.LabelJA, entra.LabelEN = "Microsoft でサインイン", "Sign in with Microsoft"
	okta := stubProvider("okta", idp, auth.TrustEmailVerified) // no labels => generated
	cfg := oauthTestConfig(t, stubProvider(auth.GoogleProviderID, idp, auth.TrustEmailVerified), entra, okta)
	cfg.providers[0].(*auth.OIDCProvider).LabelJA = auth.LoginText["ja"].Signin
	cfg.providers[0].(*auth.OIDCProvider).LabelEN = auth.LoginText["en"].Signin

	for _, tc := range []struct{ lang, accept, want string }{
		{"ja", "ja,en;q=0.8", "Microsoft でサインイン"},
		{"en", "en-US,en;q=0.9", "Sign in with Microsoft"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/login?next=%2Fsessions", nil)
		r.Header.Set("Accept-Language", tc.accept)
		w := httptest.NewRecorder()
		cfg.handleLogin(w, r)
		page := w.Body.String()
		if n := strings.Count(page, `class="gbtn"`); n != 3 {
			t.Fatalf("%s: %d buttons, want 3", tc.lang, n)
		}
		if !strings.Contains(page, tc.want) {
			t.Errorf("%s: missing %q", tc.lang, tc.want)
		}
		for _, id := range []string{"google", "entra", "okta"} {
			if !strings.Contains(page, "provider="+id) {
				t.Errorf("%s: no button for %q", tc.lang, id)
			}
		}
		// next= must survive onto every button, and stay escaped as an attribute.
		if n := strings.Count(page, "next=%2Fsessions"); n != 3 {
			t.Errorf("%s: next= carried on %d buttons, want 3", tc.lang, n)
		}
		if strings.Contains(page, `href="/oauth2/login?next=%2Fsessions&provider`) {
			t.Errorf("%s: unescaped & in an href attribute", tc.lang)
		}
	}
	// Generated fallback label for a provider that declared none.
	if got := okta.Label("en"); got != "Sign in with Okta" {
		t.Errorf("generated en label = %q", got)
	}
	if got := okta.Label("ja"); got != "Okta でサインイン" {
		t.Errorf("generated ja label = %q", got)
	}
}

// --- startup validation (fail closed) ---------------------------------------

func setOIDCEnv(t *testing.T, id string, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv("AF_OIDC_"+strings.ToUpper(id)+"_"+k, v)
	}
}

// ★ 決定 7: the multi-tenant Entra endpoints are fatal without a tenant
// allowlist — not "disabled with a warning". Allowing them would put every
// Microsoft account in the world in front of an email allowlist that personal
// accounts can spoof.
func TestMultiTenantIssuerWithoutTIDsIsFatal(t *testing.T) {
	for _, issuer := range []string{
		"https://login.microsoftonline.com/common/v2.0",
		"https://login.microsoftonline.com/organizations/v2.0",
		"https://login.microsoftonline.com/consumers/v2.0",
	} {
		t.Run(issuer, func(t *testing.T) {
			t.Setenv("AF_OIDC_PROVIDERS", "entra")
			setOIDCEnv(t, "entra", map[string]string{
				"ISSUER": issuer, "CLIENT_ID": "c", "CLIENT_SECRET": "s", "TRUST": "issuer", "ALLOWED_TIDS": "",
			})
			if _, err := buildLoginProviders(config{}); err == nil {
				t.Fatal("want a fatal error for a multi-tenant issuer with no ALLOWED_TIDS")
			}
			// With the tenant allowlist set, the same issuer is accepted.
			t.Setenv("AF_OIDC_ENTRA_ALLOWED_TIDS", "11111111-2222-3333-4444-555555555555")
			ps, err := buildLoginProviders(config{})
			if err != nil || len(ps) != 1 {
				t.Fatalf("with ALLOWED_TIDS: %d providers, err %v", len(ps), err)
			}
		})
	}
}

// 決定 11: one misconfigured IdP disables itself; it never takes the deployment
// down or, worse, comes up with an undeclared trust rule.
func TestIncompleteProviderIsDisabledNotFatal(t *testing.T) {
	full := map[string]string{
		"ISSUER": "https://idp.example.com", "CLIENT_ID": "c", "CLIENT_SECRET": "s", "TRUST": "issuer",
	}
	for _, tc := range []struct{ name, key, value string }{
		{"no issuer", "ISSUER", ""},
		{"http issuer", "ISSUER", "http://idp.example.com"},
		{"no client id", "CLIENT_ID", ""},
		{"no client secret", "CLIENT_SECRET", ""},
		{"no trust declared", "TRUST", ""},
		{"unknown trust", "TRUST", "whatever"},
		{"api trust is the GitHub rule (P2)", "TRUST", auth.TrustAPI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AF_OIDC_PROVIDERS", "okta")
			env := map[string]string{}
			for k, v := range full {
				env[k] = v
			}
			env[tc.key] = tc.value
			setOIDCEnv(t, "okta", env)
			// Google stays configured, so the deployment still has a working door.
			ps, err := buildLoginProviders(config{googleClientID: "g", googleClientSecret: "s"})
			if err != nil {
				t.Fatalf("must not be fatal: %v", err)
			}
			if len(ps) != 1 || ps[0].ID() != auth.GoogleProviderID {
				t.Fatalf("providers = %v, want google only", providerIDs(ps))
			}
		})
	}
}

// The complete, ordered set: Google first, then AF_OIDC_PROVIDERS as listed.
func TestBuildLoginProvidersOrderAndDefaults(t *testing.T) {
	t.Setenv("AF_OIDC_PROVIDERS", " Entra , keycloak ")
	setOIDCEnv(t, "entra", map[string]string{
		"ISSUER":    "https://login.microsoftonline.com/tenant-guid/v2.0",
		"CLIENT_ID": "c", "CLIENT_SECRET": "s", "TRUST": "issuer",
		"LABEL_JA": "Microsoft でサインイン", "LABEL_EN": "Sign in with Microsoft",
		"ALLOWED_DOMAINS": "acme.co.jp",
	})
	setOIDCEnv(t, "keycloak", map[string]string{
		"ISSUER":    "https://kc.example.com/realms/acme",
		"CLIENT_ID": "c", "CLIENT_SECRET": "s", "TRUST": "email_verified",
	})
	ps, err := buildLoginProviders(config{googleClientID: "g", googleClientSecret: "gs"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := strings.Join(providerIDs(ps), ","); got != "google,entra,keycloak" {
		t.Fatalf("providers = %s", got)
	}
	entra := ps[1].(*auth.OIDCProvider)
	if entra.Label("ja") != "Microsoft でサインイン" || entra.Label("en") != "Sign in with Microsoft" {
		t.Errorf("entra labels: %q / %q", entra.Label("ja"), entra.Label("en"))
	}
	if !entra.HasOwnAllowlist() || !auth.AnyProviderAllowlist(ps) {
		t.Error("entra should carry its own allowlist")
	}
	kc := ps[2].(*auth.OIDCProvider)
	if kc.Scope != "openid email profile" || kc.Prompt != "select_account" {
		t.Errorf("keycloak defaults: scope=%q prompt=%q", kc.Scope, kc.Prompt)
	}
	// Google keeps the exact scope/endpoints it has always used.
	g := ps[0].(*auth.OIDCProvider)
	if g.Scope != "openid email" || g.CachedEndpoints().Token != auth.GoogleTokenURL {
		t.Errorf("google: scope=%q token=%q", g.Scope, g.CachedEndpoints().Token)
	}
}

// Duplicate and malformed ids are dropped rather than shadowing a real provider.
func TestBuildLoginProvidersRejectsBadIDs(t *testing.T) {
	t.Setenv("AF_OIDC_PROVIDERS", "google,okta,okta,-bad,UP@PER")
	setOIDCEnv(t, "google", map[string]string{
		"ISSUER": "https://accounts.google.com", "CLIENT_ID": "c", "CLIENT_SECRET": "s", "TRUST": "email_verified",
	})
	setOIDCEnv(t, "okta", map[string]string{
		"ISSUER": "https://okta.example.com", "CLIENT_ID": "c", "CLIENT_SECRET": "s", "TRUST": "email_verified",
	})
	ps, err := buildLoginProviders(config{googleClientID: "g", googleClientSecret: "gs"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := strings.Join(providerIDs(ps), ","); got != "google,okta" {
		t.Fatalf("providers = %s", got)
	}
	// The google slot is still the historical one, not the AF_OIDC_GOOGLE_* copy.
	if ps[0].(*auth.OIDCProvider).ClientID != "g" {
		t.Fatal("AF_OIDC_GOOGLE_* must not shadow GOOGLE_OAUTH_CLIENT_ID")
	}
}

// Half-configured Google is dropped (not run with an empty secret), and with no
// provider at all the deployment is unconfigured — main() turns that into the
// startup fatal.
func TestGoogleNeedsBothHalvesAndZeroProvidersIsUnconfigured(t *testing.T) {
	t.Setenv("AF_OIDC_PROVIDERS", "")
	ps, err := buildLoginProviders(config{googleClientID: "g"})
	if err != nil || len(ps) != 0 {
		t.Fatalf("half-configured google: %d providers, err %v", len(ps), err)
	}
	cfg := config{publicBaseURL: "https://af.example.com", cookieSecret: []byte("0123456789abcdef")}
	cfg.setProviders(ps)
	if cfg.oauthConfigured() {
		t.Fatal("no providers => AUTH=oauth must not be considered configured")
	}
}

func providerIDs(ps []auth.LoginProvider) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID())
	}
	return out
}
