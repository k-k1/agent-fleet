package main

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// docs/log/61 P2 — the GitHub adapter. GitHub is unreachable from CI, so the REST API
// is stubbed; what these tests pin down is every trap §61.7 lists (the Accept
// header on the token endpoint, primary&&verified as the only usable email, the
// numeric id as the subject, the org 403 that means "the app was never approved")
// plus the authorization loop that only this provider has: a per-subject cache
// with a TTL, a grace window for API outages, and — after a restart wiped both the
// cache and the access token — a denial that asks for a fresh sign-in instead of
// telling the person they are not allowed.

// --- stub GitHub ------------------------------------------------------------

type stubGitHub struct {
	*httptest.Server
	userID      int64
	emails      []map[string]any
	orgStatus   map[string]int    // org -> HTTP status (default 200)
	orgState    map[string]string // org -> membership state (default "active")
	tokenHits   int
	memberHits  int
	apiDown     bool // every API call answers 502
	ignoreAccep bool // reply to the token endpoint in form encoding regardless of Accept
}

func newStubGitHub(t *testing.T, s *stubGitHub) *stubGitHub {
	t.Helper()
	if s.userID == 0 {
		s.userID = 4242
	}
	if s.emails == nil {
		s.emails = []map[string]any{
			{"email": "noreply@users.github.com", "primary": false, "verified": true},
			{"email": "yamada@acme.co.jp", "primary": true, "verified": true},
		}
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.Server = srv

	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		s.tokenHits++
		// The real GitHub answers in application/x-www-form-urlencoded unless the
		// caller asks for JSON. Reproduce that exactly, so forgetting the header
		// fails the test rather than silently yielding an empty token (§61.7).
		if s.ignoreAccep || !strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
			_, _ = w.Write([]byte("access_token=gho_stub&scope=read%3Aorg%2Cuser%3Aemail&token_type=bearer"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "gho_stub", "token_type": "bearer"})
	})
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if s.apiDown {
			w.WriteHeader(http.StatusBadGateway)
			return false
		}
		if r.Header.Get("Authorization") != "Bearer gho_stub" || r.Header.Get("User-Agent") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		// `email` is deliberately a decoy: /user's address has no verified flag and
		// must never be the one that reaches the allowlist.
		writeJSON(w, http.StatusOK, map[string]any{"id": s.userID, "login": "yamada", "email": "decoy@evil.example"})
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, s.emails)
	})
	mux.HandleFunc("/user/memberships/orgs/", func(w http.ResponseWriter, r *http.Request) {
		s.memberHits++
		if !authed(w, r) {
			return
		}
		org := strings.TrimPrefix(r.URL.Path, "/user/memberships/orgs/")
		if code := s.orgStatus[org]; code != 0 && code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		state := "active"
		if v, ok := s.orgState[org]; ok {
			state = v
		}
		writeJSON(w, http.StatusOK, map[string]any{"state": state, "role": "member"})
	})
	return s
}

// stubGitHubProvider wires the adapter at its defaults (10m TTL / 1h grace)
// against the stub, with acme.co.jp as the email gate.
func stubGitHubProvider(gh *stubGitHub, orgs ...string) *auth.GitHubProvider {
	if len(orgs) == 0 {
		orgs = []string{"acme"}
	}
	return &auth.GitHubProvider{
		ProviderID: auth.GithubProviderID, LabelJA: "GitHub でサインイン", LabelEN: "Sign in with GitHub",
		ClientID: "client-id", ClientSecret: "client-secret",
		AllowedOrgs:  orgs,
		AllowDomains: envx.DomainSet("acme.co.jp"),
		TTL:          auth.GithubDefaultTTL,
		Grace:        auth.GithubDefaultGrace,
		WebBase:      gh.URL, APIBase: gh.URL, HTTPClient: gh.Client(),
	}
}

// captureLog redirects the standard logger into w until the returned function is
// called, so a test can assert that a config path stays SILENT.
func captureLog(w io.Writer) func() {
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(w)
	log.SetFlags(0)
	return func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) }
}

// --- the login itself -------------------------------------------------------

