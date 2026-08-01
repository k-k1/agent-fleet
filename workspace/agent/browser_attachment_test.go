package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func fakeAttachmentManager(cdp *fakeBrowserCDP, ttl time.Duration) *browserAttachmentManager {
	if ttl == 0 {
		ttl = time.Hour
	}
	return newBrowserAttachmentManager(browserAttachmentManagerConfig{
		UnviewedTTL: ttl, ViewerGrace: time.Hour, HandoffTTL: time.Hour,
		DiscoveryTimeout: time.Second, CommandTimeout: time.Second, FrameInterval: time.Millisecond,
		Discover: func(int, time.Duration) (cdpDiscovery, error) {
			return cdpDiscovery{
				DebuggerURL: "ws://untrusted.invalid/devtools/browser/raw-secret",
				Targets:     []browserAttachTarget{{TargetID: "target-1", Type: "page", Title: "External", URL: "https://example.invalid/edit?secret=redacted"}},
			}, nil
		},
		Dial: func(context.Context, int, string) (browserCDP, error) { return cdp, nil },
	})
}

func createFakeAttachment(t *testing.T, m *browserAttachmentManager) browserAttachmentResponse {
	t.Helper()
	resp, err := m.Create(browserAttachmentCreateRequest{
		Port: 9222, TargetID: "target-1",
		Viewport: browserViewportRequest{Width: 1280, Height: 900, DeviceScaleFactor: 1},
	})
	if err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	return resp
}

func TestBrowserAttachmentLifecycleDoesNotCloseExternalTarget(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	resp := createFakeAttachment(t, m)
	if !validAttachmentID(resp.ID) || resp.OpenURL != "/open/browser-attachment/"+resp.ID || resp.State != attachmentStateAttached {
		t.Fatalf("attachment response = %+v", resp)
	}
	wire, _ := json.Marshal(resp)
	if strings.Contains(string(wire), "9222") || strings.Contains(string(wire), "target-1") || strings.Contains(string(wire), "devtools/browser") {
		t.Fatalf("raw CDP coordinates leaked in response: %s", wire)
	}
	if _, err := m.Create(browserAttachmentCreateRequest{
		Port: 9222, TargetID: "target-1",
		Viewport: browserViewportRequest{Width: 800, Height: 600, DeviceScaleFactor: 1},
	}); asAttachmentAPIError(err).Code != "browser_already_attached" {
		t.Fatalf("duplicate target error = %v", err)
	}
	m.Delete(resp.ID)
	m.Delete(resp.ID) // idempotent
	methods := cdp.methods()
	if !containsBrowserMethod(methods, "Target.detachFromTarget") {
		t.Fatalf("detach command missing: %v", methods)
	}
	for _, forbidden := range []string{"Target.closeTarget", "Target.disposeBrowserContext", "Browser.close"} {
		if containsBrowserMethod(methods, forbidden) {
			t.Fatalf("detach sent owner-destructive %s: %v", forbidden, methods)
		}
	}
	if _, err := m.Get(resp.ID); asAttachmentAPIError(err).Code != "browser_attachment_not_found" {
		t.Fatalf("status after detach = %v", err)
	}
}

func TestBrowserAttachmentControlModesAndNoNavigateMessage(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	resp := createFakeAttachment(t, m)
	a, err := m.lookupActive(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	v := &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 8), done: make(chan struct{})}

	v.handleControl([]byte(`{"type":"mouse","event":"move","x":1,"y":1,"button":"none"}`))
	if containsBrowserMethod(cdp.methods(), "Input.dispatchMouseEvent") {
		t.Fatal("view-only mode forwarded input")
	}
	if _, err := m.UpdateHandoff(resp.ID, browserAttachmentHandoffRequest{
		Message: "ownerを停止してから操作してください", ControlMode: attachmentControlUser,
	}); err != nil {
		t.Fatal(err)
	}
	v.handleControl([]byte(`{"type":"reload"}`))
	if !containsBrowserMethod(cdp.methods(), "Page.reload") {
		t.Fatal("user-control mode did not forward allowed reload")
	}
	v.handleControl([]byte(`{"type":"navigate","path":"/forbidden"}`))
	if containsBrowserMethod(cdp.methods(), "Page.navigate") {
		t.Fatal("attachment namespace forwarded navigate")
	}
	if _, err := m.SetHandoffResult(resp.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	status, err := m.Get(resp.ID)
	if err != nil || status.ControlMode != attachmentControlLocked || status.Handoff == nil || status.Handoff.Result != "completed" {
		t.Fatalf("completed handoff status = %+v err=%v", status, err)
	}
	m.Delete(resp.ID)
}

