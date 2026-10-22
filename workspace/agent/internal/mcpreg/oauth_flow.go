package mcpreg

// OAuth sign-in for registered MCP servers (ADR 0119 decisions 3-5, 10). The Control Plane
// owns the public callback and the CSRF state; everything that touches a secret happens here:
//
//	StartOAuth     discover, pick the client identity, keep the PKCE verifier, return the
//	               authorization URL
//	CompleteOAuth  validate iss, exchange the code with the verifier, store the grant
//
// Prohibitions this file keeps: a verifier, code or token is never logged, put in an error
// message or returned to the caller (the Control Plane forwards what it gets, so anything
// returned here can reach the browser); every request to the authorization server goes
// through oauthHTTP, which follows no redirect (a 307 would replay a client secret or an
// authorization code to wherever the AS points).

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// OAuthRegPreregistered is the registration path of a client id the member entered.
const OAuthRegPreregistered = "preregistered"

// Wire codes of OAuthError. The Console and the Control Plane's callback redirect map each to
// its own text; add, never rename.
const (
	OAuthErrNotFound         = "mcp_not_found"
	OAuthErrNotOAuthServer   = "not_oauth_server"
	OAuthErrNotRequired      = "oauth_not_required"
	OAuthErrDiscovery        = "oauth_discovery_failed"
	OAuthErrEndpoints        = "as_endpoints_unusable"
	OAuthErrClientIDNeeded   = "client_id_needed"
	OAuthErrRegistration     = "registration_failed"
	OAuthErrBadRequest       = "bad_request"
	OAuthErrStateUnknown     = "state_unknown"
	OAuthErrStateMismatch    = "state_mismatch"
	OAuthErrDenied           = "authorization_denied"
	OAuthErrNoCode           = "no_code"
	OAuthErrIssMismatch      = "iss_mismatch"
	OAuthErrIssMissing       = "iss_missing"
	OAuthErrTokenExchange    = "token_exchange_failed"
	OAuthErrInternal         = "store_failed"
	oauthStateTTL            = 10 * time.Minute
	maxPendingAuth           = 128
	oauthHTTPTimeout         = 15 * time.Second
	maxOAuthBody             = 64 << 10
	maxScopeLen              = 1024
	pkceVerifierBytes        = 32
	minStateLen, maxStateLen = 16, 256
)

// OAuthError is a refusal with a stable code. Message is safe to show to the member: it never
// carries a value the AS or the caller sent.
type OAuthError struct {
	Status  int
	Code    string
	Message string
	// Issuer and Registration say which authorization server and client path the refusal
	// concerns, when that is known.
	Issuer       string
	Registration string
}

func (e *OAuthError) Error() string { return e.Code + ": " + e.Message }

func oerr(status int, code, msg string) *OAuthError {
	return &OAuthError{Status: status, Code: code, Message: msg}
}

