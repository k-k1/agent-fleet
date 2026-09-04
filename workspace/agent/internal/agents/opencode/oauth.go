package opencode

// OAuth device flow for the opencode Console account (the opencode.ai org = workspace). It is
// a second auth route that coexists with the API-key scheme (env injection in auth.go); either
// one alone or both together work (measured: the gate on paid models is the OR of
// `OPENCODE_API_KEY || Console connection || explicit apiKey`).
//
// The route is the HTTP API of `opencode serve`, not a PTY scrape of the CLI (measured on
// 1.18.13; the OpenAPI /doc is authoritative). The same flow shape as cursor/codex
// (start→poll) is available as structured JSON from the start:
//
//	POST /api/integration/opencode/connect/oauth {methodID:"device",inputs:{}}
//	  → {data:{attemptID,url,instructions,mode:"auto"|"code",time:{created,expires}}}
//	GET  /api/integration/attempt/{attemptID}
//	  → {data:{status:"pending"|"complete"|"failed"|"expired", message?}}
//	DELETE /api/integration/attempt/{attemptID}      … cancel
//	GET  /api/integration/opencode
//	  → {data:{connections:[{type:"credential",id,label} | {type:"env",name}]}}
//	DELETE /api/credential/{credentialID}            … disconnect (connections[] carries the id)
//
// The device mode is "auto" (opencode polls for the token itself), so the Console never has to
// make the user paste a code — the same "show a URL and poll" shape as cursor.
//
// What going through the daemon buys is the timing of the update (docs/log/54): when the login
// succeeds the daemon publishes integration.connection.updated, and the opencode plugin
// subscribes to it, refetches the Console org's /api/config and calls catalog.reload(). So
// managed sessions on the shared daemon see the new model set without a restart. The only
// thing that goes stale on this side is the 60-second cache in models.go, which is therefore
// dropped explicitly when the login succeeds.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// oauthIntegrationID / oauthMethodID are fixed IDs on the opencode side (measured): the
// opencode Console account is published as the oauth method "device" of integration
// "opencode".
const (
	oauthIntegrationID = "opencode"
	oauthMethodID      = "device"
)

// oauthDaemon returns the serve daemon that the auth API is called against, starting it if
// necessary. Seam the tests replace.
//
// This is the one place that wakes the daemon even while unconnected (ensure's allowUnauthed):
// this API group is precisely the route that turns unconnected into connected, so refusing on
// the grounds of being unconnected would make login impossible forever. Instead oauthTouch
// counts it as demand, and auto-stop reclaims the daemon once the flow ends.
var oauthDaemon = func() (string, error) {
	oauthTouch()
	addr, _, err := Serve().ensure(true)
	return addr, err
}

// oauthHoldTTL is the window after the last OAuth operation during which a flow still counts
// as in progress. The Console polls the attempt every few seconds during the device flow, so
// the window keeps being refreshed.
const oauthHoldTTL = 3 * time.Minute

var (
	oauthTouchMu sync.Mutex
	oauthTouchAt time.Time
)

// oauthTouch marks the OAuth flow as active so the zero-demand watcher does not fold the
// daemon up mid-flow. Called at the entry of start / poll / cancel / disconnect.
func oauthTouch() {
	oauthTouchMu.Lock()
	oauthTouchAt = time.Now()
	oauthTouchMu.Unlock()
}

// oauthBusy reports whether an OAuth flow touched the daemon recently.
func oauthBusy() bool {
	oauthTouchMu.Lock()
	defer oauthTouchMu.Unlock()
	return !oauthTouchAt.IsZero() && time.Since(oauthTouchAt) < oauthHoldTTL
}

// oauthProbe returns the URL of a daemon that is already running, and never starts one. It is
// called from the /connections polling, which must not wake the daemon merely to display
// state. Seam the tests replace.
var oauthProbe = func() (string, bool) {
	addr := serveAddr()
	if !healthy(addr) {
		return "", false
	}
	return addr, true
}

