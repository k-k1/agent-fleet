package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type browserTestRuntime struct {
	endpoint string
	token    string
	state    string
}

func (r browserTestRuntime) Start(context.Context) error { return nil }
func (r browserTestRuntime) Stop(context.Context) error  { return nil }
func (r browserTestRuntime) State(context.Context) string {
	return r.state
}
func (r browserTestRuntime) Endpoint() string { return r.endpoint }
func (r browserTestRuntime) Token() string    { return r.token }
func (r browserTestRuntime) Name() string     { return "browser-test" }

type browserTestEnv struct {
	mgr       *manager
	mux       *http.ServeMux
	store     *sqlStore
	tenant    Tenant
	workspace Workspace
}

func newBrowserTestEnv(t *testing.T, rt Runtime) browserTestEnv {
	t.Helper()
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	tenant, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatalf("default tenant: %v", err)
	}
	ident, err := st.UpsertIdentity(ctx, "", "browser-user", "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	membership, err := st.EnsureMembership(ctx, ident.ID, tenant.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	workspace := Workspace{ID: "ws-browser", TenantID: tenant.ID, MembershipID: membership.ID,
		ContainerName: "browser", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mgr := &manager{
		rts:             map[string]cachedRT{membership.ID: {rt: rt, ws: workspace}},
		store:           st,
		authMode:        "dev",
		devUser:         "browser-user",
		provisionMode:   "auto",
		defaultTenantID: tenant.ID,
		conns:           newConnRegistry(),
	}
	mux := http.NewServeMux()
	registerBrowserRoutes(mux, config{mgr: mgr})
	return browserTestEnv{mgr: mgr, mux: mux, store: st, tenant: tenant, workspace: workspace}
}

func TestBrowserRESTRelayPreservesContractAndAgentBearer(t *testing.T) {
	type seenRequest struct {
		method, path, query, body, auth, cookie, tenant string
	}
	seen := make(chan seenRequest, 3)
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen <- seenRequest{
			method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(body),
			auth: r.Header.Get("Authorization"), cookie: r.Header.Get("Cookie"),
			tenant: r.Header.Get("X-AF-Tenant"),
		}
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"page-1","state":"starting"}`))
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"page-1","state":"ready"}`))
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer agent.Close()

	env := newBrowserTestEnv(t, browserTestRuntime{endpoint: agent.URL, token: "agent-secret", state: "running"})
	postBody := `{"port":3000,"path":"/","viewport":{"width":900,"height":600,"deviceScaleFactor":1}}`
	req := httptest.NewRequest(http.MethodPost, "/api/browser/pages?tenant=default&probe=1", bytes.NewBufferString(postBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer console-token")
	req.Header.Set("Cookie", "session=console-secret")
	req.Header.Set("X-AF-Tenant", "default")
	w := httptest.NewRecorder()
	env.mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated || w.Body.String() != `{"id":"page-1","state":"starting"}` {
		t.Fatalf("POST relay = %d %q", w.Code, w.Body.String())
	}

	for _, tc := range []struct {
		method string
		status int
	}{
		{http.MethodGet, http.StatusOK},
		{http.MethodDelete, http.StatusNoContent},
	} {
		w = httptest.NewRecorder()
		env.mux.ServeHTTP(w, httptest.NewRequest(tc.method, "/api/browser/pages/page-1?tenant=default", nil))
		if w.Code != tc.status {
			t.Fatalf("%s relay status = %d body=%q", tc.method, w.Code, w.Body.String())
		}
	}

	got := <-seen
	if got.method != http.MethodPost || got.path != "/browser/pages" || got.query != "probe=1" || got.body != postBody {
		t.Fatalf("Agent POST = %+v", got)
	}
	if got.auth != "Bearer agent-secret" || got.cookie != "" || got.tenant != "" {
		t.Fatalf("Agent trust headers = auth %q cookie %q tenant %q", got.auth, got.cookie, got.tenant)
	}
	got = <-seen
	if got.method != http.MethodGet || got.path != "/browser/pages/page-1" || got.query != "" {
		t.Fatalf("Agent GET = %+v", got)
	}
	got = <-seen
	if got.method != http.MethodDelete || got.path != "/browser/pages/page-1" {
		t.Fatalf("Agent DELETE = %+v", got)
	}
}

func TestBrowserRoutesFailFastForRuntimeState(t *testing.T) {
	for _, tc := range []struct {
		state, code string
	}{
		{"starting", "workspace_starting"},
		{"stopped", "workspace_stopped"},
		{"none", "workspace_stopped"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			env := newBrowserTestEnv(t, browserTestRuntime{endpoint: "http://127.0.0.1:1", state: tc.state})
			for _, request := range []*http.Request{
				httptest.NewRequest(http.MethodPost, "/api/browser/pages", bytes.NewBufferString(`{}`)),
				httptest.NewRequest(http.MethodGet, "/ws/browser?id=page-1", nil),
			} {
				w := httptest.NewRecorder()
				env.mux.ServeHTTP(w, request)
				if w.Code != http.StatusConflict {
					t.Fatalf("%s status = %d body=%s", request.URL.Path, w.Code, w.Body.String())
				}
				var body struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != tc.code {
					t.Fatalf("%s error = %+v decode=%v", request.URL.Path, body, err)
				}
			}
		})
	}
}

