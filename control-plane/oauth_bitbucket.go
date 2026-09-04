package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Bitbucket OAuth (Authorization Code Grant). The Control Plane owns the public
// callback (the Agent is internal-only), so it generates the authorize URL with
// a CSRF state, handles the redirect, exchanges the code for tokens, and hands
// them to the Agent to store + install the refreshing credential helper.
//
// No auth exemption is needed for the callback: it's a browser redirect that
// carries the CP session cookie (the user is signed in to the Console), so
// authGate lets it through like any other authenticated request.

const bbAuthorizeURL = "https://bitbucket.org/site/oauth2/authorize"

// bbTokenURL is a var, not a const, for one reason: the refresh bridge
// (git_oauth_bridge.go) is a real HTTP conversation with retry and status handling, and
// the only honest way to pin that behaviour is to point it at a stub server.
var bbTokenURL = "https://bitbucket.org/site/oauth2/access_token"

// bbHTTPClient bounds the call OUT to bitbucket.org (http.DefaultClient has no
// timeout — same reasoning as oidcHTTPClient). ★ It is not for the CP→Agent leg:
// that one goes through agentHTTPClient, whose Transport carries the Cloud Map
// fallback (agent_dial.go). Using the default client there made the save fail with
// "no such host" for every workspace created after the CP task started.
var bbHTTPClient = &http.Client{Timeout: 20 * time.Second}

type bbState struct {
	user   string // identity user key
	tenant string // selected tenant (X-AF-Tenant) at start time
	// tenantID is the RESOLVED tenant, and it is what the callback looks the OAuth app
	// up by. It has to be carried rather than re-derived: the callback is a plain
	// browser redirect from bitbucket.org with no X-AF-Tenant header, so re-resolving
	// there would land on whatever tenant happens to come first for this person and
	// exchange the code against another tenant's app (docs/log/71 §71.5).
	tenantID string
	created  time.Time
}

// bbFlowRegistry owns the in-flight OAuth CSRF states. They live in process memory, so
// running more than one CP instance needs either sticky routing or the states moved to the
// DB (P3-7).
type bbFlowRegistry struct {
	mu     sync.Mutex
	states map[string]bbState // csrf state -> {user, tenant, created}
}

// put registers a new flow state, reaping entries older than 10 minutes.
func (b *bbFlowRegistry) put(state string, s bbState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, old := range b.states {
		if time.Since(old.created) > 10*time.Minute {
			delete(b.states, k)
		}
	}
	b.states[state] = s
}

// take consumes a flow state (single use — the callback deletes it).
func (b *bbFlowRegistry) take(state string) (bbState, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.states[state]
	delete(b.states, state)
	return s, ok
}

var bbFlows = &bbFlowRegistry{states: map[string]bbState{}}

func (c config) bbRedirectURI() string {
	return c.mgr.gitOAuthRedirectURI(gitOAuthBitbucket)
}

func (c config) handleBitbucketOAuthStart(w http.ResponseWriter, r *http.Request) {
	// The flow is keyed by the person's real user_key, not by sanitizeUser(email):
	// since docs/log/61 §61.5 an identity keeps its key when the IdP changes the email,
	// so the two can differ, and the callback would otherwise resolve a DIFFERENT
	// workspace and install the token there. The membership comes from the same
	// resolution so the tenant carried into the callback is the one the Console is
	// actually showing.
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
	// docs/log/71: the OAuth app is the TENANT's, registered by its administrator. No env
	// fallback — "not configured" here means this tenant has no app, and the Console
	// says so pointing at the tenant admin rather than at the operator. Only the key is
	// read: the callback re-reads the row for the secret, so the client secret never
	// has to survive in the flow state.
	key, _, ok, err := c.mgr.gitOAuthApp(r.Context(), mv.TenantID, gitOAuthBitbucket)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "not_configured",
			"this tenant has no Bitbucket OAuth app (a tenant administrator registers it in tenant settings)"})
		return
	}
	// ★ A missing PUBLIC_BASE_URL is a DIFFERENT failure and must not be reported as
	// "not configured": the app is registered, and the tenant administrator would go
	// re-enter a setting that is already correct. This one is the operator's — the code
	// grant has nowhere to come back to without a public base.
	redirect := c.bbRedirectURI()
	if redirect == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "no_public_base_url",
			"the Bitbucket code grant needs a callback URL, but this deployment has no PUBLIC_BASE_URL set — ask the operator"})
		return
	}
	state := randHex(16)
	bbFlows.put(state, bbState{
		user: ident.UserKey, tenant: tenantSel(r), tenantID: mv.TenantID, created: time.Now(),
	})

	au := bbAuthorizeURL + "?client_id=" + url.QueryEscape(key) +
		"&response_type=code&state=" + url.QueryEscape(state) +
		"&redirect_uri=" + url.QueryEscape(redirect)
	writeJSON(w, http.StatusOK, map[string]any{"authorize_url": au})
}