// oauthClient is the short-timeout HTTP client for control calls (same character as
// serveClient).
var oauthClient = &http.Client{Timeout: 15 * time.Second}

// --- daemon calls -----------------------------------------------------------

// attemptInfo is the `data` of a connect/oauth response (measured against the OpenAPI).
type attemptInfo struct {
	AttemptID    string `json:"attemptID"`
	URL          string `json:"url"`
	Instructions string `json:"instructions"`
	Mode         string `json:"mode"` // "auto" | "code"
	Time         struct {
		Created float64 `json:"created"`
		Expires float64 `json:"expires"`
	} `json:"time"`
}

// attemptStatus is the `data` of an attempt status response (measured against the OpenAPI).
type attemptStatus struct {
	Status  string `json:"status"` // pending | complete | failed | expired
	Message string `json:"message"`
}

// integrationInfo is the `data` of GET /api/integration/{id} (measured against the OpenAPI).
type integrationInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Methods []struct {
		ID   string `json:"id"`   // only oauth methods carry one
		Type string `json:"type"` // "oauth" | "key" | "env"
	} `json:"methods"`
	Connections []struct {
		Type  string `json:"type"` // "credential" (from key/oauth) | "env"
		ID    string `json:"id"`
		Label string `json:"label"` // Console org name on a device connection (measured label resolution)
		Name  string `json:"name"`  // variable name of an env connection
	} `json:"connections"`
}

// envelope is the {location, data} wrapper common to this API group.
type envelope[T any] struct {
	Data T `json:"data"`
}

func daemonJSON[T any](method, addr, path string, body any, out *envelope[T]) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, addr+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := oauthClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2<<10))
		return fmt.Errorf("opencode serve %s %s: %s: %s", method, path, res.Status, bytes.TrimSpace(msg))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// --- state ------------------------------------------------------------------

// oauthState is the cached view of the Console connection for /connections.
type oauthState struct {
	connected bool
	label     string
	known     bool // whether the daemon was ever read successfully (drives stale-if-error)
}

var (
	oauthMu    sync.Mutex
	oauthCache oauthState
	oauthAt    time.Time
)

// oauthStatusTTL thins out the round trips to the daemon: /connections arrives every few
// seconds.
const oauthStatusTTL = 15 * time.Second

// invalidateOAuthStatus drops the cached connection view so the next /connections
// poll reflects a login/logout at once instead of after the TTL.
func invalidateOAuthStatus() {
	oauthMu.Lock()
	oauthAt = time.Time{}
	oauthMu.Unlock()
}

// oauthStatus reports the Console-account connection for Status(). When no daemon is running
// it is not worth waking one to find out: the last value read is returned as stale, and if
// nothing was ever read, unknown (connected=false, known=false).
func oauthStatus() oauthState {
	oauthMu.Lock()
	defer oauthMu.Unlock()
	if !oauthAt.IsZero() && time.Since(oauthAt) < oauthStatusTTL {
		return oauthCache
	}
	addr, up := oauthProbe()
	if !up {
		return oauthCache // stale-if-error: a stopped daemon must not clear the display
	}
	var env envelope[integrationInfo]
	if err := daemonJSON("GET", addr, "/api/integration/"+oauthIntegrationID, nil, &env); err != nil {
		return oauthCache
	}
	if env.Data.ID == "" {
		// Right after startup the integration is not registered yet and data:null comes
		// back. Settling on "not connected" here would briefly show a logged-in user as
		// disconnected (the same startup race as the start side — see waitOAuthMethod).
		return oauthCache
	}
	st := oauthState{known: true}
	for _, c := range env.Data.Connections {
		// An env-sourced entry (OPENCODE_API_KEY) is the API-key scheme's display, not a
		// Console connection.
		if c.Type == "credential" {
			st.connected = true
			st.label = c.Label
			break
		}
	}
	oauthCache = st
	oauthAt = time.Now()
	return st
}