// Seams for tests; production values only.
var (
	oauthNow      = time.Now
	updateSecrets = secrets.Update
	// oauthHTTP is the client for every request to an authorization server: the process's
	// default transport (the workspace's own egress, as the probe), no redirects.
	oauthHTTP = func() *http.Client {
		return &http.Client{
			Timeout:       oauthHTTPTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
)

// StartRequest is what the Control Plane sends to prepare an authorization URL.
type StartRequest struct {
	State       string `json:"state"`
	RedirectURI string `json:"redirectUri"`
	// CIMDURL is the deployment's client ID metadata document; empty when it has no https
	// PUBLIC_BASE_URL, which takes CIMD off the list.
	CIMDURL    string `json:"cimdUrl,omitempty"`
	ClientName string `json:"clientName,omitempty"`
	// Registration forces one path: "dcr" (the retry after a CIMD failure), "cimd" or
	// "preregistered". Empty follows the specification's order.
	Registration string `json:"registration,omitempty"`
	// Scope replaces the challenge's scope for this request (a step-up).
	Scope string `json:"scope,omitempty"`
}

// StartResult is what the Console needs to open the authorization page and to say how the
// client was identified.
type StartResult struct {
	AuthorizeURL string `json:"authorizeUrl"`
	ServerName   string `json:"serverName"`
	Issuer       string `json:"issuer"`
	Registration string `json:"registration"`
	Scope        string `json:"scope,omitempty"`
}

// pendingAuth is one authorization in flight, bound to its state. It lives in memory only:
// the verifier must not reach disk, and a restart inside the 10 minutes costs a new sign-in.
type pendingAuth struct {
	serverID, serverName string
	issuer               string
	issParam             bool
	registration         string
	clientID, secret     string
	tokenAuth            string
	tokenEndpoint        string
	revocationEndpoint   string
	resource             string
	scope                string
	redirectURI          string
	verifier             string
	expires              time.Time
}

var (
	pendingMu    sync.Mutex
	pendingAuths = map[string]*pendingAuth{}
	// dcrMu serializes dynamic registrations so two sign-ins started together register one
	// client, not two.
	dcrMu sync.Mutex
)

func putPending(state string, p *pendingAuth) *OAuthError {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	now := oauthNow()
	for k, v := range pendingAuths {
		if now.After(v.expires) {
			delete(pendingAuths, k)
		}
	}
	if len(pendingAuths) >= maxPendingAuth {
		return oerr(http.StatusTooManyRequests, OAuthErrBadRequest, "too many sign-ins in progress")
	}
	if _, dup := pendingAuths[state]; dup {
		return oerr(http.StatusBadRequest, OAuthErrBadRequest, "state already in use")
	}
	pendingAuths[state] = p
	return nil
}

// takePending removes and returns the pending authorization for state: exactly once, expired
// or not.
func takePending(state string) (*pendingAuth, bool) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	p, ok := pendingAuths[state]
	delete(pendingAuths, state)
	if !ok || oauthNow().After(p.expires) {
		return nil, false
	}
	return p, true
}

func validState(s string) bool {
	if len(s) < minStateLen || len(s) > maxStateLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// validScope reports whether s is a well-formed scope parameter (RFC 6749 §3.3).
func validScope(s string) bool {
	if len(s) > maxScopeLen {
		return false
	}
	for _, tok := range strings.Fields(s) {
		for i := 0; i < len(tok); i++ {
			c := tok[i]
			if c < 0x21 || c == 0x22 || c == 0x5c || c > 0x7e {
				return false
			}
		}
	}
	return true
}

func redirectURIUsable(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.Fragment == "" && u.User == nil &&
		(u.Scheme == "https" || (u.Scheme == "http" && isLoopbackHost(u.Hostname())))
}

// selectScope is the scope of the first authorization request (ADR 0119 decision 4): the
// challenge's scope when it has one, else the protected resource's scopes_supported, plus
// offline_access when the authorization server lists it.
func selectScope(challenge string, prmScopes, asScopes []string) string {
	var scopes []string
	if f := strings.Fields(challenge); len(f) > 0 {
		scopes = f
	} else {
		scopes = append(scopes, prmScopes...)
	}
	has := func(s string) bool {
		for _, x := range scopes {
			if x == s {
				return true
			}
		}
		return false
	}
	if !has("offline_access") {
		for _, s := range asScopes {
			if s == "offline_access" {
				scopes = append(scopes, s)
				break
			}
		}
	}
	return strings.Join(scopes, " ")
}

func unionScope(a, b string) string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(strings.Fields(a), strings.Fields(b)...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return strings.Join(out, " ")
}

func pkcePair() (verifier, challenge string, err error) {
	b := make([]byte, pkceVerifierBytes)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// discoverForSignIn asks the server for its challenge the way the connection test does (the
// stateless server/discover call, with the definition's static headers) and runs the P1
// discovery on the answer.
func discoverForSignIn(ctx context.Context, d ServerDef) (OAuthReport, bool) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	hdr := http.Header{}
	if b, err := json.Marshal(statelessReq(1, "server/discover")); err == nil {
		if req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(b)); err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
			req.Header.Set("Mcp-Method", "server/discover")
			for k, v := range d.Headers {
				req.Header.Set(k, v)
			}
			if resp, err := sameOriginClient(d.URL).Do(req); err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxProbeBody))
				resp.Body.Close()
				if resp.StatusCode == http.StatusUnauthorized {
					hdr = resp.Header
				}
			}
		}
	}
	return probeOAuth(ctx, d.URL, hdr)
}

// sameOriginClient follows redirects only within the origin of raw: the definition's static
// headers go with the request and must not follow a redirect to another host.
func sameOriginClient(raw string) *http.Client {
	start, _ := url.Parse(raw)
	return &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if start == nil || r.URL.Scheme != start.Scheme || r.URL.Host != start.Host || len(via) > maxMetadataHops {
			return http.ErrUseLastResponse
		}
		return nil
	}}
}

