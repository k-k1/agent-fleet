package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GitHub login adapter (docs/61 §61.7 + ADR0043). GitHub has no OIDC for user
// sign-in — its OIDC is the Actions token — so this is the one provider that gets
// protocol code of its own instead of riding on oauth_oidc.go.
//
// ★ What this door is, after the P1 revision (§61.5): "for people who have their
// company address on their GitHub account". There is no mechanism to merge two
// different emails into one person, so passing the org membership check with an
// out-of-domain primary email lands the person in a NEW identity — a new
// workspace, with the notice that says so. That is why an email allowlist on this
// provider is recommended: refusing at the door is kinder than letting someone
// work in a workspace they did not mean to create.
//
// Authorization here is two independent gates, both of which must pass:
//
//  1. org membership — GET /user/memberships/orgs/{org} says "active". Required:
//     AF_GITHUB_ALLOWED_ORGS with no orgs disables the provider, because GitHub was
//     only ever adopted *together with* the membership check (§61.3).
//  2. the email allowlist — the provider's own if it declares one, else the
//     deployment-wide one, and no email gate at all when the deployment configures
//     neither (the orgs are then the whole allowlist, which is a real one).
//
// The membership answer costs an API call, but Allowed() runs on every request
// (offboarding must not wait for the cookie to expire). So a positive answer is
// cached per subject for AF_GITHUB_MEMBERSHIP_TTL, and the person's access token
// is kept beside it so the entry can be refreshed when it goes stale. If GitHub is
// unreachable at refresh time, the last positive answer is honored for
// AF_GITHUB_MEMBERSHIP_GRACE and then denied — availability and fail-closed split
// down the middle (§61.7).
//
// ★ Tokens live in memory only, and are therefore lost on restart. A request whose
// subject has no cache entry cannot be judged at all, so it is refused with
// errNeedsReauth, which sends the browser back to /login with "sign in again"
// rather than "you are not allowed" — the person is not forbidden, we simply no
// longer hold what it takes to prove they are still in the org. GitHub usually
// completes that round trip without asking them anything.
const (
	githubProviderID = "github"

	githubWebBase = "https://github.com"
	githubAPIBase = "https://api.github.com"

	githubDefaultTTL   = 10 * time.Minute
	githubDefaultGrace = time.Hour

	// GitHub rejects API calls without a User-Agent, and read:org is what makes
	// the membership endpoint answer for organizations the person belongs to.
	githubUserAgent = "agent-fleet-control-plane"
	githubScopes    = "read:org user:email"
)

// errNeedsReauth denies a request without accusing the person: we lack the
// material to re-check them (see the file header), so the honest remedy is a fresh
// sign-in. authGate turns it into /login?error=reauth (or a 401 for the SPA)
// instead of the "not allowed" 403.
var errNeedsReauth = errors.New("re-authentication required")

// githubMembership is one person's cached authorization state. `token` is their
// OAuth access token, kept solely to refresh this entry.
type githubMembership struct {
	ok     bool
	token  string
	at     time.Time // when GitHub last gave an authoritative answer
	lastOK time.Time // when that answer was last a positive one (grace window)
}

type githubProvider struct {
	id           string
	labelJA      string
	labelEN      string
	clientID     string
	clientSecret string

	allowedOrgs []string // required; membership in ANY of them is enough

	// Email gate. A provider-specific list replaces the deployment-wide one
	// entirely; with neither configured the orgs are the only gate.
	allowEmails   map[string]bool
	allowDomains  map[string]bool
	deployAllowed func(email string) bool
	deployHasList bool

	ttl   time.Duration
	grace time.Duration

	webBase string // overridden in tests
	apiBase string
	client  *http.Client

	mu    sync.Mutex
	cache map[string]*githubMembership
}

func (p *githubProvider) ID() string { return p.id }

func (p *githubProvider) Label(lang string) string {
	if lang == "en" {
		return p.labelEN
	}
	return p.labelJA
}

// hasOwnAllowlist is true for every configured GitHub provider: the org list is an
// allowlist, so a deployment whose only provider is GitHub must not be warned that
// every login will be denied.
func (p *githubProvider) hasOwnAllowlist() bool { return len(p.allowedOrgs) > 0 }

