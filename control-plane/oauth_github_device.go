package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// GitHub git-connection OAuth via the Device Authorization Grant (RFC 8628), run by
// the Control Plane (docs/71 §71.5).
//
// ★ It used to run in the Workspace Agent, reading GITHUB_OAUTH_CLIENT_ID out of the
// container's environment, which the CP injected from its own env. Moving it here is
// what makes the app per-tenant, and the reason is the environment, not tidiness:
// container env is fixed at `docker run` / task-definition / process-spawn time, one
// implementation per runtime (docker, native, ecs, ecs-ec2). A per-tenant value
// delivered that way would need all four plumbed AND would only reach a member after
// their workspace was restarted — so a tenant administrator registering an app would
// be telling everyone to stop and start their workspace. Read from the CP's database
// at the moment the button is pressed, it takes effect immediately, everywhere.
//
// The device grant needs no client_secret and no callback: the Console shows the
// user_code + verification_uri and polls. On success the CP hands the token to the
// Agent through the ordinary connection endpoint (PUT /connections/git/github.com),
// which is the same path a pasted PAT takes — so storage, the credential helper and
// the account lookup are unchanged.
//
// Bitbucket's grant lives in oauth_bitbucket.go; the two differ only in which leg the
// browser walks.

const (
	ghDeviceCodeURL  = "https://github.com/login/device/code"
	ghAccessTokenURL = "https://github.com/login/oauth/access_token"
	ghDeviceGrant    = "urn:ietf:params:oauth:grant-type:device_code"
	// repo = private read + push. workflow is the extra scope GitHub demands for a push
	// that creates or changes anything under .github/workflows/ (without it the remote
	// rejects), and matches the gh CLI's defaults. Existing connections are NOT
	// retroactively upgraded — reconnecting mints a token with the current scopes.
	ghDeviceScope = "repo workflow"
)

// ghDeviceHTTPClient bounds the calls OUT to github.com. ★ Not for the CP→Agent leg:
// that one goes through agentHTTPClient, whose Transport carries the Cloud Map
// fallback (agent_dial.go) — using the default client there is what made the Bitbucket
// save fail with "no such host" for every workspace created after the CP task started.
var ghDeviceHTTPClient = &http.Client{Timeout: 20 * time.Second}

// ghDeviceFlow is one in-flight authorization.
//
// user / tenant are captured at START and re-used at every poll, for the same reason
// bbState carries them: the poll must install the token in the workspace of the person
// who began the flow, in the tenant they were looking at.
type ghDeviceFlow struct {
	deviceCode string
	interval   int
	deadline   time.Time
	user       string // identity user key
	tenant     string // tenant selector as sent at start
	tenantID   string
	clientID   string
}

// ghDeviceRegistry owns the in-flight flows. Process memory, like bbFlows: a
// multi-instance CP needs sticky routing or a DB spill (docs/dev P3-7).
type ghDeviceRegistry struct {
	mu    sync.Mutex
	flows map[string]*ghDeviceFlow
}

func (g *ghDeviceRegistry) put(id string, f *ghDeviceFlow) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, old := range g.flows { // reap expired flows
		if time.Now().After(old.deadline) {
			delete(g.flows, k)
		}
	}
	g.flows[id] = f
}

func (g *ghDeviceRegistry) get(id string) *ghDeviceFlow {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.flows[id]
}

func (g *ghDeviceRegistry) forget(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.flows, id)
}

var ghDeviceFlows = &ghDeviceRegistry{flows: map[string]*ghDeviceFlow{}}

// handleGithubDeviceStart (POST /api/connections/git/github/oauth/start).
func (c config) handleGithubDeviceStart(w http.ResponseWriter, r *http.Request) {
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
	clientID, _, ok, err := c.mgr.gitOAuthApp(r.Context(), mv.TenantID, gitOAuthGitHub)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "not_configured",
			"this tenant has no GitHub OAuth app (a tenant administrator registers it in tenant settings)"})
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
	if err := ghDevicePostForm(ghDeviceCodeURL, url.Values{"client_id": {clientID}, "scope": {ghDeviceScope}}, &resp); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadGateway, "github_error", err.Error()})
		return
	}
	if resp.DeviceCode == "" {
		// The commonest cause by far is an OAuth App whose "Device flow" checkbox was
		// never ticked, and GitHub answers that with a bare error code. Say which
		// setting, or the tenant administrator has nothing to act on.
		writeAPIErr(w, &apiError{http.StatusBadGateway, "github_error",
			"github returned no device_code (" + resp.Error + ") — check that the OAuth app has device flow enabled"})
		return
	}
	interval := resp.Interval
	if interval <= 0 {
		interval = 5
	}
	expires := resp.ExpiresIn
	if expires <= 0 {
		expires = 900
	}
	flowID := randHex(16)
	ghDeviceFlows.put(flowID, &ghDeviceFlow{
		deviceCode: resp.DeviceCode, interval: interval,
		deadline: time.Now().Add(time.Duration(expires) * time.Second),
		user:     ident.UserKey, tenant: tenantSel(r), tenantID: mv.TenantID, clientID: clientID,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"flow_id": flowID, "user_code": resp.UserCode, "verification_uri": resp.VerificationURI,
		"interval": interval, "expires_in": expires,
	})
}

