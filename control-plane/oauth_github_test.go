package main

import (
	"encoding/json"
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
		// ★ The real GitHub answers in application/x-www-form-urlencoded unless the
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
func stubGitHubProvider(gh *stubGitHub, orgs ...string) *githubProvider {
	if len(orgs) == 0 {
		orgs = []string{"acme"}
	}
	return &githubProvider{
		ProviderID: githubProviderID, LabelJA: "GitHub でサインイン", LabelEN: "Sign in with GitHub",
		ClientID: "client-id", ClientSecret: "client-secret",
		AllowedOrgs:  orgs,
		AllowDomains: domainSet("acme.co.jp"),
		TTL:          githubDefaultTTL,
		Grace:        githubDefaultGrace,
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

// ★ 受入条件: the identity a GitHub login resolves to is keyed on the numeric
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
	if got := au.Query().Get("scope"); got != githubScopes {
		t.Fatalf("scope = %q, want %q (read:org drives the membership check, user:email the verified address)", got, githubScopes)
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
	case claims.Prov != githubProviderID:
		t.Fatalf("prov = %q", claims.Prov)
	case claims.Sub != "99001":
		t.Fatalf("sub = %q, want the numeric account id 99001 (a login name can be renamed and re-registered)", claims.Sub)
	case claims.Email != "yamada@acme.co.jp":
		t.Fatalf("email = %q, want the primary verified address", claims.Email)
	}
}

// ★ §61.7 の罠 1: the token endpoint answers in form encoding unless we ask for
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
		// ★ §61.7 の罠: the org restricts third-party OAuth apps and nobody approved
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

// 会社ドメイン外の primary email は入口で落とす。P1 の改訂で別 email の結合は
// 作らないと決めたので、通してしまうと本人が気付かないまま別 workspace で
// 作業を始める（§61.7）。
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

// org を 1 つも指定していない設定では GitHub を有効にしない。メンバーシップ判定と
// セットでのみ採用した入口なので（§61.3）、それが無いと「許可リストのドメインの
// email を GitHub に登録した全人類」が入口に立つ。
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
		// ★ 既存デプロイの回帰: GITHUB_OAUTH_CLIENT_ID は git 連携（device flow）が
		// 前から使っていて .env.example にも載っている。ログインを頼んでいない
		// デプロイを毎起動 warning で叩いてはいけない。
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
			p := newGitHubProvider(func(string) bool { return true }, nil, true)
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
			if p.TTL != githubDefaultTTL || p.Grace != githubDefaultGrace {
				t.Fatalf("ttl=%s grace=%s, want the documented defaults", p.TTL, p.Grace)
			}
		})
	}
}

// ログイン専用の OAuth App を分けたい運用のための上書き。git 連携（device flow）と
// 同じアプリを org に承認させたくない場合の逃げ道なので、共有 env より強いこと。
func TestGitHubLoginClientIDOverridesTheSharedOne(t *testing.T) {
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "device-flow-app")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "device-flow-secret")
	t.Setenv("AF_GITHUB_LOGIN_CLIENT_ID", "login-app")
	t.Setenv("AF_GITHUB_LOGIN_CLIENT_SECRET", "login-secret")
	t.Setenv("AF_GITHUB_ALLOWED_ORGS", "acme")
	p := newGitHubProvider(func(string) bool { return true }, nil, true)
	if p == nil {
		t.Fatal("provider not configured")
	}
	if p.ClientID != "login-app" || p.ClientSecret != "login-secret" {
		t.Fatalf("client = %q/%q, want the AF_GITHUB_LOGIN_* pair", p.ClientID, p.ClientSecret)
	}
}