func (p *githubProvider) httpClient() *http.Client {
	if p.client != nil {
		return p.client
	}
	return oidcHTTPClient
}

func (p *githubProvider) web() string {
	if p.webBase != "" {
		return strings.TrimRight(p.webBase, "/")
	}
	return githubWebBase
}

func (p *githubProvider) api() string {
	if p.apiBase != "" {
		return strings.TrimRight(p.apiBase, "/")
	}
	return githubAPIBase
}

// --- authorize --------------------------------------------------------------

func (p *githubProvider) AuthorizeURL(_ context.Context, state, redirectURI string) (string, error) {
	q := url.Values{
		"client_id":    {p.clientID},
		"redirect_uri": {redirectURI},
		"scope":        {githubScopes},
		"state":        {state},
	}
	return p.web() + "/login/oauth/authorize?" + q.Encode(), nil
}

// --- token exchange ---------------------------------------------------------

func (p *githubProvider) Exchange(ctx context.Context, code, redirectURI string) (principal, error) {
	token, err := p.exchangeCode(ctx, code, redirectURI)
	if err != nil {
		return principal{}, err
	}
	id, err := p.currentUserID(ctx, token)
	if err != nil {
		return principal{}, err
	}
	email, err := p.primaryVerifiedEmail(ctx, token)
	if err != nil {
		return principal{}, err
	}
	// Settle the membership question now, while we are certain to have a working
	// token, and seed the cache with it. An API failure here fails the login
	// rather than being cached as a denial: there is no last-known-good answer to
	// fall back on for someone signing in for the first time.
	member, err := p.memberOfAnyOrg(ctx, token)
	if err != nil {
		return principal{}, fmt.Errorf("github org membership: %w", err)
	}
	p.remember(id, token, member)
	// trust: "api" (§61.4) — the address came from /user/emails carrying its own
	// verified flag, which is the only GitHub source that has one.
	return principal{Provider: p.id, Subject: id, Email: email, Verified: true}, nil
}

// exchangeCode swaps the authorization code for an access token.
//
// ★ Without Accept: application/json GitHub answers this endpoint in
// application/x-www-form-urlencoded — a JSON decode then succeeds on nothing and
// yields an empty token rather than an error (§61.7).
func (p *githubProvider) exchangeCode(ctx context.Context, code, redirectURI string) (string, error) {
	form := url.Values{
		"client_id": {p.clientID}, "client_secret": {p.clientSecret},
		"code": {code}, "redirect_uri": {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.web()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", githubUserAgent)
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return "", fmt.Errorf("token endpoint: HTTP %d: %w", resp.StatusCode, err)
	}
	if tok.Error != "" {
		return "", fmt.Errorf("token endpoint: %s (%s)", tok.Error, tok.ErrorDesc)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("token endpoint: HTTP %d, no access_token in response", resp.StatusCode)
	}
	return tok.AccessToken, nil
}

// apiGet performs an authenticated REST call and decodes it into out. It reports
// the status code separately so callers can treat 403/404 as an answer rather than
// as a failure.
func (p *githubProvider) apiGet(ctx context.Context, token, path string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.api()+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", githubUserAgent)
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return 0, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return resp.StatusCode, nil
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("GET %s: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}

// currentUserID returns the numeric account id as the IdP subject.
//
// ★ Not `login`: a GitHub username can be changed and then claimed by somebody
// else, so keying an identity on it would hand over a workspace (§61.7).
func (p *githubProvider) currentUserID(ctx context.Context, token string) (string, error) {
	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	status, err := p.apiGet(ctx, token, "/user", &u)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("GET /user: HTTP %d", status)
	}
	if u.ID == 0 {
		return "", errors.New("GET /user: no account id in response")
	}
	return strconv.FormatInt(u.ID, 10), nil
}

// primaryVerifiedEmail returns the one address GitHub is willing to vouch for.
//
// ★ /user's `email` is not usable: it is null whenever the person keeps their
// address private, and it carries no verified flag — anyone can add someone else's
// company address to their account, so an unverified one must never reach the
// allowlist (§61.4).
func (p *githubProvider) primaryVerifiedEmail(ctx context.Context, token string) (string, error) {
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	status, err := p.apiGet(ctx, token, "/user/emails", &emails)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("GET /user/emails: HTTP %d (the user:email scope is required)", status)
	}
	for _, e := range emails {
		if e.Primary && e.Verified && strings.TrimSpace(e.Email) != "" {
			return strings.TrimSpace(e.Email), nil
		}
	}
	return "", fmt.Errorf("%w: the GitHub account has no primary verified email address", errNotAllowed)
}

// memberOfAnyOrg reports active membership in at least one allowed org. 404 (not a
// member) and 403 are answers, not failures; only a transport-level problem is an
// error, and only then so that the grace window in Allowed can apply.
func (p *githubProvider) memberOfAnyOrg(ctx context.Context, token string) (bool, error) {
	var lastErr error
	for _, org := range p.allowedOrgs {
		var m struct {
			State string `json:"state"`
		}
		status, err := p.apiGet(ctx, token, "/user/memberships/orgs/"+url.PathEscape(org), &m)
		switch {
		case err != nil:
			lastErr = err
		case status == http.StatusOK:
			if strings.EqualFold(m.State, "active") {
				return true, nil
			}
		case status == http.StatusForbidden:
			// ★ The "correct config, everybody rejected" trap (§61.7): an org that
			// restricts third-party OAuth apps hides its membership until an org
			// owner approves this app. Say so, or the operator has nothing to go on.
			log.Printf("WARNING: github: org %q returned 403 for a membership check — if the org restricts third-party OAuth apps, an org owner must approve this OAuth app before anyone can sign in", org)
		case status == http.StatusNotFound:
			// Not a member, or the org is invisible to this token. Either way: no.
		default:
			lastErr = fmt.Errorf("GET /user/memberships/orgs/%s: HTTP %d", org, status)
		}
	}
	return false, lastErr
}

// --- authorization ----------------------------------------------------------

func (p *githubProvider) remember(subject, token string, ok bool) {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		p.cache = map[string]*githubMembership{}
	}
	e := p.cache[subject]
	if e == nil {
		e = &githubMembership{}
		p.cache[subject] = e
	}
	e.ok, e.at = ok, now
	if token != "" {
		e.token = token
	}
	if ok {
		e.lastOK = now
	}
}