// handleGithubDevicePoll (POST /api/connections/git/github/oauth/poll).
func (c config) handleGithubDevicePoll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FlowID string `json:"flow_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	f := ghDeviceFlows.get(req.FlowID)
	if f == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_flow", "unknown or expired flow_id"})
		return
	}
	// ★ The token is installed into the flow's OWN user's workspace, so only that
	// person may poll it: a flow_id is a bearer of somebody's pending grant, and a
	// different caller polling it would put the token in the first person's workspace
	// and be told "connected". Answered as no_flow rather than forbidden — a stranger
	// learns nothing about whether the id exists.
	ident, aerr := c.mgr.identityFor(r.Context(), r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if ident.UserKey != f.user {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_flow", "unknown or expired flow_id"})
		return
	}
	if time.Now().After(f.deadline) {
		ghDeviceFlows.forget(req.FlowID)
		writeAPIErr(w, &apiError{http.StatusBadRequest, "expired_token", "device code expired; restart"})
		return
	}
	var resp struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	form := url.Values{"client_id": {f.clientID}, "device_code": {f.deviceCode}, "grant_type": {ghDeviceGrant}}
	if err := ghDevicePostForm(ghAccessTokenURL, form, &resp); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadGateway, "github_error", err.Error()})
		return
	}
	switch {
	case resp.AccessToken != "":
		if aerr := c.storeGithubToken(r, f, resp.AccessToken); aerr != nil {
			// The grant is spent, so the flow is over either way — keeping it would only
			// let the Console poll a device code GitHub has already consumed.
			ghDeviceFlows.forget(req.FlowID)
			writeAPIErr(w, aerr)
			return
		}
		ghDeviceFlows.forget(req.FlowID)
		writeJSON(w, http.StatusOK, map[string]any{"connected": true})
	case resp.Error == "authorization_pending":
		writeJSON(w, http.StatusOK, map[string]any{"pending": true})
	case resp.Error == "slow_down":
		ghDeviceFlows.mu.Lock()
		f.interval += 5
		iv := f.interval
		ghDeviceFlows.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"pending": true, "interval": iv})
	default:
		ghDeviceFlows.forget(req.FlowID)
		writeAPIErr(w, &apiError{http.StatusBadRequest, "oauth_error", resp.Error})
	}
}

// storeGithubToken hands the access token to the member's Agent through the ordinary
// connection endpoint, so the token lands exactly where a pasted PAT would (encrypted
// store + credential helper + account lookup) with no second storage path to keep in
// step.
func (c config) storeGithubToken(r *http.Request, f *ghDeviceFlow, token string) *apiError {
	rt, aerr := c.mgr.resolve(r.Context(), f.user, "", f.tenant)
	if aerr != nil {
		return aerr
	}
	payload, _ := json.Marshal(map[string]any{"token": token})
	areq, _ := http.NewRequest("PUT", rt.Endpoint()+"/connections/git/github.com", strings.NewReader(string(payload)))
	areq.Header.Set("Content-Type", "application/json")
	if rt.Token() != "" {
		areq.Header.Set("Authorization", "Bearer "+rt.Token()) // CP↔Agent auth
	}
	aresp, err := agentHTTPClient.Do(areq)
	if err != nil {
		return &apiError{http.StatusBadGateway, "store_unreachable",
			"authorized, but the workspace agent could not be reached to save the token (is the workspace running?): " + err.Error()}
	}
	defer aresp.Body.Close()
	if aresp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(aresp.Body, 4096))
		return &apiError{http.StatusBadGateway, "store_failed", "authorized, but saving the token failed: " + string(b)}
	}
	return nil
}

// ghDevicePostForm POSTs a urlencoded form and decodes a JSON response (GitHub returns
// form-encoded by default; Accept: application/json switches it to JSON).
func ghDevicePostForm(endpoint string, form url.Values, out any) error {
	req, err := http.NewRequest("POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := ghDeviceHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}
