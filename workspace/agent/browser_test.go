package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeBrowserCall struct {
	Method    string
	SessionID string
	Params    map[string]any
}

type fakeBrowserCDP struct {
	mu        sync.Mutex
	calls     []fakeBrowserCall
	events    chan browserCDPEvent
	done      chan error
	closeOnce sync.Once
	fail      map[string]error
	// transient per-method failures: while count > 0 the method returns transientErr
	// and the counter is decremented. Lets a test model a call that fails a few times
	// then succeeds (e.g. Page.startScreencast during the initial navigation commit).
	transient    map[string]int
	transientErr error
}

func newFakeBrowserCDP() *fakeBrowserCDP {
	return &fakeBrowserCDP{events: make(chan browserCDPEvent, 32), done: make(chan error, 1), fail: make(map[string]error)}
}

func (f *fakeBrowserCDP) Call(_ context.Context, method string, params any, session string, result any) error {
	var values map[string]any
	if params != nil {
		b, _ := json.Marshal(params)
		_ = json.Unmarshal(b, &values)
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeBrowserCall{Method: method, SessionID: session, Params: values})
	if f.transient[method] > 0 {
		f.transient[method]--
		terr := f.transientErr
		f.mu.Unlock()
		return terr
	}
	err := f.fail[method]
	f.mu.Unlock()
	if err != nil {
		return err
	}
	var response any = map[string]any{}
	switch method {
	case "Target.createBrowserContext":
		response = map[string]any{"browserContextId": "context-1"}
	case "Target.createTarget":
		response = map[string]any{"targetId": "target-1"}
	case "Target.attachToTarget":
		response = map[string]any{"sessionId": "session-1"}
	case "Page.getFrameTree":
		response = map[string]any{"frameTree": map[string]any{"frame": map[string]any{"id": "frame-1"}}}
	case "Page.getLayoutMetrics":
		// A desktop page that does not fit a narrow pane: content is wider than
		// whatever viewport it was laid out in.
		response = map[string]any{"cssContentSize": map[string]any{"width": 1240, "height": 2000}}
	case "Runtime.evaluate":
		response = map[string]any{"result": map[string]any{"value": "コピー対象のテキスト"}}
	case "Page.getNavigationHistory":
		response = map[string]any{"currentIndex": 0, "entries": []map[string]any{{"id": 7, "url": "http://127.0.0.1:3000/", "title": "App"}}}
	}
	if result != nil {
		b, _ := json.Marshal(response)
		_ = json.Unmarshal(b, result)
	}
	return nil
}

func (f *fakeBrowserCDP) Events() <-chan browserCDPEvent { return f.events }
func (f *fakeBrowserCDP) Done() <-chan error             { return f.done }
func (f *fakeBrowserCDP) Close() error {
	f.closeOnce.Do(func() { f.done <- nil })
	return nil
}
func (f *fakeBrowserCDP) crash(err error) { f.closeOnce.Do(func() { f.done <- err }) }
func (f *fakeBrowserCDP) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	methods := make([]string, len(f.calls))
	for i := range f.calls {
		methods[i] = f.calls[i].Method
	}
	return methods
}
func (f *fakeBrowserCDP) last(method string) (fakeBrowserCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method == method {
			return f.calls[i], true
		}
	}
	return fakeBrowserCall{}, false
}

func fakeBrowserManager(cdp *fakeBrowserCDP) *browserManager {
	return newBrowserManager(browserManagerConfig{
		MaxPages: 1, DetachedGrace: time.Hour, ChromiumIdle: time.Hour,
		CommandTimeout: time.Second, FrameInterval: time.Millisecond,
		CDPFactory: func(context.Context) (browserCDP, error) { return cdp, nil },
	})
}