func endpointUsable(raw string) bool {
	u, err := url.Parse(raw)
	return metadataURLUsable(u, err)
}

// clientIdentity is the client the authorization is made as.
type clientIdentity struct {
	path, id, secret, tokenAuth string
}

func tokenAuthFor(secret string, methods []string) string {
	if secret == "" {
		return "none"
	}
	basic, post := false, false
	for _, m := range methods {
		switch m {
		case "client_secret_basic":
			basic = true
		case "client_secret_post":
			post = true
		}
	}
	if post && !basic {
		return "post"
	}
	return "basic"
}

func hasReg(srv OAuthServer, path string) bool {
	for _, r := range srv.Registration {
		if r == path {
			return true
		}
	}
	return false
}

// chooseClient follows the registration order: pre-registered, CIMD, DCR, then "client id
// needed". force pins one path.
func chooseClient(ctx context.Context, d ServerDef, srv OAuthServer, req StartRequest) (clientIdentity, *OAuthError) {
	force := req.Registration
	switch force {
	case "", OAuthRegPreregistered, OAuthRegCIMD, OAuthRegDCR:
	default:
		return clientIdentity{}, oerr(http.StatusBadRequest, OAuthErrBadRequest, "unknown registration path")
	}
	data, err := secrets.Load()
	if err != nil {
		return clientIdentity{}, oerr(http.StatusInternalServerError, OAuthErrInternal, "the secret store could not be read")
	}
	if force == "" || force == OAuthRegPreregistered {
		for _, c := range data.MCPOAuthClients {
			if c.Kind == OAuthRegPreregistered && c.Issuer == srv.Issuer && c.ServerID == d.ID {
				return clientIdentity{OAuthRegPreregistered, c.ClientID, c.ClientSecret, tokenAuthFor(c.ClientSecret, srv.ep.tokenAuthMethods)}, nil
			}
		}
	}
	if (force == "" || force == OAuthRegCIMD) && hasReg(srv, OAuthRegCIMD) && req.CIMDURL != "" {
		return clientIdentity{OAuthRegCIMD, req.CIMDURL, "", "none"}, nil
	}
	if (force == "" || force == OAuthRegDCR) && srv.ep.registration != "" && endpointUsable(srv.ep.registration) {
		return registerDCR(ctx, srv, req)
	}
	e := oerr(http.StatusConflict, OAuthErrClientIDNeeded,
		"the authorization server offers no automatic registration this deployment can use; enter a client id for it")
	e.Issuer = srv.Issuer
	return clientIdentity{}, e
}

// registerDCR reuses the client stored for this issuer and redirect URI, else registers one
// (RFC 7591) and stores it. application_type is "web": the redirect is an https URL on the
// deployment, and the MCP specification requires the field.
func registerDCR(ctx context.Context, srv OAuthServer, req StartRequest) (clientIdentity, *OAuthError) {
	dcrMu.Lock()
	defer dcrMu.Unlock()
	now := oauthNow().Unix()
	if data, err := secrets.Load(); err == nil {
		for _, c := range data.MCPOAuthClients {
			if c.Kind == OAuthRegDCR && c.Issuer == srv.Issuer && c.RedirectURI == req.RedirectURI &&
				(c.SecretExpiresAt == 0 || c.SecretExpiresAt > now) {
				return clientIdentity{OAuthRegDCR, c.ClientID, c.ClientSecret, tokenAuthFor(c.ClientSecret, srv.ep.tokenAuthMethods)}, nil
			}
		}
	}
	name := strings.TrimSpace(req.ClientName)
	if name == "" {
		name = "Agent Fleet"
	}
	body, _ := json.Marshal(map[string]any{
		"client_name":                name,
		"redirect_uris":              []string{req.RedirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"application_type":           "web",
	})
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.ep.registration, bytes.NewReader(body))
	if err != nil {
		return clientIdentity{}, regFailed(srv, "bad registration endpoint")
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json")
	resp, err := oauthHTTP().Do(hreq)
	if err != nil {
		return clientIdentity{}, regFailed(srv, "the registration endpoint is unreachable")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxOAuthBody))
	var out struct {
		ClientID        string  `json:"client_id"`
		ClientSecret    string  `json:"client_secret"`
		SecretExpiresAt flexInt `json:"client_secret_expires_at"`
	}
	if (resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated) ||
		json.Unmarshal(raw, &out) != nil || out.ClientID == "" {
		return clientIdentity{}, regFailed(srv, fmt.Sprintf("the registration endpoint answered HTTP %d without a client", resp.StatusCode))
	}
	cl := secrets.MCPOAuthClient{
		Issuer: srv.Issuer, Kind: OAuthRegDCR, RedirectURI: req.RedirectURI,
		ClientID: out.ClientID, ClientSecret: out.ClientSecret,
		SecretExpiresAt: int64(out.SecretExpiresAt), CreatedAt: now,
	}
	if err := updateSecrets(func(s *secrets.Data) error {
		kept := s.MCPOAuthClients[:0]
		for _, c := range s.MCPOAuthClients {
			if !(c.Kind == OAuthRegDCR && c.Issuer == cl.Issuer && c.RedirectURI == cl.RedirectURI) {
				kept = append(kept, c)
			}
		}
		s.MCPOAuthClients = append(kept, cl)
		return nil
	}); err != nil {
		return clientIdentity{}, oerr(http.StatusInternalServerError, OAuthErrInternal, "the secret store could not be written")
	}
	return clientIdentity{OAuthRegDCR, cl.ClientID, cl.ClientSecret, tokenAuthFor(cl.ClientSecret, srv.ep.tokenAuthMethods)}, nil
}