// emailAllowed applies gate 2 (see the file header). With no list anywhere the
// org membership is the whole allowlist, so this passes.
func (p *githubProvider) emailAllowed(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(email, '@')
	if email == "" || at < 0 {
		return false
	}
	if len(p.allowEmails) > 0 || len(p.allowDomains) > 0 {
		return p.allowEmails[email] || p.allowDomains[email[at+1:]]
	}
	if p.deployHasList && p.deployAllowed != nil {
		return p.deployAllowed(email)
	}
	return true
}

// Allowed runs both gates. The email one is first because it costs nothing and
// needs no token — a person outside the allowed domains never causes an API call.
func (p *githubProvider) Allowed(ctx context.Context, pr principal) (bool, error) {
	if !p.emailAllowed(pr.Email) {
		return false, nil
	}
	if pr.Subject == "" {
		return false, errNeedsReauth // a pre-P0 session cookie, which has no subject
	}

	p.mu.Lock()
	e := p.cache[pr.Subject]
	var snapshot githubMembership
	if e != nil {
		snapshot = *e
	}
	p.mu.Unlock()

	switch {
	case e == nil:
		// Nothing to judge them by (CP restarted, or this is another replica).
		return false, errNeedsReauth
	case time.Since(snapshot.at) < p.ttl:
		return snapshot.ok, nil
	case snapshot.token == "":
		return false, errNeedsReauth
	}

	member, err := p.memberOfAnyOrg(ctx, snapshot.token)
	if err != nil {
		// GitHub is unreachable. Honor the last positive answer for the grace
		// window, then deny — an outage must not be an open door forever, nor lock
		// everyone out the moment it starts.
		if !snapshot.lastOK.IsZero() && time.Since(snapshot.lastOK) < p.grace {
			log.Printf("github: membership re-check failed (%v) — honoring the last positive answer for %s more",
				err, (p.grace - time.Since(snapshot.lastOK)).Round(time.Second))
			return true, nil
		}
		return false, nil
	}
	p.remember(pr.Subject, "", member)
	return member, nil
}

// --- construction from the environment --------------------------------------

