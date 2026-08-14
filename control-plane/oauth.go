package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// CP-native OIDC login (Authorization Code Grant). Replaces the external
// oauth2-proxy: the Control Plane sits directly behind Tailscale Funnel, runs the
// login itself, and carries the authenticated email in a signed session cookie.
// authGate() then injects that email into the same header AUTH=proxy used to read,
// so the entire downstream (resolveIdentity / tenants / bitbucket) is unchanged.
// Enabled by AUTH=oauth.
//
// This file owns everything that is provider-independent — the loginProvider
// abstraction, the signed state/session cookies, the allowlist, authGate and the
// login page. The IdP-facing protocol lives in oauth_oidc.go (docs/61 §61.6);
// Google is one instance of that generic OIDC client, keeping its historical env
// names (GOOGLE_OAUTH_*) so existing deployments need no config change.
//
// Security note: because CP is now the edge (Funnel forwards client headers
// verbatim), authGate strips any inbound identity header before trusting our own
// session — oauth2-proxy used to own that boundary.

const (
	sessionCookie = "af_session"
	stateCookie   = "af_oauth_state"

	// googleProviderID is also the transitional default for sessions and state
	// cookies minted before providers existed (they carry no provider id).
	googleProviderID = "google"
)

// --- provider abstraction (docs/61 §61.6) ----------------------------------

// principal is what a provider proves about the person who just signed in.
// Verified means the provider's declared `trust` rule (§61.4) was satisfied —
// not merely that an email claim was present.
type principal struct {
	Provider string
	Subject  string // the IdP's stable subject id (unlike email, it never changes)
	Email    string
	Verified bool
}

// loginProvider is one sign-in button: an IdP the deployment enabled. Every
// provider shares the single redirect_uri (/oauth2/callback) — which provider a
// callback belongs to is carried in the signed state cookie, so the operator
// registers exactly one URI per IdP no matter how many are configured (決定 8).
type loginProvider interface {
	ID() string
	Label(lang string) string // login page button text
	// AuthorizeURL may hit the network (OIDC discovery is lazy), hence ctx+error.
	AuthorizeURL(ctx context.Context, state, redirectURI string) (string, error)
	Exchange(ctx context.Context, code, redirectURI string) (principal, error)
	// Allowed re-checks authorization. It is called at login AND on every
	// request (authGate) — removing someone from the allowlist is the
	// offboarding path and must not wait for the session cookie to expire.
	Allowed(ctx context.Context, p principal) (bool, error)
}

// setProviders installs the enabled providers in display order and builds the
// id lookup used by the callback (an unknown id must never reach a provider).
func (c *config) setProviders(ps []loginProvider) {
	c.providers = ps
	c.providerByID = make(map[string]loginProvider, len(ps))
	for _, p := range ps {
		c.providerByID[p.ID()] = p
	}
}

// providerFor resolves a provider id from a query string or state cookie. An
// empty id means "the deployment's first button" — that also covers bookmarked
// /oauth2/login links and state cookies minted before P0.
func (c config) providerFor(id string) loginProvider {
	if id == "" {
		if len(c.providers) == 0 {
			return nil
		}
		return c.providers[0]
	}
	return c.providerByID[id]
}

func (c config) oauthConfigured() bool {
	return len(c.providers) > 0 && len(c.cookieSecret) > 0 && c.publicBaseURL != ""
}

func (c config) oauthRedirectURI() string {
	return strings.TrimRight(c.publicBaseURL, "/") + "/oauth2/callback"
}

// --- signed cookies (HMAC-SHA256, stdlib only) -----------------------------

// signCookie returns base64(payload)"."base64(HMAC(payload)).
func (c config) signCookie(payload []byte) string {
	mac := hmac.New(sha256.New, c.cookieSecret)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c config) verifyCookie(s string) ([]byte, bool) {
	i := strings.IndexByte(s, '.')
	if i < 0 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(s[:i])
	if err != nil {
		return nil, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(s[i+1:])
	if err != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, c.cookieSecret)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, false
	}
	return payload, true
}

func (c config) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", HttpOnly: true,
		Secure: c.cookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

// --- session --------------------------------------------------------------

// sessionClaims is the signed session cookie payload. `prov` / `sub` were added
// in P0 (docs/61 §61.6): JSON decoding tolerates their absence, so cookies minted
// by the previous version keep working — no forced logout on upgrade.
type sessionClaims struct {
	Email string `json:"email"`
	Exp   int64  `json:"exp"`
	Prov  string `json:"prov,omitempty"` // provider id the person signed in with
	Sub   string `json:"sub,omitempty"`  // that provider's subject id
}

