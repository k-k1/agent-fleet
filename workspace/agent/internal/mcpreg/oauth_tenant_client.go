package mcpreg

// A tenant's pre-registered OAuth client for a distributed server (ADR 0119 decision 9).
//
// A public client is just a client id: it is used like one the member entered. A confidential
// client's secret never reaches the workspace. The CP keeps it and runs the token and revocation
// requests for this Agent (POST /internal/mcp-servers/{id}/oauth/{token,revoke}, authenticated
// with the membership's AF_MCP_TOKEN like the distribution poll). The verifier, the code and the
// tokens pass through the CP without being stored there; the grant stays in this workspace.

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cpurl"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// OAuthRegTenant is the registration path of a client the tenant pre-registered.
const OAuthRegTenant = "tenant"

// tokenAuthCP marks a grant whose token and revocation requests go through the CP.
const tokenAuthCP = "cp"

// tenantClient is the tenant's client for srv, when the definition carries one for that issuer.
// A client registered at another AS says nothing about this one, so the order goes on.
func tenantClient(d ServerDef, srv OAuthServer) (clientIdentity, bool) {
	c := d.OAuthClient
	if d.Origin != OriginTenant || c == nil || !sameIssuer(c.Issuer, srv.Issuer) || !validTenantClient(*c) {
		return clientIdentity{}, false
	}
	auth := "none"
	if c.Confidential {
		auth = tokenAuthCP
	}
	return clientIdentity{OAuthRegTenant, c.ClientID, "", auth}, true
}

// cpOAuthBridgeHTTP talks to the CP only. It follows no redirect: the request carries the
// membership's token.
var cpOAuthBridgeHTTP = &http.Client{
	Timeout:       oauthHTTPTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// cpOAuthRequest names the client the grant belongs to (issuer and client id) next to the form.
// The CP refuses when the server's client is no longer that one, so a grant made with a client
// the admin has since replaced never reaches the new client's authorization server.
func cpOAuthRequest(ctx context.Context, serverID, issuer, clientID, op string, in url.Values) (*http.Response, error) {
	base, token := cpurl.Request(), os.Getenv("AF_MCP_TOKEN")
	if base == "" || token == "" {
		return nil, ErrTenantBridgeOff
	}
	form := url.Values{}
	for k, v := range in {
		form[k] = v
	}
	form.Set("issuer", issuer)
	form.Set("client_id", clientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+"/internal/mcp-servers/"+url.PathEscape(serverID)+"/oauth/"+op,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return cpOAuthBridgeHTTP.Do(req)
}

// postTokenCP sends a token request through the CP. The form carries no client authentication:
// the CP adds the tenant's secret for the server named in the path. Its answer has
// the token endpoint's shape, with the CP's own refusals as RFC 6749-style error codes.
func postTokenCP(ctx context.Context, serverID, issuer, clientID string, form url.Values) (tokenResponse, *tokenError) {
	resp, err := cpOAuthRequest(ctx, serverID, issuer, clientID, "token", form)
	if err != nil {
		return tokenResponse{}, &tokenError{code: "cp_unreachable"}
	}
	return readTokenResponse(resp)
}

func revokeTokenCP(ctx context.Context, serverID, issuer, clientID, token, hint string) bool {
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := cpOAuthRequest(rctx, serverID, issuer, clientID, "revoke", url.Values{"token": {token}, "token_type_hint": {hint}})
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// sameIssuer compares issuer identifiers ignoring one trailing "/". The specification compares
// them exactly, and the AS metadata check still does; this only keeps an admin's
// "https://as.example.com/" from silently not matching "https://as.example.com" (and the
// reverse, for issuers such as Auth0's that end in "/").
func sameIssuer(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

// pendingStillCovers reports whether the sign-in p was started for what the server is now: still
// an OAuth definition at the URL p's resource names, with the same tenant client (or none).
func pendingStillCovers(p *pendingAuth) bool {
	d, err := Get(p.serverID)
	if err != nil || d.Auth != AuthOAuth {
		return false
	}
	u, err := url.Parse(d.URL)
	if err != nil || !relayBound(p.resource, u) {
		return false
	}
	return reflect.DeepEqual(d.OAuthClient, p.tenantClient)
}

func copyTenantClient(c *secrets.MCPTenantClient) *secrets.MCPTenantClient {
	if c == nil {
		return nil
	}
	cc := *c
	return &cc
}
