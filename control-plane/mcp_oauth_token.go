// mcp_oauth_token.go — the server-to-server half of the /mcp authorization server: the token
// endpoint (authorization_code and refresh_token grants) and RFC 7009 revocation.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

type mcpTokenError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

func mcpTokenErr(w http.ResponseWriter, status int, code, desc string) {
	noStore(w)
	writeJSON(w, status, mcpTokenError{Error: code, Description: desc})
}

type mcpTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// publicClient reads the client_id of a public-client request. A client that authenticates
// (Basic header, client_secret) is refused: every client here is public, and quietly ignoring
// a secret would let a misconfigured client believe it was protected.
func (a *mcpOAuthAPI) publicClient(w http.ResponseWriter, r *http.Request) (string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		mcpTokenErr(w, http.StatusBadRequest, "invalid_request", "unreadable form body")
		return "", false
	}
	if r.Header.Get("Authorization") != "" || r.PostForm.Has("client_secret") || r.PostForm.Has("client_assertion") {
		mcpTokenErr(w, http.StatusUnauthorized, "invalid_client", "only public clients (token_endpoint_auth_method none) are supported")
		return "", false
	}
	id := r.PostForm.Get("client_id")
	if id == "" {
		mcpTokenErr(w, http.StatusBadRequest, "invalid_request", "client_id is required")
		return "", false
	}
	return id, true
}

// clientStillRegistered is the token endpoint's client check. A DCR client must still be in the
// table (a deleted one is invalid_client, which is claude.ai's cue to register again); a CIMD
// client must still be on a trusted host. The document is not fetched here: the code's own
// stored redirect is what is matched.
func (a *mcpOAuthAPI) clientStillRegistered(ctx context.Context, trusted trustedHosts, id string) (ok bool, err error) {
	if strings.HasPrefix(id, "https://") {
		_, ok := a.cimdURLAllowed(trusted, id)
		return ok, nil
	}
	_, ok, err = a.mgr.store.GetMCPOAuthClient(ctx, id)
	return ok, err
}

func (a *mcpOAuthAPI) token(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	clientID, ok := a.publicClient(w, r)
	if !ok {
		return
	}
	trusted, _ := a.trustedNow(ctx)
	switch registered, err := a.clientStillRegistered(ctx, trusted, clientID); {
	case err != nil:
		log.Printf("mcp oauth: client lookup: %v", err)
		mcpTokenErr(w, http.StatusInternalServerError, "server_error", "could not look the client up")
		return
	case !registered:
		mcpTokenErr(w, http.StatusUnauthorized, "invalid_client", "the client is not registered")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		a.exchangeCode(w, r, clientID)
	case "refresh_token":
		a.refresh(w, r, clientID)
	default:
		mcpTokenErr(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code and refresh_token are supported")
	}
}

func (a *mcpOAuthAPI) audit(ctx context.Context, tenantID, identityID, action, target, detail string) error {
	return a.mgr.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: tenantID, ActorKind: "user", ActorID: identityID,
		Action: action, Target: target, Detail: detail, At: store.NowTS(),
	})
}

// auditGrant writes a grant-scoped audit row under the grant's own tenant, so the tenant's
// administrators see revocations as well as issuance.
func (a *mcpOAuthAPI) auditGrant(ctx context.Context, grantID, identityID, detail string) error {
	tenant := ""
	if g, ok, err := a.mgr.store.GetMCPOAuthGrant(ctx, grantID); err == nil && ok {
		tenant = g.TenantID
	}
	return a.audit(ctx, tenant, identityID, "mcp.oauth.grant", grantID, detail)
}

