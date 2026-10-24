package mcpsrv

// Token bridge for a tenant's confidential OAuth client (ADR 0119 decision 9).
//
// A tenant administrator may distribute an OAuth MCP server together with a client they
// registered at its authorization server, secret included. The secret must not reach the
// members' workspaces (anyone with a shell there could read it), so for such a client the Agent
// sends its token and revocation requests here and the CP adds the client authentication. This
// is the shape of the git OAuth refresh bridge (control-plane/git_oauth_bridge.go): the tenant's
// secret here, the member's tokens there.
//
// What keeps it from being a general-purpose token proxy:
//   - the tenant comes from the AF_MCP_TOKEN's membership, never the request, and the server id
//     must be one of that tenant's enabled OAuth rows with a confidential client;
//   - the request names the issuer and client id its grant was made with, and they must still be
//     the row's: after an admin replaces the client, an old grant's tokens are refused here
//     rather than sent to the new client's authorization server (nor can they be revoked at
//     the old one, whose secret is gone);
//   - only authorization_code and refresh_token are relayed, and resource must be that row's URL;
//   - the endpoint is never taken from the caller: the CP discovers the issuer the admin entered,
//     requires the metadata to name the same issuer, and dials public addresses only with no
//     redirect followed (the guard the CIMD fetch uses).
//
// Codes, verifiers and tokens pass through and are neither stored nor logged here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	bridgeTimeout   = 20 * time.Second
	bridgeMaxBody   = 64 << 10
	bridgeMetaTTL   = 10 * time.Minute
	bridgeMaxIssuer = 256
)

// bridgeHTTP is the client the bridge reaches authorization servers with. Tests replace it.
var bridgeHTTP = func() *http.Client {
	return &http.Client{
		Timeout: bridgeTimeout,
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
			DialContext:       (&net.Dialer{Timeout: bridgeTimeout, Control: bridgePublicDial}).DialContext,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// bridgePublicDial refuses anything but a public unicast address, so an issuer an admin typed
// cannot make the CP reach into its own network.
func bridgePublicDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("refusing to dial %s", host)
	}
	ip = ip.Unmap()
	if ip.IsUnspecified() || ip.IsMulticast() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
		return fmt.Errorf("refusing to dial non-public address %s", host)
	}
	return nil
}

// asMeta is what the bridge uses of an authorization server's metadata.
type asMeta struct {
	Issuer            string   `json:"issuer"`
	Token             string   `json:"token_endpoint"`
	Revocation        string   `json:"revocation_endpoint"`
	AuthMethods       []string `json:"token_endpoint_auth_methods_supported"`
	RevokeAuthMethods []string `json:"revocation_endpoint_auth_methods_supported"`
}

var (
	bridgeMetaMu    sync.Mutex
	bridgeMetaCache = map[string]asMetaEntry{}
)

type asMetaEntry struct {
	meta    asMeta
	expires time.Time
}

// discoverAS reads the issuer's metadata in the MCP specification's order (RFC 8414 path
// insertion, OIDC path insertion, OIDC appending) and keeps only a document that names the same
// issuer with an https token endpoint.
func discoverAS(ctx context.Context, issuer string) (asMeta, error) {
	bridgeMetaMu.Lock()
	if e, ok := bridgeMetaCache[issuer]; ok && time.Now().Before(e.expires) {
		bridgeMetaMu.Unlock()
		return e.meta, nil
	}
	bridgeMetaMu.Unlock()
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return asMeta{}, errors.New("issuer is not an https URL")
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	origin := "https://" + u.Host
	candidates := []string{origin + "/.well-known/oauth-authorization-server" + path}
	candidates = append(candidates, origin+"/.well-known/openid-configuration"+path)
	if path != "" {
		candidates = append(candidates, origin+path+"/.well-known/openid-configuration")
	}
	for _, c := range candidates {
		m, err := fetchASMeta(ctx, c)
		if err != nil || !sameIssuer(m.Issuer, issuer) || !httpsURL(m.Token) || (m.Revocation != "" && !httpsURL(m.Revocation)) {
			continue
		}
		bridgeMetaMu.Lock()
		if len(bridgeMetaCache) >= bridgeMaxIssuer {
			bridgeMetaCache = map[string]asMetaEntry{}
		}
		bridgeMetaCache[issuer] = asMetaEntry{m, time.Now().Add(bridgeMetaTTL)}
		bridgeMetaMu.Unlock()
		return m, nil
	}
	return asMeta{}, errors.New("no usable authorization server metadata")
}

