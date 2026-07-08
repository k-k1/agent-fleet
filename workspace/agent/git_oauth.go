package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// GitHub OAuth via the Device Authorization Grant (RFC 8628): no callback, no
// client_secret — only the OAuth App's client_id (with "device flow" enabled).
// The Console shows the user_code + verification_uri and polls; on success the
// access token is stored as a normal git credential (connections.go), exactly
// like the PAT path. Token is effectively non-expiring (no refresh). See plan.

const (
	ghDeviceCodeURL  = "https://github.com/login/device/code"
	ghAccessTokenURL = "https://github.com/login/oauth/access_token"
	ghDeviceGrant    = "urn:ietf:params:oauth:grant-type:device_code"
	// repo = private read + push。workflow は .github/workflows/ 配下の作成・変更を
	// 含む push に GitHub が要求する追加スコープ（無いと remote rejected）。gh CLI の
	// 既定スコープと同等。既存接続には遡及しない — 再接続で新スコープのトークンになる。
	ghScope = "repo workflow"
)

func githubClientID() string { return os.Getenv("GITHUB_OAUTH_CLIENT_ID") }

type ghFlow struct {
	deviceCode string
	interval   int
	deadline   time.Time
}

var (
	ghMu    sync.Mutex
	ghFlows = map[string]*ghFlow{}
)

func handleGithubOAuthStart(w http.ResponseWriter, r *http.Request) {
	cid := githubClientID()
	if cid == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "not_configured", "GITHUB_OAUTH_CLIENT_ID is not set")
		return
	}
	var resp struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
	}
	if err := ghPostForm(ghDeviceCodeURL, url.Values{"client_id": {cid}, "scope": {ghScope}}, &resp); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "github_error", err.Error())
		return
	}
	if resp.DeviceCode == "" {
		httpx.WriteErr(w, http.StatusBadGateway, "github_error", "no device_code returned: "+resp.Error)
		return
	}
	interval := resp.Interval
	if interval <= 0 {
		interval = 5
	}
	id := newFlowID()
	ghMu.Lock()
	for k, f := range ghFlows { // reap expired flows
		if time.Now().After(f.deadline) {
			delete(ghFlows, k)
		}
	}
	ghFlows[id] = &ghFlow{deviceCode: resp.DeviceCode, interval: interval, deadline: time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)}
	ghMu.Unlock()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"flow_id": id, "user_code": resp.UserCode, "verification_uri": resp.VerificationURI,
		"interval": interval, "expires_in": resp.ExpiresIn,
	})
}

type ghPollReq struct {
	FlowID string `json:"flow_id"`
}

func handleGithubOAuthPoll(w http.ResponseWriter, r *http.Request) {
	var req ghPollReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	ghMu.Lock()
	f := ghFlows[req.FlowID]
	ghMu.Unlock()
	if f == nil {
		httpx.WriteErr(w, http.StatusNotFound, "no_flow", "unknown or expired flow_id")
		return
	}
	if time.Now().After(f.deadline) {
		ghForget(req.FlowID)
		httpx.WriteErr(w, http.StatusBadRequest, "expired_token", "device code expired; restart")
		return
	}
	var resp struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	form := url.Values{"client_id": {githubClientID()}, "device_code": {f.deviceCode}, "grant_type": {ghDeviceGrant}}
	if err := ghPostForm(ghAccessTokenURL, form, &resp); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "github_error", err.Error())
		return
	}
	switch {
	case resp.AccessToken != "":
		if err := upsertGitCredential("github.com", "x-access-token", resp.AccessToken); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
			return
		}
		ghForget(req.FlowID)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": true})
	case resp.Error == "authorization_pending":
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"pending": true})
	case resp.Error == "slow_down":
		ghMu.Lock()
		f.interval += 5
		iv := f.interval
		ghMu.Unlock()
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"pending": true, "interval": iv})
	default:
		ghForget(req.FlowID)
		httpx.WriteErr(w, http.StatusBadRequest, "oauth_error", resp.Error)
	}
}

func ghForget(id string) {
	ghMu.Lock()
	delete(ghFlows, id)
	ghMu.Unlock()
}

// ghPostForm POSTs a urlencoded form and decodes a JSON response (GitHub returns
// form-encoded by default; Accept: application/json switches it to JSON).
func ghPostForm(endpoint string, form url.Values, out any) error {
	req, err := http.NewRequest("POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- Bitbucket OAuth (Authorization Code Grant) ---
//
// Bitbucket has no device flow, so the Control Plane runs the auth-code grant
// (it owns the public callback) and hands the tokens here to store. Bitbucket
// access tokens expire in ~2h, so git uses the cred helper (cred_helper.go,
// `workspace-agent cred`), which refreshes on demand. The helper covers BOTH our
// /repos calls and git run inside claude sessions. Refresh creds live in the
// encrypted store (secrets.Data.Bitbucket, see internal/secrets). See plan.

const bbTokenURL = "https://bitbucket.org/site/oauth2/access_token"

// writeBitbucketCreds persists the OAuth refresh creds into the encrypted store.
func writeBitbucketCreds(c secrets.BitbucketCreds) error {
	s, err := secrets.Load()
	if err != nil {
		return err
	}
	s.Bitbucket = &c
	return s.Save()
}

type bitbucketStoreReq struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Key          string `json:"key"`
	Secret       string `json:"secret"`
}

// handleBitbucketStore persists tokens (from the CP callback) and installs the
// credential helper for bitbucket.org only. The empty-helper reset clears the
// inherited global `store` helper so our refreshing helper is the sole source.
func handleBitbucketStore(w http.ResponseWriter, r *http.Request) {
	var req bitbucketStoreReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.AccessToken == "" || req.Key == "" || req.Secret == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "access_token, key, secret are required")
		return
	}
	exp := req.ExpiresIn
	if exp == 0 {
		exp = 7200
	}
	c := secrets.BitbucketCreds{
		AccessToken: req.AccessToken, RefreshToken: req.RefreshToken,
		Expiry: time.Now().Unix() + exp, Key: req.Key, Secret: req.Secret,
	}
	if err := writeBitbucketCreds(c); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	if err := ensureCredHelper(); err != nil { // unified cred helper handles refresh
		httpx.WriteErr(w, http.StatusInternalServerError, "config_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": true})
}

// removeBitbucketOAuth clears the stored OAuth refresh creds. Called from the
// generic disconnect (connections.go) so one ✕ covers both paths.
func removeBitbucketOAuth() {
	s, err := secrets.Load()
	if err != nil {
		return
	}
	s.Bitbucket = nil
	_ = s.Save()
}

// refreshBitbucket exchanges the refresh_token for a fresh access token.
func refreshBitbucket(c secrets.BitbucketCreds) (secrets.BitbucketCreds, error) {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.RefreshToken}}
	req, err := http.NewRequest("POST", bbTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return c, err
	}
	req.SetBasicAuth(c.Key, c.Secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return c, err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return c, err
	}
	if out.AccessToken == "" {
		return c, fmt.Errorf("refresh returned no access_token")
	}
	c.AccessToken = out.AccessToken
	if out.RefreshToken != "" {
		c.RefreshToken = out.RefreshToken
	}
	exp := out.ExpiresIn
	if exp == 0 {
		exp = 7200
	}
	c.Expiry = time.Now().Unix() + exp
	return c, nil
}