// Acceptance criterion: the identity a GitHub login resolves to is keyed on the numeric
// account id and the primary VERIFIED address — not on the username (renameable,
// then claimable by someone else) and not on /user's `email` (no verified flag).
func TestGitHubLoginUsesTheNumericIDAndPrimaryVerifiedEmail(t *testing.T) {
	gh := newStubGitHub(t, &stubGitHub{userID: 99001})
	p := stubGitHubProvider(gh)
	cfg := oauthTestConfig(t, p)

	state, au := startLogin(t, cfg, "?provider=github&next=%2Fsessions")
	if !strings.HasPrefix(au.String(), gh.URL+"/login/oauth/authorize") {
		t.Fatalf("authorize URL = %s", au)
	}
	if got := au.Query().Get("scope"); got != auth.GithubScopes {
		t.Fatalf("scope = %q, want %q (read:org drives the membership check, user:email the verified address)", got, auth.GithubScopes)
	}

	w := callback(t, cfg, state, "code", au.Query().Get("state"))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/sessions" {
		t.Fatalf("callback: %d -> %q (%s)", w.Code, w.Header().Get("Location"), w.Body.String())
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
	switch {
	case claims.Prov != auth.GithubProviderID:
		t.Fatalf("prov = %q", claims.Prov)
	case claims.Sub != "99001":
		t.Fatalf("sub = %q, want the numeric account id 99001 (a login name can be renamed and re-registered)", claims.Sub)
	case claims.Email != "yamada@acme.co.jp":
		t.Fatalf("email = %q, want the primary verified address", claims.Email)
	}
}

// Trap 1 of §61.7: the token endpoint answers in form encoding unless we ask for
// JSON, and a JSON decode of that yields no error — just an empty token. The stub
// mirrors that, so this test fails the moment the Accept header is dropped.
func TestGitHubTokenExchangeAsksForJSON(t *testing.T) {
	gh := newStubGitHub(t, &stubGitHub{})
	p := stubGitHubProvider(gh)
	if _, err := p.ExchangeCode(t.Context(), "code", "https://af.example.com/oauth2/callback"); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	// Same server, now answering form-encoded no matter what: this is what CP used
	// to see, and it must be an error rather than an empty-token success.
	gh.ignoreAccep = true
	if tok, err := p.ExchangeCode(t.Context(), "code", "https://af.example.com/oauth2/callback"); err == nil {
		t.Fatalf("a form-encoded token response was accepted, token=%q", tok)
	}
}

func TestGitHubLoginRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*stubGitHub)
		orgs  []string
		want  string
	}{{
		name: "primary な verified email が無い",
		// Somebody else's company address, added but never verified: exactly what
		// the verified flag exists to stop (§61.4).
		setup: func(s *stubGitHub) {
			s.emails = []map[string]any{
				{"email": "yamada@acme.co.jp", "primary": true, "verified": false},
				{"email": "someone@personal.dev", "primary": false, "verified": true},
			}
		},
		want: "forbidden",
	}, {
		name:  "org のメンバーではない",
		setup: func(s *stubGitHub) { s.orgStatus = map[string]int{"acme": http.StatusNotFound} },
		want:  "forbidden",
	}, {
		name:  "招待されただけで active ではない",
		setup: func(s *stubGitHub) { s.orgState = map[string]string{"acme": "pending"} },
		want:  "forbidden",
	}, {
		// A trap from §61.7: the org restricts third-party OAuth apps and nobody approved
		// this one. Everybody is rejected even though the config is right — the
		// adapter must survive it and log the hint (checked by hand; here we only
		// pin that it denies rather than 500s).
		name:  "OAuth App が org に承認されていない（403）",
		setup: func(s *stubGitHub) { s.orgStatus = map[string]int{"acme": http.StatusForbidden} },
		want:  "forbidden",
	}, {
		name:  "GitHub API が落ちている",
		setup: func(s *stubGitHub) { s.apiDown = true },
		want:  "exchange", // transient: "sign in again", not "you are not allowed"
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := newStubGitHub(t, &stubGitHub{})
			tc.setup(gh)
			cfg := oauthTestConfig(t, stubGitHubProvider(gh, tc.orgs...))
			state, au := startLogin(t, cfg, "?provider=github")
			w := callback(t, cfg, state, "code", au.Query().Get("state"))
			if got := loginErrorOf(t, w); got != tc.want {
				t.Fatalf("error = %q, want %q", got, tc.want)
			}
			if sessionCookieOf(t, w) != nil {
				t.Fatal("拒否したのにセッションが発行されている")
			}
		})
	}
}

// A primary email outside the company domain is refused at the gate. Identities under
// different addresses are never linked, so letting one through means the person starts
// working in a second workspace without noticing (§61.7).
func TestGitHubEmailAllowlistRefusesAnOutsideAddress(t *testing.T) {
	gh := newStubGitHub(t, &stubGitHub{emails: []map[string]any{
		{"email": "yamada@personal.dev", "primary": true, "verified": true},
	}})
	cfg := oauthTestConfig(t, stubGitHubProvider(gh))
	state, au := startLogin(t, cfg, "?provider=github")
	if got := loginErrorOf(t, callback(t, cfg, state, "code", au.Query().Get("state"))); got != "forbidden" {
		t.Fatalf("error = %q, want forbidden", got)
	}
}