// provider applies the one-version transitional rule: a cookie without `prov`
// predates multi-IdP support, and back then the only IdP was Google.
func (s sessionClaims) provider() string {
	if s.Prov == "" {
		return googleProviderID
	}
	return s.Prov
}

func (c config) issueSession(w http.ResponseWriter, p principal) {
	b, _ := json.Marshal(sessionClaims{
		Email: p.Email,
		Exp:   time.Now().Add(c.sessionTTL).Unix(),
		Prov:  p.Provider,
		Sub:   p.Subject,
	})
	c.setCookie(w, sessionCookie, c.signCookie(b), int(c.sessionTTL.Seconds()))
}

// session returns the verified, unexpired claims from the session cookie.
func (c config) session(r *http.Request) (sessionClaims, bool) {
	ck, err := r.Cookie(sessionCookie)
	if err != nil {
		return sessionClaims{}, false
	}
	payload, ok := c.verifyCookie(ck.Value)
	if !ok {
		return sessionClaims{}, false
	}
	var claims sessionClaims
	if json.Unmarshal(payload, &claims) != nil {
		return sessionClaims{}, false
	}
	if claims.Email == "" || time.Now().Unix() > claims.Exp {
		return sessionClaims{}, false
	}
	return claims, true
}

// sessionAllowed re-runs the authorization check for an established session
// against the provider it was issued by. A session whose provider is no longer
// configured is denied (the operator removed that IdP = that door is closed).
func (c config) sessionAllowed(ctx context.Context, claims sessionClaims) bool {
	p := c.providerByID[claims.provider()]
	if p == nil {
		return false
	}
	ok, err := p.Allowed(ctx, principal{
		Provider: claims.provider(), Subject: claims.Sub, Email: claims.Email, Verified: true,
	})
	return err == nil && ok
}

// --- allowlist (emails.txt successor) -------------------------------------

// emailAllowed checks the exact-email and domain allowlists (env CSVs) plus the
// live-read allowlist file (so edits need no restart). In the file, a line is an
// exact email, or "@example.com" for a whole domain. All sources empty => deny
// all (fail closed). This is the deployment-wide list; a provider may declare its
// own (AF_OIDC_<ID>_ALLOWED_*), in which case that one applies instead.
func (c config) emailAllowed(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(email, '@')
	if email == "" || at < 0 {
		return false
	}
	domain := email[at+1:]
	if c.allowEmails[email] || c.allowDomains[domain] {
		return true
	}
	if c.allowEmailsFile != "" {
		if b, err := os.ReadFile(c.allowEmailsFile); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				line = strings.ToLower(strings.TrimSpace(line))
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				if strings.HasPrefix(line, "@") {
					if line[1:] == domain {
						return true
					}
				} else if line == email {
					return true
				}
			}
		}
	}
	return false
}

// hasDeploymentAllowlist reports whether any deployment-wide allowlist source is
// configured at all (startup warning: with none, every login is denied).
func (c config) hasDeploymentAllowlist() bool {
	return len(c.allowEmails) > 0 || len(c.allowDomains) > 0 || c.allowEmailsFile != ""
}

// --- OAuth flow -----------------------------------------------------------

type oauthState struct {
	Nonce string `json:"n"`
	Next  string `json:"x"`
	Exp   int64  `json:"e"`
	Prov  string `json:"p,omitempty"` // provider id; empty on pre-P0 state cookies
}

// sanitizeNext keeps post-login redirects on-site (no open redirect): a single
// leading slash only. "/\" is rejected too — browsers normalize backslash to
// slash, so "/\evil.com" would become the scheme-relative "//evil.com". The
// parse check backstops both against any other absolute-URL form.
func sanitizeNext(n string) string {
	if n == "" || !strings.HasPrefix(n, "/") || strings.HasPrefix(n, "//") || strings.HasPrefix(n, "/\\") {
		return "/"
	}
	if u, err := url.Parse(n); err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return n
}

