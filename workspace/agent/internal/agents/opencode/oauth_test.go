package opencode

// The fake daemon replies with the shapes measured against a live `opencode serve`
// 1.18.13（OpenAPI /doc・docs/54 §契約）: every response is a {location, data}
// envelope, attempt statuses are pending|complete|failed|expired, and the Console
// connection surfaces as connections[].type == "credential" with the org name as its
// label（env 由来の OPENCODE_API_KEY は別 type）。

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
	// nullData makes the integration GET answer data:null（起動直後の実挙動）。
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
				d.notReady-- // プラグイン読み込み中: device メソッドがまだ生えていない
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
		// opencode が居ない環境（CI の一部）では start は 409 で止まるのが正しい。
		if code != http.StatusConflict {
			t.Fatalf("opencode 不在時は 409 のはず: %d %v", code, out)
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
	// device は mode=auto — Console にコード入力欄を出さない判断の根拠なので固定する。
	if out["mode"] != "auto" {
		t.Errorf("mode = %v, want auto", out["mode"])
	}
	if out["instructions"] == "" {
		t.Errorf("instructions が空")
	}
	// 手順①として見せるコード。URL のクエリが第一情報源。
	if out["user_code"] != "ABCD-EFGH" {
		t.Errorf("user_code = %v, want ABCD-EFGH", out["user_code"])
	}
}

func TestUserCodeExtraction(t *testing.T) {
	for _, tc := range []struct {
		name, url, instructions, want string
	}{
		{"URL のクエリ優先", "https://x/auth/device?user_code=WXYZ-1234", "Enter code: OTHER-CODE", "WXYZ-1234"},
		{"クエリが無ければ文言から", "https://x/auth/device", "Enter code: ABCD-EFGH", "ABCD-EFGH"},
		{"どちらにも無ければ空", "https://x/auth/device", "Approve in your browser", ""},
		{"壊れた URL でも落ちない", "://", "Enter code: ABCD-EFGH", "ABCD-EFGH"},
	} {
		if got := userCode(tc.url, tc.instructions); got != tc.want {
			t.Errorf("%s: userCode = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// 実機の再現（ref=err_91d98832）: 起動 85ms 後の click。/global/health は 200 でも
// device メソッドはまだ登録されておらず、素直に POST すると daemon が 500
// `OAuth method not found: opencode/device` を返す。メソッドが生えるまで待つこと。
func TestOAuthStartWaitsForPluginLoad(t *testing.T) {
	if !Available() {
		t.Skip("opencode CLI がこの環境に無い")
	}
	d := newFakeDaemon(t)
	d.mu.Lock()
	d.notReady = 3 // 3 回ぶん「まだ読み込み中」を返す
	d.mu.Unlock()
	orig := oauthReadyPoll
	oauthReadyPoll = time.Millisecond
	defer func() { oauthReadyPoll = orig }()

	code, out := doJSON(t, HandleOAuthStart, "POST", "/connections/opencode/oauth/start", "")
	if code != http.StatusOK {
		t.Fatalf("読み込み待ちの後に成功すべき: status=%d out=%v", code, out)
	}
	if out["flow_id"] != "att_1" {
		t.Errorf("flow_id = %v", out["flow_id"])
	}
}

// いつまでもメソッドが生えないとき（古い CLI 等）は、待ち続けず理由を返す。
func TestOAuthStartGivesUpWhenMethodNeverAppears(t *testing.T) {
	if !Available() {
		t.Skip("opencode CLI がこの環境に無い")
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
		t.Errorf("理由が伝わっていない: %v", out)
	}
}

// 起動直後は integration そのものが data:null。ここで「未接続」を確定させない。
func TestOAuthStatusIgnoresNullIntegration(t *testing.T) {
	d := newFakeDaemon(t)
	d.setConns([]map[string]any{{"type": "credential", "id": "con_1", "label": "acme-inc"}})
	if st := oauthStatus(); !st.connected {
		t.Fatal("前提: 接続済みを読めていない")
	}
	invalidateOAuthStatus()
	d.mu.Lock()
	d.nullData = true
	d.mu.Unlock()
	if st := oauthStatus(); !st.connected || st.label != "acme-inc" {
		t.Errorf("起動レース中に接続表示が消えた: %+v", st)
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
			t.Errorf("failed の理由が落ちている: %v", out["message"])
		}
	}
}

// 成立時にモデルカタログのキャッシュを落とすこと。ここが抜けると、ログイン直後の
// 起動モーダルが最大60秒ぶん古い（ログイン前の）モデル集合を出す。
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
		t.Error("ログイン成立後もモデルキャッシュが有効なまま")
	}
}

func TestOAuthPollRequiresFlowID(t *testing.T) {
	newFakeDaemon(t)
	if code, _ := doJSON(t, HandleOAuthPoll, "POST", "/connections/opencode/oauth/poll", `{}`); code != http.StatusBadRequest {
		t.Errorf("flow_id 無しは 400 のはず: %d", code)
	}
}

func TestOAuthCancelDeletesAttempt(t *testing.T) {
	d := newFakeDaemon(t)
	if code, _ := doJSON(t, HandleOAuthCancel, "POST", "/connections/opencode/oauth/cancel", `{"flow_id":"att_1"}`); code != http.StatusOK {
		t.Fatalf("cancel status=%d", code)
	}
	if got := d.deletes(); len(got) != 1 || got[0] != "/api/integration/attempt/att_1" {
		t.Errorf("attempt を消していない: %v", got)
	}
}

// 切断先は credential ID 指定の削除。v1 の `DELETE /auth/opencode` では消えない
// （あれは auth.json 側で、Console アカウントの資格情報は SQLite に載っている —
// 実機で叩いても新規プロセスから接続が見え続けた）。
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
		t.Errorf("資格情報の削除先が違う: %v", got)
	}
}