func TestBrowserTargetValidation(t *testing.T) {
	t.Setenv("AGENT_ADDR", ":8800")
	t.Setenv("AF_CP_BASE_URL", "https://cp.internal:8443")
	good, err := browserTargetURL(3000, "/users?q=日本語#row")
	if err != nil || good != "http://127.0.0.1:3000/users?q=日本語#row" {
		t.Fatalf("valid target = %q, %v", good, err)
	}
	for _, tc := range []struct {
		port int
		path string
	}{{0, "/"}, {65536, "/"}, {browserAgentPort, "/"}, {8800, "/"}, {3000, "relative"}, {3000, "//evil.test/x"}, {3000, `/\evil`}, {3000, "/\ncontrol"}} {
		if _, err := browserTargetURL(tc.port, tc.path); err == nil {
			t.Errorf("browserTargetURL(%d, %q) unexpectedly succeeded", tc.port, tc.path)
		}
	}
	for _, raw := range []string{
		"http://169.254.169.254/latest/meta-data", "http://127.0.0.1:7700/healthz", "http://127.0.0.1:8800/healthz",
		"http://host.docker.internal/admin", "https://cp.internal:8443/api", "file:///etc/passwd",
	} {
		if !forbiddenBrowserResource(raw) {
			t.Errorf("resource %q was not blocked", raw)
		}
	}
	if forbiddenBrowserResource("http://127.0.0.1:8080/api") || forbiddenBrowserResource("https://example.com/app.js") {
		t.Fatal("allowed loopback/external egress resource was blocked")
	}
}

func TestBrowserManagerCreateLimitDeleteAndCrash(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeBrowserManager(cdp)
	t.Cleanup(m.Close)
	req := browserCreateRequest{Port: 3000, Path: "/", Viewport: browserViewportRequest{Width: 900, Height: 600, DeviceScaleFactor: 1}}
	created, err := m.Create(req)
	if err != nil {
		t.Fatal(err)
	}
	if created.State != "starting" || created.Port != 3000 || created.ID == "" {
		t.Fatalf("unexpected create response: %+v", created)
	}
	if _, err := m.Create(req); !errors.Is(err, errBrowserPageLimit) {
		t.Fatalf("second Create error = %v, want page limit", err)
	}
	if _, ok := m.Get(created.ID); !ok {
		t.Fatal("created page missing from ownership map")
	}
	m.Delete(created.ID)
	m.Delete(created.ID) // idempotent
	if _, ok := m.Get(created.ID); ok {
		t.Fatal("deleted page remains in ownership map")
	}
	if call, ok := cdp.last("Target.disposeBrowserContext"); !waitFor(time.Second, func() bool { _, ok = cdp.last("Target.disposeBrowserContext"); return ok }) || call.Method == "" {
		call, _ = cdp.last("Target.disposeBrowserContext")
		if call.Method == "" {
			t.Fatal("browser context was not disposed")
		}
	}

	created, err = m.Create(req)
	if err != nil {
		t.Fatal(err)
	}
	cdp.crash(errors.New("boom"))
	if !waitFor(time.Second, func() bool { _, ok := m.Get(created.ID); return !ok }) {
		t.Fatal("crashed Chromium did not invalidate page ownership")
	}
}