func (c config) handleOAuthLogin(w http.ResponseWriter, r *http.Request) {
	if !c.oauthConfigured() {
		http.Error(w, "oauth not configured", http.StatusServiceUnavailable)
		return
	}
	p := c.providerFor(strings.TrimSpace(r.URL.Query().Get("provider")))
	if p == nil {
		loginRedirect(w, r, "provider")
		return
	}
	next := sanitizeNext(r.URL.Query().Get("next"))
	nonce := randHex(16)
	b, _ := json.Marshal(oauthState{Nonce: nonce, Next: next, Exp: time.Now().Add(10 * time.Minute).Unix(), Prov: p.ID()})
	c.setCookie(w, stateCookie, c.signCookie(b), 600)

	au, err := p.AuthorizeURL(r.Context(), nonce, c.oauthRedirectURI())
	if err != nil {
		// Discovery failed (unreachable/misconfigured issuer) — there is nowhere
		// to send the browser, so fall back to the login page with an error.
		log.Printf("oauth: provider %s authorize: %v", p.ID(), err)
		loginRedirect(w, r, "exchange")
		return
	}
	http.Redirect(w, r, au, http.StatusFound)
}

func (c config) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	// Validate CSRF state from the signed, single-use state cookie.
	ck, err := r.Cookie(stateCookie)
	c.setCookie(w, stateCookie, "", -1) // burn it regardless
	if err != nil {
		loginRedirect(w, r, "session")
		return
	}
	payload, ok := c.verifyCookie(ck.Value)
	if !ok {
		loginRedirect(w, r, "session")
		return
	}
	var st oauthState
	if json.Unmarshal(payload, &st) != nil || time.Now().Unix() > st.Exp ||
		subtle.ConstantTimeCompare([]byte(st.Nonce), []byte(r.URL.Query().Get("state"))) != 1 {
		loginRedirect(w, r, "session")
		return
	}
	// The state cookie is signed, but still resolve its provider id against the
	// configured set before branching on it (決定 8) — a provider that was
	// removed from the config must not keep serving callbacks.
	p := c.providerFor(st.Prov)
	if p == nil {
		loginRedirect(w, r, "provider")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		loginRedirect(w, r, "denied")
		return
	}

	pr, err := p.Exchange(r.Context(), code, c.oauthRedirectURI())
	if err != nil {
		log.Printf("oauth: provider %s exchange: %v", p.ID(), err)
		if errors.Is(err, errNotAllowed) { // e.g. a tid outside ALLOWED_TIDS
			loginRedirect(w, r, "forbidden")
		} else {
			loginRedirect(w, r, "exchange")
		}
		return
	}
	if pr.Email == "" || !pr.Verified {
		loginRedirect(w, r, "denied")
		return
	}
	allowed, err := p.Allowed(r.Context(), pr)
	if err != nil || !allowed {
		loginRedirect(w, r, "forbidden")
		return
	}
	c.issueSession(w, pr)
	http.Redirect(w, r, sanitizeNext(st.Next), http.StatusFound)
}

func (c config) handleOAuthLogout(w http.ResponseWriter, r *http.Request) {
	c.setCookie(w, sessionCookie, "", -1)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func loginRedirect(w http.ResponseWriter, r *http.Request, errCode string) {
	u := "/login"
	if errCode != "" {
		u += "?error=" + url.QueryEscape(errCode)
	}
	http.Redirect(w, r, u, http.StatusFound)
}

// --- authGate middleware --------------------------------------------------

func (c config) authGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Anti-spoof: CP is the edge now, so never trust an inbound identity
		// header — only our verified session may set it.
		r.Header.Del(c.mgr.emailHeader)
		if isAuthExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		claims, ok := c.session(r)
		if !ok {
			if wantsHTML(r) {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			} else {
				writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "login required"})
			}
			return
		}
		// Re-check authorization on every request, not just at login: removing an
		// email from the allowlist is the offboarding path, and must take effect
		// before the session cookie's TTL runs out.
		if !c.sessionAllowed(r.Context(), claims) {
			if wantsHTML(r) {
				c.setCookie(w, sessionCookie, "", -1)
				loginRedirect(w, r, "forbidden")
			} else {
				writeAPIErr(w, &apiError{http.StatusForbidden, "forbidden", "email not allowed"})
			}
			return
		}
		r.Header.Set(c.mgr.emailHeader, claims.Email)
		next.ServeHTTP(w, r)
	})
}

// isAuthExempt（セッション無しで到達できるパス）は routes.go の除外レジストリに
// 移動した — 各 register 関数が自分の除外を宣言する（docs/23 P2-W1）。