// 接続が無いのに押された場合（表示のズレ・二重クリック）は冪等に成功で返し、
// env 側の APIキー接続を巻き添えで消しにいかないこと。
func TestOAuthDisconnectIdempotentWithoutCredential(t *testing.T) {
	d := newFakeDaemon(t)
	d.setConns([]map[string]any{{"type": "env", "name": "OPENCODE_API_KEY"}})
	code, out := doJSON(t, HandleOAuthDisconnect, "DELETE", "/connections/opencode/oauth", "")
	if code != http.StatusOK || out["disconnected"] != "opencode" {
		t.Fatalf("status=%d out=%v", code, out)
	}
	if got := d.deletes(); len(got) != 0 {
		t.Errorf("消すものが無いのに DELETE した: %v", got)
	}
}

func TestOAuthStatusReadsCredentialConnection(t *testing.T) {
	d := newFakeDaemon(t)

	// env 接続だけ = APIキー方式。Console アカウントは未接続。
	d.setConns([]map[string]any{{"type": "env", "name": "OPENCODE_API_KEY"}})
	if st := oauthStatus(); st.connected || !st.known {
		t.Errorf("env のみで connected=%v known=%v", st.connected, st.known)
	}

	invalidateOAuthStatus()
	d.setConns([]map[string]any{
		{"type": "env", "name": "OPENCODE_API_KEY"},
		{"type": "credential", "id": "con_1", "label": "acme-inc"},
	})
	st := oauthStatus()
	if !st.connected || st.label != "acme-inc" {
		t.Errorf("credential 接続を拾えていない: %+v", st)
	}
}

// daemon が落ちている間に「未接続」へ落とさないこと（stale-if-error）。接続表示が
// 点滅すると、ユーザーはログインし直しに誘導されてしまう。
func TestOAuthStatusKeepsLastKnownWhenDaemonDown(t *testing.T) {
	d := newFakeDaemon(t)
	d.setConns([]map[string]any{{"type": "credential", "id": "con_1", "label": "acme-inc"}})
	if st := oauthStatus(); !st.connected {
		t.Fatal("前提: 接続済みを読めていない")
	}
	invalidateOAuthStatus()
	orig := oauthProbe
	oauthProbe = func() (string, bool) { return "", false }
	defer func() { oauthProbe = orig }()
	if st := oauthStatus(); !st.connected || st.label != "acme-inc" {
		t.Errorf("daemon 停止で接続表示が消えた: %+v", st)
	}
}

func TestOAuthStatusUnknownBeforeAnyRead(t *testing.T) {
	newFakeDaemon(t)
	orig := oauthProbe
	oauthProbe = func() (string, bool) { return "", false }
	defer func() { oauthProbe = orig }()
	st := oauthStatus()
	if st.connected || st.known {
		t.Errorf("一度も読めていないなら unknown のはず: %+v", st)
	}
}
