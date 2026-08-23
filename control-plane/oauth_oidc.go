package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Generic OIDC login client (docs/61 §61.6 + ADR0043 決定 1). One implementation
// carries Entra ID / Okta / Keycloak / Auth0 / Cognito / GitLab — and Google, which
// is just the instance whose env names stayed GOOGLE_OAUTH_* so existing
// deployments need no config change. The flow is discovery -> authorize -> token
// -> userinfo, all with the standard library: no JWT dependency is added (受入条件 7).
//
// ★ We deliberately do NOT verify the id_token signature (ADR0043 決定 9). The
// token arrives as the response to a back-channel POST that CP itself made to the
// IdP's token endpoint, authenticated with client_secret over TLS — the same
// argument OIDC Core §3.1.3.7 makes for skipping signature validation on the code
// flow. Claims that userinfo does not return (Entra's `tid`) are therefore read
// out of the id_token payload without signature verification, and that is sound
// ONLY because the payload came back inside that same TLS response.
//
// ★ If a front-channel path is ever added (implicit / form_post, where the browser
// hands us an id_token), JWKS signature verification becomes mandatory — an
// attacker controls that channel. Do not reuse decodeIDTokenClaims there.

const (
	// email trust rules (docs/61 §61.4). "api" (a second API call carrying the
	// verified flag) belongs to the GitHub adapter (P2) and is not valid here.
	trustEmailVerified = "email_verified"
	trustIssuer        = "issuer"
	trustAPI           = "api"

	// Google's well-known endpoints, seeded statically so the historical
	// deployment path performs no discovery request at all (受入条件 6).
	googleIssuer       = "https://accounts.google.com"
	googleAuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL     = "https://oauth2.googleapis.com/token"
	googleUserinfoURL  = "https://openidconnect.googleapis.com/v1/userinfo"

	oidcDiscoveryTTL = 24 * time.Hour
)

// errNotAllowed marks an authorization failure inside Exchange (e.g. a `tid`
// outside AF_OIDC_<ID>_ALLOWED_TIDS) so the callback shows "forbidden" rather
// than the generic transport error.
var errNotAllowed = errors.New("principal not allowed")

// oidcHTTPClient bounds every IdP call: an unresponsive IdP must not pin a CP
// request goroutine (http.DefaultClient has no timeout).
var oidcHTTPClient = &http.Client{Timeout: 20 * time.Second}

type oidcEndpoints struct {
	Issuer    string `json:"issuer"`
	Authorize string `json:"authorization_endpoint"`
	Token     string `json:"token_endpoint"`
	Userinfo  string `json:"userinfo_endpoint"`
	// SubjectTypes is `subject_types_supported`. Nothing in the login flow reads it —
	// it is here for the admin API, which asks one question before a tenant registers
	// a SECOND app registration of a directory it already has: does this issuer hand
	// the same person a different `sub` per client (docs/61 §61.17.4 (b))?
	// Measured 2026-08-20: Google ["public"], Entra /common ["pairwise"].
	SubjectTypes []string `json:"subject_types_supported"`
}

// pairwiseSubjects reports an issuer that mints a per-client `sub`, i.e. one where
// two app registrations make the same person look like two people.
//
// ★ Read from discovery, never guessed from the issuer's hostname: "microsoftonline"
// is not a rule, it is a coincidence that holds until the next IdP.
func (ep oidcEndpoints) pairwiseSubjects() bool {
	for _, t := range ep.SubjectTypes {
		if strings.EqualFold(strings.TrimSpace(t), "pairwise") {
			return true
		}
	}
	return false
}

type oidcProvider struct {
	id           string
	labelJA      string
	labelEN      string
	issuer       string
	clientID     string
	clientSecret string
	trust        string
	scope        string
	prompt       string     // "" omits the prompt parameter
	extraAuth    url.Values // provider-specific authorize params (Google: access_type)
	// linkClaim names a STABLE claim to carry alongside `sub`, for rule 1.5's second
	// key (docs/61 §61.15.10 + 決定 38). Entra's `sub` is pairwise — different per app
	// registration — so two buttons onto one Entra tenant are two subjects; `oid` is
	// the same value in both. "" (the default) takes no part in the rule.
	linkClaim string

	// Authorization. A provider-specific allowlist replaces the DEPLOYMENT-WIDE
	// list entirely (docs/61 §61.8: "未設定なら共通の許可リストを使う") — that is a
	// per-provider narrowing operators rely on and P3 did not change it.
	// dbAllowed is the separate, database-derived term P3 adds on top of whichever
	// of the two applies: see Allowed.
	allowedTIDs   map[string]bool
	allowEmails   map[string]bool
	allowDomains  map[string]bool
	deployAllowed func(email string) bool
	dbAllowed     func(ctx context.Context, email string) bool

	client *http.Client

	mu    sync.Mutex
	ep    oidcEndpoints
	epExp time.Time // discovery cache expiry; zero+filled ep = seeded statically
}