func TestBrowserInputConversionAndLatestFrame(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeBrowserManager(cdp)
	t.Cleanup(m.Close)
	created, err := m.Create(browserCreateRequest{Port: 3000, Path: "/", Viewport: browserViewportRequest{Width: 900, Height: 600, DeviceScaleFactor: 1}})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	p := m.pages[created.ID]
	m.mu.Unlock()
	v := &browserViewer{page: p, control: make(chan browserOutbound, 16), done: make(chan struct{})}
	p.mu.Lock()
	p.viewer, p.visible = v, true
	p.mu.Unlock()

	v.handleControl([]byte(`{"type":"mouse","event":"down","x":10,"y":20,"button":"left","buttons":1,"modifiers":0,"clickCount":1}`))
	call, ok := cdp.last("Input.dispatchMouseEvent")
	if !ok || call.Params["type"] != "mousePressed" || call.Params["x"] != float64(10) {
		t.Fatalf("mouse conversion: %+v", call)
	}
	v.handleControl([]byte(`{"type":"key","event":"down","key":"a","code":"KeyA","modifiers":0,"repeat":false}`))
	call, ok = cdp.last("Input.dispatchKeyEvent")
	if !ok || call.Params["type"] != "keyDown" || call.Params["text"] != "a" {
		t.Fatalf("key conversion: %+v", call)
	}
	v.handleControl([]byte(`{"type":"text","text":"日本語"}`))
	call, ok = cdp.last("Input.insertText")
	if !ok || call.Params["text"] != "日本語" {
		t.Fatalf("IME conversion: %+v", call)
	}

	p.enqueueFrame([]byte("old"))
	p.enqueueFrame([]byte("new"))
	if got := <-p.latestFrame; !reflect.DeepEqual(got, []byte("new")) {
		t.Fatalf("latest frame = %q, want new", got)
	}

	v.handleControl([]byte(`{"type":"unknown"}`))
	select {
	case out := <-v.control:
		var msg map[string]any
		_ = json.Unmarshal(out.data, &msg)
		if msg["type"] != "protocol-error" {
			t.Fatalf("unknown control response = %s", out.data)
		}
	default:
		t.Fatal("unknown control did not return protocol-error")
	}
}

func TestBrowserScreencastAckPacesCaptureBeforeNextFrame(t *testing.T) {
	cdp := newFakeBrowserCDP()
	interval := 80 * time.Millisecond
	m := newBrowserManager(browserManagerConfig{
		MaxPages: 1, DetachedGrace: time.Hour, ChromiumIdle: time.Hour,
		CommandTimeout: time.Second, FrameInterval: interval, JPEGQuality: 70,
		CDPFactory: func(context.Context) (browserCDP, error) { return cdp, nil },
	})
	t.Cleanup(m.Close)
	created, err := m.Create(browserCreateRequest{Port: 3000, Path: "/", Viewport: browserViewportRequest{Width: 900, Height: 600, DeviceScaleFactor: 1}})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	p := m.pages[created.ID]
	m.mu.Unlock()
	v := &browserViewer{page: p, control: make(chan browserOutbound, 4), done: make(chan struct{})}
	p.mu.Lock()
	p.viewer, p.visible = v, true
	p.mu.Unlock()
	if err := p.startScreencast(); err != nil {
		t.Fatal(err)
	}
	if call, ok := cdp.last("Page.startScreencast"); !ok {
		t.Fatal("Page.startScreencast was not called")
	} else if _, exists := call.Params["everyNthFrame"]; exists {
		t.Fatal("screencast still relies on everyNthFrame instead of ACK pacing")
	}

	raw, _ := json.Marshal(map[string]any{
		"data": base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0xff}), "sessionId": 17,
	})
	started := time.Now()
	m.handleEvent(cdp, browserCDPEvent{Method: "Page.screencastFrame", SessionID: p.sessionID, Params: raw})
	time.Sleep(interval / 3)
	if _, ok := cdp.last("Page.screencastFrameAck"); ok {
		t.Fatal("screencast frame was acknowledged before the capture interval")
	}
	if !waitFor(3*interval, func() bool { _, ok := cdp.last("Page.screencastFrameAck"); return ok }) {
		t.Fatal("paced screencast ACK was not sent")
	}
	if elapsed := time.Since(started); elapsed < interval {
		t.Fatalf("screencast ACK after %s, want >= %s", elapsed, interval)
	}
	select {
	case frame := <-p.latestFrame:
		if !reflect.DeepEqual(frame, []byte{0xff, 0xd8, 0xff}) {
			t.Fatalf("decoded frame = %x", frame)
		}
	default:
		t.Fatal("screencast frame was not decoded")
	}
}