// GitHub stays disabled unless at least one org is configured. This entry point was only
// adopted together with the membership check (§61.3); without it, anyone in the world who
// registered an address in an allowlisted domain with GitHub stands at the door.
func TestGitHubProviderRequiresItsOrgList(t *testing.T) {
	for _, tc := range []struct {
		name              string
		id, secret, orgs  string
		wantProvider      bool
		wantSilent        bool
		wantHasAllowlist  bool
		wantOrgsLowercase []string
	}{
		{name: "未設定なら黙って無効", wantProvider: false, wantSilent: true},
		// GITHUB_OAUTH_CLIENT_ID is also the git integration's (device flow) variable and
		// appears in .env.example, so a deployment that never asked for GitHub login must
		// not be nagged with a warning on every start.
		{name: "device flow の client_id だけなら黙って無効", id: "cid", wantProvider: false, wantSilent: true},
		{name: "secret はあるが org が無ければ警告して無効", id: "cid", secret: "sec", wantProvider: false},
		{name: "org はあるが secret が無ければ警告して無効", id: "cid", orgs: "acme", wantProvider: false},
		{name: "揃っていれば有効", id: "cid", secret: "sec", orgs: "Acme, acme-labs",
			wantProvider: true, wantHasAllowlist: true, wantOrgsLowercase: []string{"acme", "acme-labs"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GITHUB_OAUTH_CLIENT_ID", tc.id)
			t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", tc.secret)
			t.Setenv("AF_GITHUB_ALLOWED_ORGS", tc.orgs)
			t.Setenv("AF_GITHUB_LOGIN_CLIENT_ID", "")
			t.Setenv("AF_GITHUB_LOGIN_CLIENT_SECRET", "")
			var logs strings.Builder
			restore := captureLog(&logs)
			p := auth.NewGitHubProvider(func(string) bool { return true }, nil, true)
			restore()
			if (p != nil) != tc.wantProvider {
				t.Fatalf("provider = %v, want configured=%v", p, tc.wantProvider)
			}
			if tc.wantSilent && logs.Len() > 0 {
				t.Fatalf("何も設定していないのに警告が出ている: %s", logs.String())
			}
			if p == nil {
				return
			}
			if p.HasOwnAllowlist() != tc.wantHasAllowlist {
				t.Fatalf("hasOwnAllowlist = %v", p.HasOwnAllowlist())
			}
			if strings.Join(p.AllowedOrgs, ",") != strings.Join(tc.wantOrgsLowercase, ",") {
				t.Fatalf("orgs = %v, want %v (org 名は大小文字を区別しない)", p.AllowedOrgs, tc.wantOrgsLowercase)
			}
			if p.TTL != auth.GithubDefaultTTL || p.Grace != auth.GithubDefaultGrace {
				t.Fatalf("ttl=%s grace=%s, want the documented defaults", p.TTL, p.Grace)
			}
		})
	}
}

// The override for a deployment that wants a separate OAuth App for login. It is the way
// out when the org must not be asked to approve the same app the git integration (device
// flow) uses, so it has to win over the shared env pair.
func TestGitHubLoginClientIDOverridesTheSharedOne(t *testing.T) {
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "device-flow-app")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "device-flow-secret")
	t.Setenv("AF_GITHUB_LOGIN_CLIENT_ID", "login-app")
	t.Setenv("AF_GITHUB_LOGIN_CLIENT_SECRET", "login-secret")
	t.Setenv("AF_GITHUB_ALLOWED_ORGS", "acme")
	p := auth.NewGitHubProvider(func(string) bool { return true }, nil, true)
	if p == nil {
		t.Fatal("provider not configured")
	}
	if p.ClientID != "login-app" || p.ClientSecret != "login-secret" {
		t.Fatalf("client = %q/%q, want the AF_GITHUB_LOGIN_* pair", p.ClientID, p.ClientSecret)
	}
}

