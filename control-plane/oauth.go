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

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// CP-native OIDC login (Authorization Code Grant). Replaces the external
// oauth2-proxy: the Control Plane sits directly behind Tailscale Funnel, runs the
// login itself, and carries the authenticated email in a signed session cookie.
// authGate() then injects that email into the same header AUTH=proxy used to read,
// so the entire downstream (resolveIdentity / tenants / bitbucket) is unchanged.
// Enabled by AUTH=oauth.
//
// This file owns the HTTP half: the signed state/session cookies, the allowlist,
// authGate and the login page handlers. The IdP-facing protocol and the provider
// abstraction it implements moved to internal/auth (docs/log/61 §61.6); Google is one
// instance of that generic OIDC client, keeping its historical env names
// (GOOGLE_OAUTH_*) so existing deployments need no config change.
//
// Why the split runs here: everything below hangs off config, which is the Control
// Plane's own settings struct — Go cannot grow those methods from another package,
// and config's fields are read across the whole binary, so it does not travel
// (ADR 0067 決定 1). The handlers call internal/auth directly (alias_auth.go was reclaimed).
//
// Security note: because CP is now the edge (Funnel forwards client headers
// verbatim), authGate strips any inbound identity header before trusting our own
// session — oauth2-proxy used to own that boundary.

const (
	sessionCookie = "af_session"
	stateCookie   = "af_oauth_state"
)

// The provider abstraction (docs/log/61 §61.6) — principal, loginProvider,
// providerRealm — and the login page vocabulary moved to internal/auth together
// with the adapters that implement them. They are reachable here under their
// internal/auth directly (the alias_auth.go layer was reclaimed in RECLAIM-B).

// buildLoginProviders assembles the enabled login providers in display order.
// The assembly itself moved to internal/auth with the adapters it constructs;
// this is the seam that hands it the five terms it used to read off config,
// which package auth cannot see. main.go calls it exactly as before.
func buildLoginProviders(c config) ([]auth.LoginProvider, error) {
	return auth.BuildLoginProviders(auth.Deployment{
		GoogleClientID:     c.googleClientID,
		GoogleClientSecret: c.googleClientSecret,
		DeployAllowed:      c.emailAllowed,
		DBAllowed:          c.tenantEmailAllowed,
		HasAllowlist:       c.hasDeploymentAllowlist(),
	})
}

// setProviders installs the enabled providers in display order and builds the
// id lookup used by the callback (an unknown id must never reach a provider).
func (c *config) setProviders(ps []auth.LoginProvider) {
	c.providers = ps
	c.providerByID = make(map[string]auth.LoginProvider, len(ps))
	for _, p := range ps {
		c.providerByID[p.ID()] = p
	}
}

// providerFor resolves a provider id from a query string or state cookie. An
// empty id means "the deployment's first button" — that also covers bookmarked
// /oauth2/login links and state cookies minted before P0.
//
// ★ A "t:<slug>:<name>" id is a tenant-defined provider (docs/log/61 §61.11) and is
// resolved through the runtime registry, which only holds APPROVED (active) rows.
// That is what makes "a pending provider issues no session" true at the callback,
// not merely on the login page — hiding a button is presentation, and decision 14
// says presentation is never the enforcement.
func (c config) providerFor(ctx context.Context, id string) auth.LoginProvider {
	if id == "" {
		if len(c.providers) == 0 {
			return nil
		}
		return c.providers[0]
	}
	if auth.IsTenantProviderID(id) {
		if c.mgr == nil {
			return nil
		}
		return c.mgr.tenantIdP.ProviderFor(ctx, id)
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
// in P0 (docs/log/61 §61.6): JSON decoding tolerates their absence, so cookies minted
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
		return auth.GoogleProviderID
	}
	return s.Prov
}

