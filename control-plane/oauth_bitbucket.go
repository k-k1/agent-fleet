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
// No oauth2-proxy exemption is needed: the callback is a browser redirect, and
// the user's browser already carries the Google session cookie (they're in the
// Console), so it passes the perimeter and Caddy routes /agent-fleet/* to here.

const (
	bbAuthorizeURL = "https://bitbucket.org/site/oauth2/authorize"
	bbTokenURL     = "https://bitbucket.org/site/oauth2/access_token"
)

type bbState struct {
	user    string
	created time.Time
}

var (
	bbStateMu sync.Mutex
	bbStates  = map[string]bbState{} // csrf state -> {user, created}
)

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
	state := randHex(16)
	user := c.mgr.resolveUser(r)
	bbStateMu.Lock()
	for k, s := range bbStates { // reap stale states
		if time.Since(s.created) > 10*time.Minute {
			delete(bbStates, k)
		}
	}
	bbStates[state] = bbState{user: user, created: time.Now()}
	bbStateMu.Unlock()

	au := bbAuthorizeURL + "?client_id=" + url.QueryEscape(c.bbKey) +
		"&response_type=code&state=" + url.QueryEscape(state) +
		"&redirect_uri=" + url.QueryEscape(c.bbRedirectURI())
	writeJSON(w, http.StatusOK, map[string]any{"authorize_url": au})
}

func (c config) handleBitbucketOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	bbStateMu.Lock()
	st, ok := bbStates[state]
	delete(bbStates, state)
	bbStateMu.Unlock()
	if !ok {
		bbCallbackPage(w, "認証エラー: state が一致しません。Console からやり直してください。")
		return
	}
	if code == "" {
		bbCallbackPage(w, "認証エラー: code がありません（承認が拒否された可能性）。")
		return
	}

	// Exchange the code for tokens (Basic auth = consumer key:secret).
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.bbRedirectURI()}}
	req, _ := http.NewRequest("POST", bbTokenURL, strings.NewReader(form.Encode()))
	req.SetBasicAuth(c.bbKey, c.bbSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		bbCallbackPage(w, "トークン交換に失敗: "+err.Error())
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
		bbCallbackPage(w, "トークン交換に失敗: "+tok.Error+" "+tok.ErrorDesc)
		return
	}

	// Hand the tokens to the Agent to store + install the refresh helper.
	payload, _ := json.Marshal(map[string]any{
		"access_token": tok.AccessToken, "refresh_token": tok.RefreshToken,
		"expires_in": tok.ExpiresIn, "key": c.bbKey, "secret": c.bbSecret,
	})
	rt := c.mgr.forUser(st.user)
	areq, _ := http.NewRequest("PUT", rt.agentBase()+"/connections/git/bitbucket/oauth", strings.NewReader(string(payload)))
	areq.Header.Set("Content-Type", "application/json")
	if rt.token != "" {
		areq.Header.Set("Authorization", "Bearer "+rt.token) // CP↔Agent auth
	}
	aresp, err := http.DefaultClient.Do(areq)
	if err != nil {
		bbCallbackPage(w, "保存に失敗（Workspace Agent に到達できません。Workspace は起動していますか）: "+err.Error())
		return
	}
	defer aresp.Body.Close()
	if aresp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(aresp.Body)
		bbCallbackPage(w, "保存に失敗: "+string(b))
		return
	}
	bbCallbackPage(w, "Bitbucket 接続が完了しました。このタブを閉じて Console に戻ってください。")
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
