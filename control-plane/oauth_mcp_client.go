package main

// Sign-in for registered MCP servers that require OAuth (ADR 0119 decisions 3, 4 and 10).
// Agent Fleet is the OAuth client here; this is the opposite direction from mcp_oauth.go,
// where the Control Plane is the authorization server of its own /mcp.
//
//	Console ──POST /api/mcp-servers/{id}/oauth/start──► CP: mint state, ask the Agent for the
//	                                                    authorization URL, record the state
//	member's browser ──► the MCP server's authorization server ──► GET /oauth2/mcp/callback
//	CP: consume the state once, require the browser's session to be the member who started it,
//	    forward code / iss / state to that workspace's Agent, redirect to the Console
//
// The split is the connections one (oauth_jira.go) without the token exchange: the verifier,
// the code exchange and the tokens stay in the Agent, so nothing in this file ever holds a
// token, a code's meaning or a verifier. The code is forwarded and forgotten.
//
// /oauth2/ is auth-exempt (routes.go), so the callback owns its own session gate: authGate does
// not run the "is there a session" check for it, and a missing check here would let any
// browser holding a leaked state complete somebody else's sign-in.
//
// Wire shapes the Console consumes (console/src/features/settings/mcp/):
//
//	POST   /api/mcp-servers/{id}/oauth/start   body {registration?: "dcr"|"cimd"|"preregistered", scope?}
//	       200 {authorize_url, issuer, registration, scope?}
//	       409 client_id_needed {error:{code,message,issuer}}: enter a client id for that issuer
//	       other refusals: {error:{code,message,issuer?,registration?}} (codes: mcpreg.OAuthErr*)
//	GET    /api/mcp-servers/{id}/oauth         Agent status (mcpreg.OAuthStatus), proxied
//	DELETE /api/mcp-servers/{id}/oauth         {serverId, issuer, hadGrant, revoked}
//	PUT    /api/mcp-servers/{id}/oauth/client  body {issuer, clientId, clientSecret?}, proxied
//	GET    /oauth2/mcp/callback                → 302 /?mcp_oauth=ok|failed&server=<id>[&reason=<code>][&registration=<path>]

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	mcpClientOAuthCallbackPath = "/oauth2/mcp/callback"
	mcpClientOAuthCIMDPath     = "/oauth/mcp-client.json"
	mcpClientOAuthTTL          = 10 * time.Minute
	mcpClientOAuthAudit        = "mcp.client_oauth"
	// mcpClientOAuthAgentTimeout bounds the Agent calls: the callback holds the member's tab,
	// and the code exchange reaches an authorization server through the workspace's egress.
	mcpClientOAuthAgentTimeout = 60 * time.Second
	// mcpClientOAuthAbandonTimeout bounds the best-effort cleanup call, which runs while the
	// member's browser waits for the redirect.
	mcpClientOAuthAbandonTimeout = 5 * time.Second
	maxCallbackParam             = 4096
)

var mcpServerIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// mcpClientOAuthReasons is the closed set of failure reasons the callback redirect may carry.
// The Agent's code is mapped through it, so nothing the Agent or the authorization server said
// reaches the address bar except one of these words.
var mcpClientOAuthReasons = map[string]bool{
	"state_unknown": true, "state_expired": true, "state_mismatch": true, "session": true,
	"authorization_denied": true, "iss_mismatch": true, "iss_missing": true, "no_code": true,
	"token_exchange_failed": true, "workspace_unreachable": true, "internal": true, "failed": true,
}

var mcpClientOAuthRegistrations = map[string]bool{"preregistered": true, "cimd": true, "dcr": true}

func (c config) mcpClientOAuthBase() string { return strings.TrimRight(c.mgr.publicBaseURL, "/") }

// handleMCPClientOAuthCIMD serves the deployment's client ID metadata document: one per
// deployment, whose client_id is its own URL. CIMD needs an https URL the authorization server
// can fetch, so anything else answers 404 and the Agent never offers it.
func (c config) handleMCPClientOAuthCIMD(w http.ResponseWriter, r *http.Request) {
	base := c.mcpClientOAuthBase()
	if !strings.HasPrefix(base, "https://") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = json.NewEncoder(w).Encode(mcpClientCIMDWire{
		ClientID:                base + mcpClientOAuthCIMDPath,
		ClientName:              c.brand.get(r.Context()).name("Agent Fleet"),
		RedirectURIs:            []string{base + mcpClientOAuthCallbackPath},
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
	})
}

// mcpClientCIMDWire is the client ID metadata document (draft-ietf-oauth-client-id-metadata-document).
type mcpClientCIMDWire struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