func TestBrowserAttachmentTargetCloseAndTTL(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 30*time.Millisecond)
	resp := createFakeAttachment(t, m)
	m.handleEvent(m.attachments[resp.ID], browserCDPEvent{Method: "Target.targetDestroyed", Params: json.RawMessage(`{"targetId":"target-1"}`)})
	status, err := m.Get(resp.ID)
	if err != nil || status.State != attachmentStateTargetClosed {
		t.Fatalf("target close state = %+v err=%v", status, err)
	}
	m.Delete(resp.ID)

	cdp = newFakeBrowserCDP()
	m = fakeAttachmentManager(cdp, 20*time.Millisecond)
	resp = createFakeAttachment(t, m)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := m.Get(resp.ID); err != nil {
			if asAttachmentAPIError(err).Code != "browser_attachment_not_found" {
				t.Fatalf("TTL error = %v", err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("unviewed attachment did not expire")
}

func TestBrowserAttachmentViewerUsesWorkspaceWideLeaseLimit(t *testing.T) {
	previous := workspaceBrowserViewerLeases
	workspaceBrowserViewerLeases = &browserViewerLeasePool{limit: 1, owners: make(map[string]struct{})}
	t.Cleanup(func() { workspaceBrowserViewerLeases = previous })
	m1 := fakeAttachmentManager(newFakeBrowserCDP(), 0)
	m2 := fakeAttachmentManager(newFakeBrowserCDP(), 0)
	r1 := createFakeAttachment(t, m1)
	r2 := createFakeAttachment(t, m2)
	a1, err := m1.reserveViewer(r1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m2.reserveViewer(r2.ID); asAttachmentAPIError(err).Code != "browser_already_attached" {
		t.Fatalf("viewer limit error = %v", err)
	}
	m1.releaseViewerReservation(a1)
	a2, err := m2.reserveViewer(r2.ID)
	if err != nil {
		t.Fatalf("released viewer lease was not reusable: %v", err)
	}
	m2.releaseViewerReservation(a2)
	m1.Delete(r1.ID)
	m2.Delete(r2.ID)
}

func TestCDPDiscoveryFiltersTargetsAndRebuildsLoopbackSocket(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/version":
			_, _ = w.Write([]byte(`{"Browser":"Chrome/150","Protocol-Version":"1.3","webSocketDebuggerUrl":"ws://evil.invalid:1/devtools/browser/opaque?token=path-only"}`))
		case "/json/list":
			_, _ = w.Write([]byte(`[{"id":"page-1","type":"page","title":"Page","url":"https://example.invalid/"},{"id":"worker-1","type":"service_worker","title":"Worker","url":"https://example.invalid/sw.js"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	discovery, err := discoverCDPTargets(port, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Targets) != 1 || discovery.Targets[0].TargetID != "page-1" {
		t.Fatalf("filtered targets = %+v", discovery.Targets)
	}
	rebuilt, err := reconstructCDPWebSocketURL(port, discovery.DebuggerURL)
	if err != nil {
		t.Fatal(err)
	}
	want := "ws://127.0.0.1:" + strconv.Itoa(port) + "/devtools/browser/opaque?token=path-only"
	if rebuilt != want || strings.Contains(rebuilt, "evil.invalid") {
		t.Fatalf("rebuilt WebSocket URL = %q, want %q", rebuilt, want)
	}
}

func TestCDPDiscoveryRejectsRedirectAndReservedPort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid/", http.StatusFound)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	if _, err := discoverCDPTargets(port, time.Second); asAttachmentAPIError(err).Code != "cdp_endpoint_invalid" {
		t.Fatalf("redirect discovery error = %v", err)
	}
	if err := validateCDPPort(browserAgentPort); asAttachmentAPIError(err).Code != "bad_cdp_port" {
		t.Fatalf("reserved port error = %v", err)
	}
	t.Setenv("AF_CP_BASE_URL", "http://127.0.0.1:8443")
	if err := validateCDPPort(8443); asAttachmentAPIError(err).Code != "bad_cdp_port" {
		t.Fatalf("Control Plane port error = %v", err)
	}
}

func TestBrowserAttachmentUsesWebSocketCDPAdapter(t *testing.T) {
	methods := make(chan string, 32)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/version":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Browser": "Chrome/150", "Protocol-Version": "1.3",
				"webSocketDebuggerUrl": "ws://ignored.invalid/devtools/browser/external-owner",
			})
		case "/json/list":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "page-real", "type": "page", "title": "External", "url": "https://example.invalid/edit",
			}})
		case "/devtools/browser/external-owner":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				_, data, err := conn.ReadMessage()
				if err != nil {
					return
				}
				var call struct {
					ID     int64  `json:"id"`
					Method string `json:"method"`
				}
				if json.Unmarshal(data, &call) != nil {
					continue
				}
				methods <- call.Method
				result := map[string]any{}
				if call.Method == "Target.attachToTarget" {
					result["sessionId"] = "session-real"
				}
				_ = conn.WriteJSON(map[string]any{"id": call.ID, "result": result})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	m := newBrowserAttachmentManager(browserAttachmentManagerConfig{
		UnviewedTTL: time.Hour, ViewerGrace: time.Hour, HandoffTTL: time.Hour,
		DiscoveryTimeout: time.Second, CommandTimeout: time.Second, FrameInterval: time.Millisecond,
		Discover: discoverCDPTargets, Dial: dialWebSocketCDP,
	})
	resp, err := m.Create(browserAttachmentCreateRequest{
		Port: port, TargetID: "page-real",
		Viewport: browserViewportRequest{Width: 800, Height: 600, DeviceScaleFactor: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Delete(resp.ID)
	close(methods)
	var got []string
	for method := range methods {
		got = append(got, method)
	}
	if !containsBrowserMethod(got, "Target.attachToTarget") || !containsBrowserMethod(got, "Target.detachFromTarget") {
		t.Fatalf("WebSocket adapter CDP methods = %v", got)
	}
	for _, forbidden := range []string{"Target.closeTarget", "Target.disposeBrowserContext", "Browser.close"} {
		if containsBrowserMethod(got, forbidden) {
			t.Fatalf("external owner command %s sent over WebSocket: %v", forbidden, got)
		}
	}
}

func containsBrowserMethod(methods []string, want string) bool {
	for _, method := range methods {
		if method == want {
			return true
		}
	}
	return false
}