// Writing github into AF_OIDC_PROVIDERS must not let a generic OIDC provider take the
// GitHub adapter's id: if it did, GitHub login would silently become something else.
func TestGitHubIDIsReservedAgainstAnOIDCProvider(t *testing.T) {
	t.Setenv("AF_OIDC_PROVIDERS", "github")
	t.Setenv("AF_OIDC_GITHUB_ISSUER", "https://github.example.com")
	t.Setenv("AF_OIDC_GITHUB_CLIENT_ID", "cid")
	t.Setenv("AF_OIDC_GITHUB_CLIENT_SECRET", "sec")
	t.Setenv("AF_OIDC_GITHUB_TRUST", auth.TrustEmailVerified)
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "cid")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "sec")
	t.Setenv("AF_GITHUB_ALLOWED_ORGS", "acme")

	ps, err := buildLoginProviders(config{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var n int
	for _, p := range ps {
		if p.ID() == auth.GithubProviderID {
			n++
			if _, isGH := p.(*auth.GitHubProvider); isGH {
				t.Fatal("OIDC 側が先に id を取っているのに GitHub アダプタも登録された")
			}
		}
	}
	if n != 1 {
		t.Fatalf("github id を持つ provider が %d 個", n)
	}
}

// --- the per-request re-check ----------------------------------------------

// Leaving the company or the org has to take effect on the next request rather than
// waiting out the session TTL — but the API cannot be called on every request, so a TTL
// thins it out.
func TestGitHubMembershipIsRecheckedAfterTheTTL(t *testing.T) {
	gh := newStubGitHub(t, &stubGitHub{})
	p := stubGitHubProvider(gh)
	pr := auth.Principal{Provider: auth.GithubProviderID, Subject: "4242", Email: "yamada@acme.co.jp"}

	if _, err := p.Exchange(t.Context(), "code", "https://af.example.com/oauth2/callback"); err != nil {
		t.Fatalf("login: %v", err)
	}
	hitsAfterLogin := gh.memberHits

	// Inside the TTL the API is not called at all.
	for range 5 {
		if ok, err := p.Allowed(t.Context(), pr); !ok || err != nil {
			t.Fatalf("cached allow: ok=%v err=%v", ok, err)
		}
	}
	if gh.memberHits != hitsAfterLogin {
		t.Fatalf("TTL 内で %d 回 API を叩いている", gh.memberHits-hitsAfterLogin)
	}

	// Once the TTL expires it asks again, and a removal takes effect there.
	p.TTL = 0
	gh.orgStatus = map[string]int{"acme": http.StatusNotFound}
	ok, err := p.Allowed(t.Context(), pr)
	if ok || err != nil {
		t.Fatalf("org から外れた人が通った: ok=%v err=%v", ok, err)
	}
	if gh.memberHits == hitsAfterLogin {
		t.Fatal("TTL 切れ後に再判定していない")
	}
}

// A CP restart wipes both the cache and the access token. Telling somebody there is no
// evidence for "not allowed" is untrue — they are still a member of the org — so a
// distinct code asks for a fresh sign-in, and the API answers 401 so the SPA's existing
// unauthenticated path handles it.
func TestGitHubSessionAfterARestartAsksForAFreshLogin(t *testing.T) {
	gh := newStubGitHub(t, &stubGitHub{})
	p := stubGitHubProvider(gh) // empty cache = just after a restart
	cfg := oauthTestConfig(t, p)

	b, _ := json.Marshal(sessionClaims{
		Email: "yamada@acme.co.jp", Exp: time.Now().Add(time.Hour).Unix(),
		Prov: auth.GithubProviderID, Sub: "4242",
	})
	cookie := &http.Cookie{Name: sessionCookie, Value: cfg.signCookie(b)}
	gate := cfg.authGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("判定材料が無いのにハンドラまで通した")
	}))

	// A browser navigation: reauth, not forbidden.
	r := httptest.NewRequest(http.MethodGet, "/api/tenants", nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	gate.ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("want 302 got %d", w.Code)
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	if got := u.Query().Get("error"); got != "reauth" {
		t.Fatalf("error = %q, want reauth（メンバーのままの人に「許可されていません」は誤り）", got)
	}
	if page := auth.LoginErrorBlock("reauth", "ja"); !strings.Contains(page, "もう一度サインイン") {
		t.Fatalf("reauth の文言が出ていない: %s", page)
	}

	// An XHR from the SPA: 401, not 403 — the SPA's unauthenticated path sends it to login.
	r = httptest.NewRequest(http.MethodGet, "/api/tenants", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	gate.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("XHR: want 401 got %d (%s)", w.Code, w.Body.String())
	}

	// Somebody genuinely not allowed (outside the domain) still gets forbidden.
	b, _ = json.Marshal(sessionClaims{
		Email: "someone@personal.dev", Exp: time.Now().Add(time.Hour).Unix(),
		Prov: auth.GithubProviderID, Sub: "4242",
	})
	r = httptest.NewRequest(http.MethodGet, "/api/tenants", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cfg.signCookie(b)})
	w = httptest.NewRecorder()
	gate.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("ドメイン外: want 403 got %d", w.Code)
	}
}