// agentOAuthError is the Agent's error envelope.
type agentOAuthError struct {
	Error struct {
		Code         string `json:"code"`
		Message      string `json:"message"`
		Issuer       string `json:"issuer"`
		Registration string `json:"registration"`
	} `json:"error"`
}

// callAgentOAuth sends one request to the member's Agent and returns the status and the
// (bounded) body.
func (c config) callAgentOAuth(ctx context.Context, res *resolved, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, mcpClientOAuthAgentTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, res.rt.Endpoint()+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, b, nil
}

// writeAgentOAuthError relays an Agent refusal in the shared envelope. Only the fields of the
// envelope pass; the status is kept for the codes the Console reacts to and otherwise 502.
func writeAgentOAuthError(w http.ResponseWriter, status int, raw []byte) {
	var e agentOAuthError
	if json.Unmarshal(raw, &e) != nil || e.Error.Code == "" {
		writeAPIErr(w, &apiError{http.StatusBadGateway, "agent_error", "the workspace agent could not prepare the sign-in"})
		return
	}
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	var out mcpClientErrorWire
	out.Error.Code, out.Error.Message = e.Error.Code, e.Error.Message
	out.Error.Issuer, out.Error.Registration = e.Error.Issuer, e.Error.Registration
	writeJSON(w, status, out)
}

// mcpClientErrorWire is the shared error envelope plus the issuer and registration path a
// refusal concerns (client_id_needed names the issuer to enter a client id for).
type mcpClientErrorWire struct {
	Error struct {
		Code         string `json:"code"`
		Message      string `json:"message"`
		Issuer       string `json:"issuer,omitempty"`
		Registration string `json:"registration,omitempty"`
	} `json:"error"`
}

func (c config) auditMCPClientOAuth(ctx context.Context, st store.MCPClientOAuthState, event, reason string) {
	target := st.ServerName
	if target == "" {
		target = st.ServerID
	}
	// Detail names the server and the authorization server; a code, token or verifier is never
	// among what this function is given.
	detail := "event=" + event + " server=" + st.ServerID + " issuer=" + clipAudit(st.Issuer)
	if st.Registration != "" {
		detail += " registration=" + st.Registration
	}
	if reason != "" {
		detail += " reason=" + reason
	}
	if err := c.mgr.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: st.TenantID, ActorKind: "user", ActorID: st.IdentityID,
		Action: mcpClientOAuthAudit, Target: target, Detail: detail,
		HTTPStatus: http.StatusOK, At: store.NowTS(),
	}); err != nil {
		log.Printf("mcp client oauth: audit %s: %v", event, err)
	}
}

func clipAudit(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// handleMCPClientOAuthStart (POST /api/mcp-servers/{id}/oauth/start) prepares the sign-in and
// returns the URL to open.
func (c config) handleMCPClientOAuthStart(w http.ResponseWriter, r *http.Request, res *resolved) {
	serverID := r.PathValue("id")
	if !mcpServerIDRe.MatchString(serverID) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid server id"})
		return
	}
	base := c.mcpClientOAuthBase()
	if base == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "no_public_base_url",
			"signing in to an MCP server needs a callback URL, but this deployment has no PUBLIC_BASE_URL set — ask the operator"})
		return
	}
	var in struct {
		Registration string `json:"registration"`
		Scope        string `json:"scope"`
	}
	if b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10)); len(bytes.TrimSpace(b)) > 0 {
		if json.Unmarshal(b, &in) != nil {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
			return
		}
	}
	if in.Registration != "" && !mcpClientOAuthRegistrations[in.Registration] {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "unknown registration path"})
		return
	}
	cimd := ""
	if strings.HasPrefix(base, "https://") {
		cimd = base + mcpClientOAuthCIMDPath
	}
	state := randHex(32)
	status, raw, err := c.callAgentOAuth(r.Context(), res, http.MethodPost,
		"/mcp-servers/"+url.PathEscape(serverID)+"/oauth/start", map[string]string{
			"state": state, "redirectUri": base + mcpClientOAuthCallbackPath, "cimdUrl": cimd,
			"clientName":   c.brand.get(r.Context()).name("Agent Fleet"),
			"registration": in.Registration, "scope": in.Scope,
		})
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusBadGateway, "workspace_unreachable", "workspace agent unreachable (is the workspace running?)"})
		return
	}
	if status != http.StatusOK {
		writeAgentOAuthError(w, status, raw)
		return
	}
	var out struct {
		AuthorizeURL string `json:"authorizeUrl"`
		ServerName   string `json:"serverName"`
		Issuer       string `json:"issuer"`
		Registration string `json:"registration"`
		Scope        string `json:"scope"`
	}
	if json.Unmarshal(raw, &out) != nil || out.AuthorizeURL == "" {
		writeAPIErr(w, &apiError{http.StatusBadGateway, "agent_error", "the workspace agent returned no authorization URL"})
		return
	}
	now := time.Now()
	st := store.MCPClientOAuthState{
		IdentityID: res.ident.ID, MembershipID: res.mv.MembershipID, TenantID: res.mv.TenantID,
		WorkspaceID: res.ws.ID, ServerID: serverID, ServerName: out.ServerName,
		Issuer: out.Issuer, Registration: out.Registration, ExpiresAt: now.Add(mcpClientOAuthTTL).Unix(),
	}
	if err := c.mgr.store.PutMCPClientOAuthState(r.Context(), state, st, now.Unix()); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	c.auditMCPClientOAuth(r.Context(), st, "started", "")
	writeJSON(w, http.StatusOK, mcpClientStartWire{
		AuthorizeURL: out.AuthorizeURL, Issuer: out.Issuer, Registration: out.Registration, Scope: out.Scope,
	})
}

