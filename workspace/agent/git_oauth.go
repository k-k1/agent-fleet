package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Git provider OAuth, workspace side.
//
// ★ The GitHub device flow used to live HERE, reading GITHUB_OAUTH_CLIENT_ID out of
// the container environment. Since docs/71 both providers' flows run in the Control
// Plane, because the OAuth app is now a per-tenant row in the CP's database and
// container env is fixed at container start (a tenant administrator registering an app
// would otherwise be telling every member to restart their workspace). What the Agent
// keeps is storage: GitHub's token arrives through the ordinary PUT
// /connections/git/github.com — the same path a pasted PAT takes — and Bitbucket's
// refresh credentials arrive through handleBitbucketStore below.

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
//
// ★ key/secret are the TENANT's OAuth app since docs/71, and they are copied into this
// workspace's encrypted store because that is what the refresh grant is Basic-
// authenticated with and the refresh has to work offline of the CP (git runs inside
// sessions). Structurally unchanged from when they were the deployment's app — the
// credential simply belongs to a tenant now, which is why the tenant screen says so.
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

// refreshBitbucket exchanges the refresh_token for a fresh access token. Transient failures
// (transport error / 429 / 5xx) are retried a few times with a short backoff — this refresh
// backs the repo/branch pickers AND the git credential helper (clone/fetch/push in sessions),
// so a single blip here otherwise surfaces as an intermittent 401 that a manual retry hides.
// A 4xx like invalid_grant is permanent (the refresh token was revoked) and returns at once.
func refreshBitbucket(c secrets.BitbucketCreds) (secrets.BitbucketCreds, error) {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.RefreshToken}}
	// A bounded timeout (http.DefaultClient has none) so a hung refresh can't stall its caller.
	client := &http.Client{Timeout: 15 * time.Second}
	const attempts = 3
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(retryBackoff(i))
		}
		req, err := http.NewRequest("POST", bbTokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			return c, err
		}
		req.SetBasicAuth(c.Key, c.Secret)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err // transport error: retry
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("bitbucket refresh failed: %d", resp.StatusCode)
			if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
				return c, lastErr // 4xx (e.g. invalid_grant): permanent, don't retry
			}
			continue // 429 / 5xx: transient
		}
		var out struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
		}
		derr := json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if derr != nil {
			return c, derr
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
	return c, lastErr
}
