package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

const (
	bbAuthorizeURL = "https://bitbucket.org/site/oauth2/authorize"
	bbTokenURL     = "https://bitbucket.org/site/oauth2/access_token"
)

type bbState struct {
	user    string // identity user key
	tenant  string // selected tenant (X-AF-Tenant) at start time
	created time.Time
}

// bbFlowRegistry owns the in-flight OAuth CSRF states（docs/23 P2-W4: 生の
// package 変数 map+mutex から struct 化）。プロセス内メモリなので、CP を
// マルチインスタンス化する際は sticky ルーティングか DB 退避が必要（P3-7）。
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

func (c config) bbConfigured() bool {
	return c.bbKey != "" && c.bbSecret != "" && c.publicBaseURL != ""
}

func (c config) bbRedirectURI() string {
	return strings.TrimRight(c.publicBaseURL, "/") + "/api/oauth/bitbucket/callback"
}

func (c config) handleBitbucketOAuthStart(w http.ResponseWriter, r *http.Request) {
	if !c.bbConfigured() {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"code": "not_configured", "message": "bitbucket oauth not configured (key/secret/PUBLIC_BASE_URL)"},
		})
		return
	}
	// The flow is keyed by the person's real user_key, not by sanitizeUser(email):
	// since docs/61 §61.5 an identity keeps its key when the IdP changes the email,
	// so the two can differ, and the callback would otherwise resolve a DIFFERENT
	// workspace and install the token there.
	ident, aerr := c.mgr.identityFor(r.Context(), r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	user := ident.UserKey
	state := randHex(16)
	bbFlows.put(state, bbState{user: user, tenant: r.Header.Get("X-AF-Tenant"), created: time.Now()})

	au := bbAuthorizeURL + "?client_id=" + url.QueryEscape(c.bbKey) +
		"&response_type=code&state=" + url.QueryEscape(state) +
		"&redirect_uri=" + url.QueryEscape(c.bbRedirectURI())
	writeJSON(w, http.StatusOK, map[string]any{"authorize_url": au})
}

func (c config) handleBitbucketOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	t := bbText[preferredUILang(r)] // docs/28 P3: Accept-Language で ja/en

	st, ok := bbFlows.take(state)
	if !ok {
		bbCallbackPage(w, t.stateMismatch)
		return
	}
	if code == "" {
		bbCallbackPage(w, t.noCode)
		return
	}

	// Exchange the code for tokens (Basic auth = consumer key:secret).
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.bbRedirectURI()}}
	req, _ := http.NewRequest("POST", bbTokenURL, strings.NewReader(form.Encode()))
	req.SetBasicAuth(c.bbKey, c.bbSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
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
	payload, _ := json.Marshal(map[string]any{
		"access_token": tok.AccessToken, "refresh_token": tok.RefreshToken,
		"expires_in": tok.ExpiresIn, "key": c.bbKey, "secret": c.bbSecret,
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
	aresp, err := http.DefaultClient.Do(areq)
	if err != nil {
		bbCallbackPage(w, t.saveUnreachable+err.Error())
		return
	}
	defer aresp.Body.Close()
	if aresp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(aresp.Body)
		bbCallbackPage(w, t.saveFailed+string(b))
		return
	}
	bbCallbackPage(w, t.success)
}

// bbText holds the localized strings for the CP-rendered Bitbucket OAuth callback
// page (docs/28 P3). The detail-bearing entries are prefixes; the underlying error
// detail is appended verbatim. ja is the default; en is served when Accept-Language
// prefers English (preferredUILang, defined in oauth_google.go).
type bbStrings struct {
	stateMismatch, noCode                                                    string
	tokenExchangeFailed, workspaceResolveFailed, saveUnreachable, saveFailed string
	success                                                                  string
}

var bbText = map[string]bbStrings{
	"ja": {
		stateMismatch:          "認証エラー: state が一致しません。Console からやり直してください。",
		noCode:                 "認証エラー: code がありません（承認が拒否された可能性）。",
		tokenExchangeFailed:    "トークン交換に失敗: ",
		workspaceResolveFailed: "Workspace の解決に失敗しました: ",
		saveUnreachable:        "保存に失敗（Workspace Agent に到達できません。Workspace は起動していますか）: ",
		saveFailed:             "保存に失敗: ",
		success:                "Bitbucket 接続が完了しました。このタブを閉じて Console に戻ってください。",
	},
	"en": {
		stateMismatch:          "Authentication error: state mismatch. Please retry from the Console.",
		noCode:                 "Authentication error: no code (authorization may have been denied).",
		tokenExchangeFailed:    "Token exchange failed: ",
		workspaceResolveFailed: "Failed to resolve the workspace: ",
		saveUnreachable:        "Save failed (can't reach the Workspace Agent — is the Workspace running?): ",
		saveFailed:             "Save failed: ",
		success:                "Bitbucket connection complete. Close this tab and return to the Console.",
	},
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