func TestBrowserScreencastAbsorbsInFlightBurst(t *testing.T) {
	cdp := newFakeBrowserCDP()
	interval := 20 * time.Millisecond
	m := newBrowserManager(browserManagerConfig{
		MaxPages: 1, DetachedGrace: time.Hour, ChromiumIdle: time.Hour,
		CommandTimeout: time.Second, FrameInterval: interval, JPEGQuality: 70,
		CDPFactory: func(context.Context) (browserCDP, error) { return cdp, nil },
	})
	t.Cleanup(m.Close)
	created, err := m.Create(browserCreateRequest{Port: 3000, Path: "/", Viewport: browserViewportRequest{Width: 900, Height: 600, DeviceScaleFactor: 1}})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	p := m.pages[created.ID]
	m.mu.Unlock()
	v := &browserViewer{page: p, control: make(chan browserOutbound, 4), done: make(chan struct{})}
	p.mu.Lock()
	p.viewer, p.visible = v, true
	p.mu.Unlock()
	if err := p.startScreencast(); err != nil {
		t.Fatal(err)
	}

	// Real Chromium keeps several frames in flight before it sees an ACK. A burst
	// far larger than the single-slot buffer must never invalidate the Page.
	frame := func(id int) browserCDPEvent {
		raw, _ := json.Marshal(map[string]any{
			"data": base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, byte(id)}), "sessionId": id,
		})
		return browserCDPEvent{Method: "Page.screencastFrame", SessionID: p.sessionID, Params: raw}
	}
	for i := 1; i <= 50; i++ {
		m.handleEvent(cdp, frame(i))
	}
	m.mu.Lock()
	stillOwned := m.pages[created.ID] == p
	m.mu.Unlock()
	if !stillOwned {
		t.Fatal("in-flight frame burst invalidated the Page (screencast backpressure regression)")
	}

	// A frame must have been decoded and paced-acked; the Page keeps streaming.
	select {
	case got := <-p.latestFrame:
		if len(got) < 2 || got[0] != 0xff || got[1] != 0xd8 {
			t.Fatalf("decoded frame is not JPEG: %x", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no frame was decoded from the burst")
	}
	if !waitFor(time.Second, func() bool { _, ok := cdp.last("Page.screencastFrameAck"); return ok }) {
		t.Fatal("no screencast frame was acknowledged")
	}

	// Latest-only convergence: after the burst settles, a final frame wins.
	fin, _ := json.Marshal(map[string]any{
		"data": base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0x99}), "sessionId": 777,
	})
	if !waitFor(2*time.Second, func() bool {
		m.handleEvent(cdp, browserCDPEvent{Method: "Page.screencastFrame", SessionID: p.sessionID, Params: fin})
		select {
		case got := <-p.latestFrame:
			return len(got) == 3 && got[2] == 0x99
		default:
			return false
		}
	}) {
		t.Fatal("latest-only buffer never converged to the final frame")
	}
}

