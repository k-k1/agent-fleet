// mcp_oauth.go — the Control Plane as the OAuth 2.1 authorization server of its own /mcp
// endpoint (ADR 0118), so remote MCP clients connect by signing the member in to the Console
// instead of pasting a PAT.
//
//	client (claude.ai, ChatGPT, Claude Code) ──► /mcp                       401 + resource_metadata
//	                                         ──► /.well-known/…            discovery (no session)
//	member's browser                         ──► /mcp-oauth/authorize       (inside authGate)
//	                                         ──► /mcp-oauth/consent         (inside authGate, same-origin POST)
//	client (server to server)                ──► /mcp-oauth/token, register, revoke   (no session)
//
// The issuer is {PUBLIC_BASE_URL}/mcp-oauth, separate from /dev-idp: its own keys-free tables,
// audit actions and clients. Served only when AF_MCP_ENABLED=true, AF_MCP_OAUTH=1 and
// PUBLIC_BASE_URL is https; otherwise every path here answers 404 and /mcp keeps its bare 401.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	mcpOAuthPath         = "/mcp-oauth"
	mcpOAuthAuthorize    = mcpOAuthPath + "/authorize"
	mcpOAuthConsent      = mcpOAuthPath + "/consent"
	mcpOAuthToken        = mcpOAuthPath + "/token"
	mcpOAuthRegister     = mcpOAuthPath + "/register"
	mcpOAuthRevoke       = mcpOAuthPath + "/revoke"
	mcpOAuthPRMPath      = "/.well-known/oauth-protected-resource/mcp"
	mcpOAuthMetadataPath = "/.well-known/oauth-authorization-server" + mcpOAuthPath

	mcpOAuthCodeTTL      = 60 * time.Second
	mcpOAuthConsentTTL   = 5 * time.Minute
	mcpOAuthAccessTTL    = time.Hour
	mcpOAuthIdleTTL      = 30 * 24 * time.Hour
	mcpOAuthAbsoluteTTL  = 90 * 24 * time.Hour
	mcpOAuthClientTTL    = 30 * 24 * time.Hour // a registration with no grant is deleted after this
	mcpOAuthMaxClients   = 2000
	mcpOAuthRegPerHour   = 10
	mcpOAuthMaxRedirects = 8

	// The wire value of the one tier OAuth can never grant (pat.go's ladder).
	mcpOAuthForbiddenScope = scopeDangerous

	defaultMCPOAuthTrustedHosts = "claude.ai,claude.com,chatgpt.com"
)

type mcpOAuthAPI struct {
	mgr   *manager
	now   func() time.Time
	brand *brandResolver

	// issuer and resource are "" while the deployment does not serve OAuth for /mcp.
	issuer, resource string
	trusted          map[string]bool

	// fetch reads a CIMD document; tests replace it. It is only ever called for a URL on a
	// trusted host.
	fetch func(ctx context.Context, rawURL string) (body []byte, maxAge time.Duration, err error)
	cimd  *cimdCache
	reg   *rateLimiter
}

func newMCPOAuthAPI(cfg config) *mcpOAuthAPI {
	m := cfg.mgr
	a := &mcpOAuthAPI{mgr: m, now: time.Now, brand: cfg.brand,
		trusted: parseTrustedClientHosts(envx.Or("AF_MCP_OAUTH_TRUSTED_HOSTS", defaultMCPOAuthTrustedHosts)),
		cimd:    newCIMDCache(64), reg: newRateLimiter(mcpOAuthRegPerHour, time.Hour, 10000)}
	a.fetch = fetchCIMDDocument
	base := strings.TrimRight(m.publicBaseURL, "/")
	if envx.Or("AF_MCP_ENABLED", "") == "true" && envx.Or("AF_MCP_OAUTH", "") == "1" && strings.HasPrefix(base, "https://") {
		a.issuer, a.resource = base+mcpOAuthPath, base+"/mcp"
	}
	m.mcpOAuthServed.Store(a.issuer != "")
	return a
}