func (a *mcpOAuthAPI) exchangeCode(w http.ResponseWriter, r *http.Request, clientID string) {
	ctx := r.Context()
	if r.PostForm.Get("resource") != a.resource {
		mcpTokenErr(w, http.StatusBadRequest, "invalid_target", "resource must be "+a.resource)
		return
	}
	now := a.now()
	codeHash := devIdPHash(r.PostForm.Get("code"))
	c, spend, err := a.mgr.store.ConsumeMCPOAuthToken(ctx, codeHash, store.MCPOAuthCode, now.Unix())
	if err != nil {
		log.Printf("mcp oauth: consume code: %v", err)
		mcpTokenErr(w, http.StatusInternalServerError, "server_error", "could not redeem the code")
		return
	}
	switch spend {
	case store.MCPOAuthSpendUnknown:
		mcpTokenErr(w, http.StatusBadRequest, "invalid_grant", "the code is unknown or expired")
		return
	case store.MCPOAuthSpendReplayed:
		// A spent code presented again: whoever holds the tokens it produced may not be who was
		// meant to (RFC 6749 §4.1.2), so the grant it minted goes down.
		a.revokeForReplay(ctx, codeHash, c.IdentityID, now.Unix())
		mcpTokenErr(w, http.StatusBadRequest, "invalid_grant", "the code was already used")
		return
	}
	switch {
	case c.ClientID != clientID:
		mcpTokenErr(w, http.StatusBadRequest, "invalid_grant", "the code was issued to another client")
		return
	case r.PostForm.Get("redirect_uri") != c.RedirectURI:
		mcpTokenErr(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the authorization request")
		return
	case c.Resource != a.resource:
		mcpTokenErr(w, http.StatusBadRequest, "invalid_target", "the code is bound to another resource")
		return
	case !pkceMatches(r.PostForm.Get("code_verifier"), c.Challenge):
		mcpTokenErr(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match the code_challenge")
		return
	}
	// The permission is re-decided now, not trusted from the minute before: the member must
	// still hold the membership the code was issued for.
	ms, err := a.mgr.store.ListMemberships(ctx, c.IdentityID)
	if err != nil {
		log.Printf("mcp oauth: memberships: %v", err)
		mcpTokenErr(w, http.StatusInternalServerError, "server_error", "could not check the membership")
		return
	}
	var mv *store.MembershipView
	for i := range ms {
		if ms[i].MembershipID == c.MembershipID {
			mv = &ms[i]
		}
	}
	enabled := false
	if mv != nil {
		enabled, err = a.mgr.store.GetTenantMCPOAuth(ctx, mv.TenantID)
	}
	if err != nil {
		mcpTokenErr(w, http.StatusInternalServerError, "server_error", "could not check tenant")
		return
	}
	if mv == nil || !enabled || c.State == "tenant-disabled" {
		mcpTokenErr(w, http.StatusBadRequest, "invalid_grant", "the membership is no longer active")
		return
	}
	host := ""
	if u, err := url.Parse(c.RedirectURI); err == nil {
		host = u.Host
	}
	g := store.MCPOAuthGrant{
		ID: store.NewID(), ClientID: c.ClientID, ClientName: c.ClientName, RedirectHost: host,
		IdentityID: c.IdentityID, MembershipID: c.MembershipID, TenantID: mv.TenantID,
		Scope: scopeString(scopeLevel(c.Scope)), Resource: a.resource, CodeHash: codeHash,
		CreatedAt: now.Unix(), IdleExpiresAt: now.Add(mcpOAuthIdleTTL).Unix(), AbsExpiresAt: now.Add(mcpOAuthAbsoluteTTL).Unix(),
	}
	// Audited before anything leaves: no row, no token. The rows name the client, the grant and
	// the scope, never a token.
	if err := a.audit(ctx, g.TenantID, g.IdentityID, "mcp.oauth.grant", g.ID,
		"created client="+strconv.Quote(g.ClientID)+" redirect_host="+strconv.Quote(host)+" scope="+strconv.Quote(g.Scope)); err != nil {
		log.Printf("mcp oauth: audit: %v", err)
		mcpTokenErr(w, http.StatusInternalServerError, "server_error", "audit unavailable")
		return
	}
	if err := a.mgr.store.CreateMCPOAuthGrant(ctx, g); err != nil {
		if errors.Is(err, store.ErrMCPOAuthDisabled) {
			if auditErr := a.audit(ctx, g.TenantID, g.IdentityID, "mcp.oauth.grant", g.ID, "refused reason=tenant-disabled"); auditErr != nil {
				log.Printf("mcp oauth: audit refusal: %v", auditErr)
				mcpTokenErr(w, http.StatusInternalServerError, "server_error", "audit unavailable")
				return
			}
			mcpTokenErr(w, http.StatusBadRequest, "invalid_grant", "the tenant disabled this connection")
			return
		}
		log.Printf("mcp oauth: create grant: %v", err)
		mcpTokenErr(w, http.StatusInternalServerError, "server_error", "could not create the grant")
		return
	}
	// Insert first, look second: a replay that marked the code before this point is seen here,
	// and one that marks it later finds the grant and revokes it. Neither order leaves a live token.
	if cur, ok, err := a.mgr.store.GetMCPOAuthTokenByHash(ctx, codeHash, now.Unix()); err != nil || (ok && cur.State == "replayed") {
		a.revokeForReplay(ctx, codeHash, g.IdentityID, now.Unix())
		mcpTokenErr(w, http.StatusBadRequest, "invalid_grant", "the code was already used")
		return
	}
	a.issueTokens(w, r, g, g.Scope, "issued")
}

// revokeForReplay takes down every grant minted from a code that was presented again.
func (a *mcpOAuthAPI) revokeForReplay(ctx context.Context, codeHash, identityID string, now int64) {
	if err := a.mgr.store.MarkMCPOAuthCodeReplayed(ctx, codeHash); err != nil {
		log.Printf("mcp oauth: mark replay: %v", err)
	}
	ids, err := a.mgr.store.RevokeMCPOAuthGrantsByCode(ctx, codeHash, "authorization code replayed", now)
	if err != nil {
		log.Printf("mcp oauth: revoke on replay: %v", err)
	}
	for _, id := range ids {
		_ = a.auditGrant(ctx, id, identityID, "reuse-revoked reason=code-replay")
	}
}

// issueTokens mints a fresh access and refresh token for a live grant and answers with them.
// accessScope may be narrower than the grant's.
func (a *mcpOAuthAPI) issueTokens(w http.ResponseWriter, r *http.Request, g store.MCPOAuthGrant, accessScope, why string) {
	ctx := r.Context()
	now := a.now()
	access, refresh := "afmcp_at_"+devIdPRandom(32), "afmcp_rt_"+devIdPRandom(32)
	capped := func(d time.Duration) int64 {
		if exp := now.Add(d).Unix(); exp < g.AbsExpiresAt {
			return exp
		}
		return g.AbsExpiresAt
	}
	if err := a.audit(ctx, g.TenantID, g.IdentityID, "mcp.oauth.token", g.ID,
		why+" client="+strconv.Quote(g.ClientID)+" scope="+strconv.Quote(accessScope)); err != nil {
		log.Printf("mcp oauth: audit: %v", err)
		mcpTokenErr(w, http.StatusInternalServerError, "server_error", "audit unavailable")
		return
	}
	base := store.MCPOAuthToken{GrantID: g.ID, ClientID: g.ClientID, IdentityID: g.IdentityID,
		MembershipID: g.MembershipID, Resource: a.resource}
	at, rt := base, base
	at.Hash, at.Kind, at.Scope, at.ExpiresAt = devIdPHash(access), store.MCPOAuthAccessKind, accessScope, capped(mcpOAuthAccessTTL)
	rt.Hash, rt.Kind, rt.Scope, rt.ExpiresAt = devIdPHash(refresh), store.MCPOAuthRefresh, g.Scope, capped(mcpOAuthIdleTTL)
	for _, t := range []store.MCPOAuthToken{at, rt} {
		if err := a.mgr.store.InsertMCPOAuthToken(ctx, t); err != nil {
			log.Printf("mcp oauth: store token: %v", err)
			mcpTokenErr(w, http.StatusInternalServerError, "server_error", "could not issue a token")
			return
		}
	}
	// Read after inserting: disabling before this point is observed here; disabling
	// afterwards revokes the stored grant. The revocation survives re-enabling.
	enabled, gateErr := a.mgr.store.GetTenantMCPOAuth(ctx, g.TenantID)
	current, found, readErr := a.mgr.store.GetMCPOAuthGrant(ctx, g.ID)
	if gateErr != nil || readErr != nil {
		mcpTokenErr(w, http.StatusInternalServerError, "server_error", "could not check tenant")
		return
	}
	if !enabled || !found || current.RevokedReason == "tenant-disabled" {
		mcpTokenErr(w, http.StatusBadRequest, "invalid_grant", "the connection has ended")
		return
	}

	_ = a.mgr.store.TouchMCPOAuthGrant(ctx, g.ID, now.Unix(), int64(mcpOAuthIdleTTL.Seconds()))
	noStore(w)
	writeJSON(w, http.StatusOK, mcpTokenResponse{
		AccessToken: access, TokenType: "Bearer", ExpiresIn: int(mcpOAuthAccessTTL.Seconds()),
		RefreshToken: refresh, Scope: accessScope,
	})
}

func (a *mcpOAuthAPI) refresh(w http.ResponseWriter, r *http.Request, clientID string) {
	ctx := r.Context()
	if res := r.PostForm.Get("resource"); res != "" && res != a.resource {
		mcpTokenErr(w, http.StatusBadRequest, "invalid_target", "resource must be "+a.resource)
		return
	}
	now := a.now()
	hash := devIdPHash(r.PostForm.Get("refresh_token"))
	// Look before spending: a token presented under the wrong client_id must not burn the
	// rightful client's token.
	cur, found, err := a.mgr.store.GetMCPOAuthTokenByHash(ctx, hash, now.Unix())
	if err != nil {
		log.Printf("mcp oauth: refresh lookup: %v", err)
		mcpTokenErr(w, http.StatusInternalServerError, "server_error", "could not read the token")
		return
	}
	if !found || cur.Kind != store.MCPOAuthRefresh || cur.ClientID != clientID {
		mcpTokenErr(w, http.StatusBadRequest, "invalid_grant", "the refresh token is unknown or expired")
		return
	}
	g, ok, err := a.mgr.store.GetMCPOAuthGrant(ctx, cur.GrantID)
	if err != nil {
		log.Printf("mcp oauth: grant: %v", err)
		mcpTokenErr(w, http.StatusInternalServerError, "server_error", "could not read the grant")
		return
	}
	if !ok {
		mcpTokenErr(w, http.StatusBadRequest, "invalid_grant", "the connection has ended")
		return
	}
	enabled, gateErr := a.mgr.store.GetTenantMCPOAuth(ctx, g.TenantID)
	if gateErr != nil {
		mcpTokenErr(w, http.StatusInternalServerError, "server_error", "could not check tenant")
		return
	}
	if !enabled || g.RevokedAt != 0 || g.AbsExpiresAt <= now.Unix() || g.IdleExpiresAt <= now.Unix() {
		mcpTokenErr(w, http.StatusBadRequest, "invalid_grant", "the connection has ended")
		return
	}
	scope := g.Scope
	if want := r.PostForm.Get("scope"); want != "" {
		level, ok := parseRequestedScope(want)
		if !ok || level > scopeLevel(g.Scope) {
			mcpTokenErr(w, http.StatusBadRequest, "invalid_scope", "a refresh can only narrow the granted scope")
			return
		}
		scope = scopeString(level)
	}
	// The grant is checked before the token is spent, so of concurrent refreshes the winner is
	// decided by the spend alone; the losers then present a spent token and take the grant down,
	// which is what reuse means.
	t, spend, err := a.mgr.store.ConsumeMCPOAuthToken(ctx, hash, store.MCPOAuthRefresh, now.Unix())
	if err != nil {
		log.Printf("mcp oauth: consume refresh: %v", err)
		mcpTokenErr(w, http.StatusInternalServerError, "server_error", "could not redeem the token")
		return
	}
	switch spend {
	case store.MCPOAuthSpendUnknown:
		mcpTokenErr(w, http.StatusBadRequest, "invalid_grant", "the refresh token is unknown or expired")
		return
	case store.MCPOAuthSpendReplayed:
		// A spent refresh token presented again means two parties hold it (OAuth 2.1 §4.3.1):
		// the whole grant goes, so neither keeps working.
		if err := a.auditGrant(ctx, t.GrantID, t.IdentityID, "reuse-revoked reason=refresh-reuse"); err != nil {
			log.Printf("mcp oauth: audit: %v", err)
		}
		if _, err := a.mgr.store.RevokeMCPOAuthGrant(ctx, t.GrantID, "refresh token reused", now.Unix()); err != nil {
			log.Printf("mcp oauth: revoke on reuse: %v", err)
		}
		a.mgr.mcpCalls.Forget(mcpsrv.CallKey(true, t.GrantID))
		mcpTokenErr(w, http.StatusBadRequest, "invalid_grant", "the refresh token was already used; the connection was revoked")
		return
	}
	a.issueTokens(w, r, g, scope, "refreshed")
}

// revoke is RFC 7009. Whatever the token turns out to be, the answer is 200 (the endpoint
// must not tell a caller which tokens exist); a token that is the client's own ends its whole
// grant, since access and refresh tokens are one connection.
func (a *mcpOAuthAPI) revoke(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	clientID, ok := a.publicClient(w, r)
	if !ok {
		return
	}
	now := a.now()
	t, found, err := a.mgr.store.GetMCPOAuthTokenByHash(ctx, devIdPHash(r.PostForm.Get("token")), now.Unix())
	if err != nil {
		log.Printf("mcp oauth: revoke lookup: %v", err)
		mcpTokenErr(w, http.StatusInternalServerError, "server_error", "could not read the token")
		return
	}
	if found && t.ClientID == clientID && t.GrantID != "" && (t.Kind == store.MCPOAuthAccessKind || t.Kind == store.MCPOAuthRefresh) {
		if err := a.auditGrant(ctx, t.GrantID, t.IdentityID, "revoked by client"); err != nil {
			log.Printf("mcp oauth: audit: %v", err)
			mcpTokenErr(w, http.StatusInternalServerError, "server_error", "audit unavailable")
			return
		}
		if _, err := a.mgr.store.RevokeMCPOAuthGrant(ctx, t.GrantID, "revoked by client", now.Unix()); err != nil {
			log.Printf("mcp oauth: revoke: %v", err)
			mcpTokenErr(w, http.StatusInternalServerError, "server_error", "could not revoke")
			return
		}
		a.mgr.mcpCalls.Forget(mcpsrv.CallKey(true, t.GrantID))
	}
	noStore(w)
	w.WriteHeader(http.StatusOK)
}