func fetchASMeta(ctx context.Context, raw string) (asMeta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return asMeta{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := bridgeHTTP().Do(req)
	if err != nil {
		return asMeta{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return asMeta{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	var m asMeta
	if err := json.NewDecoder(io.LimitReader(resp.Body, bridgeMaxBody)).Decode(&m); err != nil {
		return asMeta{}, err
	}
	return m, nil
}

// sameIssuer compares issuer identifiers ignoring one trailing "/", as the Agent's sameIssuer
// does, so an admin's spelling with or without it reaches the same AS on both sides.
func sameIssuer(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

// redirectURIUsable mirrors the Agent's: https, or http on loopback (a development Console).
func redirectURIUsable(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	h := u.Hostname()
	ip := net.ParseIP(h)
	return u.Scheme == "http" && (strings.EqualFold(h, "localhost") || (ip != nil && ip.IsLoopback()))
}

func httpsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// sameResource compares two URLs the way the Agent's canonicalResource does: scheme and host
// case-insensitively, a default port dropped, a lone "/" path equal to none.
func sameResource(a, b string) bool {
	canon := func(raw string) (string, bool) {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return "", false
		}
		host := strings.ToLower(u.Hostname())
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		if p := u.Port(); p != "" && !(u.Scheme == "https" && p == "443") && !(u.Scheme == "http" && p == "80") {
			host += ":" + p
		}
		path := u.EscapedPath()
		if path == "/" {
			path = ""
		}
		out := strings.ToLower(u.Scheme) + "://" + host + path
		if u.RawQuery != "" {
			out += "?" + u.RawQuery
		}
		return out, true
	}
	x, ok1 := canon(a)
	y, ok2 := canon(b)
	return ok1 && ok2 && x == y
}

// tokenErrWire is the token endpoint's own error shape (RFC 6749 §5.2), which the Agent already
// reads.
type tokenErrWire struct {
	Error string `json:"error"`
}

func writeTokenErr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(tokenErrWire{Error: code})
}

// bridgeClient resolves the confidential client of one of the caller's tenant's rows, and only
// while it is still the client (issuer, client id) the caller's grant was made with.
func (a ServerAPI) bridgeClient(ctx context.Context, mv store.MembershipView, id string, in url.Values) (store.MCPServerRow, string, bool) {
	row, found, err := a.store.GetMCPServer(ctx, mv.TenantID, id)
	if err != nil || !found || !row.Enabled || row.Auth != mcpAuthOAuth || row.OAuthClientID == "" || row.OAuthSecretEnc == "" {
		return store.MCPServerRow{}, "", false
	}
	if in.Get("client_id") != row.OAuthClientID || in.Get("issuer") == "" || !sameIssuer(in.Get("issuer"), row.OAuthIssuer) {
		return store.MCPServerRow{}, "", false
	}
	h, err := a.openHeaders(ctx, row.OAuthSecretEnc, row.OAuthKeyRef)
	if err != nil || h[oauthSecretKey] == "" {
		return store.MCPServerRow{}, "", false
	}
	return row, h[oauthSecretKey], true
}

// postClientAuth posts form to endpoint with the tenant client's authentication and returns the
// status and (bounded) body. The metadata lists what the AS supports (an absent list means
// client_secret_basic, RFC 8414 §2), not what this client was registered with, so when both
// basic and post are listed, basic goes first and an invalid_client answer is retried once with
// post: client authentication fails before the AS looks at the code or token.
func postClientAuth(ctx context.Context, endpoint string, form url.Values, methods []string, clientID, secret string) (int, []byte, error) {
	basic, post := len(methods) == 0, false
	for _, m := range methods {
		basic = basic || m == "client_secret_basic"
		post = post || m == "client_secret_post"
	}
	tries := []bool{false} // usePost
	switch {
	case post && !basic:
		tries = []bool{true}
	case post && basic:
		tries = []bool{false, true}
	}
	var status int
	var body []byte
	for i, usePost := range tries {
		f := url.Values{}
		for k, v := range form {
			f[k] = v
		}
		if usePost {
			f.Set("client_id", clientID)
			f.Set("client_secret", secret)
		}
		enc := f.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(enc))
		if err != nil {
			return 0, nil, err
		}
		if !usePost {
			req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, err := bridgeHTTP().Do(req)
		if err != nil {
			return 0, nil, err
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, bridgeMaxBody))
		resp.Body.Close()
		if err != nil {
			return 0, nil, err
		}
		status = resp.StatusCode
		if i == len(tries)-1 || !clientAuthRefused(status, body) {
			break
		}
	}
	return status, body, nil
}

func clientAuthRefused(status int, body []byte) bool {
	if status == http.StatusUnauthorized {
		return true
	}
	var e tokenErrWire
	return status == http.StatusBadRequest && json.Unmarshal(body, &e) == nil && e.Error == "invalid_client"
}

// OAuthToken (POST /internal/mcp-servers/{id}/oauth/token) runs an authorization_code or
// refresh_token grant with the tenant's confidential client and returns the AS's answer.
func (a ServerAPI) OAuthToken(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil || len(r.PostForm) == 0 {
		writeTokenErr(w, http.StatusBadRequest, "invalid_request")
		return
	}
	in := r.PostForm
	row, secret, ok := a.bridgeClient(r.Context(), mv, r.PathValue("id"), in)
	if !ok {
		writeTokenErr(w, http.StatusBadRequest, "unauthorized_client")
		return
	}
	if !sameResource(in.Get("resource"), row.URL) {
		writeTokenErr(w, http.StatusBadRequest, "invalid_target")
		return
	}
	form := url.Values{"grant_type": {in.Get("grant_type")}, "resource": {in.Get("resource")}}
	switch in.Get("grant_type") {
	case "authorization_code":
		if in.Get("code") == "" || in.Get("code_verifier") == "" || !redirectURIUsable(in.Get("redirect_uri")) {
			writeTokenErr(w, http.StatusBadRequest, "invalid_request")
			return
		}
		form.Set("code", in.Get("code"))
		form.Set("code_verifier", in.Get("code_verifier"))
		form.Set("redirect_uri", in.Get("redirect_uri"))
	case "refresh_token":
		if in.Get("refresh_token") == "" {
			writeTokenErr(w, http.StatusBadRequest, "invalid_request")
			return
		}
		form.Set("refresh_token", in.Get("refresh_token"))
	default:
		writeTokenErr(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	meta, err := discoverAS(r.Context(), row.OAuthIssuer)
	if err != nil {
		writeTokenErr(w, http.StatusBadGateway, "temporarily_unavailable")
		return
	}
	status, body, err := postClientAuth(r.Context(), meta.Token, form, meta.AuthMethods, row.OAuthClientID, secret)
	if err != nil {
		writeTokenErr(w, http.StatusBadGateway, "temporarily_unavailable")
		return
	}
	var out struct {
		AccessToken  string          `json:"access_token,omitempty"`
		TokenType    string          `json:"token_type,omitempty"`
		ExpiresIn    json.RawMessage `json:"expires_in,omitempty"`
		RefreshToken string          `json:"refresh_token,omitempty"`
		Scope        string          `json:"scope,omitempty"`
		Error        string          `json:"error,omitempty"`
	}
	jerr := json.Unmarshal(body, &out)
	if status != http.StatusOK {
		code := "server_error"
		if jerr == nil && tokenErrCodes[out.Error] {
			code = out.Error
		}
		answer := http.StatusBadGateway
		if status == http.StatusBadRequest || status == http.StatusUnauthorized {
			answer = http.StatusBadRequest
		}
		writeTokenErr(w, answer, code)
		return
	}
	if jerr != nil || out.AccessToken == "" {
		writeTokenErr(w, http.StatusBadGateway, "invalid_response")
		return
	}
	out.Error = ""
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

// tokenErrCodes are the token endpoint error codes relayed to the Agent (RFC 6749 §5.2, and
// RFC 8707's invalid_target). Anything else becomes server_error, so no text the AS chose
// reaches the Agent's error message.
var tokenErrCodes = map[string]bool{
	"invalid_request": true, "invalid_client": true, "invalid_grant": true, "unauthorized_client": true,
	"unsupported_grant_type": true, "invalid_scope": true, "invalid_target": true,
	"temporarily_unavailable": true, "server_error": true,
}

// OAuthRevoke (POST /internal/mcp-servers/{id}/oauth/revoke) revokes a token (RFC 7009) with the
// tenant's confidential client. 200 means the AS confirmed it.
func (a ServerAPI) OAuthRevoke(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil || r.PostForm.Get("token") == "" {
		writeTokenErr(w, http.StatusBadRequest, "invalid_request")
		return
	}
	row, secret, ok := a.bridgeClient(r.Context(), mv, r.PathValue("id"), r.PostForm)
	if !ok {
		writeTokenErr(w, http.StatusBadRequest, "unauthorized_client")
		return
	}
	meta, err := discoverAS(r.Context(), row.OAuthIssuer)
	if err != nil || meta.Revocation == "" {
		writeTokenErr(w, http.StatusBadGateway, "temporarily_unavailable")
		return
	}
	form := url.Values{"token": {r.PostForm.Get("token")}}
	if h := r.PostForm.Get("token_type_hint"); h == "refresh_token" || h == "access_token" {
		form.Set("token_type_hint", h)
	}
	// The revocation endpoint has its own list of methods (RFC 8414 §2).
	status, _, err := postClientAuth(r.Context(), meta.Revocation, form, meta.RevokeAuthMethods, row.OAuthClientID, secret)
	if err != nil {
		writeTokenErr(w, http.StatusBadGateway, "temporarily_unavailable")
		return
	}
	if status != http.StatusOK {
		writeTokenErr(w, http.StatusBadGateway, "server_error")
		return
	}
	w.WriteHeader(http.StatusOK)
}