// withOAuth teaches the /mcp handler the second token kind and the challenge, when served.
func (a *mcpOAuthAPI) withOAuth(h mcpsrv.API) mcpsrv.API {
	if a.issuer == "" {
		return h
	}
	return h.WithOAuth(a.resource, strings.TrimSuffix(a.resource, "/mcp")+mcpOAuthPRMPath)
}

func registerMCPOAuthRoutes(mux *http.ServeMux, cfg config) *mcpOAuthAPI {
	a := newMCPOAuthAPI(cfg)
	a.mount(mux)
	return a
}

func (a *mcpOAuthAPI) mount(mux *http.ServeMux) {
	exemptExact(mcpOAuthPRMPath, mcpOAuthMetadataPath, mcpOAuthToken, mcpOAuthRegister, mcpOAuthRevoke)
	mux.HandleFunc(mcpOAuthPRMPath, a.gate(http.MethodGet, a.protectedResource))
	mux.HandleFunc(mcpOAuthMetadataPath, a.gate(http.MethodGet, a.metadata))
	mux.HandleFunc(mcpOAuthAuthorize, a.gatePage(http.MethodGet, a.authorize))
	mux.HandleFunc(mcpOAuthConsent, a.gatePage(http.MethodPost, a.consent))
	mux.HandleFunc(mcpOAuthToken, a.gate(http.MethodPost, a.token))
	mux.HandleFunc(mcpOAuthRegister, a.gate(http.MethodPost, a.register))
	mux.HandleFunc(mcpOAuthRevoke, a.gate(http.MethodPost, a.revoke))
}

func (a *mcpOAuthAPI) gate(method string, h http.HandlerFunc) http.HandlerFunc {
	return a.gateWith(method, false, h)
}

// gatePage is gate for the endpoints a browser navigates to, whose 405 is an error page.
func (a *mcpOAuthAPI) gatePage(method string, h http.HandlerFunc) http.HandlerFunc {
	return a.gateWith(method, true, h)
}

// gateWith answers 404 while the deployment does not serve OAuth, whatever the method, then 405.
func (a *mcpOAuthAPI) gateWith(method string, page bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.issuer == "" {
			http.NotFound(w, r)
			return
		}
		if r.Method != method {
			w.Header().Set("Allow", method)
			if page {
				a.errPage(w, r, http.StatusMethodNotAllowed, mcpErrMethod, "")
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	}
}

// --- discovery ----------------------------------------------------------------

type mcpProtectedResource struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}

func (a *mcpOAuthAPI) protectedResource(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, mcpProtectedResource{Resource: a.resource, AuthorizationServers: []string{a.issuer},
		ScopesSupported: []string{scopeRead, scopeWrite}, BearerMethodsSupported: []string{"header"}})
}

// mcpASMetadata is the RFC 8414 document.
type mcpASMetadata struct {
	Issuer                       string   `json:"issuer"`
	AuthorizationEndpoint        string   `json:"authorization_endpoint"`
	TokenEndpoint                string   `json:"token_endpoint"`
	RegistrationEndpoint         string   `json:"registration_endpoint"`
	RevocationEndpoint           string   `json:"revocation_endpoint"`
	ResponseTypes                []string `json:"response_types_supported"`
	ResponseModes                []string `json:"response_modes_supported"`
	GrantTypes                   []string `json:"grant_types_supported"`
	TokenAuthMethods             []string `json:"token_endpoint_auth_methods_supported"`
	RevocationAuthMethods        []string `json:"revocation_endpoint_auth_methods_supported"`
	CodeChallengeMethods         []string `json:"code_challenge_methods_supported"`
	Scopes                       []string `json:"scopes_supported"`
	IssParameterSupported        bool     `json:"authorization_response_iss_parameter_supported"`
	ClientIDMetadataDocSupported bool     `json:"client_id_metadata_document_supported"`
}

