package main

// Git OAuth refresh bridge — the internal (per-membership token) face of the tenant's
// git provider OAuth app (docs/log/71 §71.8 + ADR0052 決定 7).
//
// ★ Why this exists at all. Bitbucket access tokens expire in ~2h, so something has to
// run the refresh grant, and that grant is Basic-authenticated with the OAuth app's
// key:secret. Until now the CP handed key AND secret to the workspace at connect time
// and the Agent refreshed on its own — which meant the TENANT's client secret was
// copied into every one of its members' encrypted stores, readable by anyone with a
// shell in their own container. That was tolerable while the app belonged to the
// deployment operator; once it belongs to a tenant administrator (docs/log/71) it is their
// credential sitting on other people's disks.
//
// So the refresh moves here: the Agent posts the REFRESH TOKEN, the CP adds the app's
// secret and talks to bitbucket.org.
//
// ★ Note what did NOT move. The refresh token itself stays in the workspace — the CP
// does not store it, and this endpoint does not remember it. That keeps the standing
// rule that the CP passes credentials through without holding them (docs/build/08), and
// it is the reason the split is "the tenant's secret here, the member's token there"
// rather than "all of it here".
//
// The Agent authenticates with AF_GIT_OAUTH_TOKEN, a per-membership deterministic HMAC
// token injected at container start — the same shape as the memo / schedule / MCP /
// docs bridges, and a SEPARATE credential from all of them so a leak is scoped to
// "refresh this member's git token".

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func gitOAuthSignKey(master32 []byte) []byte {
	mac := hmac.New(sha256.New, master32)
	mac.Write([]byte("af-git-oauth-token-sign/v1"))
	return mac.Sum(nil)
}

// mintGitOAuthToken returns the deterministic refresh-bridge token for a membership.
// Format: "afo_" + b64url(membershipID) + "." + tag. Deterministic, so re-injecting it
// on every container start is idempotent (same as the other bridges).
func mintGitOAuthToken(signKey []byte, membershipID string) string {
	return "afo_" + base64.RawURLEncoding.EncodeToString([]byte(membershipID)) + "." + gitOAuthTokenTag(signKey, membershipID)
}

func gitOAuthTokenTag(signKey []byte, membershipID string) string {
	mac := hmac.New(sha256.New, signKey)
	mac.Write([]byte(membershipID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// verifyGitOAuthToken checks the tag and returns the embedded membership id. It does
// NOT resolve tenant/role — that is a live store lookup by the caller, so a revoked
// membership stops refreshing immediately rather than when the token rotates.
func verifyGitOAuthToken(signKey []byte, token string) (membershipID string, ok bool) {
	body, hasPrefix := strings.CutPrefix(strings.TrimSpace(token), "afo_")
	if !hasPrefix {
		return "", false
	}
	dot := strings.LastIndexByte(body, '.')
	if dot < 0 {
		return "", false
	}
	idRaw, err := base64.RawURLEncoding.DecodeString(body[:dot])
	if err != nil || len(idRaw) == 0 {
		return "", false
	}
	mid := string(idRaw)
	if !hmac.Equal([]byte(body[dot+1:]), []byte(gitOAuthTokenTag(signKey, mid))) {
		return "", false
	}
	return mid, true
}

type gitOAuthBridgeAPI struct{ mgr *manager }

func newGitOAuthBridgeAPI(m *manager) gitOAuthBridgeAPI { return gitOAuthBridgeAPI{m} }

// withGitOAuthToken adapts a membership-scoped handler to the internal token face.
func (a gitOAuthBridgeAPI) withGitOAuthToken(h func(http.ResponseWriter, *http.Request, MembershipView)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mid, ok := verifyGitOAuthToken(gitOAuthSignKey(a.mgr.tokenSignMaster()), tok)
		if !ok {
			writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "invalid git oauth token"})
			return
		}
		mv, ok, err := a.mgr.store.GetMembershipByID(r.Context(), mid)
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		if !ok {
			writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "membership not active"})
			return
		}
		h(w, r, mv)
	}
}

// refreshBitbucket (POST /internal/git-oauth/bitbucket/refresh) runs the refresh grant
// with the caller's OWN tenant's app.
//
// ★ The tenant comes from the token → membership → store, never from the request. A
// client-chosen tenant would be a cross-tenant use of somebody else's OAuth app.
func (a gitOAuthBridgeAPI) refreshBitbucket(w http.ResponseWriter, r *http.Request, mv MembershipView) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	body.RefreshToken = strings.TrimSpace(body.RefreshToken)
	if body.RefreshToken == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "refresh_token is required"})
		return
	}
	key, secret, ok, err := a.mgr.gitOAuthApp(r.Context(), mv.TenantID, gitOAuthBitbucket)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		// The tenant removed the app after this member connected. Their existing token
		// keeps working until it expires and then stops — say which, because "clone
		// suddenly asks for a password" is otherwise unattributable.
		writeAPIErr(w, &apiError{http.StatusBadRequest, "not_configured",
			"this tenant no longer has a Bitbucket OAuth app, so the token cannot be refreshed"})
		return
	}
	tok, aerr := bbRefreshGrant(key, secret, body.RefreshToken)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, tok)
}