// TestBrowserScreencastRetriesFrameNotActive covers the immediate-attach race: a
// viewer attaches right after POST returns, while the page target is still swapping
// from about:blank to the navigated document, so the first Page.startScreencast
// calls are rejected with "Not attached to an active page". startScreencast must
// retry that transient rejection until the frame is live rather than surfacing it as
// a fatal error that crashes the whole pane with zero frames.
func TestBrowserScreencastRetriesFrameNotActive(t *testing.T) {
	cdp := newFakeBrowserCDP()
	cdp.transient = map[string]int{"Page.startScreencast": 3}
	cdp.transientErr = fmt.Errorf("CDP Page.startScreencast (-32000): Not attached to an active page")
	m := fakeBrowserManager(cdp)
	t.Cleanup(m.Close)
	created, err := m.Create(browserCreateRequest{Port: 3000, Path: "/", Viewport: browserViewportRequest{Width: 900, Height: 600, DeviceScaleFactor: 1}})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	p := m.pages[created.ID]
	m.mu.Unlock()
	p.mu.Lock()
	p.visible = true
	p.mu.Unlock()

	if err := p.startScreencast(); err != nil {
		t.Fatalf("startScreencast should retry the transient not-active rejection, got %v", err)
	}
	p.castMu.Lock()
	casting := p.casting
	p.castMu.Unlock()
	if !casting {
		t.Fatal("screencast did not arm after the transient rejections cleared")
	}
	// It should have taken more than one attempt (3 failures + 1 success).
	n := 0
	for _, mth := range cdp.methods() {
		if mth == "Page.startScreencast" {
			n++
		}
	}
	if n < 4 {
		t.Fatalf("expected startScreencast to be retried (>=4 calls), got %d", n)
	}

	// A non-transient error must NOT be retried: it returns immediately.
	p.stopScreencast()
	cdp.mu.Lock()
	cdp.fail = map[string]error{"Page.startScreencast": fmt.Errorf("CDP Page.startScreencast (-32000): some other failure")}
	cdp.calls = nil
	cdp.mu.Unlock()
	if err := p.startScreencast(); err == nil {
		t.Fatal("a non-transient startScreencast error should be returned, not swallowed")
	}
	n = 0
	for _, mth := range cdp.methods() {
		if mth == "Page.startScreencast" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("non-transient error must not be retried, got %d calls", n)
	}
}

func TestBrowserViewportSkipsRedundantRestart(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeBrowserManager(cdp)
	t.Cleanup(m.Close)
	created, err := m.Create(browserCreateRequest{Port: 3000, Path: "/", Viewport: browserViewportRequest{Width: 900, Height: 600, DeviceScaleFactor: 1}})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	p := m.pages[created.ID]
	m.mu.Unlock()
	v := &browserViewer{page: p, control: make(chan browserOutbound, 8), done: make(chan struct{})}
	p.mu.Lock()
	p.viewer, p.visible = v, true
	p.mu.Unlock()
	if err := p.startScreencast(); err != nil {
		t.Fatal(err)
	}

	countStops := func() int {
		n := 0
		for _, method := range cdp.methods() {
			if method == "Page.stopScreencast" {
				n++
			}
		}
		return n
	}
	before := countStops()
	// Identical viewport (Console's post-attach resend) must not stop/start the cast.
	v.handleControl([]byte(`{"type":"viewport","width":900,"height":600}`))
	if got := countStops(); got != before {
		t.Fatalf("identical viewport restarted the screencast (%d -> %d stops)", before, got)
	}
	// A genuine resize must restart the cast so maxWidth/maxHeight track the change.
	v.handleControl([]byte(`{"type":"viewport","width":1000,"height":700}`))
	if !waitFor(time.Second, func() bool { return countStops() > before }) {
		t.Fatal("a real viewport resize did not restart the screencast")
	}
}