func (a *mcpOAuthAPI) metadata(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, mcpASMetadata{
		Issuer: a.issuer, AuthorizationEndpoint: a.issuer + "/authorize", TokenEndpoint: a.issuer + "/token",
		RegistrationEndpoint: a.issuer + "/register", RevocationEndpoint: a.issuer + "/revoke",
		ResponseTypes: []string{"code"}, ResponseModes: []string{"query"},
		GrantTypes:            []string{"authorization_code", "refresh_token"},
		TokenAuthMethods:      []string{"none"},
		RevocationAuthMethods: []string{"none"},
		CodeChallengeMethods:  []string{"S256"},
		Scopes:                []string{scopeRead, scopeWrite},
		IssParameterSupported: true, ClientIDMetadataDocSupported: true,
	})
}

// --- redirect rule (ADR 0118 decision 3) ------------------------------------------

func parseTrustedClientHosts(s string) map[string]bool {
	out := map[string]bool{}
	for _, h := range strings.Split(s, ",") {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || h == "none" || strings.ContainsAny(h, ":/@?#\\ ") || strings.HasSuffix(h, ".") {
			continue
		}
		out[h] = true
	}
	return out
}

type redirectClass int

const (
	redirectRefused redirectClass = iota
	redirectLoopback
	redirectHosted
)

// classifyRedirect decides whether a redirect URI may ever receive a code. It admits
//
//   - http://127.0.0.1, http://localhost and http://[::1] on any port (RFC 8252 §7.3), and
//   - https on exactly a trusted-client host.
//
// The match is on the canonical spelling: an upper-case host, a trailing dot, an explicit :443,
// userinfo, a fragment or a backslash are refused rather than normalised, because every
// normalisation is a chance for this check and the browser to disagree.
func (a *mcpOAuthAPI) classifyRedirect(raw string) redirectClass {
	u, ok := parseCanonicalRedirect(raw)
	if !ok {
		return redirectRefused
	}
	switch u.Scheme {
	case "http":
		host := u.Hostname()
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			return redirectRefused
		}
		want := host
		if host == "::1" {
			want = "[::1]"
		}
		if port := u.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
				return redirectRefused
			}
			want += ":" + port
		}
		if u.Host != want {
			return redirectRefused
		}
		return redirectLoopback
	case "https":
		if u.Host != u.Hostname() || !a.trusted[u.Host] {
			return redirectRefused
		}
		return redirectHosted
	}
	return redirectRefused
}

// redirectRegistered reports whether requested is one of the client's registered URIs. A
// loopback URI matches ignoring the port, which a CLI picks at random; everything else is an
// exact string match.
func (a *mcpOAuthAPI) redirectRegistered(registered []string, requested string) bool {
	class := a.classifyRedirect(requested)
	if class == redirectRefused {
		return false
	}
	ru, _ := url.Parse(requested)
	for _, reg := range registered {
		if reg == requested {
			return true
		}
		if class != redirectLoopback || a.classifyRedirect(reg) != redirectLoopback {
			continue
		}
		gu, err := url.Parse(reg)
		// Only the port may differ: the path is compared as written (encoded), not decoded.
		if err == nil && gu.Hostname() == ru.Hostname() && gu.EscapedPath() == ru.EscapedPath() &&
			gu.RawQuery == ru.RawQuery && gu.ForceQuery == ru.ForceQuery {
			return true
		}
	}
	return false
}

// --- clients ------------------------------------------------------------------

type mcpClient struct {
	ID, Name  string
	Redirects []string
	CIMD      bool
}

var (
	errMCPClientUnknown = errors.New("unknown client")
	errMCPClientStore   = errors.New("client store")
)

// cimdURLAllowed is the SSRF guard: a CIMD client_id is fetched only when it is an https URL
// with a path on a trusted host, so the server never makes a request to an address a stranger
// chose.
func (a *mcpOAuthAPI) cimdURLAllowed(id string) (*url.URL, bool) {
	u, ok := parseCanonicalRedirect(id)
	if !ok || u.Scheme != "https" || u.Host != u.Hostname() || !a.trusted[u.Host] ||
		u.RawQuery != "" || u.ForceQuery || len(u.Path) < 2 || path.Clean(u.Path) != u.Path {
		return nil, false
	}
	return u, true
}