// AF_OIDC_PROVIDERS に github を書かれても、GitHub アダプタの id を OIDC 実装に
// 奪わせない（奪われると GitHub のログインが黙って別物にすり替わる）。
func TestGitHubIDIsReservedAgainstAnOIDCProvider(t *testing.T) {
	t.Setenv("AF_OIDC_PROVIDERS", "github")
	t.Setenv("AF_OIDC_GITHUB_ISSUER", "https://github.example.com")
	t.Setenv("AF_OIDC_GITHUB_CLIENT_ID", "cid")
	t.Setenv("AF_OIDC_GITHUB_CLIENT_SECRET", "sec")
	t.Setenv("AF_OIDC_GITHUB_TRUST", trustEmailVerified)
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "cid")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "sec")
	t.Setenv("AF_GITHUB_ALLOWED_ORGS", "acme")

	ps, err := buildLoginProviders(config{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var n int
	for _, p := range ps {
		if p.ID() == githubProviderID {
			n++
			if _, isGH := p.(*githubProvider); isGH {
				t.Fatal("OIDC 側が先に id を取っているのに GitHub アダプタも登録された")
			}
		}
	}
	if n != 1 {
		t.Fatalf("github id を持つ provider が %d 個", n)
	}
}

// --- the per-request re-check ----------------------------------------------

// 退職・org 除名は次のリクエストで効く必要がある（セッション TTL を待たない）。
// ただし毎リクエスト API を叩くわけにはいかないので TTL で間引く。
func TestGitHubMembershipIsRecheckedAfterTheTTL(t *testing.T) {
	gh := newStubGitHub(t, &stubGitHub{})
	p := stubGitHubProvider(gh)
	pr := principal{Provider: githubProviderID, Subject: "4242", Email: "yamada@acme.co.jp"}

	if _, err := p.Exchange(t.Context(), "code", "https://af.example.com/oauth2/callback"); err != nil {
		t.Fatalf("login: %v", err)
	}
	hitsAfterLogin := gh.memberHits

	// TTL の内側では API を叩かない。
	for range 5 {
		if ok, err := p.Allowed(t.Context(), pr); !ok || err != nil {
			t.Fatalf("cached allow: ok=%v err=%v", ok, err)
		}
	}
	if gh.memberHits != hitsAfterLogin {
		t.Fatalf("TTL 内で %d 回 API を叩いている", gh.memberHits-hitsAfterLogin)
	}

	// TTL が切れたら問い合わせ直し、除名がそこで効く。
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

// GitHub 障害で全員が締め出されるのも、いつまでも通り続けるのも避ける — 最後の
// 肯定結果を猶予期間だけ延命する（§61.7）。
func TestGitHubOutageHonorsTheLastPositiveAnswerForTheGraceWindow(t *testing.T) {
	gh := newStubGitHub(t, &stubGitHub{})
	p := stubGitHubProvider(gh)
	pr := principal{Provider: githubProviderID, Subject: "4242", Email: "yamada@acme.co.jp"}
	if _, err := p.Exchange(t.Context(), "code", "https://af.example.com/oauth2/callback"); err != nil {
		t.Fatalf("login: %v", err)
	}

	p.TTL = 0 // 毎回 stale 扱いにして再判定へ入れる
	gh.apiDown = true
	if ok, err := p.Allowed(t.Context(), pr); !ok || err != nil {
		t.Fatalf("猶予期間内なのに拒否された: ok=%v err=%v", ok, err)
	}

	// 猶予を超えたら閉じる。判定は time.Since(lastOK) < Grace なので、最後の肯定
	// 結果を 2 時間前へ巻き戻すのと、猶予を 0 にするのは同じ枝を通る。アダプタが
	// internal/auth へ出てキャッシュに直接触れなくなったので後者で書く。
	p.Grace = 0
	if ok, _ := p.Allowed(t.Context(), pr); ok {
		t.Fatal("猶予期間を過ぎても通り続けている")
	}
}

// ★ CP 再起動でキャッシュも access token も消える。判定材料が無い人を「許可されて
// いません」と突き放すのは事実と違う（本人は org のメンバーのまま）。再ログインを
// 求める別のコードを出し、API には 401 を返して SPA の既存経路に乗せる。
func TestGitHubSessionAfterARestartAsksForAFreshLogin(t *testing.T) {
	gh := newStubGitHub(t, &stubGitHub{})
	p := stubGitHubProvider(gh) // キャッシュ空＝再起動直後
	cfg := oauthTestConfig(t, p)

	b, _ := json.Marshal(sessionClaims{
		Email: "yamada@acme.co.jp", Exp: time.Now().Add(time.Hour).Unix(),
		Prov: githubProviderID, Sub: "4242",
	})
	cookie := &http.Cookie{Name: sessionCookie, Value: cfg.signCookie(b)}
	gate := cfg.authGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("判定材料が無いのにハンドラまで通した")
	}))

	// ブラウザのナビゲーション: forbidden ではなく reauth。
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
	if page := loginErrorBlock("reauth", "ja"); !strings.Contains(page, "もう一度サインイン") {
		t.Fatalf("reauth の文言が出ていない: %s", page)
	}

	// SPA からの XHR: 403 ではなく 401（SPA の未認証経路がログインへ送る）。
	r = httptest.NewRequest(http.MethodGet, "/api/tenants", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	gate.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("XHR: want 401 got %d (%s)", w.Code, w.Body.String())
	}

	// 一方、本当に許可されていない人（ドメイン外）は従来どおり forbidden のまま。
	b, _ = json.Marshal(sessionClaims{
		Email: "someone@personal.dev", Exp: time.Now().Add(time.Hour).Unix(),
		Prov: githubProviderID, Sub: "4242",
	})
	r = httptest.NewRequest(http.MethodGet, "/api/tenants", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cfg.signCookie(b)})
	w = httptest.NewRecorder()
	gate.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("ドメイン外: want 403 got %d", w.Code)
	}
}