// wantsHTML is true for top-level browser navigations (redirect to /login);
// everything else (XHR, WS) gets a 401 the SPA handles.
func wantsHTML(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

// --- login landing page ---------------------------------------------------

func (c config) handleLogin(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.session(r); ok { // already signed in
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	lang := preferredUILang(r)
	t := loginText[lang]
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	page := strings.NewReplacer(
		"{{LANG}}", lang,
		"{{TITLE}}", t.title,
		"{{NOTE}}", t.note,
		"{{ERROR}}", loginErrorBlock(r.URL.Query().Get("error"), lang),
		"{{BUTTONS}}", c.loginButtons(sanitizeNext(r.URL.Query().Get("next")), lang),
	).Replace(loginPageHTML)
	_, _ = w.Write([]byte(page))
}

// loginButtons renders one sign-in button per enabled provider (docs/61 §61.6).
// With a single provider the markup is the same single .gbtn as before, so an
// existing Google-only deployment looks unchanged.
func (c config) loginButtons(next, lang string) string {
	if len(c.providers) == 0 {
		return `<div class="err">` + loginText[lang].errUnconfigured + `</div>`
	}
	var b strings.Builder
	for _, p := range c.providers {
		q := url.Values{"provider": {p.ID()}}
		if next != "/" {
			q.Set("next", next)
		}
		b.WriteString(`<a class="gbtn" href="/oauth2/login?` + html.EscapeString(q.Encode()) + `">`)
		b.WriteString(providerIcon(p.ID()))
		b.WriteString(html.EscapeString(p.Label(lang)))
		b.WriteString("</a>\n")
	}
	return b.String()
}

// providerIcon returns the inline SVG mark for a provider's button: the Google
// wordmark for Google (unchanged), a neutral key glyph for everything else — CP
// must not ship third-party logos it has no license for.
func providerIcon(id string) string {
	if id == googleProviderID {
		return googleIconSVG
	}
	return genericIconSVG
}

func loginErrorBlock(code, lang string) string {
	t := loginText[lang]
	var msg string
	switch code {
	case "forbidden":
		msg = t.errForbidden
	case "denied":
		msg = t.errDenied
	case "session", "exchange":
		msg = t.errSession
	case "provider":
		msg = t.errProvider
	default:
		return ""
	}
	return `<div class="err">` + msg + `</div>`
}

// preferredUILang picks the UI language for CP-rendered pages (login / OAuth
// callbacks) from Accept-Language, since these are served before any locale cookie
// exists (docs/28 P3). It scans the header's language ranges in order and returns the
// first supported one; Japanese is the default (the product's primary audience and the
// prior hardcoded language). The Console SPA owns locale once signed in.
func preferredUILang(r *http.Request) string {
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag := strings.TrimSpace(part)
		if i := strings.IndexByte(tag, ';'); i >= 0 { // drop the q-value
			tag = tag[:i]
		}
		switch tag = strings.ToLower(strings.TrimSpace(tag)); {
		case strings.HasPrefix(tag, "ja"):
			return "ja"
		case strings.HasPrefix(tag, "en"):
			return "en"
		}
	}
	return "ja"
}

// loginText holds the localized strings for the CP-rendered login page. ja is the
// default; en is served when Accept-Language prefers English (preferredUILang).
type loginStrings struct {
	title, signin, signinWith, note                  string
	errForbidden, errDenied, errSession, errProvider string
	errUnconfigured                                  string
}

var loginText = map[string]loginStrings{
	"ja": {
		title:           "Agent Fleet — サインイン",
		signin:          "Google でサインイン",
		signinWith:      "%s でサインイン",
		note:            "アクセスは許可されたアカウントに限定されています。",
		errForbidden:    "このアカウントはアクセスを許可されていません。管理者にメールアドレスの追加を依頼してください。",
		errDenied:       "サインインがキャンセルされました。もう一度お試しください。",
		errSession:      "セッションの確立に失敗しました。もう一度サインインしてください。",
		errProvider:     "指定されたサインイン方法は利用できません。下のボタンから選び直してください。",
		errUnconfigured: "サインイン方法が設定されていません。管理者に連絡してください。",
	},
	"en": {
		title:           "Agent Fleet — Sign in",
		signin:          "Sign in with Google",
		signinWith:      "Sign in with %s",
		note:            "Access is limited to allowed accounts.",
		errForbidden:    "This account isn't allowed access. Ask an administrator to add your email address.",
		errDenied:       "Sign-in was canceled. Please try again.",
		errSession:      "Couldn't establish a session. Please sign in again.",
		errProvider:     "That sign-in method isn't available. Pick one of the buttons below.",
		errUnconfigured: "No sign-in method is configured. Please contact your administrator.",
	},
}

// defaultProviderLabel builds a button label for a provider that declared no
// AF_OIDC_<ID>_LABEL_*: "<Id> でサインイン" / "Sign in with <Id>".
func defaultProviderLabel(id, lang string) string {
	t, ok := loginText[lang]
	if !ok {
		t = loginText["ja"]
	}
	name := id
	if name != "" {
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	return fmt.Sprintf(t.signinWith, name)
}

const googleIconSVG = `<svg viewBox="0 0 48 48" aria-hidden="true">
    <path fill="#EA4335" d="M24 9.5c3.54 0 6.71 1.22 9.21 3.6l6.85-6.85C35.9 2.38 30.47 0 24 0 14.62 0 6.51 5.38 2.56 13.22l7.98 6.19C12.43 13.72 17.74 9.5 24 9.5z"/>
    <path fill="#4285F4" d="M46.98 24.55c0-1.57-.15-3.09-.38-4.55H24v9.02h12.94c-.58 2.96-2.26 5.48-4.78 7.18l7.73 6c4.51-4.18 7.09-10.36 7.09-17.65z"/>
    <path fill="#FBBC05" d="M10.53 28.59c-.48-1.45-.76-2.99-.76-4.59s.27-3.14.76-4.59l-7.98-6.19C.92 16.46 0 20.12 0 24c0 3.88.92 7.54 2.56 10.78l7.97-6.19z"/>
    <path fill="#34A853" d="M24 48c6.48 0 11.93-2.13 15.89-5.81l-7.73-6c-2.15 1.45-4.92 2.3-8.16 2.3-6.26 0-11.57-4.22-13.47-9.91l-7.98 6.19C6.51 42.62 14.62 48 24 48z"/>
   </svg>
   `

const genericIconSVG = `<svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="#1f2937" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
    <rect x="3" y="11" width="18" height="10" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>
   </svg>
   `

// loginPageHTML — self-contained (inline CSS, no external assets but the brand
// banner). The banner carries the wordmark + tagline; if it fails to load the
// text wordmark below it shows instead. Tokens {{ERROR}} and {{BUTTONS}} are
// substituted by handleLogin (no fmt verbs — the CSS contains literal % units).
const loginPageHTML = `<!doctype html><html lang="{{LANG}}"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{TITLE}}</title>
<style>
:root{--teal:#2aa79b;--ink:#e8eef6;--muted:#9fb0c4}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;
 font:16px/1.6 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;color:var(--ink);
 background:radial-gradient(1200px 600px at 50% -10%,#1d3357,#0c1626)}
.card{width:min(94vw,560px);background:#0f1c30;border:1px solid #22344f;border-radius:16px;
 overflow:hidden;box-shadow:0 20px 60px rgba(0,0,0,.45)}
.hero{display:block;width:100%;height:auto;background:#0d3b66}
.body{padding:28px 32px 32px;text-align:center}
.wordmark{display:none;font-size:34px;font-weight:800;letter-spacing:.5px;margin:8px 0}
.wordmark b{color:var(--teal)}
.tag{color:var(--muted);letter-spacing:3px;font-size:12px;text-transform:uppercase;margin:0 0 22px}
.btns{display:grid;gap:10px}
.gbtn{display:inline-flex;align-items:center;gap:12px;justify-content:center;width:100%;
 padding:13px 18px;border-radius:10px;border:0;cursor:pointer;background:#fff;color:#1f2937;
 font-size:15px;font-weight:600;text-decoration:none}
.gbtn:hover{background:#f1f3f5}
.gbtn svg{width:20px;height:20px}
.note{margin-top:18px;color:var(--muted);font-size:13px}
.err{margin:0 0 18px;padding:11px 14px;border-radius:9px;background:rgba(220,68,68,.12);
 border:1px solid rgba(220,68,68,.4);color:#ffb4b4;font-size:14px;text-align:left}
</style></head><body>
<main class="card">
 <img class="hero" src="/brand/agent-fleet-banner.webp" alt="Agent Fleet — Deploy. Connect. Scale."
  onerror="this.style.display='none';document.getElementById('wm').style.display='block';document.getElementById('tg').style.display='block'">
 <div class="body">
  <div id="wm" class="wordmark">Agent <b>Fleet</b></div>
  <p id="tg" class="tag" style="display:none">Deploy. Connect. Scale.</p>
  {{ERROR}}
  <div class="btns">
  {{BUTTONS}}
  </div>
  <p class="note">{{NOTE}}</p>
 </div>
</main>
</body></html>`