// loadClient resolves a client_id to the registration the redirect is checked against.
func (a *mcpOAuthAPI) loadClient(ctx context.Context, id string) (mcpClient, error) {
	switch {
	case id == "" || len(id) > 512:
		return mcpClient{}, errMCPClientUnknown
	case strings.HasPrefix(id, "https://"):
		return a.loadCIMD(ctx, id)
	}
	row, ok, err := a.mgr.store.GetMCPOAuthClient(ctx, id)
	if err != nil {
		log.Printf("mcp oauth: client lookup: %v", err)
		return mcpClient{}, errMCPClientStore
	}
	if !ok {
		return mcpClient{}, errMCPClientUnknown
	}
	var uris []string
	_ = json.Unmarshal([]byte(row.RedirectURIs), &uris)
	return mcpClient{ID: row.ClientID, Name: row.Name, Redirects: a.vettedRedirects(uris)}, nil
}

// vettedRedirects keeps the URIs the current rule still admits (the trusted list may have
// shrunk since the client registered).
func (a *mcpOAuthAPI) vettedRedirects(uris []string) []string {
	var out []string
	for _, u := range uris {
		if a.classifyRedirect(u) != redirectRefused {
			out = append(out, u)
		}
	}
	return out
}

type cimdDocument struct {
	ClientID             string   `json:"client_id"`
	ClientName           string   `json:"client_name"`
	RedirectURIs         []string `json:"redirect_uris"`
	AuthMethod           string   `json:"token_endpoint_auth_method"`
	AuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	ClientSecret         string   `json:"client_secret"`
}

const (
	cimdMaxBody    = 64 << 10
	cimdFetchLimit = 3 * time.Second
	cimdMinTTL     = time.Minute
	cimdMaxTTL     = time.Hour
	cimdDefaultTTL = 5 * time.Minute
)

func (a *mcpOAuthAPI) loadCIMD(ctx context.Context, id string) (mcpClient, error) {
	if _, ok := a.cimdURLAllowed(id); !ok {
		return mcpClient{}, errMCPClientUnknown
	}
	now := a.now()
	if c, ok := a.cimd.get(id, now); ok {
		return c, nil
	}
	fctx, cancel := context.WithTimeout(ctx, cimdFetchLimit)
	defer cancel()
	body, maxAge, err := a.fetch(fctx, id)
	if err != nil {
		log.Printf("mcp oauth: client metadata fetch failed: %v", err)
		return mcpClient{}, errMCPClientUnknown
	}
	if len(body) > cimdMaxBody {
		return mcpClient{}, errMCPClientUnknown
	}
	// A preferred method can differ from the common method: ChatGPT supports none
	// even though its document prefers private_key_jwt. Tokens still require none.
	var doc cimdDocument
	if json.Unmarshal(body, &doc) != nil || doc.ClientID != id || doc.ClientSecret != "" ||
		(doc.AuthMethod != "" && doc.AuthMethod != "none" && !slices.Contains(doc.AuthMethodsSupported, "none")) {
		return mcpClient{}, errMCPClientUnknown
	}
	if len(doc.RedirectURIs) > mcpOAuthMaxRedirects {
		doc.RedirectURIs = doc.RedirectURIs[:mcpOAuthMaxRedirects]
	}
	redirects := a.vettedRedirects(doc.RedirectURIs)
	if len(redirects) == 0 {
		return mcpClient{}, errMCPClientUnknown
	}
	if maxAge < cimdMinTTL {
		maxAge = cimdMinTTL
	}
	if maxAge > cimdMaxTTL {
		maxAge = cimdMaxTTL
	}
	c := mcpClient{ID: id, Name: cleanClientName(doc.ClientName), Redirects: redirects, CIMD: true}
	a.cimd.put(id, c, now.Add(maxAge), now)
	return c, nil
}

