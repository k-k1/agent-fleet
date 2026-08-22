package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
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

// bbTokenURL is a var so the legacy direct-grant fallback can be pointed at a stub in
// tests. The normal path does not use it at all — the CP runs the grant (docs/71 §71.8).
var bbTokenURL = "https://bitbucket.org/site/oauth2/access_token"

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
	// Key/Secret are the tenant's OAuth app. The CP stopped sending them in docs/71
	// §71.8 — the refresh grant runs there now — and they are accepted-and-ignored
	// rather than removed so an older CP talking to this Agent still connects instead
	// of failing on an unknown field's absence.
	Key    string `json:"key,omitempty"`
	Secret string `json:"secret,omitempty"`
}

// handleBitbucketStore persists tokens (from the CP callback) and installs the
// credential helper for bitbucket.org only. The empty-helper reset clears the
// inherited global `store` helper so our refreshing helper is the sole source.
//
// ★ The tenant's client key/secret are NOT stored (docs/71 §71.8). What lands here is
// this member's own access + refresh token; the refresh grant runs in the CP, which is
// where the tenant's secret stays.
func handleBitbucketStore(w http.ResponseWriter, r *http.Request) {
	var req bitbucketStoreReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.AccessToken == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "access_token is required")
		return
	}
	exp := req.ExpiresIn
	if exp == 0 {
		exp = 7200
	}
	c := secrets.BitbucketCreds{
		AccessToken: req.AccessToken, RefreshToken: req.RefreshToken,
		Expiry: time.Now().Unix() + exp,
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
//
// ★ The grant runs in the CONTROL PLANE (docs/71 §71.8). It is Basic-authenticated with
// the tenant's OAuth app secret, and that secret used to be copied into every member's
// store so this function could run it locally — a tenant-wide credential sitting on each
// member's disk. Now the refresh token goes up and a fresh access token comes back; the
// secret never leaves the CP. The retry policy for transient bitbucket.org failures went
// with the grant (git_oauth_bridge.go).
//
// Two consequences worth stating, because they are the cost of the change:
//
//   - a refresh now needs the CP reachable, where before it only needed bitbucket.org.
//     The access token is still valid for ~2h, so a CP restart is invisible; a CP that
//     is down for longer surfaces as git asking for credentials.
//   - a store written BEFORE this change still holds key/secret. Those are used only as
//     a fallback if the bridge is unavailable, and are dropped the first time the bridge
//     answers — proving the replacement works before removing what it replaces.
func refreshBitbucket(c secrets.BitbucketCreds) (secrets.BitbucketCreds, error) {
	if b := loadGitOAuthBridge(); b != nil {
		nc, err := refreshBitbucketViaCP(*b, c)
		if err == nil {
			return nc, nil
		}
		if c.Key == "" || c.Secret == "" {
			return c, err // nothing to fall back to: report what actually failed
		}
		log.Printf("bitbucket refresh: CP bridge failed (%v) — falling back to the stored client secret", err)
	}
	return refreshBitbucketDirect(c)
}

// loadGitOAuthBridge reads the CP bridge out of the store. nil = not configured, which
// is normal on a deployment with no PUBLIC_BASE_URL (the CP injects nothing then) and on
// a container started before docs/71.
func loadGitOAuthBridge() *secrets.CPBridge {
	s, err := secrets.Load()
	if err != nil || s.GitOAuthBridge == nil {
		return nil
	}
	if s.GitOAuthBridge.BaseURL == "" || s.GitOAuthBridge.Token == "" {
		return nil
	}
	return s.GitOAuthBridge
}

// refreshBitbucketViaCP posts the refresh token to the CP and returns the refreshed
// creds with the legacy client key/secret CLEARED — the caller persists the result, so
// the scrub of a pre-docs/71 store happens as a side effect of the first refresh that
// proves the bridge works.
func refreshBitbucketViaCP(b secrets.CPBridge, c secrets.BitbucketCreds) (secrets.BitbucketCreds, error) {
	if c.RefreshToken == "" {
		return c, fmt.Errorf("no refresh token stored")
	}
	body, _ := json.Marshal(map[string]string{"refresh_token": c.RefreshToken})
	req, err := http.NewRequest("POST",
		strings.TrimRight(b.BaseURL, "/")+"/internal/git-oauth/bitbucket/refresh", strings.NewReader(string(body)))
	if err != nil {
		return c, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.Token)
	// Bounded: this call sits in front of every git clone/fetch/push once the access
	// token is near expiry, so a hung CP must fail rather than wedge git. The CP's own
	// retries against bitbucket.org fit inside this budget.
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return c, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return c, fmt.Errorf("cp refresh failed: %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return c, err
	}
	if out.AccessToken == "" {
		return c, fmt.Errorf("cp refresh returned no access_token")
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
	c.Key, c.Secret = "", "" // the whole point: stop holding the tenant's app secret
	return c, nil
}

// refreshBitbucketDirect is the pre-docs/71 path, kept only for stores that still carry
// the client key/secret. Transient failures (transport error / 429 / 5xx) are retried a
// few times with a short backoff — this refresh backs the repo/branch pickers AND the git
// credential helper (clone/fetch/push in sessions), so a single blip here otherwise
// surfaces as an intermittent 401 that a manual retry hides. A 4xx like invalid_grant is
// permanent (the refresh token was revoked) and returns at once.
func refreshBitbucketDirect(c secrets.BitbucketCreds) (secrets.BitbucketCreds, error) {
	if c.Key == "" || c.Secret == "" {
		return c, fmt.Errorf("bitbucket refresh is unavailable: no control-plane bridge is configured for this workspace")
	}
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