// newGitHubProvider builds the adapter from GITHUB_OAUTH_* / AF_GITHUB_*, or
// returns nil with a warning when the deployment did not (fully) configure it.
// Following 決定 11, an incomplete GitHub config disables GitHub only — it never
// stops CP from starting, because that would let one IdP's typo lock out the
// people signing in through the others.
// ★ GITHUB_OAUTH_CLIENT_ID is NOT ours alone: it already carries the OAuth App
// client_id of the GitHub *device flow* that connects git repos, and CP injects it
// into every workspace container (main.go). One OAuth App can serve both flows —
// scopes are granted per authorization, so the login asks for read:org+user:email
// while the device flow asks for repo — and the doc's env names assume exactly
// that. It only needs the callback URL <PUBLIC_BASE_URL>/oauth2/callback added.
// An operator who would rather keep the two apart (approving one app for an org
// approves it for both) sets AF_GITHUB_LOGIN_CLIENT_ID / _SECRET instead.
//
// ★ Which is why AF_GITHUB_ALLOWED_ORGS, not the client id, is the signal that a
// GitHub *login* was wanted at all: a deployment that has only ever used the
// device flow has GITHUB_OAUTH_CLIENT_ID set and must not be nagged at every
// startup about a feature it never asked for.
func newGitHubProvider(deployAllowed func(string) bool, deployHasList bool) *githubProvider {
	clientID := firstNonEmpty(os.Getenv("AF_GITHUB_LOGIN_CLIENT_ID"), os.Getenv("GITHUB_OAUTH_CLIENT_ID"))
	clientSecret := firstNonEmpty(os.Getenv("AF_GITHUB_LOGIN_CLIENT_SECRET"), os.Getenv("GITHUB_OAUTH_CLIENT_SECRET"))
	orgs := splitCSV(os.Getenv("AF_GITHUB_ALLOWED_ORGS"))
	switch {
	case len(orgs) == 0 && clientSecret == "":
		return nil // nothing here asks for a GitHub login: stay quiet
	case len(orgs) == 0:
		// ★ GitHub was adopted only together with the membership check (§61.3):
		// without it the door stands open to every GitHub account on earth whose
		// primary address happens to match the email allowlist.
		log.Printf("WARNING: github login disabled — AF_GITHUB_ALLOWED_ORGS is required (membership in one of those orgs is what authorizes a GitHub sign-in)")
		return nil
	case clientID == "" || clientSecret == "":
		log.Printf("WARNING: github login disabled — set both GITHUB_OAUTH_CLIENT_ID and GITHUB_OAUTH_CLIENT_SECRET (or AF_GITHUB_LOGIN_CLIENT_ID / AF_GITHUB_LOGIN_CLIENT_SECRET for an OAuth App used only for signing in)")
		return nil
	}
	for i, o := range orgs {
		orgs[i] = strings.ToLower(o)
	}
	p := &githubProvider{
		id:            githubProviderID,
		labelJA:       envOr("AF_GITHUB_LABEL_JA", "GitHub でサインイン"),
		labelEN:       envOr("AF_GITHUB_LABEL_EN", "Sign in with GitHub"),
		clientID:      clientID,
		clientSecret:  clientSecret,
		allowedOrgs:   orgs,
		allowEmails:   emailSet(os.Getenv("AF_GITHUB_ALLOWED_EMAILS")),
		allowDomains:  domainSet(os.Getenv("AF_GITHUB_ALLOWED_DOMAINS")),
		deployAllowed: deployAllowed,
		deployHasList: deployHasList,
		ttl:           parseDurationOr(os.Getenv("AF_GITHUB_MEMBERSHIP_TTL"), githubDefaultTTL),
		grace:         parseDurationOr(os.Getenv("AF_GITHUB_MEMBERSHIP_GRACE"), githubDefaultGrace),
		cache:         map[string]*githubMembership{},
	}
	if len(p.allowEmails) == 0 && len(p.allowDomains) == 0 && !deployHasList {
		log.Printf("WARNING: github login has no email allowlist (AF_GITHUB_ALLOWED_DOMAINS) — anyone in %s can sign in, and a member whose primary GitHub address is outside your company domain lands in a NEW workspace rather than their existing one",
			strings.Join(orgs, ", "))
	}
	return p
}