func regFailed(srv OAuthServer, msg string) *OAuthError {
	e := oerr(http.StatusBadGateway, OAuthErrRegistration, msg)
	e.Issuer, e.Registration = srv.Issuer, OAuthRegDCR
	return e
}

// StartOAuth prepares the authorization for the registered server serverID and returns the URL
// the member's browser is sent to.
func StartOAuth(ctx context.Context, serverID string, req StartRequest) (StartResult, error) {
	if !validState(req.State) {
		return StartResult{}, oerr(http.StatusBadRequest, OAuthErrBadRequest, "invalid state")
	}
	if !redirectURIUsable(req.RedirectURI) {
		return StartResult{}, oerr(http.StatusBadRequest, OAuthErrBadRequest, "invalid redirect URI")
	}
	if req.CIMDURL != "" && !strings.HasPrefix(req.CIMDURL, "https://") {
		return StartResult{}, oerr(http.StatusBadRequest, OAuthErrBadRequest, "the client metadata document URL must be https")
	}
	if !validScope(req.Scope) {
		return StartResult{}, oerr(http.StatusBadRequest, OAuthErrBadRequest, "invalid scope")
	}
	d, err := Get(serverID)
	if err != nil {
		if err == ErrNotFound {
			return StartResult{}, oerr(http.StatusNotFound, OAuthErrNotFound, "no such MCP server")
		}
		return StartResult{}, oerr(http.StatusInternalServerError, OAuthErrInternal, "the secret store could not be read")
	}
	if d.Transport != TransportHTTP || d.URL == "" || d.Origin == OriginBuiltin || d.Auth != AuthOAuth {
		return StartResult{}, oerr(http.StatusBadRequest, OAuthErrNotOAuthServer, "only a remote server whose authentication is OAuth can be signed in to")
	}
	rep, ok := discoverForSignIn(ctx, d)
	if !ok {
		return StartResult{}, oerr(http.StatusUnprocessableEntity, OAuthErrNotRequired, "the server did not ask for OAuth")
	}
	var srv OAuthServer
	for _, s := range rep.Servers {
		if s.Valid && s.Issuer == rep.Selected {
			srv = s
		}
	}
	if rep.Selected == "" || srv.Issuer == "" {
		codes := make([]string, 0, len(rep.Problems))
		for _, p := range rep.Problems {
			codes = append(codes, p.Code)
		}
		return StartResult{}, oerr(http.StatusUnprocessableEntity, OAuthErrDiscovery,
			"the server's OAuth metadata could not be used: "+strings.Join(codes, ", "))
	}
	if !endpointUsable(srv.ep.authorization) || !endpointUsable(srv.ep.token) {
		e := oerr(http.StatusUnprocessableEntity, OAuthErrEndpoints, "the authorization server's authorization or token endpoint is missing or not https")
		e.Issuer = srv.Issuer
		return StartResult{}, e
	}
	revocation := ""
	if endpointUsable(srv.ep.revocation) {
		revocation = srv.ep.revocation
	}
	scope := req.Scope
	if scope == "" {
		if g := currentGrant(serverID); g != nil && g.StepUpScope != "" {
			scope = unionScope(g.Scope, g.StepUpScope)
		}
	}
	if scope == "" {
		scope = selectScope(rep.ChallengedScope, rep.prmScopes, srv.ep.asScopes)
	}
	cl, oe := chooseClient(ctx, d, srv, req)
	if oe != nil {
		return StartResult{}, oe
	}
	verifier, challenge, err := pkcePair()
	if err != nil {
		return StartResult{}, oerr(http.StatusInternalServerError, OAuthErrInternal, "no randomness")
	}
	au, err := url.Parse(srv.ep.authorization)
	if err != nil {
		return StartResult{}, oerr(http.StatusUnprocessableEntity, OAuthErrEndpoints, "bad authorization endpoint")
	}
	q := au.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cl.id)
	q.Set("redirect_uri", req.RedirectURI)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", req.State)
	q.Set("resource", rep.Resource)
	if scope != "" {
		q.Set("scope", scope)
	}
	au.RawQuery = q.Encode()
	if e := putPending(req.State, &pendingAuth{
		serverID: d.ID, serverName: d.Name, issuer: srv.Issuer, issParam: srv.IssParam,
		registration: cl.path, clientID: cl.id, secret: cl.secret, tokenAuth: cl.tokenAuth,
		tokenEndpoint: srv.ep.token, revocationEndpoint: revocation, resource: rep.Resource,
		scope: scope, redirectURI: req.RedirectURI, verifier: verifier,
		expires: oauthNow().Add(oauthStateTTL),
	}); e != nil {
		return StartResult{}, e
	}
	return StartResult{
		AuthorizeURL: au.String(), ServerName: d.Name, Issuer: srv.Issuer,
		Registration: cl.path, Scope: scope,
	}, nil
}