func TestBrowserRoutesRejectOutsideMembership(t *testing.T) {
	var agentCalls atomic.Int32
	agent := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		agentCalls.Add(1)
	}))
	defer agent.Close()
	env := newBrowserTestEnv(t, browserTestRuntime{endpoint: agent.URL, state: "running"})
	if _, err := env.store.CreateTenant(context.Background(), "outside", "Outside"); err != nil {
		t.Fatalf("create outside tenant: %v", err)
	}

	w := httptest.NewRecorder()
	env.mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/browser/pages/page-1?tenant=outside", nil))
	if w.Code != http.StatusForbidden || agentCalls.Load() != 0 {
		t.Fatalf("outside membership = status %d calls %d body=%s", w.Code, agentCalls.Load(), w.Body.String())
	}
}

func TestBrowserWebSocketRelayAndVisibilityTracking(t *testing.T) {
	agentInputs := make(chan browserMessage, 4)
	agentConnected := make(chan struct{})
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/browser" || r.URL.Query().Get("id") != "page-1" || r.URL.Query().Get("tenant") != "" {
			t.Errorf("Agent WebSocket target = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer agent-secret" {
			t.Errorf("Agent Authorization = %q", got)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Agent upgrade: %v", err)
			return
		}
		defer conn.Close()
		close(agentConnected)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ready","version":1}`)); err != nil {
			return
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte{0xff, 0xd8, 0xff}); err != nil {
			return
		}
		for {
			typ, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			agentInputs <- browserMessage{typ: typ, data: data}
		}
	}))
	defer agent.Close()

	env := newBrowserTestEnv(t, browserTestRuntime{endpoint: agent.URL, token: "agent-secret", state: "running"})
	cp := httptest.NewServer(env.mux)
	defer cp.Close()
	wsURL := "ws" + cp.URL[len("http"):] + "/ws/browser?id=page-1&tenant=default"
	client, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("CP dial: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("CP dial: %v", err)
	}
	defer client.Close()
	<-agentConnected

	typ, data, err := client.ReadMessage()
	if err != nil || typ != websocket.TextMessage || string(data) != `{"type":"ready","version":1}` {
		t.Fatalf("ready relay = type %d data %q err %v", typ, data, err)
	}
	typ, data, err = client.ReadMessage()
	if err != nil || typ != websocket.BinaryMessage || !bytes.Equal(data, []byte{0xff, 0xd8, 0xff}) {
		t.Fatalf("frame relay = type %d data %v err %v", typ, data, err)
	}
	waitBrowserConns(t, env.mgr.conns, env.workspace.ID, 1)

	hidden := []byte(`{"type":"visibility","visible":false}`)
	if err := client.WriteMessage(websocket.TextMessage, hidden); err != nil {
		t.Fatalf("send hidden: %v", err)
	}
	assertAgentInput(t, agentInputs, websocket.TextMessage, hidden)
	waitBrowserConns(t, env.mgr.conns, env.workspace.ID, 0)

	shown := []byte(`{"type":"visibility","visible":true}`)
	if err := client.WriteMessage(websocket.TextMessage, shown); err != nil {
		t.Fatalf("send shown: %v", err)
	}
	assertAgentInput(t, agentInputs, websocket.TextMessage, shown)
	waitBrowserConns(t, env.mgr.conns, env.workspace.ID, 1)

	mouse := []byte(`{"type":"mouse","event":"move","x":10,"y":20}`)
	if err := client.WriteMessage(websocket.TextMessage, mouse); err != nil {
		t.Fatalf("send mouse: %v", err)
	}
	assertAgentInput(t, agentInputs, websocket.TextMessage, mouse)
	if err := client.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}
	waitBrowserConns(t, env.mgr.conns, env.workspace.ID, 0)
}