// mcpClientStartWire is the answer to the Console's sign-in start.
type mcpClientStartWire struct {
	AuthorizeURL string `json:"authorize_url"`
	Issuer       string `json:"issuer"`
	Registration string `json:"registration"`
	Scope        string `json:"scope,omitempty"`
}

// consoleRedirect sends the browser back to the Console with the outcome and nothing else. The
// target is a fixed origin-relative path: no request parameter chooses where the browser goes,
// and every value placed in the query is from a closed set or a server id this handler
// validated, so there is no open redirect and no code, token or provider text in the URL.
func consoleRedirect(w http.ResponseWriter, r *http.Request, ok bool, serverID, reason, registration string) {
	q := url.Values{}
	if ok {
		q.Set("mcp_oauth", "ok")
	} else {
		q.Set("mcp_oauth", "failed")
		if !mcpClientOAuthReasons[reason] {
			reason = "failed"
		}
		q.Set("reason", reason)
	}
	if mcpServerIDRe.MatchString(serverID) {
		q.Set("server", serverID)
	}
	if mcpClientOAuthRegistrations[registration] {
		q.Set("registration", registration)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, "/?"+q.Encode(), http.StatusFound)
}

// handleMCPClientOAuthCallback (GET /oauth2/mcp/callback) finishes a sign-in.
func (c config) handleMCPClientOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" || len(state) > maxCallbackParam {
		consoleRedirect(w, r, false, "", "state_unknown", "")
		return
	}
	// The state is spent before anything else is checked, so every outcome below, a refusal
	// included, leaves it unusable.
	st, ok, expired, err := c.mgr.store.TakeMCPClientOAuthState(r.Context(), state, time.Now().Unix())
	if err != nil {
		log.Printf("mcp client oauth: take state: %v", err)
		consoleRedirect(w, r, false, "", "internal", "")
		return
	}
	if !ok {
		if expired {
			c.auditMCPClientOAuth(r.Context(), st, "failed", "state_expired")
			c.abandonMCPClientOAuth(r.Context(), st, state)
			consoleRedirect(w, r, false, st.ServerID, "state_expired", st.Registration)
			return
		}
		consoleRedirect(w, r, false, "", "state_unknown", "")
		return
	}
	// forwarded is true once the Agent has seen the callback: it spends its own pending entry
	// then, so there is nothing to abandon.
	forwarded := false
	fail := func(reason string) {
		c.auditMCPClientOAuth(r.Context(), st, "failed", reason)
		if !forwarded {
			c.abandonMCPClientOAuth(r.Context(), st, state)
		}
		consoleRedirect(w, r, false, st.ServerID, reason, st.Registration)
	}
	// The browser must be the member who started the sign-in: without this, whoever obtains
	// the redirect URL (a shared link, a proxy log) could finish the flow in their own browser,
	// or push the code into another member's workspace.
	ident, ok := c.mcpClientCallbackCaller(r)
	if !ok || ident.ID != st.IdentityID {
		fail("session")
		return
	}
	code, iss, aserr := q.Get("code"), q.Get("iss"), q.Get("error")
	if len(code) > maxCallbackParam || len(iss) > maxCallbackParam {
		fail("failed")
		return
	}
	// The membership must still be active, and the workspace is the one that began the flow.
	rs, aerr := c.mgr.resolveByMembership(r.Context(), st.IdentityID, st.MembershipID)
	if aerr != nil || rs.ws.ID != st.WorkspaceID {
		fail("workspace_unreachable")
		return
	}
	body := map[string]string{"state": state, "code": code, "iss": iss}
	if aserr != "" {
		body["error"] = "authorization_error" // the AS's own text is not forwarded
		body["code"] = ""
	}
	status, raw, err := c.callAgentOAuth(r.Context(), rs, http.MethodPost,
		"/mcp-servers/"+url.PathEscape(st.ServerID)+"/oauth/callback", body)
	if err != nil {
		fail("workspace_unreachable") // the Agent saw nothing, so it is still told to drop the verifier
		return
	}
	forwarded = true
	if status != http.StatusOK {
		var e agentOAuthError
		reason := "failed"
		if json.Unmarshal(raw, &e) == nil && mcpClientOAuthReasons[e.Error.Code] {
			reason = e.Error.Code
		}
		if e.Error.Issuer != "" {
			st.Issuer = e.Error.Issuer
		}
		if mcpClientOAuthRegistrations[e.Error.Registration] {
			st.Registration = e.Error.Registration
		}
		fail(reason)
		return
	}
	var done struct {
		Issuer       string `json:"issuer"`
		Registration string `json:"registration"`
	}
	_ = json.Unmarshal(raw, &done)
	if done.Issuer != "" {
		st.Issuer = done.Issuer
	}
	if mcpClientOAuthRegistrations[done.Registration] {
		st.Registration = done.Registration
	}
	c.auditMCPClientOAuth(r.Context(), st, "completed", "")
	consoleRedirect(w, r, true, st.ServerID, "", st.Registration)
}

