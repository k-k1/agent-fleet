package main

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Jira OAuth 2.0 (3LO) — docs/log/80 §80.17.
//
// Structurally this is the Bitbucket code grant again (oauth_bitbucket.go): the CP owns
// the public callback, mints the CSRF state, exchanges the code, and hands the tokens to
// the Agent; the tenant's client secret stays here and the member's tokens live in their
// workspace. Read that file first — only the differences are commented here.
//
// ⚠️ It is NOT the same OAuth app as Bitbucket, even though both are Atlassian. Bitbucket
// consumers live in a Bitbucket workspace and authorize at bitbucket.org; a Jira 3LO app
// lives on developer.atlassian.com and authorizes at auth.atlassian.com against a
// different scope namespace. A tenant registers two apps or neither.
//
// ⚠️ Three things differ from Bitbucket in ways that touch code:
//   - the token endpoint takes JSON with client_id/client_secret in the BODY (Bitbucket
//     uses form + Basic),
//   - `offline_access` must be requested or no refresh token comes back at all, and the
//     access token lives ~1h, so the refresh bridge is load-bearing rather than a nicety,
//   - the API is not reached at the site's own host: 3LO calls go to
//     api.atlassian.com/ex/jira/{cloudId}, and the cloudId comes from a second call the
//     AGENT makes (accessible-resources). That is also why the site picker exists — one
//     authorization can cover several Jira sites.
const (
	jiraAuthorizeURL = "https://auth.atlassian.com/authorize"
	// jiraScopes: read for the rail, write for the "comment the work back" action
	// (docs/log/80 §80.10 — the user asked for it to be included), offline_access for the
	// refresh token. ⚠️ Consent shows every scope, so a deployment that never wants af
	// to write would need a second app; that trade was accepted deliberately rather than
	// splitting the connection in two.
	jiraScopes = "read:jira-work read:jira-user write:jira-work offline_access"
)

// jiraTokenURL is a var for the same reason bbTokenURL is: the refresh grant is a real
// HTTP conversation and the only honest way to pin its behaviour is a stub server.
var jiraTokenURL = "https://auth.atlassian.com/oauth/token"

var jiraFlows = &bbFlowRegistry{states: map[string]bbState{}}

func (c config) jiraRedirectURI() string { return c.mgr.gitOAuthRedirectURI(gitOAuthJira) }

// handleJiraOAuthStart (POST /api/connections/jira/oauth/start) returns the authorize URL.
func (c config) handleJiraOAuthStart(w http.ResponseWriter, r *http.Request) {
	id := c.mgr.resolveIdentity(r)
	if id.key == "" {
		writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"})
		return
	}
	ident, mv, aerr := c.mgr.resolveMembership(r.Context(), id.key, id.email, tenantSel(r))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	key, _, ok, err := c.mgr.gitOAuthApp(r.Context(), mv.TenantID, gitOAuthJira)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "not_configured",
			"this tenant has no Jira OAuth app (a tenant administrator registers it in tenant settings)"})
		return
	}
	redirect := c.jiraRedirectURI()
	if redirect == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "no_public_base_url",
			"the Jira code grant needs a callback URL, but this deployment has no PUBLIC_BASE_URL set — ask the operator"})
		return
	}
	state := randHex(16)
	jiraFlows.put(state, bbState{
		user: ident.UserKey, tenant: tenantSel(r), tenantID: mv.TenantID, created: time.Now(),
	})
	au := jiraAuthorizeURL +
		"?audience=api.atlassian.com" +
		"&client_id=" + url.QueryEscape(key) +
		"&scope=" + url.QueryEscape(jiraScopes) +
		"&redirect_uri=" + url.QueryEscape(redirect) +
		"&state=" + url.QueryEscape(state) +
		"&response_type=code" +
		// ⚠️ prompt=consent is required for offline_access to actually return a refresh
		// token on a RE-authorization; without it a second connect silently yields an
		// access token that dies in an hour with nothing to renew it.
		"&prompt=consent"
	writeJSON(w, http.StatusOK, map[string]any{"authorize_url": au})
}

func (c config) handleJiraOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	t := oauthCallbackText(auth.PreferredUILang(r), "Jira")

	st, ok := jiraFlows.take(state)
	if !ok {
		bbCallbackPage(w, t.stateMismatch)
		return
	}
	if code == "" {
		bbCallbackPage(w, t.noCode)
		return
	}
	key, secret, ok, err := c.mgr.gitOAuthApp(r.Context(), st.tenantID, gitOAuthJira)
	if err != nil || !ok {
		bbCallbackPage(w, t.notConfigured)
		return
	}
	tok, err := jiraExchangeCode(key, secret, code, c.jiraRedirectURI())
	if err != nil {
		bbCallbackPage(w, t.tokenExchangeFailed+err.Error())
		return
	}
	// ★ The CP forwards and forgets. It does not resolve the cloud id, does not call the
	// Jira API, and stores none of this — the Agent does all of that, because that is
	// where the member's token is allowed to live (ADR 0061 決定 2).
	payload, _ := json.Marshal(map[string]any{
		"access_token": tok.AccessToken, "refresh_token": tok.RefreshToken, "expires_in": tok.ExpiresIn,
	})
	rt, aerr := c.mgr.resolve(r.Context(), st.user, "", st.tenant)
	if aerr != nil {
		bbCallbackPage(w, t.workspaceResolveFailed+aerr.message)
		return
	}
	areq, _ := http.NewRequest("PUT", rt.Endpoint()+"/connections/jira/oauth", strings.NewReader(string(payload)))
	areq.Header.Set("Content-Type", "application/json")
	if rt.Token() != "" {
		areq.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	aresp, err := agentHTTPClient.Do(areq)
	if err != nil {
		bbCallbackPage(w, t.saveUnreachable+err.Error())
		return
	}
	defer aresp.Body.Close()
	if aresp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(aresp.Body, 1<<16))
		if aresp.StatusCode == http.StatusNotFound {
			// フリート再ビルド前の Agent にはこのルートが無い。「設定の誤り」ではないと
			// 分かる言い方にする（Bitbucket の staleAgent と同じ配慮）。
			bbCallbackPage(w, t.staleAgent)
			return
		}
		bbCallbackPage(w, t.saveFailed+string(b))
		return
	}
	bbCallbackPage(w, t.success)
}

type jiraTokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// jiraExchangeCode runs the authorization-code grant. JSON body, not form+Basic.
func jiraExchangeCode(key, secret, code, redirect string) (jiraTokenResp, error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type": "authorization_code", "client_id": key, "client_secret": secret,
		"code": code, "redirect_uri": redirect,
	})
	req, err := http.NewRequest("POST", jiraTokenURL, strings.NewReader(string(body)))
	if err != nil {
		return jiraTokenResp{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := bbHTTPClient.Do(req)
	if err != nil {
		return jiraTokenResp{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out jiraTokenResp
	_ = json.Unmarshal(raw, &out)
	if out.AccessToken == "" {
		return jiraTokenResp{}, &httpStatusError{resp.StatusCode, strings.TrimSpace(string(raw))}
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 3600
	}
	return out, nil
}