func (p *oidcProvider) ID() string { return p.id }

// Label falls back per language: a provider that declared only AF_OIDC_<ID>_LABEL_JA
// still gets a readable English button (generated from the id) rather than Japanese
// text on an English page.
func (p *oidcProvider) Label(lang string) string {
	if lang == "en" && p.labelEN != "" {
		return p.labelEN
	}
	if lang != "en" && p.labelJA != "" {
		return p.labelJA
	}
	return defaultProviderLabel(p.id, lang)
}

// issuerURL names the identity source for the admin list (login_provider_api.go).
// Not a credential: the same URL is in the deployment's own discovery traffic.
func (p *oidcProvider) issuerURL() string { return p.issuer }

// hasOwnAllowlist reports whether this provider carries its own allowlist (used
// by the startup warning about a deployment where every login would be denied).
func (p *oidcProvider) hasOwnAllowlist() bool {
	return len(p.allowEmails) > 0 || len(p.allowDomains) > 0
}

// --- discovery -------------------------------------------------------------

func (p *oidcProvider) httpClient() *http.Client {
	if p.client != nil {
		return p.client
	}
	return oidcHTTPClient
}

// seedEndpoints installs endpoints statically (no discovery). Used for Google,
// whose endpoints this code has always had hardcoded.
func (p *oidcProvider) seedEndpoints(ep oidcEndpoints) {
	p.ep, p.epExp = ep, time.Now().Add(100*365*24*time.Hour)
}

// endpoints returns the provider's endpoints, running (and caching) OIDC
// discovery when they are not already known. Discovery is lazy on purpose: doing
// it at startup would make CP boot depend on the IdP being reachable.
func (p *oidcProvider) endpoints(ctx context.Context) (oidcEndpoints, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ep.Authorize != "" && p.ep.Token != "" && time.Now().Before(p.epExp) {
		return p.ep, nil
	}
	du := strings.TrimRight(p.issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, du, nil)
	if err != nil {
		return oidcEndpoints{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return oidcEndpoints{}, fmt.Errorf("discovery %s: %w", du, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return oidcEndpoints{}, fmt.Errorf("discovery %s: HTTP %d", du, resp.StatusCode)
	}
	var ep oidcEndpoints
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ep); err != nil {
		return oidcEndpoints{}, fmt.Errorf("discovery %s: %w", du, err)
	}
	if ep.Authorize == "" || ep.Token == "" {
		return oidcEndpoints{}, fmt.Errorf("discovery %s: no authorization/token endpoint", du)
	}
	// The discovered issuer must be the configured one. The one exception is
	// Entra's multi-tenant document, which returns the literal template
	// "https://login.microsoftonline.com/{tenantid}/v2.0" — that configuration is
	// only reachable at all when ALLOWED_TIDS is set (決定 7), which is the real
	// check there.
	if ep.Issuer != "" && strings.TrimRight(ep.Issuer, "/") != strings.TrimRight(p.issuer, "/") &&
		!strings.Contains(ep.Issuer, "{tenantid}") {
		return oidcEndpoints{}, fmt.Errorf("discovery %s: issuer mismatch (got %q)", du, ep.Issuer)
	}
	p.ep, p.epExp = ep, time.Now().Add(oidcDiscoveryTTL)
	return ep, nil
}

// --- authorize -------------------------------------------------------------

func (p *oidcProvider) AuthorizeURL(ctx context.Context, state, redirectURI string) (string, error) {
	ep, err := p.endpoints(ctx)
	if err != nil {
		return "", err
	}
	q := url.Values{
		"client_id":     {p.clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {p.scope},
		"state":         {state},
	}
	if p.prompt != "" {
		q.Set("prompt", p.prompt)
	}
	for k, vs := range p.extraAuth {
		q[k] = vs
	}
	sep := "?"
	if strings.Contains(ep.Authorize, "?") {
		sep = "&"
	}
	return ep.Authorize + sep + q.Encode(), nil
}

// --- token exchange --------------------------------------------------------

// flexBool decodes a JSON boolean that some IdPs send as the string "true".
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	switch s {
	case "true":
		*b = true
	case "false", "null", "":
		*b = false
	default:
		return fmt.Errorf("not a boolean: %s", string(data))
	}
	return nil
}

// oidcClaims is the subset of standard claims we read, from either the id_token
// payload or the userinfo response.
type oidcClaims struct {
	Sub               string   `json:"sub"`
	Email             string   `json:"email"`
	EmailVerified     flexBool `json:"email_verified"`
	PreferredUsername string   `json:"preferred_username"`
	UPN               string   `json:"upn"` // Entra's user principal name
	TID               string   `json:"tid"` // Entra tenant id (never in userinfo)
	// raw is the same payload as a plain object. AF_OIDC_<ID>_LINK_CLAIM and
	// tenant_idp.link_claim name a claim at RUNTIME, so it cannot be a struct field
	// — see claimString.
	raw map[string]any
}

// claimString reads a named claim, and only when it is a string. A number or an
// object is not a stable identifier we can compare as text, and quietly formatting
// one would make the key depend on Go's float formatting — a silent way for two
// logins of the same person to disagree.
func claimString(raw map[string]any, name string) string {
	if raw == nil || name == "" {
		return ""
	}
	s, _ := raw[name].(string)
	return strings.TrimSpace(s)
}

// decodeIDTokenClaims reads an id_token payload WITHOUT verifying its signature.
// See the file header: this is only sound for a token received as the body of our
// own back-channel token request. Never call it on a front-channel token.
func decodeIDTokenClaims(idToken string) (oidcClaims, error) {
	var c oidcClaims
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return c, errors.New("id_token: not a JWS compact serialization")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return c, fmt.Errorf("id_token payload: %w", err)
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, fmt.Errorf("id_token payload: %w", err)
	}
	// A second pass for the claims nobody named at compile time (linkClaim). It is
	// best-effort: the struct above is what the login itself runs on.
	_ = json.Unmarshal(payload, &c.raw)
	return c, nil
}