// mcpClientCallbackCaller identifies the browser that reached the callback. The path is
// auth-exempt, so nothing upstream has done it: under AUTH=oauth the Console session cookie is
// verified here, and re-authorized the way authGate does on every other request; under
// AUTH=proxy the gateway's identity header decides (an absent one fails closed); under
// AUTH=dev the one fixed user is the caller.
func (c config) mcpClientCallbackCaller(r *http.Request) (store.Identity, bool) {
	if c.mgr.authMode == "oauth" {
		claims, ok := c.session(r)
		if !ok {
			return store.Identity{}, false
		}
		if allowed, _ := c.sessionAllowed(r.Context(), claims); !allowed {
			return store.Identity{}, false
		}
		return c.linkCaller(r, claims)
	}
	ident, aerr := c.mgr.identityFor(r.Context(), r)
	return ident, aerr == nil
}

// abandonMCPClientOAuth tells the Agent to drop the verifier it holds for a state that failed
// here. The Agent expires it on its own; this only shortens the window. Best effort.
func (c config) abandonMCPClientOAuth(ctx context.Context, st store.MCPClientOAuthState, state string) {
	rs, aerr := c.mgr.resolveByMembership(ctx, st.IdentityID, st.MembershipID)
	if aerr != nil || rs.ws.ID != st.WorkspaceID {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, mcpClientOAuthAbandonTimeout)
	defer cancel()
	_, _, _ = c.callAgentOAuth(ctx, rs, http.MethodPost,
		"/mcp-servers/"+url.PathEscape(st.ServerID)+"/oauth/callback",
		map[string]string{"state": state, "error": "abandoned"})
}

// handleMCPClientOAuthDisconnect (DELETE /api/mcp-servers/{id}/oauth) revokes at the
// authorization server when it offers revocation and deletes the grant either way. It is a CP
// handler rather than a plain proxy so the disconnect is audited.
func (c config) handleMCPClientOAuthDisconnect(w http.ResponseWriter, r *http.Request, res *resolved) {
	serverID := r.PathValue("id")
	if !mcpServerIDRe.MatchString(serverID) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid server id"})
		return
	}
	status, raw, err := c.callAgentOAuth(r.Context(), res, http.MethodDelete,
		"/mcp-servers/"+url.PathEscape(serverID)+"/oauth", nil)
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusBadGateway, "workspace_unreachable", "workspace agent unreachable (is the workspace running?)"})
		return
	}
	if status != http.StatusOK {
		writeAgentOAuthError(w, status, raw)
		return
	}
	var out struct {
		ServerID string `json:"serverId"`
		Issuer   string `json:"issuer"`
		HadGrant bool   `json:"hadGrant"`
		Revoked  bool   `json:"revoked"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadGateway, "agent_error", "unreadable answer from the workspace agent"})
		return
	}
	// A sign-in still open in a browser tab must not complete after the disconnect.
	if err := c.mgr.store.DeleteMCPClientOAuthStates(r.Context(), res.mv.MembershipID, serverID); err != nil {
		log.Printf("mcp client oauth: drop open sign-ins: %v", err)
	}
	if out.HadGrant {
		reason := ""
		if !out.Revoked {
			reason = "not_revoked_at_as"
		}
		c.auditMCPClientOAuth(r.Context(), store.MCPClientOAuthState{
			IdentityID: res.ident.ID, TenantID: res.mv.TenantID, ServerID: serverID, Issuer: out.Issuer,
		}, "disconnected", reason)
	}
	writeJSON(w, http.StatusOK, out)
}