// cleanClientName makes the self-declared name safe to show: no control or format characters,
// bounded length. It proves nothing about who the client is, and the page says so.
func cleanClientName(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			continue
		}
		if n++; n > 80 {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// cimdCache is a bounded, TTL'd cache of fetched client metadata documents.
type cimdCache struct {
	mu  sync.Mutex
	max int
	m   map[string]cimdEntry
}

type cimdEntry struct {
	c       mcpClient
	expires time.Time
}

func newCIMDCache(max int) *cimdCache { return &cimdCache{max: max, m: map[string]cimdEntry{}} }

func (c *cimdCache) get(id string, now time.Time) (mcpClient, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	if !ok || !now.Before(e.expires) {
		delete(c.m, id)
		return mcpClient{}, false
	}
	return e.c, true
}

func (c *cimdCache) put(id string, cl mcpClient, expires, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[id]; !ok && len(c.m) >= c.max {
		for k, e := range c.m { // drop the expired first, then whatever expires soonest
			if !now.Before(e.expires) {
				delete(c.m, k)
			}
		}
		for len(c.m) >= c.max {
			var oldest string
			var at time.Time
			for k, e := range c.m {
				if oldest == "" || e.expires.Before(at) {
					oldest, at = k, e.expires
				}
			}
			delete(c.m, oldest)
		}
	}
	c.m[id] = cimdEntry{cl, expires}
}

// fetchCIMDDocument GETs a client metadata document. No redirect is followed, the answer is
// capped, and the dialer refuses any address that is not public, so a trusted name that resolves
// inward is still not reachable.
func fetchCIMDDocument(ctx context.Context, rawURL string) ([]byte, time.Duration, error) {
	return fetchCIMDWith(ctx, rawURL, publicDialControl)
}

// fetchCIMDWith is fetchCIMDDocument with the dial check as a parameter, so the redirect, size
// and timeout rules can be exercised against a loopback test server.
func fetchCIMDWith(ctx context.Context, rawURL string, control func(network, address string, c syscall.RawConn) error) ([]byte, time.Duration, error) {
	client := &http.Client{
		Timeout: cimdFetchLimit,
		Transport: &http.Transport{
			Proxy: nil,
			// A one-shot client: without this the connection and its goroutines outlive the fetch.
			DisableKeepAlives: true,
			DialContext:       (&net.Dialer{Timeout: cimdFetchLimit, Control: control}).DialContext,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, cimdMaxBody+1))
	if err != nil {
		return nil, 0, err
	}
	if len(body) > cimdMaxBody {
		return nil, 0, errors.New("document too large")
	}
	return body, cacheControlMaxAge(resp.Header.Get("Cache-Control")), nil
}

func cacheControlMaxAge(h string) time.Duration {
	for _, part := range strings.Split(h, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "no-store" || part == "no-cache" {
			return 0
		}
		if v, ok := strings.CutPrefix(part, "max-age="); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return time.Duration(n) * time.Second
			}
		}
	}
	return cimdDefaultTTL
}

// publicDialControl refuses to connect to anything but a public unicast address.
func publicDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !isPublicIP(ip) {
		return fmt.Errorf("refusing to dial non-public address %s", host)
	}
	return nil
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func isPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && !ip.IsMulticast() && !ip.IsUnspecified() && !cgnat.Contains(ip)
}

// --- dynamic client registration (RFC 7591) ---------------------------------------

// rateLimiter is a sliding-window counter per key with a bounded number of keys.
type rateLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	maxKeys int
	hits    map[string][]time.Time
}

func newRateLimiter(max int, window time.Duration, maxKeys int) *rateLimiter {
	return &rateLimiter{max: max, window: window, maxKeys: maxKeys, hits: map[string][]time.Time{}}
}