func TestBrowserSingleViewerAndDetachedExpiry(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := newBrowserManager(browserManagerConfig{
		MaxPages: 1, DetachedGrace: 100 * time.Millisecond, ChromiumIdle: time.Hour,
		CommandTimeout: time.Second, FrameInterval: time.Millisecond,
		CDPFactory: func(context.Context) (browserCDP, error) { return cdp, nil },
	})
	t.Cleanup(m.Close)
	created, err := m.Create(browserCreateRequest{Port: 3000, Path: "/", Viewport: browserViewportRequest{Width: 900, Height: 600, DeviceScaleFactor: 1}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.reserve(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.reserve(created.ID); !errors.Is(err, errBrowserAttached) {
		t.Fatalf("second viewer reservation = %v, want already attached", err)
	}
	v := &browserViewer{page: p, control: make(chan browserOutbound, 4), done: make(chan struct{})}
	if !m.attach(p, v) {
		t.Fatal("reserved viewer could not attach")
	}
	if _, err := m.reserve(created.ID); !errors.Is(err, errBrowserAttached) {
		t.Fatalf("attached page reservation = %v, want already attached", err)
	}
	m.detach(p, v)
	if !waitFor(time.Second, func() bool { _, ok := m.Get(created.ID); return !ok }) {
		t.Fatal("detached page was not destroyed after its grace period")
	}
}

func TestBrowserNavigationPolicyAtFetchBoundary(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeBrowserManager(cdp)
	t.Cleanup(m.Close)
	created, err := m.Create(browserCreateRequest{Port: 3000, Path: "/", Viewport: browserViewportRequest{Width: 900, Height: 600, DeviceScaleFactor: 1}})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	p := m.pages[created.ID]
	m.mu.Unlock()

	m.handleRequestPaused(cdp, p, json.RawMessage(`{"requestId":"r1","frameId":"frame-1","resourceType":"Document","request":{"url":"https://example.com/escape"}}`))
	if call, ok := cdp.last("Fetch.failRequest"); !ok || call.Params["requestId"] != "r1" {
		t.Fatalf("external top navigation was not failed: %+v", call)
	}
	m.handleRequestPaused(cdp, p, json.RawMessage(`{"requestId":"r2","frameId":"sub","resourceType":"Script","request":{"url":"http://127.0.0.1:8080/app.js"}}`))
	if call, ok := cdp.last("Fetch.continueRequest"); !ok || call.Params["requestId"] != "r2" {
		t.Fatalf("loopback subresource was not continued: %+v", call)
	}
	m.handleRequestedNavigation(cdp, p, json.RawMessage(`{"frameId":"frame-1","url":"data:text/html,escape"}`))
	if _, ok := cdp.last("Page.stopLoading"); !ok {
		t.Fatal("non-network external top navigation was not stopped")
	}
}

func TestBrowserRESTRoutes(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeBrowserManager(cdp)
	previous := workspaceBrowserManager
	workspaceBrowserManager = m
	t.Cleanup(func() {
		workspaceBrowserManager = previous
		m.Close()
	})
	h := buildMux()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := do(http.MethodPost, "/browser/pages", `{"port":3000,"path":"/","viewport":{"width":900,"height":600,"deviceScaleFactor":1}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /browser/pages: %d %s", w.Code, w.Body.String())
	}
	var created browserPageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("create response: %s (err=%v)", w.Body.String(), err)
	}
	if w := do(http.MethodGet, "/browser/pages/"+created.ID, ""); w.Code != http.StatusOK {
		t.Fatalf("GET page: %d %s", w.Code, w.Body.String())
	}
	for i := 0; i < 2; i++ {
		if w := do(http.MethodDelete, "/browser/pages/"+created.ID, ""); w.Code != http.StatusNoContent {
			t.Fatalf("DELETE page #%d: %d %s", i+1, w.Code, w.Body.String())
		}
	}
}

func TestBrowserWebSocketProtocol(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeBrowserManager(cdp)
	previous := workspaceBrowserManager
	workspaceBrowserManager = m
	server := httptest.NewServer(buildMux())
	t.Cleanup(func() {
		server.Close()
		workspaceBrowserManager = previous
		m.Close()
	})
	created, err := m.Create(browserCreateRequest{Port: 3000, Path: "/", Viewport: browserViewportRequest{Width: 900, Height: 600, DeviceScaleFactor: 1}})
	if err != nil {
		t.Fatal(err)
	}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/browser?id=" + created.ID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	mt, payload, err := conn.ReadMessage()
	if err != nil || mt != websocket.TextMessage {
		t.Fatalf("first browser message: type=%d payload=%s err=%v", mt, payload, err)
	}
	var ready map[string]any
	if json.Unmarshal(payload, &ready) != nil || ready["type"] != "ready" || ready["version"] != float64(1) {
		t.Fatalf("first browser message is not v1 ready: %s", payload)
	}
	if _, resp, err := websocket.DefaultDialer.Dial(wsURL, nil); err == nil || resp == nil || resp.StatusCode != http.StatusConflict {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Fatalf("second viewer handshake: response=%v err=%v", resp, err)
	} else {
		_ = resp.Body.Close()
	}
	if err := conn.WriteJSON(map[string]any{"type": "key", "event": "down", "key": "a", "code": "KeyA", "modifiers": 0, "repeat": false}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(time.Second, func() bool { _, ok := cdp.last("Input.dispatchKeyEvent"); return ok }) {
		t.Fatal("WebSocket key event did not reach CDP")
	}
	if err := conn.WriteJSON(map[string]any{"type": "visibility", "visible": false}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(time.Second, func() bool { _, ok := cdp.last("Page.stopScreencast"); return ok }) {
		t.Fatal("visibility=false did not stop screencast")
	}
}

func TestBrowserChromiumIntegration(t *testing.T) {
	bin, err := findChromiumBinary()
	if err != nil {
		t.Skip("Chromium is not installed in this test environment")
	}
	cdpFactory := browserCDPFactory(launchPipeCDP)
	if strings.Contains(bin, "/.cache/ms-playwright/") {
		cdpFactory = launchPipeCDPWithoutSandboxForTest
	}
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><title>Browser smoke</title><main>日本語</main><input id="ime" autofocus>`))
	}))
	defer app.Close()
	u, err := url.Parse(app.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	m := newBrowserManager(browserManagerConfig{
		MaxPages: 1, DetachedGrace: time.Minute, ChromiumIdle: time.Minute,
		CommandTimeout: 10 * time.Second, FrameInterval: time.Second / 12, JPEGQuality: 70,
		CDPFactory: cdpFactory,
	})
	defer m.Close()
	created, err := m.Create(browserCreateRequest{
		Port: port, Path: "/", Viewport: browserViewportRequest{Width: 800, Height: 600, DeviceScaleFactor: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(10*time.Second, func() bool {
		page, ok := m.Get(created.ID)
		return ok && page.State == "ready" && page.Title == "Browser smoke"
	}) {
		page, _ := m.Get(created.ID)
		t.Fatalf("Chromium page did not become ready: %+v", page)
	}
	m.mu.Lock()
	p := m.pages[created.ID]
	cdp := m.cdp
	m.mu.Unlock()
	v := &browserViewer{page: p, control: make(chan browserOutbound, 4), done: make(chan struct{})}
	p.mu.Lock()
	p.viewer, p.visible = v, true
	p.mu.Unlock()
	if err := p.startScreencast(); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-p.latestFrame:
		if len(frame) < 2 || frame[0] != 0xff || frame[1] != 0xd8 {
			t.Fatalf("screencast frame is not JPEG: %x", frame[:min(len(frame), 8)])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Chromium did not emit a screencast frame")
	}
	if err := m.call(cdp, p.sessionID, "Runtime.evaluate", map[string]any{"expression": `document.querySelector('#ime').focus()`}, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.call(cdp, p.sessionID, "Input.insertText", map[string]any{"text": "日本語"}, nil); err != nil {
		t.Fatal(err)
	}
	var evaluated struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := m.call(cdp, p.sessionID, "Runtime.evaluate", map[string]any{
		"expression": `document.querySelector('#ime').value`, "returnByValue": true,
	}, &evaluated); err != nil || evaluated.Result.Value != "日本語" {
		t.Fatalf("IME input round-trip = %q (err=%v)", evaluated.Result.Value, err)
	}
}

func TestBrowserTwoPageCaptureIntegration(t *testing.T) {
	bin, err := findChromiumBinary()
	if err != nil {
		t.Skip("Chromium is not installed in this test environment")
	}
	if err := runBrowserSmoke(!strings.Contains(bin, "/.cache/ms-playwright/")); err != nil {
		t.Fatal(err)
	}
}

func waitFor(timeout time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return fn()
}