// oauthReadyTimeout bounds the wait for the plugin-registered device method: long enough that
// a click right after startup still makes it, short enough not to keep waiting for a method
// that is not coming. It is a var, like oauthReadyPoll, so tests do not wait real time.
var (
	oauthReadyTimeout = 20 * time.Second
	oauthReadyPoll    = 300 * time.Millisecond
)

// deviceMethodReady reports whether the daemon is already publishing the device method.
func deviceMethodReady(addr string) bool {
	var env envelope[integrationInfo]
	if err := daemonJSON("GET", addr, "/api/integration/"+oauthIntegrationID, nil, &env); err != nil {
		return false
	}
	for _, m := range env.Data.Methods {
		if m.Type == "oauth" && m.ID == oauthMethodID {
			return true
		}
	}
	return false
}

// waitOAuthMethod blocks until the device method shows up (plugin load finished).
func waitOAuthMethod(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if deviceMethodReady(addr) {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("opencode serve の初期化が終わらず、アカウントのサインインを開始できませんでした（少し待って再試行してください）")
		}
		time.Sleep(oauthReadyPoll)
	}
}

// --- handlers ---------------------------------------------------------------

// HandleOAuthStart begins the Console device flow and returns the verification URL
// with a flow_id the Console polls. POST /connections/opencode/oauth/start.
func HandleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if !Available() {
		httpx.WriteErr(w, http.StatusConflict, "opencode_unsupported", "opencode が見つかりません（旧イメージの可能性）")
		return
	}
	addr, err := oauthDaemon()
	if err != nil {
		// Reached when managed opencode is turned off. Interactive CLI login
		// (`opencode auth login`) remains available from a terminal session.
		httpx.WriteErr(w, http.StatusServiceUnavailable, "serve_unavailable", "opencode serve を起動できませんでした: "+err.Error())
		return
	}
	// health alone is not enough: /global/health returns 200 from the moment of startup, but
	// the device method is registered by an opencode plugin whose load finishes later.
	// Measured (a click 85ms after startup): hit inside that window the daemon returns 500
	// `OAuth method not found: opencode/device`. Wait until the method is visible.
	if err := waitOAuthMethod(addr, oauthReadyTimeout); err != nil {
		httpx.WriteErr(w, http.StatusServiceUnavailable, "serve_not_ready", err.Error())
		return
	}
	var env envelope[attemptInfo]
	body := map[string]any{"methodID": oauthMethodID, "inputs": map[string]string{}}
	if err := daemonJSON("POST", addr, "/api/integration/"+oauthIntegrationID+"/connect/oauth", body, &env); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "oauth_start_failed", err.Error())
		return
	}
	at := env.Data
	if at.AttemptID == "" || at.URL == "" {
		httpx.WriteErr(w, http.StatusBadGateway, "no_url", "opencode が認証 URL を返しませんでした")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"flow_id":      at.AttemptID,
		"url":          at.URL,
		"user_code":    userCode(at.URL, at.Instructions),
		"instructions": at.Instructions,
		"mode":         at.Mode,
		"expires":      at.Time.Expires,
	})
}

// userCodeRe is the fallback source: opencode phrases the instruction as
// "Enter code: ABCD-EFGH" (measured). The URL side is a standard OAuth device-flow parameter
// and survives a change of wording, so it is consulted first.
var userCodeRe = regexp.MustCompile(`\b([A-Z0-9]{4,8}-[A-Z0-9]{4,8})\b`)

// userCode extracts the device code to show as its own step. Empty when there is none — the
// Console then falls back to displaying just "open the link", which works because the URL
// itself carries the code.
func userCode(rawURL, instructions string) string {
	if u, err := url.Parse(rawURL); err == nil {
		for _, k := range []string{"user_code", "userCode", "code"} {
			if v := u.Query().Get(k); v != "" {
				return v
			}
		}
	}
	return userCodeRe.FindString(instructions)
}

type oauthPollReq struct {
	FlowID string `json:"flow_id"`
}