func (c config) issueSession(w http.ResponseWriter, p auth.Principal) {
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
//
// It returns the login-page error code to show when it denies, because the two
// denials mean different things to the person reading the page: "forbidden" says
// they are not allowed in, while "reauth" says only that CP can no longer verify
// them and they should sign in again (the GitHub adapter after a restart —
// see oauth_github.go). Telling someone they are not allowed when they are is the
// kind of message that generates a support ticket.
func (c config) sessionAllowed(ctx context.Context, claims sessionClaims) (bool, string) {
	// A tenant-defined provider goes through the registry, so suspending one (or
	// letting an edit send it back to pending) ends its sessions within the cache
	// TTL instead of waiting out AF_SESSION_TTL — the same offboarding property the
	// allowlist re-check gives env providers.
	p := c.providerFor(ctx, claims.provider())
	if p == nil {
		return false, "forbidden"
	}
	ok, err := p.Allowed(ctx, auth.Principal{
		Provider: claims.provider(), Subject: claims.Sub, Email: claims.Email, Verified: true,
	})
	switch {
	case errors.Is(err, auth.ErrNeedsReauth):
		return false, "reauth"
	case err != nil || !ok:
		return false, "forbidden"
	}
	return true, ""
}

// --- carrying the IdP subject downstream (docs/log/61 §61.5) -------------------

// loginRef is the (provider, subject) the current session was minted with. It
// travels in the REQUEST CONTEXT rather than in a header like the email does:
// CP is the edge, so a header would have to be stripped on the way in as well as
// set on the way out, and forgetting the strip would let a client claim any
// subject it likes. A context value cannot be reached from outside the process.
type loginRef struct{ provider, subject string }

type loginRefKey struct{}

func withLoginRef(ctx context.Context, ref loginRef) context.Context {
	return context.WithValue(ctx, loginRefKey{}, ref)
}

// loginRefFrom reports the proven IdP subject behind this request, if any. It is
// absent under AUTH=proxy and AUTH=dev (neither has an IdP subject to offer) and
// on sessions minted before P0 (no `sub` claim), and every caller must keep
// working by email alone in that case.
func loginRefFrom(ctx context.Context) (loginRef, bool) {
	ref, ok := ctx.Value(loginRefKey{}).(loginRef)
	return ref, ok && ref.subject != ""
}

// sessionProviderFrom reports WHICH sign-in button minted the current session.
// The tenant gate needs it (tenant.allowed_providers, docs/log/61 §61.9.4) and, unlike
// identity resolution, it is meaningful even without a subject: a pre-P0 cookie
// carries no `sub` but its provider is still known ("google", by the transitional
// rule). "" means there is no IdP behind this request at all — AUTH=proxy and
// AUTH=dev — and the gate treats that as "cannot be restricted", not as a denial.
func sessionProviderFrom(ctx context.Context) string {
	ref, ok := ctx.Value(loginRefKey{}).(loginRef)
	if !ok {
		return ""
	}
	return ref.provider
}

// sessionLoginRef is loginRefFrom WITHOUT the "there is a subject" requirement.
// Unlinking needs it (docs/log/61 §61.16.4): the method the caller is signed in with
// must not be removable, and on a pre-P0 cookie — provider known, subject not — the
// safe reading is "every row of that provider is the one in use", not "none is".
func sessionLoginRef(ctx context.Context) (loginRef, bool) {
	ref, ok := ctx.Value(loginRefKey{}).(loginRef)
	return ref, ok && ref.provider != ""
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

// tenantEmailAllowed is the DATABASE-derived term of the entry gate (docs/log/61
// §61.9.6 + 決定 16): an auto-join domain matches, or this person is on some
// tenant's roster because an administrator invited them.
//
// ★ Adding this term is what lets an invite-run deployment stop maintaining
// AF_OAUTH_ALLOWED_* at all — the roster becomes the single ledger of who may
// enter (§61.9.5). Taking the INTERSECTION instead would mean adding every invited
// person to the env as well, which is the double bookkeeping this replaces.
//
// ★ It is a term of the EMAIL axis only. It is OR'd with the email allowlists and
// never with a different kind of check: the GitHub adapter's org membership stays
// a separate AND, or holding a membership would be a way around it (決定 2).
func (c config) tenantEmailAllowed(ctx context.Context, email string) bool {
	if c.mgr == nil || c.mgr.tenantLogin == nil {
		return false
	}
	return c.mgr.tenantLogin.entryAllowed(ctx, email)
}

// --- OAuth flow -----------------------------------------------------------

type oauthState struct {
	Nonce string `json:"n"`
	Next  string `json:"x"`
	Exp   int64  `json:"e"`
	Prov  string `json:"p,omitempty"` // provider id; empty on pre-P0 state cookies
	// Tnt is the tenant slug the person started from (/login/<slug>). It is carried
	// in the signed state for exactly two reasons: an error goes back to the same
	// department's page rather than the generic one, and the Console can preselect
	// that tenant afterwards.
	//
	// ★ It is NOT an authorization input (決定 14). Anybody can type any slug; which
	// tenants a person may actually use is decided server-side from membership and
	// the tenant rules, so this value only ever picks a VIEW.
	Tnt string `json:"t,omitempty"`
	// Link marks a LINK flow (docs/log/61 §61.16): the identity the proven method is to
	// be attached to, plus the session that proved it. Unlike Tnt these ARE
	// authorization inputs, which is why they travel in the signed state and are
	// re-checked against the live session at the callback — a signed value only says
	// CP wrote it, not that the same person is still holding the browser.
	Link   string `json:"l,omitempty"`
	LEmail string `json:"le,omitempty"`
	LProv  string `json:"lp,omitempty"`
	LSub   string `json:"ls,omitempty"`
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
	tenant := sanitizeTenantSlug(r.URL.Query().Get("tenant"))
	p := c.providerFor(r.Context(), strings.TrimSpace(r.URL.Query().Get("provider")))
	if p == nil {
		loginRedirect(w, r, "provider", tenant)
		return
	}
	// A tenant-defined provider belongs to exactly one department, so carry ITS slug
	// rather than whatever the query said: an error then goes back to the page the
	// button actually lives on, and the post-login hint preselects the right tenant.
	if slug, _, ok := auth.ParseTenantProviderID(p.ID()); ok {
		tenant = slug
	}
	next := sanitizeNext(r.URL.Query().Get("next"))
	nonce := randHex(16)
	b, _ := json.Marshal(oauthState{
		Nonce: nonce, Next: next, Exp: time.Now().Add(10 * time.Minute).Unix(),
		Prov: p.ID(), Tnt: tenant,
	})
	c.setCookie(w, stateCookie, c.signCookie(b), 600)

	au, err := p.AuthorizeURL(r.Context(), nonce, c.oauthRedirectURI())
	if err != nil {
		// Discovery failed (unreachable/misconfigured issuer) — there is nowhere
		// to send the browser, so fall back to the login page with an error.
		log.Printf("oauth: provider %s authorize: %v", p.ID(), err)
		loginRedirect(w, r, "exchange", tenant)
		return
	}
	http.Redirect(w, r, au, http.StatusFound)
}

// sanitizeTenantSlug keeps a tenant hint to the character set slugs are minted
// from (sanitizeUser), so it can be pasted into a path or query without escaping
// surprises. Anything else becomes "" = the generic login.
func sanitizeTenantSlug(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 64 || s != sanitizeUser(s) {
		return ""
	}
	return s
}

func (c config) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	// Validate CSRF state from the signed, single-use state cookie.
	ck, err := r.Cookie(stateCookie)
	c.setCookie(w, stateCookie, "", -1) // burn it regardless
	if err != nil {
		loginRedirect(w, r, "session", "")
		return
	}
	payload, ok := c.verifyCookie(ck.Value)
	if !ok {
		loginRedirect(w, r, "session", "")
		return
	}
	var st oauthState
	if json.Unmarshal(payload, &st) != nil || time.Now().Unix() > st.Exp ||
		subtle.ConstantTimeCompare([]byte(st.Nonce), []byte(r.URL.Query().Get("state"))) != 1 {
		loginRedirect(w, r, "session", "")
		return
	}
	tenant := sanitizeTenantSlug(st.Tnt)
	// The state cookie is signed, but still resolve its provider id against the
	// configured set before branching on it (決定 8) — a provider that was
	// removed from the config must not keep serving callbacks.
	p := c.providerFor(r.Context(), st.Prov)
	if p == nil {
		if st.Link != "" {
			c.writeLinkResult(w, r, linkErrProv, "", sanitizeNext(st.Next))
			return
		}
		loginRedirect(w, r, "provider", tenant)
		return
	}
	// ★ A link flow branches here, before anything that would mint a session: the
	// person is already signed in and stays signed in as whoever they were
	// (docs/log/61 §61.16).
	if st.Link != "" {
		c.finishLink(w, r, st, p)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		loginRedirect(w, r, "denied", tenant)
		return
	}

	pr, err := p.Exchange(r.Context(), code, c.oauthRedirectURI())
	if err != nil {
		log.Printf("oauth: provider %s exchange: %v", p.ID(), err)
		if errors.Is(err, auth.ErrNotAllowed) { // e.g. a tid outside ALLOWED_TIDS
			loginRedirect(w, r, "forbidden", tenant)
		} else {
			loginRedirect(w, r, "exchange", tenant)
		}
		return
	}
	if pr.Email == "" || !pr.Verified {
		loginRedirect(w, r, "denied", tenant)
		return
	}
	// ★ Stamped here, from the provider CP resolved by id — not from anything the
	// exchange returned and not from the row a tenant typed (docs/log/61 §61.15).
	pr.Realm = auth.ProviderRealm(p)
	allowed, err := p.Allowed(r.Context(), pr)
	if err != nil || !allowed {
		loginRedirect(w, r, "forbidden", tenant)
		return
	}
	isNew, err := c.linkAfterLogin(r.Context(), pr)
	if errors.Is(err, store.ErrIdentityClaimed) {
		// A tenant-defined provider asserted an address that already belongs to an
		// account somebody has signed in as (docs/log/61 §61.11 rule 2'). No session is
		// issued: the remedy is to sign in the way that account already signs in.
		log.Printf("oauth: provider %s: refusing %s — the address belongs to an existing account", pr.Provider, pr.Email)
		loginRedirect(w, r, "email_taken", tenant)
		return
	}
	c.issueSession(w, pr)
	next := withTenantHint(sanitizeNext(st.Next), tenant)
	if isNew {
		c.writeNewAccountPage(w, r, pr, next)
		return
	}
	http.Redirect(w, r, next, http.StatusFound)
}

// withTenantHint appends ?tenant=<slug> to the post-login destination so the
// Console preselects the department whose login URL was used (docs/log/61 §61.10.4).
// It is a HINT: the Console only honours it if that tenant is among the
// memberships the server returned, and every API call is authorized server-side
// regardless (決定 14). An existing query string is preserved, and an existing
// tenant parameter wins so a deep link is never rewritten.
func withTenantHint(next, tenant string) string {
	if tenant == "" {
		return next
	}
	u, err := url.Parse(next)
	if err != nil {
		return next
	}
	q := u.Query()
	if q.Get("tenant") != "" {
		return next
	}
	q.Set("tenant", tenant)
	u.RawQuery = q.Encode()
	return u.String()
}

// linkAfterLogin binds the (provider, subject) that just signed in to a person and
// reports whether that created a NEW identity — i.e. whether this login landed in a
// different workspace from one the person may already have (docs/log/61 §61.5 の 3 行目).
//
// Binding here rather than leaving it to the first API request is what makes the
// answer available at all: by the next request the row exists and the login is no
// longer new. For an env provider it runs only where more than one sign-in button
// exists — with a single IdP a new identity just means a new colleague, and the
// notice would be noise on every deployment that predates this feature (受入条件 6).
//
// ★ For a TENANT-DEFINED provider it always runs, whatever the button count, because
// this is also where that login can be REFUSED: rule 2' returns errIdentityClaimed
// for an address that belongs to an existing account, and the caller must hear about
// it before a session cookie is written. Leaving it to the first API request would
// mean issuing the session and then failing every request with a 403 nobody can act
// on.
//
// ★ roleHint is suppressed for tenant-defined providers (決定 31): SUPER_ADMIN_EMAILS
// is matched on the email alone, and a subsidiary's IdP must not be able to hand out
// the deployment role by asserting the operator's address. The same suppression is
// applied again in upsertIdentity, which is the choke point for every OTHER path.
func (c config) linkAfterLogin(ctx context.Context, p auth.Principal) (bool, error) {
	if c.mgr == nil || c.mgr.store == nil {
		return false, nil
	}
	tenantDefined := auth.IsTenantProviderID(p.Provider)
	roleHint := c.mgr.roleHintFor(p.Email)
	if tenantDefined {
		roleHint = ""
	}
	_, isNew, err := c.mgr.store.LinkIdentity(ctx, store.IdentityLink{
		Provider: p.Provider, Subject: p.Subject, Realm: p.Realm,
		RealmClaim: p.RealmClaim, RealmSubject: p.RealmSubject, Email: p.Email,
		FallbackKey: sanitizeUser(p.Email), RoleHint: roleHint, EmailJoin: !tenantDefined,
	})
	switch {
	case errors.Is(err, store.ErrIdentityClaimed):
		return false, err
	case err != nil:
		// Not fatal to the login: the next request resolves the identity the usual
		// way. Only the notice is lost.
		log.Printf("oauth: link %s/%s: %v", p.Provider, p.Subject, err)
		return false, nil
	}
	// ★ The single-provider deployment used to skip this function entirely, and the
	// resolver wrote the row on the first API request instead. It now runs anyway,
	// because that path has no provider object and therefore no realm — and a row
	// with no realm cannot take part in rule 1.5 when the tenant later adds its own
	// GitHub (docs/log/61 §61.15). What stays suppressed is the NOTICE: with one button
	// there is no second account to have landed in by mistake, so announcing a new
	// account would be noise nobody can act on.
	if len(c.providers) < 2 && !tenantDefined {
		return false, nil
	}
	return isNew, nil
}

// writeNewAccountPage is the one page between a login that created a new account
// and the Console — docs/log/61 受入条件 3: never hand someone a second workspace
// silently. There is deliberately no "merge with my other account" action on it:
// being able to sign in to two accounts proves control of both, not that they
// belong to one person, and the merge could not be undone (§61.5). So the honest
// advice is to come back with the address they normally use.
func (c config) writeNewAccountPage(w http.ResponseWriter, r *http.Request, p auth.Principal, next string) {
	lang := auth.PreferredUILang(r)
	t := auth.LoginText[lang]
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	buttons := `<a class="gbtn" href="` + html.EscapeString(next) + `">` + t.NewContinue + "</a>\n" +
		`<a class="gbtn ghost" href="/oauth2/logout">` + t.NewSwitch + `</a>`
	page := strings.NewReplacer(
		"{{LANG}}", lang,
		"{{TITLE}}", t.NewTitle,
		"{{NOTE}}", t.NewNote,
		"{{ERROR}}", `<div class="msg">`+fmt.Sprintf(t.NewBody, html.EscapeString(p.Email))+`</div>`,
		"{{BUTTONS}}", buttons,
	).Replace(auth.LoginPageHTML)
	_, _ = w.Write([]byte(page))
}

func (c config) handleOAuthLogout(w http.ResponseWriter, r *http.Request) {
	c.setCookie(w, sessionCookie, "", -1)
	http.Redirect(w, r, "/login", http.StatusFound)
}

// loginRedirect sends the browser back to the login page, staying on the tenant's
// own page when the attempt started there so the person does not suddenly see a
// different set of buttons after a failure.
func loginRedirect(w http.ResponseWriter, r *http.Request, errCode, tenant string) {
	u := "/login"
	if tenant != "" {
		u += "/" + tenant
	}
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
		if ok, code := c.sessionAllowed(r.Context(), claims); !ok {
			if wantsHTML(r) {
				c.setCookie(w, sessionCookie, "", -1)
				loginRedirect(w, r, code, "")
			} else if code == "reauth" {
				// 401, not 403: the SPA's unauthenticated path sends the person to
				// /login, which is exactly the remedy here.
				writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "login required"})
			} else {
				writeAPIErr(w, &apiError{http.StatusForbidden, "forbidden", "email not allowed"})
			}
			return
		}
		r.Header.Set(c.mgr.emailHeader, claims.Email)
		// Hand the proven IdP subject to the identity resolution downstream, so a
		// person whose email changed at the IdP keeps their user_key — and with it
		// their workspace, home and secrets (docs/log/61 §61.5). Sessions minted before
		// P0 carry no subject, and loginRefFrom keeps ignoring those — but the
		// PROVIDER is set unconditionally, because the tenant gate (§61.9.4) has to
		// know which button was pressed even on a cookie that predates `sub`.
		r = r.WithContext(withLoginRef(r.Context(), loginRef{claims.provider(), claims.Sub}))
		next.ServeHTTP(w, r)
	})
}