func TestBrowserWebSocketPreservesAgentHandshakeError(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer agent-secret" {
			t.Errorf("Agent Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("WWW-Authenticate", `Bearer realm="agent-secret"`)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"browser_not_found"}}`))
	}))
	defer agent.Close()
	env := newBrowserTestEnv(t, browserTestRuntime{endpoint: agent.URL, token: "agent-secret", state: "running"})
	cp := httptest.NewServer(env.mux)
	defer cp.Close()

	wsURL := "ws" + cp.URL[len("http"):] + "/ws/browser?id=bad&tenant=default"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil || resp == nil {
		t.Fatalf("bad id dial = err %v response %v", err, resp)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNotFound || string(body) != `{"error":{"code":"browser_not_found"}}` {
		t.Fatalf("handshake relay = %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("WWW-Authenticate"); got != "" {
		t.Fatalf("Agent challenge leaked to Console: %q", got)
	}
}

func TestBrowserOutboundKeepsLatestFrameAndBoundsControls(t *testing.T) {
	q := newBrowserOutbound()
	if err := q.enqueue(websocket.BinaryMessage, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := q.enqueue(websocket.BinaryMessage, []byte("latest")); err != nil {
		t.Fatal(err)
	}
	if got := <-q.frame; got.typ != websocket.BinaryMessage || string(got.data) != "latest" {
		t.Fatalf("latest frame slot = type %d data %q", got.typ, got.data)
	}

	for i := 0; i < browserControlQueueSize; i++ {
		if err := q.enqueue(websocket.TextMessage, []byte("state")); err != nil {
			t.Fatalf("enqueue control %d: %v", i, err)
		}
	}
	if err := q.enqueue(websocket.TextMessage, []byte("overflow")); !errors.Is(err, errBrowserControlBackpressure) {
		t.Fatalf("control overflow = %v", err)
	}

	tooLarge := make([]byte, browserMaxAgentControlBytes+1)
	q = newBrowserOutbound()
	if err := q.enqueue(websocket.TextMessage, tooLarge); !errors.Is(err, errBrowserControlTooLarge) {
		t.Fatalf("oversized control = %v", err)
	}
}

type slowBrowserWriter struct {
	started   chan struct{}
	release   chan struct{}
	writes    chan browserMessage
	cancel    context.CancelFunc
	writeNum  atomic.Int32
	deadlines atomic.Int32
}

func (w *slowBrowserWriter) SetWriteDeadline(time.Time) error {
	w.deadlines.Add(1)
	return nil
}

func (w *slowBrowserWriter) WriteMessage(typ int, data []byte) error {
	n := w.writeNum.Add(1)
	if n == 1 {
		close(w.started)
		<-w.release
	}
	w.writes <- browserMessage{typ: typ, data: append([]byte(nil), data...)}
	if n == 3 {
		w.cancel()
	}
	return nil
}

func TestBrowserWriterDropsIntermediateFramesForSlowClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := newBrowserOutbound()
	if err := out.enqueue(websocket.BinaryMessage, []byte("first")); err != nil {
		t.Fatal(err)
	}
	writer := &slowBrowserWriter{
		started: make(chan struct{}), release: make(chan struct{}),
		writes: make(chan browserMessage, 3), cancel: cancel,
	}
	done := make(chan error, 1)
	go func() { done <- writeBrowserClient(ctx, writer, out) }()
	<-writer.started

	// While the first write is blocked, controls stay reliable and repeated
	// frames collapse to the newest one in the capacity-one slot.
	if err := out.enqueue(websocket.BinaryMessage, []byte("intermediate")); err != nil {
		t.Fatal(err)
	}
	if err := out.enqueue(websocket.BinaryMessage, []byte("latest")); err != nil {
		t.Fatal(err)
	}
	if err := out.enqueue(websocket.TextMessage, []byte(`{"type":"state","state":"ready"}`)); err != nil {
		t.Fatal(err)
	}
	close(writer.release)

	want := []browserMessage{
		{typ: websocket.BinaryMessage, data: []byte("first")},
		{typ: websocket.TextMessage, data: []byte(`{"type":"state","state":"ready"}`)},
		{typ: websocket.BinaryMessage, data: []byte("latest")},
	}
	for i, expected := range want {
		select {
		case got := <-writer.writes:
			if got.typ != expected.typ || !bytes.Equal(got.data, expected.data) {
				t.Fatalf("write %d = type %d data %q, want type %d data %q", i, got.typ, got.data, expected.typ, expected.data)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for write %d", i)
		}
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("writer stop = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not stop after cancellation")
	}
	if writer.deadlines.Load() != 3 {
		t.Fatalf("write deadlines = %d, want 3", writer.deadlines.Load())
	}
}

func assertAgentInput(t *testing.T, inputs <-chan browserMessage, wantType int, wantData []byte) {
	t.Helper()
	select {
	case got := <-inputs:
		if got.typ != wantType || !bytes.Equal(got.data, wantData) {
			t.Fatalf("Agent input = type %d data %q, want type %d data %q", got.typ, got.data, wantType, wantData)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Agent input")
	}
}

func waitBrowserConns(t *testing.T, registry *connRegistry, wsID string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, _, _ := registry.snapshot(wsID)
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	got, _, _ := registry.snapshot(wsID)
	t.Fatalf("browser connections = %d, want %d", got, want)
}

func TestBrowserAgentWebSocketURLUsesOnlyBrowserID(t *testing.T) {
	got, err := browserAgentWebSocketURL("https://agent.internal:7700/base?secret=old", "page id")
	if err != nil {
		t.Fatal(err)
	}
	want := url.URL{Scheme: "wss", Host: "agent.internal:7700", Path: "/ws/browser", RawQuery: "id=page+id"}
	if got != want.String() {
		t.Fatalf("Agent WebSocket URL = %q, want %q", got, want.String())
	}
}

func TestBrowserAttachmentRoutesUseDedicatedAgentNamespace(t *testing.T) {
	type seenRequest struct{ method, path, query, body string }
	seen := make(chan seenRequest, 8)
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen <- seenRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(body)}
		if r.URL.Path == "/ws/browser-attachments" {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ready","version":1}`))
			_, _, _ = conn.ReadMessage()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer agent.Close()
	env := newBrowserTestEnv(t, browserTestRuntime{endpoint: agent.URL, token: "agent-secret", state: "running"})

	for _, raw := range []string{
		"/api/browser/attach-targets?tenant=default&port=9222",
		"/api/browser/attachments/ba_0123456789abcdef0123456789abcdef?tenant=default",
	} {
		w := httptest.NewRecorder()
		env.mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, raw, nil))
		if w.Code != http.StatusOK || w.Body.String() != `{"ok":true}` {
			t.Fatalf("attachment REST relay %s = %d %q", raw, w.Code, w.Body.String())
		}
	}
	got := <-seen
	if got.path != "/browser/attach-targets" || got.query != "port=9222" {
		t.Fatalf("Agent discovery target = %+v", got)
	}
	got = <-seen
	if got.path != "/browser/attachments/ba_0123456789abcdef0123456789abcdef" || got.query != "" {
		t.Fatalf("Agent status target = %+v", got)
	}
	controlBody := `{"controlMode":"locked"}`
	controlReq := httptest.NewRequest(http.MethodPost,
		"/api/browser/attachments/ba_0123456789abcdef0123456789abcdef/control-mode?tenant=default",
		bytes.NewBufferString(controlBody))
	controlReq.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.mux.ServeHTTP(w, controlReq)
	if w.Code != http.StatusOK || w.Body.String() != `{"ok":true}` {
		t.Fatalf("control mode relay = %d %q", w.Code, w.Body.String())
	}
	got = <-seen
	if got.method != http.MethodPost || got.path != "/browser/attachments/ba_0123456789abcdef0123456789abcdef/control-mode" ||
		got.query != "" || got.body != controlBody {
		t.Fatalf("Agent control mode target = %+v", got)
	}

	cp := httptest.NewServer(env.mux)
	defer cp.Close()
	wsURL := "ws" + cp.URL[len("http"):] + "/ws/browser-attachments?id=ba_0123456789abcdef0123456789abcdef&tenant=default"
	client, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("attachment CP dial: %v status=%d", err, resp.StatusCode)
		}
		t.Fatal(err)
	}
	defer client.Close()
	_, data, err := client.ReadMessage()
	if err != nil || string(data) != `{"type":"ready","version":1}` {
		t.Fatalf("attachment ready relay = %q err=%v", data, err)
	}
	got = <-seen
	if got.path != "/ws/browser-attachments" || got.query != "id=ba_0123456789abcdef0123456789abcdef" {
		t.Fatalf("Agent attachment socket target = %+v", got)
	}
}

func TestBrowserAgentAttachmentWebSocketURLUsesOpaqueIDOnly(t *testing.T) {
	got, err := browserAgentAttachmentWebSocketURL("https://agent.internal:7700/base?secret=old", "ba_id")
	if err != nil {
		t.Fatal(err)
	}
	want := url.URL{Scheme: "wss", Host: "agent.internal:7700", Path: "/ws/browser-attachments", RawQuery: "id=ba_id"}
	if got != want.String() {
		t.Fatalf("Agent attachment WebSocket URL = %q, want %q", got, want.String())
	}
}
