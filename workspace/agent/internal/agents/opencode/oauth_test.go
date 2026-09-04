package opencode

// The fake daemon replies with the shapes measured against a live `opencode serve`
// 1.18.13 (its OpenAPI /doc, docs/log/54 §contract): every response is a {location, data}
// envelope, attempt statuses are pending|complete|failed|expired, and the Console
// connection surfaces as connections[].type == "credential" with the org name as its
// label (an OPENCODE_API_KEY coming from the env is a different type).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDaemon wires oauthDaemon/oauthProbe to a test server for the duration of a test.
type fakeDaemon struct {
	mu      sync.Mutex
	status  string // attempt status to report
	conns   []map[string]any
	deleted []string // paths of DELETE calls
	// notReady counts down the GETs that answer like a daemon whose plugin has not
	// finished loading: health is up, but the integration (and its device method)
	// is not published yet — the real 500 `OAuth method not found` window.
	notReady int
	// nullData makes the integration GET answer data:null, as it really does just after
	// startup.
	nullData bool
	startErr int // non-zero => connect/oauth replies with this status
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{status: "pending"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/integration/opencode/connect/oauth":
			var body struct {
				MethodID string            `json:"methodID"`
				Inputs   map[string]string `json:"inputs"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.MethodID != "device" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"unexpected methodID"}`))
				return
			}
			if d.startErr != 0 {
				w.WriteHeader(d.startErr)
				_, _ = w.Write([]byte(`{"error":"nope"}`))
				return
			}
			_, _ = w.Write([]byte(`{"location":{"directory":"/home/dev"},"data":{"attemptID":"att_1","url":"https://console.opencode.ai/auth/device?user_code=ABCD-EFGH","instructions":"Enter code: ABCD-EFGH","mode":"auto","time":{"created":1,"expires":600000}}}`))
		case r.Method == "GET" && r.URL.Path == "/api/integration/attempt/att_1":
			msg := ""
			if d.status == "failed" {
				msg = `,"message":"authorization denied"`
			}
			_, _ = w.Write([]byte(`{"location":{},"data":{"status":"` + d.status + `","time":{"created":1,"expires":2}` + msg + `}}`))
		case r.Method == "GET" && r.URL.Path == "/api/integration/opencode":
			if d.nullData {
				_, _ = w.Write([]byte(`{"location":{},"data":null}`))
				return
			}
			methods := []map[string]any{{"type": "key"}, {"type": "env", "names": []string{"OPENCODE_API_KEY"}}}
			if d.notReady > 0 {
				d.notReady-- // plugin still loading: the device method has not appeared yet
			} else {
				methods = append(methods, map[string]any{"id": "device", "type": "oauth", "label": "OpenCode Console account"})
			}
			b, _ := json.Marshal(map[string]any{"location": map[string]any{}, "data": map[string]any{
				"id": "opencode", "name": "OpenCode", "methods": methods, "connections": d.conns,
			}})
			_, _ = w.Write(b)
		case r.Method == "DELETE":
			d.deleted = append(d.deleted, r.URL.Path)
			_, _ = w.Write([]byte(`true`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	origDaemon, origProbe := oauthDaemon, oauthProbe
	oauthDaemon = func() (string, error) { return srv.URL, nil }
	oauthProbe = func() (string, bool) { return srv.URL, true }
	t.Cleanup(func() { oauthDaemon, oauthProbe = origDaemon, origProbe })
	resetOAuthCache()
	t.Cleanup(resetOAuthCache)
	return d
}

// resetOAuthCache clears the whole cached view, not just its age: the package-level
// cache would otherwise leak a previous test's connection into the next one.
func resetOAuthCache() {
	oauthMu.Lock()
	oauthCache = oauthState{}
	oauthAt = time.Time{}
	oauthMu.Unlock()
}

func (d *fakeDaemon) set(status string) {
	d.mu.Lock()
	d.status = status
	d.mu.Unlock()
}

func (d *fakeDaemon) setConns(c []map[string]any) {
	d.mu.Lock()
	d.conns = c
	d.mu.Unlock()
}

func (d *fakeDaemon) deletes() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.deleted...)
}