// isAuthExempt（セッション無しで到達できるパス）は routes.go の除外レジストリに
// 移動した — 各 register 関数が自分の除外を宣言する（docs/log/23 P2-W1）。

// wantsHTML is true for top-level browser navigations (redirect to /login);
// everything else (XHR, WS) gets a 401 the SPA handles.
func wantsHTML(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

// --- login landing page ---------------------------------------------------

// handleLogin serves both /login and the per-tenant /login/{slug} (docs/log/61
// §61.9.3). The slug decides which buttons to show and nothing else.
//
// ★ An unknown slug renders the GENERIC page rather than a 404. A 404 would tell
// an unauthenticated visitor which department slugs exist, and the login page is
// the one surface reachable without a session. For the same reason the unknown-slug
// page must stay byte-identical to the tenant-less one — if only one of them applied
// the default tenant's rules, comparing the two would say whether a slug exists.
//
// ★ P7-1 (docs/log/61 §61.17.6 + 決定 42): the tenant-less page takes the DEFAULT
// tenant's hidden_providers — and ONLY those. This closes §61.15.13 ("hiding a
// button has no effect on the bare /login"), which held merely because that page
// belonged to no tenant.
//
// ★★ allowed_providers is deliberately NOT applied here, and that asymmetry is the
// whole safety of this change. loginButtons has a valve on the hidden filter (all
// hidden → ignore it) but NONE on the allowed filter: narrowing allowed_providers
// until nothing matches renders a page with no buttons at all. This is the one entry
// for people who belong to no tenant yet — a new super_admin, anyone not invited
// yet — and the rule that would restore it is behind withSuperAdmin, which needs a
// session, which needs this page. Applying it here would make it possible to lock a
// deployment out of itself with no remedy short of editing the database.
func (c config) handleLogin(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.session(r); ok { // already signed in
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	lang := auth.PreferredUILang(r)
	t := auth.LoginText[lang]
	title, note := t.Title, t.Note
	slug := sanitizeTenantSlug(r.PathValue("slug"))
	rules, known := c.mgr.tenantLogin.rulesForSlug(r.Context(), slug)
	if !known {
		// Unknown (or absent) slug: generic page, and no tenant is carried forward
		// — a typo must not pin the person to a department that does not exist.
		// ★ Only HiddenProviders survives from the default tenant; AllowedProviders
		// stays empty (= every enabled provider), see the note above. The slug stays
		// empty too, so nothing here reaches the state cookie: the buttons carry no
		// tenant, and the default tenant's own t:default:* rows are not appended
		// (決定 32-4 keeps that list off the generic page).
		slug, rules = "", store.TenantLoginRules{}
		if d, ok := c.mgr.tenantLogin.rulesForSlug(r.Context(), auth.DefaultTenantSlug); ok {
			rules.HiddenProviders = d.HiddenProviders
		}
	} else {
		name := rules.Name
		if name == "" {
			name = rules.Slug
		}
		// Only the display name — never the member count or anything else that would
		// turn the login page into a directory (§61.9.3).
		title = t.Title + " — " + name
		note = fmt.Sprintf(t.TenantNote, html.EscapeString(name))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	page := strings.NewReplacer(
		"{{LANG}}", lang,
		"{{TITLE}}", html.EscapeString(title),
		"{{NOTE}}", note,
		"{{ERROR}}", auth.LoginErrorBlock(r.URL.Query().Get("error"), lang),
		"{{BUTTONS}}", c.loginButtons(r.Context(), sanitizeNext(r.URL.Query().Get("next")), lang, slug,
			rules.AllowedProviders, rules.HiddenProviders),
	).Replace(auth.LoginPageHTML)
	_, _ = w.Write([]byte(page))
}

// loginButtons renders one sign-in button per enabled provider (docs/log/61 §61.6),
// narrowed to a tenant's allowed_providers when one was named.
//
// ★ The narrowing is cosmetic. The same rule is enforced again at tenant
// resolution (resolver.go), because a person can always go to the generic /login
// and press whichever button they like (決定 14).
//
// ★ A tenant's OWN providers (docs/log/61 §61.11) are appended only when a slug was
// named — /login/<slug>. On the generic page the full list would be a directory of
// the group's subsidiaries, readable by anyone who can reach the login (決定 32-4).
// They come last: the department's own button belongs next to the deployment-wide
// ones, and an operator who wants only that button says so in allowed_providers.
func (c config) loginButtons(ctx context.Context, next, lang, tenant string, allowed, hidden []string) string {
	providers := c.providers
	if tenant != "" && c.mgr != nil {
		providers = append(append([]auth.LoginProvider(nil), providers...),
			c.mgr.tenantIdP.ProvidersForSlug(ctx, tenant)...)
	}
	if len(providers) == 0 {
		return `<div class="err">` + auth.LoginText[lang].ErrUnconfigured + `</div>`
	}
	// ★ hidden_providers removes a button WITHOUT removing the method (docs/log/61
	// §61.15.9): a subsidiary that runs on its own GitHub still has to accept the
	// parent company's method for the colleague on loan, and that person signs in on
	// the generic /login. Applied only when something would remain — a page with no
	// buttons is a dead end, and the tenant's own mistake must not become one.
	visible := providers
	if len(hidden) > 0 {
		kept := make([]auth.LoginProvider, 0, len(providers))
		for _, p := range providers {
			if auth.ProviderInList(allowed, p.ID()) && !auth.ProviderInList(hidden, p.ID()) {
				kept = append(kept, p)
			}
		}
		if len(kept) > 0 {
			visible = kept
		}
	}
	var b strings.Builder
	shown := 0
	for _, p := range visible {
		if !auth.ProviderInList(allowed, p.ID()) {
			continue
		}
		shown++
		q := url.Values{"provider": {p.ID()}}
		if next != "/" {
			q.Set("next", next)
		}
		if tenant != "" {
			q.Set("tenant", tenant)
		}
		b.WriteString(`<a class="gbtn" href="/oauth2/login?` + html.EscapeString(q.Encode()) + `">`)
		b.WriteString(auth.ProviderIcon(p.ID()))
		b.WriteString(html.EscapeString(p.Label(lang)))
		b.WriteString("</a>\n")
	}
	if shown == 0 {
		// The tenant named providers this deployment does not have — an operator
		// error, and one nobody can work around from the page.
		return `<div class="err">` + auth.LoginText[lang].ErrTenantNoProvider + `</div>`
	}
	return b.String()
}
