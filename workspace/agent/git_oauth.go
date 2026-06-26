package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
	ghScope          = "repo" // private read + push
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
		writeErr(w, http.StatusBadRequest, "not_configured", "GITHUB_OAUTH_CLIENT_ID is not set")
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
		writeErr(w, http.StatusBadGateway, "github_error", err.Error())
		return
	}
	if resp.DeviceCode == "" {
		writeErr(w, http.StatusBadGateway, "github_error", "no device_code returned: "+resp.Error)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"flow_id": id, "user_code": resp.UserCode, "verification_uri": resp.VerificationURI,
		"interval": interval, "expires_in": resp.ExpiresIn,
	})
}

type ghPollReq struct {
	FlowID string `json:"flow_id"`
}

func handleGithubOAuthPoll(w http.ResponseWriter, r *http.Request) {
	var req ghPollReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	ghMu.Lock()
	f := ghFlows[req.FlowID]
	ghMu.Unlock()
	if f == nil {
		writeErr(w, http.StatusNotFound, "no_flow", "unknown or expired flow_id")
		return
	}
	if time.Now().After(f.deadline) {
		ghForget(req.FlowID)
		writeErr(w, http.StatusBadRequest, "expired_token", "device code expired; restart")
		return
	}
	var resp struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	form := url.Values{"client_id": {githubClientID()}, "device_code": {f.deviceCode}, "grant_type": {ghDeviceGrant}}
	if err := ghPostForm(ghAccessTokenURL, form, &resp); err != nil {
		writeErr(w, http.StatusBadGateway, "github_error", err.Error())
		return
	}
	switch {
	case resp.AccessToken != "":
		if err := upsertGitCredential("github.com", "x-access-token", resp.AccessToken); err != nil {
			writeErr(w, http.StatusInternalServerError, "store_failed", err.Error())
			return
		}
		_ = gitConfigGlobal("credential.helper", "store")
		ghForget(req.FlowID)
		writeJSON(w, http.StatusOK, map[string]any{"connected": true})
	case resp.Error == "authorization_pending":
		writeJSON(w, http.StatusOK, map[string]any{"pending": true})
	case resp.Error == "slow_down":
		ghMu.Lock()
		f.interval += 5
		iv := f.interval
		ghMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"pending": true, "interval": iv})
	default:
		ghForget(req.FlowID)
		writeErr(w, http.StatusBadRequest, "oauth_error", resp.Error)
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
// access tokens expire in ~2h, so git uses a credential helper — this same
// binary invoked as `workspace-agent bitbucket-cred` — which refreshes on
// demand. The helper covers BOTH our /repos calls and git run inside claude
// sessions. Secret lives in bitbucket.json (0600, home) for refresh. See plan.

const bbTokenURL = "https://bitbucket.org/site/oauth2/access_token"

func bitbucketJSONPath() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "bitbucket.json")
}

type bitbucketCreds struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Expiry       int64  `json:"expiry"` // unix seconds
	Key          string `json:"key"`
	Secret       string `json:"secret"`
}

func readBitbucketCreds() (bitbucketCreds, error) {
	var c bitbucketCreds
	b, err := os.ReadFile(bitbucketJSONPath())
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

func writeBitbucketCreds(c bitbucketCreds) error {
	p := bitbucketJSONPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if req.AccessToken == "" || req.Key == "" || req.Secret == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "access_token, key, secret are required")
		return
	}
	exp := req.ExpiresIn
	if exp == 0 {
		exp = 7200
	}
	c := bitbucketCreds{
		AccessToken: req.AccessToken, RefreshToken: req.RefreshToken,
		Expiry: time.Now().Unix() + exp, Key: req.Key, Secret: req.Secret,
	}
	if err := writeBitbucketCreds(c); err != nil {
		writeErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	const helperKey = "credential.https://bitbucket.org.helper"
	_ = gitConfigGlobal(helperKey, "")                                  // reset inherited helpers
	if err := gitConfigAddGlobal(helperKey, "!workspace-agent bitbucket-cred"); err != nil {
		writeErr(w, http.StatusInternalServerError, "config_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true})
}

// removeBitbucketOAuth clears the stored tokens and the per-host helper. Called
// from the generic disconnect (connections.go) so one ✕ covers both paths.
func removeBitbucketOAuth() {
	_ = os.Remove(bitbucketJSONPath())
	_ = exec.Command("git", "config", "--global", "--unset-all", "credential.https://bitbucket.org.helper").Run()
}

func gitConfigAddGlobal(key, val string) error {
	if out, err := exec.Command("git", "config", "--global", "--add", key, val).CombinedOutput(); err != nil {
		return fmt.Errorf("git config --add %s: %v: %s", key, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// refreshBitbucket exchanges the refresh_token for a fresh access token.
func refreshBitbucket(c bitbucketCreds) (bitbucketCreds, error) {
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

// runBitbucketCredHelper implements the git credential helper protocol for
// bitbucket.org. git invokes `workspace-agent bitbucket-cred get` (op as argv);
// we refresh if the token is expired/near-expiry, then emit username/password.
func runBitbucketCredHelper(args []string) {
	op := ""
	if len(args) > 0 {
		op = args[0]
	}
	if op != "get" {
		return // store/erase: nothing to do
	}
	c, err := readBitbucketCreds()
	if err != nil {
		return // no creds: emit nothing, git falls through to other helpers
	}
	if time.Now().Unix() >= c.Expiry-120 { // refresh within 2 min of expiry
		if nc, rerr := refreshBitbucket(c); rerr == nil {
			c = nc
			_ = writeBitbucketCreds(c)
		}
	}
	fmt.Printf("username=x-token-auth\npassword=%s\n", c.AccessToken)
}