func doJSON(t *testing.T, h http.HandlerFunc, method, path, body string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	h(w, r)
	var out map[string]any
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

func TestOAuthStartReturnsVerificationURL(t *testing.T) {
	newFakeDaemon(t)
	code, out := doJSON(t, HandleOAuthStart, "POST", "/connections/opencode/oauth/start", "")
	if !Available() {
		// Where opencode is absent (some CI runners), start correctly stops at 409.
		if code != http.StatusConflict {
			t.Fatalf("with opencode absent this should be 409: %d %v", code, out)
		}
		return
	}
	if code != http.StatusOK {
		t.Fatalf("status=%d out=%v", code, out)
	}
	if out["flow_id"] != "att_1" {
		t.Errorf("flow_id = %v, want att_1", out["flow_id"])
	}
	if u, _ := out["url"].(string); !strings.HasPrefix(u, "https://console.opencode.ai/auth/device") {
		t.Errorf("url = %v", out["url"])
	}
	// device is mode=auto, which is the basis for not showing a code entry field in the
	// Console, so it is pinned here.
	if out["mode"] != "auto" {
		t.Errorf("mode = %v, want auto", out["mode"])
	}
	if out["instructions"] == "" {
		t.Errorf("instructions is empty")
	}
	// The code shown as step 1. The URL's query is the primary source.
	if out["user_code"] != "ABCD-EFGH" {
		t.Errorf("user_code = %v, want ABCD-EFGH", out["user_code"])
	}
}

func TestUserCodeExtraction(t *testing.T) {
	for _, tc := range []struct {
		name, url, instructions, want string
	}{
		{"the URL query wins", "https://x/auth/device?user_code=WXYZ-1234", "Enter code: OTHER-CODE", "WXYZ-1234"},
		{"falls back to the instructions text", "https://x/auth/device", "Enter code: ABCD-EFGH", "ABCD-EFGH"},
		{"empty when neither carries one", "https://x/auth/device", "Approve in your browser", ""},
		{"a broken URL does not crash it", "://", "Enter code: ABCD-EFGH", "ABCD-EFGH"},
	} {
		if got := userCode(tc.url, tc.instructions); got != tc.want {
			t.Errorf("%s: userCode = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Reproduces the field failure (ref=err_91d98832): a click 85ms after startup.
// /global/health answers 200 while the device method is not registered yet, so a naive
// POST gets a 500 `OAuth method not found: opencode/device` from the daemon. Wait until
// the method appears.
func TestOAuthStartWaitsForPluginLoad(t *testing.T) {
	if !Available() {
		t.Skip("the opencode CLI is not present in this environment")
	}
	d := newFakeDaemon(t)
	d.mu.Lock()
	d.notReady = 3 // answer "still loading" three times
	d.mu.Unlock()
	orig := oauthReadyPoll
	oauthReadyPoll = time.Millisecond
	defer func() { oauthReadyPoll = orig }()

	code, out := doJSON(t, HandleOAuthStart, "POST", "/connections/opencode/oauth/start", "")
	if code != http.StatusOK {
		t.Fatalf("should succeed after waiting for the load: status=%d out=%v", code, out)
	}
	if out["flow_id"] != "att_1" {
		t.Errorf("flow_id = %v", out["flow_id"])
	}
}

// When the method never appears (an older CLI, say), report the reason instead of waiting
// forever.
func TestOAuthStartGivesUpWhenMethodNeverAppears(t *testing.T) {
	if !Available() {
		t.Skip("the opencode CLI is not present in this environment")
	}
	d := newFakeDaemon(t)
	d.mu.Lock()
	d.notReady = 1 << 30
	d.mu.Unlock()
	orig, origTO := oauthReadyPoll, oauthReadyTimeout
	oauthReadyPoll = time.Millisecond
	oauthReadyTimeout = 20 * time.Millisecond
	defer func() { oauthReadyPoll, oauthReadyTimeout = orig, origTO }()

	code, out := doJSON(t, HandleOAuthStart, "POST", "/connections/opencode/oauth/start", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d out=%v", code, out)
	}
	if msg, _ := out["error"].(map[string]any); msg == nil || msg["code"] != "serve_not_ready" {
		t.Errorf("the reason did not come through: %v", out)
	}
}

// Just after startup the integration itself is data:null. Do not conclude "not connected"
// from that.
func TestOAuthStatusIgnoresNullIntegration(t *testing.T) {
	d := newFakeDaemon(t)
	d.setConns([]map[string]any{{"type": "credential", "id": "con_1", "label": "acme-inc"}})
	if st := oauthStatus(); !st.connected {
		t.Fatal("precondition: the connected state was not read")
	}
	invalidateOAuthStatus()
	d.mu.Lock()
	d.nullData = true
	d.mu.Unlock()
	if st := oauthStatus(); !st.connected || st.label != "acme-inc" {
		t.Errorf("the connection display vanished during the startup race: %+v", st)
	}
}

func TestOAuthPollMapsAttemptStatuses(t *testing.T) {
	d := newFakeDaemon(t)
	for _, tc := range []struct {
		status    string
		connected bool
	}{
		{"pending", false},
		{"expired", false},
		{"failed", false},
		{"complete", true},
	} {
		d.set(tc.status)
		code, out := doJSON(t, HandleOAuthPoll, "POST", "/connections/opencode/oauth/poll", `{"flow_id":"att_1"}`)
		if code != http.StatusOK {
			t.Fatalf("%s: status=%d out=%v", tc.status, code, out)
		}
		if out["connected"] != tc.connected {
			t.Errorf("%s: connected = %v, want %v", tc.status, out["connected"], tc.connected)
		}
		if out["status"] != tc.status {
			t.Errorf("status = %v, want %v", out["status"], tc.status)
		}
		if tc.status == "failed" && out["message"] != "authorization denied" {
			t.Errorf("the reason for failed was dropped: %v", out["message"])
		}
	}
}

// On success the model catalog cache must be dropped. Without this, the launch modal
// right after signing in shows a model set up to 60 seconds stale, i.e. the pre-login one.
func TestOAuthPollCompleteInvalidatesModelsCache(t *testing.T) {
	d := newFakeDaemon(t)
	d.set("complete")

	modelsMu.Lock()
	modelsList = []string{"opencode/stale"}
	modelsAt = time.Now()
	modelsMu.Unlock()

	if code, out := doJSON(t, HandleOAuthPoll, "POST", "/connections/opencode/oauth/poll", `{"flow_id":"att_1"}`); code != http.StatusOK || out["connected"] != true {
		t.Fatalf("poll: status=%d out=%v", code, out)
	}
	modelsMu.Lock()
	fresh := modelsAt.IsZero()
	modelsMu.Unlock()
	if !fresh {
		t.Error("the model cache is still valid after a successful sign-in")
	}
}

func TestOAuthPollRequiresFlowID(t *testing.T) {
	newFakeDaemon(t)
	if code, _ := doJSON(t, HandleOAuthPoll, "POST", "/connections/opencode/oauth/poll", `{}`); code != http.StatusBadRequest {
		t.Errorf("a request with no flow_id should be 400: %d", code)
	}
}

func TestOAuthCancelDeletesAttempt(t *testing.T) {
	d := newFakeDaemon(t)
	if code, _ := doJSON(t, HandleOAuthCancel, "POST", "/connections/opencode/oauth/cancel", `{"flow_id":"att_1"}`); code != http.StatusOK {
		t.Fatalf("cancel status=%d", code)
	}
	if got := d.deletes(); len(got) != 1 || got[0] != "/api/integration/attempt/att_1" {
		t.Errorf("the attempt was not deleted: %v", got)
	}
}

// Disconnecting means deleting by credential ID. v1's `DELETE /auth/opencode` does not
// remove it: that one works on auth.json, while the Console account's credential lives in
// SQLite. Measured: after calling it, a fresh process still saw the connection.
func TestOAuthDisconnectRemovesCredential(t *testing.T) {
	d := newFakeDaemon(t)
	d.setConns([]map[string]any{
		{"type": "env", "name": "OPENCODE_API_KEY"},
		{"type": "credential", "id": "cred_1", "label": "Personal"},
	})
	code, out := doJSON(t, HandleOAuthDisconnect, "DELETE", "/connections/opencode/oauth", "")
	if code != http.StatusOK || out["disconnected"] != "opencode" {
		t.Fatalf("status=%d out=%v", code, out)
	}
	if got := d.deletes(); len(got) != 1 || got[0] != "/api/credential/cred_1" {
		t.Errorf("the credential was deleted from the wrong place: %v", got)
	}
}

// Pressed with no connection (a stale display, a double click) it must succeed
// idempotently and must not take the env API-key connection down with it.
func TestOAuthDisconnectIdempotentWithoutCredential(t *testing.T) {
	d := newFakeDaemon(t)
	d.setConns([]map[string]any{{"type": "env", "name": "OPENCODE_API_KEY"}})
	code, out := doJSON(t, HandleOAuthDisconnect, "DELETE", "/connections/opencode/oauth", "")
	if code != http.StatusOK || out["disconnected"] != "opencode" {
		t.Fatalf("status=%d out=%v", code, out)
	}
	if got := d.deletes(); len(got) != 0 {
		t.Errorf("issued a DELETE with nothing to delete: %v", got)
	}
}

func TestOAuthStatusReadsCredentialConnection(t *testing.T) {
	d := newFakeDaemon(t)

	// An env connection alone means the API-key method; the Console account is not connected.
	d.setConns([]map[string]any{{"type": "env", "name": "OPENCODE_API_KEY"}})
	if st := oauthStatus(); st.connected || !st.known {
		t.Errorf("with env only: connected=%v known=%v", st.connected, st.known)
	}

	invalidateOAuthStatus()
	d.setConns([]map[string]any{
		{"type": "env", "name": "OPENCODE_API_KEY"},
		{"type": "credential", "id": "con_1", "label": "acme-inc"},
	})
	st := oauthStatus()
	if !st.connected || st.label != "acme-inc" {
		t.Errorf("the credential connection was not picked up: %+v", st)
	}
}

// While the daemon is down the status must not drop to "not connected" (stale-if-error).
// A flickering connection display pushes the user into signing in all over again.
func TestOAuthStatusKeepsLastKnownWhenDaemonDown(t *testing.T) {
	d := newFakeDaemon(t)
	d.setConns([]map[string]any{{"type": "credential", "id": "con_1", "label": "acme-inc"}})
	if st := oauthStatus(); !st.connected {
		t.Fatal("precondition: the connected state was not read")
	}
	invalidateOAuthStatus()
	orig := oauthProbe
	oauthProbe = func() (string, bool) { return "", false }
	defer func() { oauthProbe = orig }()
	if st := oauthStatus(); !st.connected || st.label != "acme-inc" {
		t.Errorf("the connection display vanished when the daemon stopped: %+v", st)
	}
}

func TestOAuthStatusUnknownBeforeAnyRead(t *testing.T) {
	newFakeDaemon(t)
	orig := oauthProbe
	oauthProbe = func() (string, bool) { return "", false }
	defer func() { oauthProbe = orig }()
	st := oauthStatus()
	if st.connected || st.known {
		t.Errorf("never having read it, this should be unknown: %+v", st)
	}
}