func (p *oidcProvider) Exchange(ctx context.Context, code, redirectURI string) (principal, error) {
	ep, err := p.endpoints(ctx)
	if err != nil {
		return principal{}, err
	}
	form := url.Values{
		"code": {code}, "client_id": {p.clientID}, "client_secret": {p.clientSecret},
		"redirect_uri": {redirectURI}, "grant_type": {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return principal{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return principal{}, fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok)
	if tok.Error != "" {
		return principal{}, fmt.Errorf("token endpoint: %s (%s)", tok.Error, tok.ErrorDesc)
	}
	if tok.AccessToken == "" && tok.IDToken == "" {
		return principal{}, fmt.Errorf("token endpoint: HTTP %d, no token in response", resp.StatusCode)
	}

	// id_token first: it carries claims userinfo omits (Entra's tid), and it lets
	// a userinfo hiccup not fail an otherwise complete login.
	var idc oidcClaims
	if tok.IDToken != "" {
		if idc, err = decodeIDTokenClaims(tok.IDToken); err != nil {
			log.Printf("oauth: provider %s: %v (continuing with userinfo)", p.id, err)
		}
	}
	// ★ Tenant pinning (決定 7): with ALLOWED_TIDS set, the token must name one of
	// those tenants. A missing tid is a denial, not a pass — a personal Microsoft
	// account can rewrite its own email, so this is what keeps the email allowlist
	// meaningful on the common/organizations endpoints.
	if len(p.allowedTIDs) > 0 {
		if idc.TID == "" || !p.allowedTIDs[strings.ToLower(idc.TID)] {
			return principal{}, fmt.Errorf("%w: tid %q not in AF_OIDC_%s_ALLOWED_TIDS", errNotAllowed, idc.TID, strings.ToUpper(p.id))
		}
	}

	uic, uiOK := p.userinfo(ctx, ep, tok.AccessToken)

	pr := principal{Provider: p.id}
	pr.Subject = firstNonEmpty(uic.Sub, idc.Sub)
	pr.Email = firstNonEmpty(uic.Email, idc.Email, emailLike(idc.PreferredUsername), emailLike(idc.UPN))
	// ★ Rule 1.5's second key, read straight out of the token this exchange returned
	// (docs/61 §61.15.10). The provider names the CLAIM; the VALUE is never taken from
	// anywhere else — a tenant that could supply it could name another person. A claim
	// the IdP did not send leaves BOTH fields empty, so the row takes no part in the
	// rule rather than matching every other row that is also missing it.
	if p.linkClaim != "" {
		if v := firstNonEmpty(claimString(uic.raw, p.linkClaim), claimString(idc.raw, p.linkClaim)); v != "" {
			pr.RealmClaim, pr.RealmSubject = p.linkClaim, v
		} else {
			log.Printf("oauth: provider %s: no %q claim in the token (rule 1.5 will fall back to sub)", p.id, p.linkClaim)
		}
	}
	switch p.trust {
	case trustIssuer:
		// The issuer is pinned (single-tenant issuer URL, or tid checked above),
		// so the email this IdP hands out is the tenant's own directory value.
		// Entra never emits email_verified, which is exactly why this rule exists.
		pr.Verified = pr.Email != ""
	default: // trustEmailVerified
		if uiOK && uic.Email != "" {
			pr.Verified = bool(uic.EmailVerified)
		} else {
			pr.Verified = bool(idc.EmailVerified)
		}
	}
	if pr.Email == "" {
		return principal{}, errors.New("no email claim from id_token or userinfo")
	}
	return pr, nil
}

// userinfo fetches the userinfo endpoint. Failures are not fatal: the id_token
// from the same token response is an equally trustworthy source for the claims we
// need, so a userinfo blip must not deny a valid sign-in.
func (p *oidcProvider) userinfo(ctx context.Context, ep oidcEndpoints, accessToken string) (oidcClaims, bool) {
	var c oidcClaims
	if ep.Userinfo == "" || accessToken == "" {
		return c, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.Userinfo, nil)
	if err != nil {
		return c, false
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		log.Printf("oauth: provider %s userinfo: %v", p.id, err)
		return c, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("oauth: provider %s userinfo: HTTP %d", p.id, resp.StatusCode)
		return c, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		log.Printf("oauth: provider %s userinfo: %v", p.id, err)
		return c, false
	}
	if err := json.Unmarshal(body, &c); err != nil {
		log.Printf("oauth: provider %s userinfo: %v", p.id, err)
		return c, false
	}
	_ = json.Unmarshal(body, &c.raw) // the runtime-named claims — see decodeIDTokenClaims
	return c, true
}

// Allowed is the entry gate for this provider: a union taken strictly WITHIN the
// email axis (docs/61 §61.9.6, revised in P3).
//
//	(this provider's own email list, or — when it declares none — the
//	 deployment-wide list)
//	OR (an auto-join domain matches, or the person holds a membership)
//
// ★ Two things this shape is careful about.
//
// First, the provider-specific list still REPLACES the deployment-wide list rather
// than adding to it. Union-ing those two would silently widen a provider an
// operator had deliberately narrowed ("Google is company-wide, Entra only for the
// subsidiary domain") — a regression against P0's documented behaviour.
//
// Second, only the email axis is union-ed. Terms of a different kind stay AND-ed:
// the GitHub adapter checks org membership separately, so holding an Agent Fleet
// membership can never be a way past its org gate (決定 2).
func (p *oidcProvider) Allowed(ctx context.Context, pr principal) (bool, error) {
	email := strings.ToLower(strings.TrimSpace(pr.Email))
	at := strings.LastIndexByte(email, '@')
	if email == "" || at < 0 {
		return false, nil
	}
	if p.hasOwnAllowlist() {
		if p.allowEmails[email] || p.allowDomains[email[at+1:]] {
			return true, nil
		}
	} else if p.deployAllowed != nil && p.deployAllowed(email) {
		return true, nil
	}
	return p.dbAllowed != nil && p.dbAllowed(ctx, email), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// emailLike passes a claim through only when it looks like an address — Entra's
// preferred_username is usually the UPN (an email) but may be a bare name.
func emailLike(s string) string {
	if i := strings.LastIndexByte(s, '@'); i > 0 && i < len(s)-1 {
		return s
	}
	return ""
}

// --- construction from the environment -------------------------------------

// newGoogleProvider builds the historical Google login as one instance of the
// generic client. Its env names, scope, prompt and endpoints are unchanged, so an
// existing deployment behaves exactly as before (受入条件 6).
func newGoogleProvider(clientID, clientSecret string, deployAllowed func(string) bool, dbAllowed func(context.Context, string) bool) *oidcProvider {
	p := &oidcProvider{
		id:            googleProviderID,
		labelJA:       loginText["ja"].signin,
		labelEN:       loginText["en"].signin,
		issuer:        googleIssuer,
		clientID:      clientID,
		clientSecret:  clientSecret,
		trust:         trustEmailVerified,
		scope:         "openid email",
		prompt:        "select_account",
		extraAuth:     url.Values{"access_type": {"online"}},
		deployAllowed: deployAllowed,
		dbAllowed:     dbAllowed,
	}
	p.seedEndpoints(oidcEndpoints{
		Issuer: googleIssuer, Authorize: googleAuthorizeURL,
		Token: googleTokenURL, Userinfo: googleUserinfoURL,
	})
	return p
}

// oidcEnv reads AF_OIDC_<ID>_<KEY>. A provider id may contain "-" (env names
// cannot), so it is folded to "_" for the lookup: id "entra-id" -> AF_OIDC_ENTRA_ID_*.
func oidcEnv(id, key string) string {
	return strings.TrimSpace(os.Getenv("AF_OIDC_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_" + key))
}

// validProviderID keeps ids to what can round-trip through an env var name and a
// URL query parameter.
func validProviderID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '-' || r == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// validIssuerURL requires an https issuer, with the customary loopback carve-out
// so a developer can point at a local Keycloak/Dex over http.
func validIssuerURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return false
}

// multiTenantIssuer reports an Entra-style issuer that accepts *any* Microsoft
// tenant (or personal accounts). Such a deployment is only safe with an explicit
// tenant allowlist — see buildLoginProviders (決定 7).
func multiTenantIssuer(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil {
		return false
	}
	for _, seg := range strings.Split(u.Path, "/") {
		switch strings.ToLower(seg) {
		case "common", "organizations", "consumers":
			return true
		}
	}
	return false
}

// buildLoginProviders assembles the enabled login providers in display order:
// Google first (when its historical env is set), then AF_OIDC_PROVIDERS in the
// order listed.
//
// Failure policy (ADR0043 決定 11): a provider whose own config is incomplete is
// dropped with a warning — one broken IdP must never lock everybody out. The one
// exception is the multi-tenant Entra hazard, which returns an error and is fatal
// at startup, because ignoring it would silently put "anyone with a Microsoft
// account" in front of an email allowlist that can be spoofed (決定 7).
func buildLoginProviders(c config) ([]loginProvider, error) {
	var out []loginProvider
	seen := map[string]bool{}

	switch {
	case c.googleClientID != "" && c.googleClientSecret != "":
		out = append(out, newGoogleProvider(c.googleClientID, c.googleClientSecret, c.emailAllowed, c.tenantEmailAllowed))
		seen[googleProviderID] = true
	case c.googleClientID != "" || c.googleClientSecret != "":
		log.Printf("WARNING: google login disabled — set both GOOGLE_OAUTH_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET")
	}

	for _, raw := range splitCSV(os.Getenv("AF_OIDC_PROVIDERS")) {
		id := strings.ToLower(raw)
		if !validProviderID(id) {
			log.Printf("WARNING: AF_OIDC_PROVIDERS: ignoring invalid provider id %q (allowed: a-z 0-9 - _)", raw)
			continue
		}
		if seen[id] {
			log.Printf("WARNING: AF_OIDC_PROVIDERS: provider %q listed twice (or already configured) — ignoring the duplicate", id)
			continue
		}
		seen[id] = true

		issuer := oidcEnv(id, "ISSUER")
		tids := emailSet(oidcEnv(id, "ALLOWED_TIDS")) // plain lowercased CSV set (tenant GUIDs)
		// ★ Checked before the "disable on missing config" rules below so a
		// dangerous issuer can never be waved through by an unrelated typo.
		if issuer != "" && multiTenantIssuer(issuer) && len(tids) == 0 {
			return nil, fmt.Errorf("AF_OIDC_%s_ISSUER is a multi-tenant endpoint (%s): set AF_OIDC_%s_ALLOWED_TIDS, or pin the issuer to one tenant — otherwise every Microsoft account in the world reaches the login, and personal accounts can rewrite their own email",
				strings.ToUpper(id), issuer, strings.ToUpper(id))
		}
		if !validIssuerURL(issuer) {
			log.Printf("WARNING: login provider %q disabled — AF_OIDC_%s_ISSUER must be the IdP's https issuer URL (http is accepted only for loopback, e.g. a local Keycloak)", id, strings.ToUpper(id))
			continue
		}
		if strings.HasPrefix(issuer, "http://") {
			log.Printf("WARNING: login provider %q uses a plaintext http issuer (%s) — acceptable for local development only; the client_secret and tokens are not protected", id, issuer)
		}
		clientID, clientSecret := oidcEnv(id, "CLIENT_ID"), oidcEnv(id, "CLIENT_SECRET")
		if clientID == "" || clientSecret == "" {
			log.Printf("WARNING: login provider %q disabled — AF_OIDC_%s_CLIENT_ID / AF_OIDC_%s_CLIENT_SECRET are required", id, strings.ToUpper(id), strings.ToUpper(id))
			continue
		}
		// ★ fail-closed: a provider that does not declare how it justifies the
		// email it hands us is refused, never defaulted (docs/61 §61.4).
		trust := strings.ToLower(oidcEnv(id, "TRUST"))
		switch trust {
		case trustEmailVerified, trustIssuer:
		case trustAPI:
			log.Printf("WARNING: login provider %q disabled — AF_OIDC_%s_TRUST=api is the GitHub adapter's rule (P2) and is not valid for OIDC", id, strings.ToUpper(id))
			continue
		default:
			log.Printf("WARNING: login provider %q disabled — AF_OIDC_%s_TRUST must be %q (the IdP asserts email_verified) or %q (the issuer is pinned to one tenant, e.g. Entra ID)", id, strings.ToUpper(id), trustEmailVerified, trustIssuer)
			continue
		}

		p := &oidcProvider{
			id:            id,
			labelJA:       oidcEnv(id, "LABEL_JA"),
			labelEN:       oidcEnv(id, "LABEL_EN"),
			issuer:        issuer,
			clientID:      clientID,
			clientSecret:  clientSecret,
			trust:         trust,
			scope:         envOr("AF_OIDC_"+strings.ToUpper(strings.ReplaceAll(id, "-", "_"))+"_SCOPES", "openid email profile"),
			prompt:        "select_account",
			allowedTIDs:   tids,
			allowEmails:   emailSet(oidcEnv(id, "ALLOWED_EMAILS")),
			allowDomains:  domainSet(oidcEnv(id, "ALLOWED_DOMAINS")),
			deployAllowed: c.emailAllowed,
			dbAllowed:     c.tenantEmailAllowed,
		}
		// ★ AF_OIDC_<ID>_LINK_CLAIM accepts ANY claim name, unlike the tenant column,
		// which is whitelisted (docs/61 §61.15.10). The difference is who is speaking:
		// this is the operator's own deployment file, and an operator who wanted to
		// join accounts by email could simply set the allowlist to do it. The hazard is
		// still real — naming `email` here makes rule 1.5 an email join for every
		// provider sharing that realm — so the guide says so in as many words.
		p.linkClaim = strings.ToLower(oidcEnv(id, "LINK_CLAIM"))
		if v := oidcEnv(id, "PROMPT"); v != "" {
			p.prompt = strings.TrimSpace(strings.ToLower(v))
			if p.prompt == "none" || p.prompt == "-" {
				p.prompt = "" // omit the parameter entirely
			}
		}
		out = append(out, p)
	}

	// GitHub last: it is the conditional door (§61.7), and putting the OIDC
	// providers — where the company's own directory lives — above it keeps the
	// button people should normally press at the top.
	if seen[githubProviderID] {
		log.Printf("WARNING: %q is the GitHub adapter's reserved id and cannot be used for an OIDC provider — the GitHub login is disabled", githubProviderID)
	} else if gh := newGitHubProvider(c.emailAllowed, c.tenantEmailAllowed, c.hasDeploymentAllowlist()); gh != nil {
		out = append(out, gh)
	}
	return out, nil
}

// providerAllowlister is implemented by providers that can carry an allowlist of
// their own (an email list for OIDC, the org list for GitHub).
type providerAllowlister interface{ hasOwnAllowlist() bool }

// anyProviderAllowlist reports whether at least one provider carries its own
// allowlist — with none, and no deployment-wide list, every login is denied.
func anyProviderAllowlist(ps []loginProvider) bool {
	for _, p := range ps {
		if a, ok := p.(providerAllowlister); ok && a.hasOwnAllowlist() {
			return true
		}
	}
	return false
}