// CompleteRequest is what the Control Plane forwards from the callback: only what the browser
// carried. Error is the AS's `error` parameter, forwarded so the pending verifier is dropped.
type CompleteRequest struct {
	State string `json:"state"`
	Code  string `json:"code,omitempty"`
	Iss   string `json:"iss,omitempty"`
	Error string `json:"error,omitempty"`
}

// CompleteResult is the grant's public face. No token appears in it.
type CompleteResult struct {
	ServerID     string `json:"serverId"`
	ServerName   string `json:"serverName"`
	Issuer       string `json:"issuer"`
	Registration string `json:"registration"`
	Scope        string `json:"scope,omitempty"`
	ExpiresAt    int64  `json:"expiresAt,omitempty"`
}

// CompleteOAuth finishes the authorization that StartOAuth began for state. The pending entry
// is consumed first, so a replayed or failed callback never gets a second try with the same
// verifier.
func CompleteOAuth(ctx context.Context, serverID string, req CompleteRequest) (CompleteResult, error) {
	p, ok := takePending(req.State)
	if !ok {
		return CompleteResult{}, oerr(http.StatusBadRequest, OAuthErrStateUnknown, "this sign-in is unknown, expired or already used")
	}
	fail := func(status int, code, msg string) (CompleteResult, error) {
		e := oerr(status, code, msg)
		e.Issuer, e.Registration = p.issuer, p.registration
		return CompleteResult{}, e
	}
	if p.serverID != serverID {
		return fail(http.StatusBadRequest, OAuthErrStateMismatch, "this sign-in was started for another server")
	}
	if req.Error != "" {
		return fail(http.StatusBadRequest, OAuthErrDenied, "the authorization server did not grant access")
	}
	// RFC 9207 / the MCP specification's table: an iss that is present must be the issuer the
	// authorization was started against; an absent one is acceptable only when the server does
	// not promise to send it.
	switch {
	case req.Iss != "" && req.Iss != p.issuer:
		return fail(http.StatusBadRequest, OAuthErrIssMismatch, "the authorization response came from a different issuer")
	case req.Iss == "" && p.issParam:
		return fail(http.StatusBadRequest, OAuthErrIssMissing, "the authorization server promises an issuer in its response and none came")
	}
	if req.Code == "" {
		return fail(http.StatusBadRequest, OAuthErrNoCode, "the authorization response carried no code")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {req.Code},
		"redirect_uri":  {p.redirectURI},
		"code_verifier": {p.verifier},
		"resource":      {p.resource},
	}
	tok, terr := postToken(ctx, p.tokenEndpoint, form, p.clientID, p.secret, p.tokenAuth)
	if terr != nil {
		return fail(http.StatusBadGateway, OAuthErrTokenExchange, "the authorization server refused the code ("+terr.code+")")
	}
	now := oauthNow().Unix()
	scope := p.scope
	if tok.Scope != "" {
		scope = tok.Scope
	}
	g := secrets.MCPOAuthGrant{
		ServerID: p.serverID, Issuer: p.issuer, Registration: p.registration,
		ClientID: p.clientID, ClientSecret: p.secret, TokenAuth: p.tokenAuth,
		TokenEndpoint: p.tokenEndpoint, RevocationEndpoint: p.revocationEndpoint,
		Resource: p.resource, Scope: scope, AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken,
		ConnectedAt: now,
	}
	if tok.ExpiresIn > 0 {
		g.ExpiresAt = now + int64(tok.ExpiresIn)
	}
	if err := updateSecrets(func(s *secrets.Data) error {
		kept := s.MCPOAuth[:0]
		for _, o := range s.MCPOAuth {
			if o.ServerID != g.ServerID { // one grant per server: another issuer's is replaced
				kept = append(kept, o)
			}
		}
		s.MCPOAuth = append(kept, g)
		return nil
	}); err != nil {
		return fail(http.StatusInternalServerError, OAuthErrInternal, "the secret store could not be written")
	}
	return CompleteResult{
		ServerID: g.ServerID, ServerName: p.serverName, Issuer: g.Issuer,
		Registration: g.Registration, Scope: g.Scope, ExpiresAt: g.ExpiresAt,
	}, nil
}