func (l *rateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) >= l.maxKeys {
		for k, v := range l.hits { // drop idle keys; if all are busy, start over rather than grow
			if len(v) == 0 || now.Sub(v[len(v)-1]) > l.window {
				delete(l.hits, k)
			}
		}
		if len(l.hits) >= l.maxKeys {
			l.hits = map[string][]time.Time{}
		}
	}
	recent := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			recent = append(recent, t)
		}
	}
	if len(recent) >= l.max {
		l.hits[key] = recent
		return false
	}
	l.hits[key] = append(recent, now)
	return true
}

func registerSource(r *http.Request) string {
	if info := clientIPFrom(r.Context()); info.OK {
		return info.IP.String()
	}
	return "unknown"
}

// mcpRegError is the RFC 7591 §3.2.2 error body.
type mcpRegError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// mcpRegistered is the RFC 7591 §3.2.1 client information response.
type mcpRegistered struct {
	ClientID     string   `json:"client_id"`
	IssuedAt     int64    `json:"client_id_issued_at"`
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
	AuthMethod   string   `json:"token_endpoint_auth_method"`
	GrantTypes   []string `json:"grant_types"`
	ResponseType []string `json:"response_types"`
}

func regError(w http.ResponseWriter, status int, code, desc string) {
	noStore(w)
	writeJSON(w, status, mcpRegError{Error: code, Description: desc})
}

func (a *mcpOAuthAPI) register(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !a.reg.allow(registerSource(r), a.now()) {
		w.Header().Set("Retry-After", "3600")
		regError(w, http.StatusTooManyRequests, "invalid_client_metadata", "too many registrations from this source")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
		GrantTypes   []string `json:"grant_types"`
		ResponseType []string `json:"response_types"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		regError(w, http.StatusBadRequest, "invalid_client_metadata", "the body is not a JSON object")
		return
	}
	if req.AuthMethod != "" && req.AuthMethod != "none" {
		regError(w, http.StatusBadRequest, "invalid_client_metadata", "only token_endpoint_auth_method none is supported")
		return
	}
	for _, g := range req.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			regError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported grant type "+strconv.Quote(g))
			return
		}
	}
	for _, t := range req.ResponseType {
		if t != "code" {
			regError(w, http.StatusBadRequest, "invalid_client_metadata", "only response type code is supported")
			return
		}
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > mcpOAuthMaxRedirects {
		regError(w, http.StatusBadRequest, "invalid_redirect_uri", "give between 1 and "+strconv.Itoa(mcpOAuthMaxRedirects)+" redirect_uris")
		return
	}
	seen := map[string]bool{}
	for _, u := range req.RedirectURIs {
		if a.classifyRedirect(u) == redirectRefused || seen[u] {
			regError(w, http.StatusBadRequest, "invalid_redirect_uri",
				"redirect_uris must be unique loopback URIs or https URIs on a trusted client host")
			return
		}
		seen[u] = true
	}
	now := a.now()
	if err := a.mgr.store.PruneMCPOAuth(ctx, now.Unix(), now.Add(-mcpOAuthClientTTL).Unix(), now.Add(-mcpOAuthClientTTL).Unix()); err != nil {
		log.Printf("mcp oauth: prune: %v", err)
	}
	uris, _ := json.Marshal(req.RedirectURIs)
	c := store.MCPOAuthClient{ClientID: "afmcp-" + strings.ToLower(devIdPRandom(18)), Name: cleanClientName(req.ClientName),
		RedirectURIs: string(uris), CreatedAt: now.Unix()}
	switch err := a.mgr.store.CreateMCPOAuthClient(ctx, c, mcpOAuthMaxClients); {
	case errors.Is(err, store.ErrMCPOAuthClientCap):
		regError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "the registration limit is reached; try again later")
		return
	case err != nil:
		log.Printf("mcp oauth: register: %v", err)
		regError(w, http.StatusInternalServerError, "server_error", "could not register the client")
		return
	}
	noStore(w)
	writeJSON(w, http.StatusCreated, mcpRegistered{
		ClientID: c.ClientID, IssuedAt: c.CreatedAt, ClientName: c.Name, RedirectURIs: req.RedirectURIs,
		AuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseType: []string{"code"},
	})
}

func constantTimeEq(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