// refreshJira (POST /internal/git-oauth/jira/refresh) is the same grant for Jira's 3LO
// app. It exists separately from refreshBitbucket for one reason only: the request shape
// differs (JSON body with client_id/client_secret vs form + Basic auth).
//
// ⚠️ Atlassian ROTATES the refresh token — every refresh returns a new one and retires
// the old. The Agent must persist what comes back; dropping it strands the connection at
// the next expiry with no way to renew. (Bitbucket may or may not rotate, so the Agent's
// store-what-you-get behaviour already covers both.)
func (a gitOAuthBridgeAPI) refreshJira(w http.ResponseWriter, r *http.Request, mv MembershipView) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	body.RefreshToken = strings.TrimSpace(body.RefreshToken)
	if body.RefreshToken == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "refresh_token is required"})
		return
	}
	key, secret, ok, err := a.mgr.gitOAuthApp(r.Context(), mv.TenantID, gitOAuthJira)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "not_configured",
			"this tenant no longer has a Jira OAuth app, so the token cannot be refreshed"})
		return
	}
	tok, aerr := jiraRefreshGrant(key, secret, body.RefreshToken)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, tok)
}

// jiraRefreshGrant mirrors bbRefreshGrant's retry policy for the same reason: this call
// sits in front of every rail refresh once the hour-old access token expires, and one
// blip should not read as "reconnect Jira".
func jiraRefreshGrant(key, secret, refreshToken string) (bbRefreshToken, *apiError) {
	payload, _ := json.Marshal(map[string]string{
		"grant_type": "refresh_token", "client_id": key, "client_secret": secret,
		"refresh_token": refreshToken,
	})
	const attempts = 3
	var last *apiError
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(time.Duration(i) * 300 * time.Millisecond)
		}
		req, err := http.NewRequest("POST", jiraTokenURL, strings.NewReader(string(payload)))
		if err != nil {
			return bbRefreshToken{}, internalErr(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := bbHTTPClient.Do(req)
		if err != nil {
			last = &apiError{http.StatusBadGateway, "jira_unreachable", err.Error()}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
				return bbRefreshToken{}, &apiError{http.StatusBadRequest, "invalid_grant",
					fmt.Sprintf("jira refused the refresh (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))}
			}
			last = &apiError{http.StatusBadGateway, "jira_error",
				fmt.Sprintf("jira refresh failed: %d", resp.StatusCode)}
			continue
		}
		var out bbRefreshToken
		derr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
		resp.Body.Close()
		if derr != nil {
			return bbRefreshToken{}, &apiError{http.StatusBadGateway, "jira_error", derr.Error()}
		}
		if out.AccessToken == "" {
			return bbRefreshToken{}, &apiError{http.StatusBadGateway, "jira_error", "refresh returned no access_token"}
		}
		if out.ExpiresIn <= 0 {
			out.ExpiresIn = 3600
		}
		return out, nil
	}
	return bbRefreshToken{}, last
}

// bbRefreshToken is the wire shape returned to the Agent — deliberately only the three
// fields it has to store. The app's key/secret are NOT among them: putting them here
// would re-create the very distribution this bridge removes.
type bbRefreshToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int64  `json:"expires_in"`
}

// bbRefreshGrant exchanges a refresh token for a fresh access token.
//
// The retry policy moved here with the grant, and it is the Agent's old one because the
// reason for it is unchanged: this call backs the repo/branch pickers AND the git
// credential helper, so one blip surfaces as an intermittent 401 that a manual retry
// hides. Transient (transport / 429 / 5xx) is retried; a 4xx like invalid_grant is
// permanent — the refresh token was revoked — and returns at once so the caller can
// stop asking and prompt for a reconnect.
func bbRefreshGrant(key, secret, refreshToken string) (bbRefreshToken, *apiError) {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}
	const attempts = 3
	var last *apiError
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(time.Duration(i) * 300 * time.Millisecond)
		}
		req, err := http.NewRequest("POST", bbTokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			return bbRefreshToken{}, internalErr(err)
		}
		req.SetBasicAuth(key, secret)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := bbHTTPClient.Do(req)
		if err != nil {
			last = &apiError{http.StatusBadGateway, "bitbucket_unreachable", err.Error()}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
				// ★ Reported as invalid_grant so the Agent can tell "reconnect" from
				// "try again later" — it is the difference between prompting the member
				// and silently retrying every git command.
				return bbRefreshToken{}, &apiError{http.StatusBadRequest, "invalid_grant",
					fmt.Sprintf("bitbucket refused the refresh (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))}
			}
			last = &apiError{http.StatusBadGateway, "bitbucket_error",
				fmt.Sprintf("bitbucket refresh failed: %d", resp.StatusCode)}
			continue
		}
		var out bbRefreshToken
		derr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
		resp.Body.Close()
		if derr != nil {
			return bbRefreshToken{}, &apiError{http.StatusBadGateway, "bitbucket_error", derr.Error()}
		}
		if out.AccessToken == "" {
			return bbRefreshToken{}, &apiError{http.StatusBadGateway, "bitbucket_error", "refresh returned no access_token"}
		}
		if out.ExpiresIn <= 0 {
			out.ExpiresIn = 7200
		}
		return out, nil
	}
	return bbRefreshToken{}, last
}