// flexInt reads a JSON number that some authorization servers send as a string.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	var n json.Number = json.Number(s)
	v, err := n.Int64()
	if err != nil {
		fv, ferr := n.Float64()
		if ferr != nil {
			return err
		}
		v = int64(fv)
	}
	*f = flexInt(v)
	return nil
}

type tokenResponse struct {
	AccessToken  string  `json:"access_token"`
	TokenType    string  `json:"token_type"`
	RefreshToken string  `json:"refresh_token"`
	ExpiresIn    flexInt `json:"expires_in"`
	Scope        string  `json:"scope"`
	Error        string  `json:"error"`
}

// tokenError is a refused or failed token request. code is the RFC 6749 error code when the
// AS sent a well-formed one, else a short transport label; never a body fragment.
type tokenError struct {
	code   string
	status int
}

func (e *tokenError) Error() string { return "token request failed: " + e.code }

func safeErrCode(s string) string {
	if s == "" || len(s) > 40 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return ""
		}
	}
	return s
}

// postToken sends one token-endpoint request with the client's authentication.
func postToken(ctx context.Context, endpoint string, form url.Values, clientID, secret, tokenAuth string) (tokenResponse, *tokenError) {
	if !endpointUsable(endpoint) {
		return tokenResponse{}, &tokenError{code: "bad_endpoint"}
	}
	f := url.Values{}
	for k, v := range form {
		f[k] = v
	}
	var basic bool
	switch tokenAuth {
	case "basic":
		basic = true
	case "post":
		f.Set("client_id", clientID)
		f.Set("client_secret", secret)
	default:
		f.Set("client_id", clientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(f.Encode()))
	if err != nil {
		return tokenResponse{}, &tokenError{code: "bad_endpoint"}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic {
		// Both parts are form-encoded first (RFC 6749 §2.3.1), so a secret holding ":" or "%"
		// survives an AS that decodes them.
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
	}
	resp, err := oauthHTTP().Do(req)
	if err != nil {
		return tokenResponse{}, &tokenError{code: "unreachable"}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxOAuthBody))
	var out tokenResponse
	jerr := json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK {
		code := "http_" + fmt.Sprint(resp.StatusCode)
		if c := safeErrCode(out.Error); jerr == nil && c != "" {
			code = c
		}
		return tokenResponse{}, &tokenError{code: code, status: resp.StatusCode}
	}
	if jerr != nil || out.AccessToken == "" {
		return tokenResponse{}, &tokenError{code: "invalid_response", status: resp.StatusCode}
	}
	if out.TokenType != "" && !strings.EqualFold(out.TokenType, "bearer") {
		return tokenResponse{}, &tokenError{code: "unsupported_token_type", status: resp.StatusCode}
	}
	return out, nil
}