// HandleOAuthPoll reports whether the browser approval has completed. The device flow runs
// with mode="auto" (opencode polls for the token itself), so this side only has to look at
// the attempt's status. POST /connections/opencode/oauth/poll.
func HandleOAuthPoll(w http.ResponseWriter, r *http.Request) {
	var req oauthPollReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.FlowID == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "flow_id が必要です")
		return
	}
	oauthTouch() // mid-flow, keep the zero-demand watcher from folding the daemon up
	addr, up := oauthProbe()
	if !up {
		httpx.WriteErr(w, http.StatusServiceUnavailable, "serve_unavailable", "opencode serve が停止しました")
		return
	}
	var env envelope[attemptStatus]
	if err := daemonJSON("GET", addr, "/api/integration/attempt/"+url.PathEscape(req.FlowID), nil, &env); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "oauth_poll_failed", err.Error())
		return
	}
	st := env.Data
	out := map[string]any{"status": st.Status, "connected": st.Status == "complete"}
	if st.Message != "" {
		out["message"] = st.Message
	}
	if st.Status == "complete" {
		// The daemon-side catalogue is already updated through the event subscription
		// (docs/log/54). Only this side's 60-second cache is stale, so drop it — the
		// launch modal and MCP list_models must see the new model set at once.
		InvalidateModels()
		invalidateOAuthStatus()
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// HandleOAuthCancel aborts an in-flight attempt so a half-finished flow doesn't sit
// in the daemon until it expires. POST /connections/opencode/oauth/cancel.
func HandleOAuthCancel(w http.ResponseWriter, r *http.Request) {
	var req oauthPollReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if addr, up := oauthProbe(); up && req.FlowID != "" {
		_ = daemonJSON[struct{}]("DELETE", addr, "/api/integration/attempt/"+url.PathEscape(req.FlowID), nil, nil)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"cancelled": true})
}

// HandleOAuthDisconnect removes the stored Console credential. The API key (env injection) is
// a separate route and is not removed by this operation. DELETE /connections/opencode/oauth.
//
// The target is `DELETE /api/credential/{credentialID}`, whose id comes from the
// integration's connections[]. v1's `DELETE /auth/{providerID}` does not remove it — that
// route rewrites auth.json, while a v2 credential lives in SQLite (measured: `opencode auth
// list` reports zero entries while connections still holds a credential). Calling
// /auth/opencode indeed left the connection visible to a fresh process.
func HandleOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	// Fetch with a start rather than a probe: serve auto-stops at zero demand, so otherwise
	// "a user not running managed sessions cannot disconnect" would be the normal case.
	addr, err := oauthDaemon()
	if err != nil {
		httpx.WriteErr(w, http.StatusServiceUnavailable, "serve_unavailable",
			"opencode serve を起動できなかったため切断できませんでした: "+err.Error())
		return
	}
	id, err := credentialID(addr)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "oauth_disconnect_failed", err.Error())
		return
	}
	if id == "" {
		// Already disconnected: treat it as an idempotent success, so a double click
		// caused by a stale display does no harm.
		invalidateOAuthStatus()
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "opencode"})
		return
	}
	if err := daemonJSON[struct{}]("DELETE", addr, "/api/credential/"+url.PathEscape(id), nil, nil); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "oauth_disconnect_failed", err.Error())
		return
	}
	invalidateOAuthStatus()
	InvalidateModels()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "opencode"})
}

// credentialID returns the id of the Console-account credential, or "" when there is
// none. env connections (OPENCODE_API_KEY) are a separate route and are left alone.
func credentialID(addr string) (string, error) {
	var env envelope[integrationInfo]
	if err := daemonJSON("GET", addr, "/api/integration/"+oauthIntegrationID, nil, &env); err != nil {
		return "", err
	}
	for _, c := range env.Data.Connections {
		if c.Type == "credential" {
			return c.ID, nil
		}
	}
	return "", nil
}