func (c config) handleBitbucketOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	t := oauthCallbackText(auth.PreferredUILang(r), "Bitbucket") // ja/en by Accept-Language

	st, ok := bbFlows.take(state)
	if !ok {
		bbCallbackPage(w, t.stateMismatch)
		return
	}
	if code == "" {
		bbCallbackPage(w, t.noCode)
		return
	}

	// The app is the one the tenant registered, resolved through the tenant carried in
	// the state (see bbState.tenantID).
	key, secret, ok, err := c.mgr.gitOAuthApp(r.Context(), st.tenantID, gitOAuthBitbucket)
	if err != nil || !ok {
		bbCallbackPage(w, t.notConfigured)
		return
	}

	// Exchange the code for tokens (Basic auth = consumer key:secret).
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.bbRedirectURI()}}
	req, _ := http.NewRequest("POST", bbTokenURL, strings.NewReader(form.Encode()))
	req.SetBasicAuth(key, secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := bbHTTPClient.Do(req)
	if err != nil {
		bbCallbackPage(w, t.tokenExchangeFailed+err.Error())
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &tok)
	if tok.AccessToken == "" {
		bbCallbackPage(w, t.tokenExchangeFailed+tok.Error+" "+tok.ErrorDesc)
		return
	}

	// Hand the tokens to the Agent to store + install the refresh helper.
	//
	// ★ key/secret are deliberately NOT sent (docs/log/71 §71.8). They used to ride along so
	// the Agent could run the refresh grant itself, which put the TENANT's client secret
	// in every member's encrypted store. The Agent now posts its refresh token back to
	// /internal/git-oauth/bitbucket/refresh instead (git_oauth_bridge.go).
	payload, _ := json.Marshal(map[string]any{
		"access_token": tok.AccessToken, "refresh_token": tok.RefreshToken,
		"expires_in": tok.ExpiresIn,
	})
	rt, aerr := c.mgr.resolve(r.Context(), st.user, "", st.tenant)
	if aerr != nil {
		bbCallbackPage(w, t.workspaceResolveFailed+aerr.message)
		return
	}
	areq, _ := http.NewRequest("PUT", rt.Endpoint()+"/connections/git/bitbucket/oauth", strings.NewReader(string(payload)))
	areq.Header.Set("Content-Type", "application/json")
	if rt.Token() != "" {
		areq.Header.Set("Authorization", "Bearer "+rt.Token()) // CP↔Agent auth
	}
	aresp, err := agentHTTPClient.Do(areq)
	if err != nil {
		bbCallbackPage(w, t.saveUnreachable+err.Error())
		return
	}
	defer aresp.Body.Close()
	if aresp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(aresp.Body)
		// ★ One upgrade window is visible here. A workspace container started before
		// docs/log/71 runs an Agent that REQUIRES key+secret in this payload and refuses
		// without them, and the raw refusal ("key, secret are required") reads like a
		// configuration mistake the member should fix — which it is not. Matching the
		// old message is a string contract and would normally be avoided, but it only
		// ever improves the wording: behaviour is identical either way, and the check
		// becomes dead code once no pre-docs/log/71 Agent is running.
		if aresp.StatusCode == http.StatusBadRequest && strings.Contains(string(b), "secret are required") {
			bbCallbackPage(w, t.staleAgent)
			return
		}
		bbCallbackPage(w, t.saveFailed+string(b))
		return
	}
	bbCallbackPage(w, t.success)
}

// oauthCallbackText holds the localized strings for the CP-rendered OAuth callback page
// (docs/log/28 P3). The detail-bearing entries are prefixes; the underlying error detail is
// appended verbatim. ja is the default; en is served when Accept-Language prefers
// English (preferredUILang, defined in oauth_google.go).
//
// Parameterized by provider display name because Bitbucket and Jira run the SAME code
// grant with the same failure modes (docs/log/80 §80.17) — two hand-written copies of nine
// strings would drift, and the only per-provider word is the name itself.
type bbStrings struct {
	stateMismatch, noCode, notConfigured, staleAgent                         string
	tokenExchangeFailed, workspaceResolveFailed, saveUnreachable, saveFailed string
	success                                                                  string
}

func oauthCallbackText(lang, provider string) bbStrings {
	if lang == "en" {
		return bbStrings{
			stateMismatch:          "Authentication error: state mismatch. Please retry from the Console.",
			noCode:                 "Authentication error: no code (authorization may have been denied).",
			notConfigured:          "This tenant has no " + provider + " OAuth app. Ask a tenant administrator to register one in tenant settings.",
			staleAgent:             "Could not save: this workspace is still running an older agent. Stop and start the workspace, then connect again. (Nothing is misconfigured.)",
			tokenExchangeFailed:    "Token exchange failed: ",
			workspaceResolveFailed: "Failed to resolve the workspace: ",
			saveUnreachable:        "Save failed (can't reach the Workspace Agent — is the Workspace running?): ",
			saveFailed:             "Save failed: ",
			success:                provider + " connection complete. Close this tab and return to the Console.",
		}
	}
	return bbStrings{
		stateMismatch:          "認証エラー: state が一致しません。Console からやり直してください。",
		noCode:                 "認証エラー: code がありません（承認が拒否された可能性）。",
		notConfigured:          "このテナントの " + provider + " OAuth アプリが見つかりません。テナント設定で登録し直してください。",
		staleAgent:             "ワークスペースが古いまま動いているため保存できませんでした。ワークスペースを一度停止してから起動し直し、もう一度接続してください（設定の誤りではありません）。",
		tokenExchangeFailed:    "トークン交換に失敗: ",
		workspaceResolveFailed: "Workspace の解決に失敗しました: ",
		saveUnreachable:        "保存に失敗（Workspace Agent に到達できません。Workspace は起動していますか）: ",
		saveFailed:             "保存に失敗: ",
		success:                provider + " 接続が完了しました。このタブを閉じて Console に戻ってください。",
	}
}

func bbCallbackPage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><body style=\"font:16px system-ui;padding:2rem;background:#1e1e1e;color:#ddd\">%s</body>", html.EscapeString(msg))
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
