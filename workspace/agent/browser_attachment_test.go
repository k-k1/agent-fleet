package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// fakeAttachmentManagerWithTargets is fakeAttachmentManager with more than the
// one fixed "target-1", for tests that need to switch between sibling tabs on
// the same port. Retarget dials a genuinely separate CDP connection for the
// new target and independently closes the old one — exactly like a real
// Chromium attach — so Dial hands out a fresh fake per call (recorded in
// dialed) rather than reusing one: sharing a single fake here would make
// closing "the old connection" also close the one still in use, which is a
// test-fixture bug, not anything Retarget itself does.
func fakeAttachmentManagerWithTargets(dialed *[]*fakeBrowserCDP, targets []browserAttachTarget) *browserAttachmentManager {
	return newBrowserAttachmentManager(browserAttachmentManagerConfig{
		UnviewedTTL: time.Hour, ViewerGrace: time.Hour, HandoffTTL: time.Hour,
		DiscoveryTimeout: time.Second, CommandTimeout: time.Second, FrameInterval: time.Millisecond,
		Discover: func(int, time.Duration) (cdpDiscovery, error) {
			return cdpDiscovery{DebuggerURL: "ws://untrusted.invalid/devtools/browser/raw-secret", Targets: targets}, nil
		},
		Dial: func(context.Context, int, string) (browserCDP, error) {
			cdp := newFakeBrowserCDP()
			cdp.attachSessionID = fmt.Sprintf("session-%d", len(*dialed)+1)
			*dialed = append(*dialed, cdp)
			return cdp, nil
		},
	})
}

func TestBrowserAttachmentLifecycleDoesNotCloseExternalTarget(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	resp := createFakeAttachment(t, m)
	if !validAttachmentID(resp.ID) || resp.OpenURL != "/open/browser-attachment/"+resp.ID || resp.State != attachmentStateAttached {
		t.Fatalf("attachment response = %+v", resp)
	}
	wire, _ := json.Marshal(resp)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"port", "targetId", "webSocketDebuggerUrl"} {
		if _, exists := fields[forbidden]; exists {
			t.Fatalf("raw CDP coordinate %q leaked in response: %s", forbidden, wire)
		}
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

func TestBrowserAttachmentLabelOverridesPageTitle(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	previous := workspaceBrowserAttachmentManager
	workspaceBrowserAttachmentManager = m
	t.Cleanup(func() { workspaceBrowserAttachmentManager = previous })
	req := httptest.NewRequest(http.MethodPost, "/browser/attachments", strings.NewReader(
		`{"port":9222,"targetId":"target-1","viewport":{"width":1280,"height":900,"deviceScaleFactor":1}}`,
	))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(browserAttachmentLabelHeader, base64.RawURLEncoding.EncodeToString([]byte("確認画面")))
	w := httptest.NewRecorder()
	buildMux().ServeHTTP(w, req)
	var resp browserAttachmentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create labeled attachment: status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}
	if resp.Title != "確認画面" {
		t.Fatalf("attachment title = %q", resp.Title)
	}
	a, err := m.lookupActive(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ready struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(a.readyMessage(), &ready); err != nil || ready.Title != "確認画面" {
		t.Fatalf("ready title = %q err=%v", ready.Title, err)
	}
	m.updateNavigation(a, "https://example.invalid/next")
	status, err := m.Get(resp.ID)
	if err != nil || status.Title != "確認画面" {
		t.Fatalf("navigation replaced label: status=%+v err=%v", status, err)
	}
	m.Delete(resp.ID)
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

// A fresh attachment is view-only, so scroll and keys are refused until the
// owner hands over. Every other live/unit test sets user-control first, so the
// path the field actually takes — attach, hand the user the link, never call
// request_browser_action — was the one path nothing covered, and it reached
// users as "the pane renders but scrolling and typing do nothing".
func TestBrowserAttachmentDefaultsToViewOnlyAndReportsRefusedInput(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	resp := createFakeAttachment(t, m)
	if resp.ControlMode != attachmentControlViewOnly {
		t.Fatalf("fresh attachment control mode = %q, want %q", resp.ControlMode, attachmentControlViewOnly)
	}
	a, err := m.lookupActive(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	v := &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 8), done: make(chan struct{})}

	for _, message := range []string{
		`{"type":"wheel","x":1,"y":1,"deltaX":0,"deltaY":400,"modifiers":0}`,
		`{"type":"key","event":"down","key":"PageDown","code":"PageDown","modifiers":0,"repeat":false}`,
	} {
		v.handleControl([]byte(message))
	}
	for _, method := range []string{"Input.dispatchMouseEvent", "Input.dispatchKeyEvent"} {
		if containsBrowserMethod(cdp.methods(), method) {
			t.Fatalf("view-only forwarded %s", method)
		}
	}
	// Refusing silently is what made this invisible: the Console had nothing to
	// show and looked exactly like a working pane.
	var refusals int
	for done := false; !done; {
		select {
		case out := <-v.control:
			var msg struct {
				Type string `json:"type"`
				Code string `json:"code"`
			}
			if json.Unmarshal(out.data, &msg) == nil && msg.Type == "protocol-error" && msg.Code == "input_not_allowed" {
				refusals++
			}
		default:
			done = true
		}
	}
	if refusals != 2 {
		t.Fatalf("input_not_allowed reported %d times, want 2", refusals)
	}

	if _, err := m.SetControlMode(resp.ID, attachmentControlUser); err != nil {
		t.Fatal(err)
	}
	v.handleControl([]byte(`{"type":"wheel","x":1,"y":1,"deltaX":0,"deltaY":400,"modifiers":0}`))
	if !containsBrowserMethod(cdp.methods(), "Input.dispatchMouseEvent") {
		t.Fatal("user-control did not forward the wheel")
	}
	m.Delete(resp.ID)
}

func TestBrowserAttachmentSetControlModeHasNoHandoffOrTTLSideEffects(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	created := createFakeAttachment(t, m)
	before, err := m.UpdateHandoff(created.ID, browserAttachmentHandoffRequest{
		Message: "操作してください", CompletionLabel: "完了", AllowCancel: true,
		ControlMode: attachmentControlUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if before.ExpiresAt == nil || before.Handoff == nil {
		t.Fatalf("handoff response = %+v", before)
	}

	locked, err := m.SetControlMode(created.ID, attachmentControlLocked)
	if err != nil {
		t.Fatal(err)
	}
	if locked.ID != created.ID || locked.OpenURL != created.OpenURL || locked.ControlMode != attachmentControlLocked {
		t.Fatalf("complete attachment response = %+v", locked)
	}
	if locked.ExpiresAt == nil || !locked.ExpiresAt.Equal(*before.ExpiresAt) {
		t.Fatalf("control mode changed expiry: before=%v after=%v", before.ExpiresAt, locked.ExpiresAt)
	}
	if locked.Handoff == nil || locked.Handoff.Result != "pending" ||
		locked.Handoff.ControlMode != attachmentControlUser || locked.Handoff.Message != before.Handoff.Message {
		t.Fatalf("control mode mutated handoff: before=%+v after=%+v", before.Handoff, locked.Handoff)
	}
	if _, err := m.SetControlMode(created.ID, "invalid"); asAttachmentAPIError(err).Code != "bad_control_mode" {
		t.Fatalf("invalid control mode error = %v", err)
	}

	a, err := m.lookupActive(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	v := &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 4), done: make(chan struct{})}
	a.mu.Lock()
	a.viewer, a.visible = v, true
	a.mu.Unlock()
	if _, err := m.SetControlMode(created.ID, attachmentControlViewOnly); err != nil {
		t.Fatal(err)
	}
	if !containsBrowserMethod(cdp.methods(), "Page.startScreencast") {
		t.Fatal("unlocking a visible attachment did not resume screencast")
	}
	if _, err := m.SetControlMode(created.ID, attachmentControlLocked); err != nil {
		t.Fatal(err)
	}
	if !containsBrowserMethod(cdp.methods(), "Page.stopScreencast") {
		t.Fatal("locking a visible attachment did not stop screencast")
	}
	var event struct {
		Type        string `json:"type"`
		ControlMode string `json:"controlMode"`
	}
	for i, want := range []string{attachmentControlViewOnly, attachmentControlLocked} {
		out := <-v.control
		if err := json.Unmarshal(out.data, &event); err != nil || event.Type != "control-mode" || event.ControlMode != want {
			t.Fatalf("control mode event %d = %s (%+v, %v)", i, out.data, event, err)
		}
	}
	m.Delete(created.ID)
}

func TestBrowserAttachmentHiddenPendingHandoffKeepsHandoffTTL(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	m.config.ViewerGrace = time.Minute
	m.config.HandoffTTL = 30 * time.Minute
	created := createFakeAttachment(t, m)
	a, err := m.lookupActive(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	v := &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 4), done: make(chan struct{})}
	a.mu.Lock()
	a.viewer, a.visible = v, true
	if a.expiry != nil {
		a.expiry.Stop()
		a.expiry = nil
		a.expiresAt = time.Time{}
	}
	a.mu.Unlock()
	if _, err := m.UpdateHandoff(created.ID, browserAttachmentHandoffRequest{
		Message: "操作してください", ControlMode: attachmentControlUser,
	}); err != nil {
		t.Fatal(err)
	}

	v.setVisible(false)
	status, err := m.Get(created.ID)
	if err != nil || status.ExpiresAt == nil {
		t.Fatalf("hidden handoff status = %+v err=%v", status, err)
	}
	remaining := time.Until(*status.ExpiresAt)
	if remaining < 29*time.Minute || remaining > 30*time.Minute {
		t.Fatalf("hidden pending handoff expiry = %v, want about 30m", remaining)
	}
	m.Delete(created.ID)
}

// TestBrowserAttachmentScreencastFrameArrivingBeforeStartReturns: attach 側でも、
// Page.startScreencast の応答より先に届いたフレームを取りこぼさない。
//
// 事情は所有ページ側（TestBrowserScreencastFrameArrivingBeforeStartReturns）と同じ
// で、attach 先はより悪い: 他人のページなので、落とした 1 枚を埋め合わせる再描画を
// こちらから起こせない。
func TestBrowserAttachmentScreencastFrameArrivingBeforeStartReturns(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	created := createFakeAttachment(t, m)
	a, err := m.lookupActive(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.viewer = &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 8), done: make(chan struct{})}
	a.visible = true
	a.mu.Unlock()

	_, sessionID := a.currentSession()
	raw, _ := json.Marshal(map[string]any{
		"data": base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0xff}), "sessionId": 1,
	})
	cdp.setOnCall("Page.startScreencast", func() {
		m.handleEvent(a, sessionID, "target-1", browserCDPEvent{
			Method: "Page.screencastFrame", SessionID: sessionID, Params: raw,
		})
	})
	if err := a.startScreencast(); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-a.latestFrame:
		if string(frame) != string([]byte{0xff, 0xd8, 0xff}) {
			t.Fatalf("decoded frame = %x", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a frame that arrived before Page.startScreencast returned was dropped")
	}
	if !waitFor(2*time.Second, func() bool { _, ok := cdp.last("Page.screencastFrameAck"); return ok }) {
		t.Fatal("the frame was not acknowledged; Chromium stops capturing once frames in flight go unacknowledged")
	}
}

func TestBrowserAttachmentResumesScreencastAfterUnsupportedURL(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	created := createFakeAttachment(t, m)
	a, err := m.lookupActive(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.viewer = &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 8), done: make(chan struct{})}
	a.visible = true
	a.mu.Unlock()
	if err := a.startScreencast(); err != nil {
		t.Fatal(err)
	}

	m.updateNavigation(a, "chrome://settings/")
	m.updateNavigation(a, "https://example.invalid/review")
	methods := cdp.methods()
	startCount, stopCount := 0, 0
	for _, method := range methods {
		if method == "Page.startScreencast" {
			startCount++
		}
		if method == "Page.stopScreencast" {
			stopCount++
		}
	}
	if startCount != 2 || stopCount != 1 {
		t.Fatalf("screencast calls after unsupported URL recovery: starts=%d stops=%d methods=%v", startCount, stopCount, methods)
	}
	status, err := m.Get(created.ID)
	if err != nil || status.State != attachmentStateViewerOpen {
		t.Fatalf("recovered attachment status = %+v err=%v", status, err)
	}
	m.Delete(created.ID)
}

// TestBrowserAttachmentScreencastRetriesFrameNotActive covers the same
// immediate-attach race as TestBrowserScreencastRetriesFrameNotActive, but for
// the Chromium attach path: a viewer attaches while the target page is still
// swapping from about:blank to the navigated document, so the first
// Page.startScreencast calls are rejected with "Not attached to an active
// page". startScreencast must retry that transient rejection instead of
// surfacing it as a fatal error that disconnects the viewer with zero frames.
func TestBrowserAttachmentScreencastRetriesFrameNotActive(t *testing.T) {
	cdp := newFakeBrowserCDP()
	cdp.transient = map[string]int{"Page.startScreencast": 3}
	cdp.transientErr = fmt.Errorf("CDP Page.startScreencast (-32000): Not attached to an active page")
	m := fakeAttachmentManager(cdp, 0)
	created := createFakeAttachment(t, m)
	a, err := m.lookupActive(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.visible = true
	a.mu.Unlock()

	if err := a.startScreencast(); err != nil {
		t.Fatalf("startScreencast should retry the transient not-active rejection, got %v", err)
	}
	a.castMu.Lock()
	casting := a.casting
	a.castMu.Unlock()
	if !casting {
		t.Fatal("screencast did not arm after the transient rejections cleared")
	}
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
	a.stopScreencast()
	cdp.mu.Lock()
	cdp.fail = map[string]error{"Page.startScreencast": fmt.Errorf("CDP Page.startScreencast (-32000): some other failure")}
	cdp.calls = nil
	cdp.mu.Unlock()
	if err := a.startScreencast(); err == nil {
		t.Fatal("a non-transient startScreencast error should be returned, not swallowed")
	}
	m.Delete(created.ID)
}

// TestBrowserAttachmentStartScreencastBringsTargetToFront covers a real-world
// gap found while investigating a live "pane stays black despite a connected
// viewer" report: an attach target is very often not Chromium's foreground
// tab (e.g. a script with many tabs open, cycling between them), and Chromium
// throttles paints on background tabs so Page.startScreencast succeeds but no
// screencastFrame ever arrives. startScreencast must activate the target
// before arming the cast so it actually renders.
func TestBrowserAttachmentStartScreencastBringsTargetToFront(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	created := createFakeAttachment(t, m)
	a, err := m.lookupActive(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.visible = true
	a.mu.Unlock()

	if err := a.startScreencast(); err != nil {
		t.Fatal(err)
	}
	methods := cdp.methods()
	frontIdx, castIdx := -1, -1
	for i, mth := range methods {
		if mth == "Page.bringToFront" && frontIdx == -1 {
			frontIdx = i
		}
		if mth == "Page.startScreencast" && castIdx == -1 {
			castIdx = i
		}
	}
	if frontIdx == -1 {
		t.Fatalf("startScreencast did not call Page.bringToFront: %v", methods)
	}
	if castIdx == -1 || frontIdx > castIdx {
		t.Fatalf("Page.bringToFront must precede Page.startScreencast: %v", methods)
	}
	m.Delete(created.ID)
}

// TestBrowserAttachmentRetargetSwitchesTargetKeepingID covers the feature this
// exists for: a script driving many tabs in turn (e.g. posting several drafts)
// used to force closing the pane and asking the agent to mint a brand new
// attachment link for every tab switch. Retarget must keep the same id/URL
// while moving the live session onto a different target, so an already-open
// Console pane keeps working across the switch.
func TestBrowserAttachmentRetargetSwitchesTargetKeepingID(t *testing.T) {
	var dialed []*fakeBrowserCDP
	targets := []browserAttachTarget{
		{TargetID: "target-1", Type: "page", Title: "First", URL: "https://example.invalid/one"},
		{TargetID: "target-2", Type: "page", Title: "Second", URL: "https://example.invalid/two"},
	}
	m := fakeAttachmentManagerWithTargets(&dialed, targets)
	created := createFakeAttachment(t, m)
	a, err := m.lookupActive(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.viewer = &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 8), done: make(chan struct{})}
	a.visible = true
	a.mu.Unlock()
	if len(dialed) != 1 {
		t.Fatalf("expected exactly one dialed connection after create, got %d", len(dialed))
	}
	oldCDP := dialed[0]

	resp, err := m.Retarget(created.ID, browserAttachmentRetargetRequest{TargetID: "target-2"})
	if err != nil {
		t.Fatalf("retarget: %v", err)
	}
	if resp.ID != created.ID || resp.OpenURL != created.OpenURL {
		t.Fatalf("retarget must keep the same id/URL, got %+v", resp)
	}
	if resp.Title != "Second" || resp.URL != "https://example.invalid/two" {
		t.Fatalf("retarget did not pick up the new target's title/url: %+v", resp)
	}

	a.mu.Lock()
	targetID, targetKey := a.targetID, a.targetKey
	a.mu.Unlock()
	if targetID != "target-2" || targetKey != "9222:target-2" {
		t.Fatalf("attachment did not move onto target-2: targetID=%s targetKey=%s", targetID, targetKey)
	}

	// The old target must be free for a new attachment to claim...
	if _, err := m.Create(browserAttachmentCreateRequest{
		Port: 9222, TargetID: "target-1", Viewport: browserViewportRequest{Width: 800, Height: 600, DeviceScaleFactor: 1},
	}); err != nil {
		t.Fatalf("target-1 should be free after retargeting away from it: %v", err)
	}
	// ...and the new one must now be claimed, refusing a second attach.
	if _, err := m.Create(browserAttachmentCreateRequest{
		Port: 9222, TargetID: "target-2", Viewport: browserViewportRequest{Width: 800, Height: 600, DeviceScaleFactor: 1},
	}); asAttachmentAPIError(err).Code != "browser_already_attached" {
		t.Fatalf("target-2 should now be claimed by the retargeted attachment: %v", err)
	}

	if len(dialed) != 3 {
		t.Fatalf("expected a fresh dial for retarget's new target plus the two Creates above, got %d", len(dialed))
	}
	newCDP := dialed[1]
	if !containsBrowserMethod(oldCDP.methods(), "Target.detachFromTarget") {
		t.Fatalf("retarget did not detach the old session on the old connection: %v", oldCDP.methods())
	}
	newMethods := newCDP.methods()
	if !containsBrowserMethod(newMethods, "Target.attachToTarget") {
		t.Fatalf("retarget did not attach the new session on a fresh connection: %v", newMethods)
	}
	if !containsBrowserMethod(newMethods, "Page.startScreencast") {
		t.Fatalf("retarget did not resume the cast on the new target for the still-visible viewer: %v", newMethods)
	}
}

func TestBrowserAttachmentRetargetConflictsWithExistingAttachment(t *testing.T) {
	var dialed []*fakeBrowserCDP
	targets := []browserAttachTarget{
		{TargetID: "target-1", Type: "page", Title: "First", URL: "https://example.invalid/one"},
		{TargetID: "target-2", Type: "page", Title: "Second", URL: "https://example.invalid/two"},
	}
	m := fakeAttachmentManagerWithTargets(&dialed, targets)
	first := createFakeAttachment(t, m)
	second, err := m.Create(browserAttachmentCreateRequest{
		Port: 9222, TargetID: "target-2", Viewport: browserViewportRequest{Width: 800, Height: 600, DeviceScaleFactor: 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = m.Retarget(first.ID, browserAttachmentRetargetRequest{TargetID: "target-2"})
	if asAttachmentAPIError(err).Code != "browser_already_attached" {
		t.Fatalf("retargeting onto an already-attached target should conflict, got %v", err)
	}

	status, err := m.Get(first.ID)
	if err != nil || status.URL != "https://example.invalid/one" {
		t.Fatalf("a failed retarget must leave the attachment on its original target: %+v err=%v", status, err)
	}
	m.Delete(first.ID)
	m.Delete(second.ID)
}

// TestBrowserAttachmentEndSessionIfCurrentIgnoresSupersededSession is a direct
// unit test of the guard that keeps a stale (pre-retarget) eventLoop
// generation from tearing down an attachment that has already moved on: its
// connection ending must only be treated as a real disconnect if the session
// id it was launched for is still the attachment's current one.
func TestBrowserAttachmentEndSessionIfCurrentIgnoresSupersededSession(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	created := createFakeAttachment(t, m)
	a, err := m.lookupActive(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	current := a.sessionID
	a.mu.Unlock()

	m.endSessionIfCurrent(a, "a-superseded-session-id", "stale connection closed")
	if status, err := m.Get(created.ID); err != nil || status.State == attachmentStateDisconnected {
		t.Fatalf("a superseded session ending must not mark the attachment terminal: %+v err=%v", status, err)
	}

	m.endSessionIfCurrent(a, current, "current connection really closed")
	if status, err := m.Get(created.ID); err != nil || status.State != attachmentStateDisconnected {
		t.Fatalf("the current session ending must mark the attachment terminal: %+v err=%v", status, err)
	}
}

// TestBrowserAttachmentListSiblingTargetsMarksCurrentAndHidesPort covers the
// Retarget picker's data source: it must return the other targets on the same
// Chromium instance, mark which one this attachment is presently on (the
// caller has no other way to tell — see the type's doc comment), and never
// require or leak the underlying port.
func TestBrowserAttachmentListSiblingTargetsMarksCurrentAndHidesPort(t *testing.T) {
	targets := []browserAttachTarget{
		{TargetID: "target-1", Type: "page", Title: "First", URL: "https://example.invalid/one"},
		{TargetID: "target-2", Type: "page", Title: "Second", URL: "https://example.invalid/two"},
	}
	var dialed []*fakeBrowserCDP
	m := fakeAttachmentManagerWithTargets(&dialed, targets)
	created := createFakeAttachment(t, m)

	resp, err := m.ListSiblingTargets(created.ID)
	if err != nil {
		t.Fatalf("list sibling targets: %v", err)
	}
	if len(resp.Targets) != 2 {
		t.Fatalf("expected both sibling targets, got %+v", resp.Targets)
	}
	byID := map[string]browserAttachmentSiblingTarget{}
	for _, target := range resp.Targets {
		byID[target.TargetID] = target
	}
	if !byID["target-1"].Current {
		t.Fatalf("target-1 (what Create attached to) should be marked current: %+v", resp.Targets)
	}
	if byID["target-2"].Current {
		t.Fatalf("target-2 should not be marked current: %+v", resp.Targets)
	}

	wire, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "9222") || strings.Contains(strings.ToLower(string(wire)), "port") {
		t.Fatalf("sibling targets response must not leak the port: %s", wire)
	}
}

func TestBrowserAttachmentControlModeHTTPContract(t *testing.T) {
	m := fakeAttachmentManager(newFakeBrowserCDP(), 0)
	created := createFakeAttachment(t, m)
	previous := workspaceBrowserAttachmentManager
	workspaceBrowserAttachmentManager = m
	t.Cleanup(func() {
		m.Delete(created.ID)
		workspaceBrowserAttachmentManager = previous
	})
	path := "/browser/attachments/" + created.ID + "/control-mode"
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"controlMode":"user-control"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	buildMux().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("control mode HTTP status = %d body=%s", w.Code, w.Body.String())
	}
	var got browserAttachmentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.ID != created.ID ||
		got.OpenURL != created.OpenURL || got.ControlMode != attachmentControlUser || got.ExpiresAt == nil {
		t.Fatalf("control mode HTTP response = %+v err=%v", got, err)
	}

	req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"controlMode":"owner-control"}`))
	w = httptest.NewRecorder()
	buildMux().ServeHTTP(w, req)
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || w.Code != http.StatusBadRequest || failure.Error.Code != "bad_control_mode" {
		t.Fatalf("invalid control mode HTTP response = %d %s err=%v", w.Code, w.Body.String(), err)
	}
}

func TestBrowserAttachmentTargetCloseAndTTL(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 30*time.Millisecond)
	resp := createFakeAttachment(t, m)
	m.handleEvent(m.attachments[resp.ID], "session-1", "target-1", browserCDPEvent{Method: "Target.targetDestroyed", Params: json.RawMessage(`{"targetId":"target-1"}`)})
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

// browserId is the caller's "this must be the Chromium I launched" assertion.
// A port collision is silent on the Chromium side, so an attach that skips this
// check can land on another session's browser (docs/53 §53.16).
func TestBrowserAttachmentRejectsForeignBrowserInstance(t *testing.T) {
	const guid = "c162d83f-b0a3-41d3-9db6-e9f6012c1491"
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	m.config.Discover = func(int, time.Duration) (cdpDiscovery, error) {
		return cdpDiscovery{
			DebuggerURL: "ws://127.0.0.1:9222/devtools/browser/" + guid,
			BrowserID:   guid,
			Targets:     []browserAttachTarget{{TargetID: "target-1", Type: "page", Title: "External", URL: "https://example.invalid/edit"}},
		}, nil
	}
	defer m.Close()

	_, err := m.Create(browserAttachmentCreateRequest{
		Port: 9222, TargetID: "target-1", BrowserID: "11111111-2222-3333-4444-555555555555",
		Viewport: browserViewportRequest{Width: 1280, Height: 900, DeviceScaleFactor: 1},
	})
	if apiErr := asAttachmentAPIError(err); apiErr.Code != "cdp_browser_mismatch" || apiErr.Status != http.StatusConflict {
		t.Fatalf("foreign browser error = %+v", apiErr)
	}
	if _, err := m.Create(browserAttachmentCreateRequest{
		Port: 9222, TargetID: "target-1", BrowserID: "not a browser id/../",
		Viewport: browserViewportRequest{Width: 1280, Height: 900, DeviceScaleFactor: 1},
	}); asAttachmentAPIError(err).Code != "bad_browser_attachment" {
		t.Fatalf("malformed browserId error = %v", err)
	}
	// The matching instance still attaches — accepted in the DevToolsActivePort
	// form the caller reads off disk, not just as a bare GUID.
	resp, err := m.Create(browserAttachmentCreateRequest{
		Port: 9222, TargetID: "target-1", BrowserID: "/devtools/browser/" + guid,
		Viewport: browserViewportRequest{Width: 1280, Height: 900, DeviceScaleFactor: 1},
	})
	if err != nil {
		t.Fatalf("matching browserId must attach: %v", err)
	}
	if resp.State != attachmentStateAttached {
		t.Fatalf("state=%q", resp.State)
	}
}

// Zoom-to-fit: a pane narrower than the site must widen the LAYOUT viewport and
// scale the image back down, not clip the page (which is what the user sees as
// "half the page is missing and the scrollbar is stuck").
func TestBrowserAttachmentZoomToFitWidensLayoutAndScalesImage(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	resp := createFakeAttachment(t, m)
	a, err := m.lookupActive(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Delete(resp.ID)
	v := &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 8), done: make(chan struct{})}

	v.handleControl([]byte(`{"type":"viewport","width":660,"height":800,"fit":true}`))

	metrics, ok := cdp.last("Emulation.setDeviceMetricsOverride")
	if !ok {
		t.Fatal("no device metrics applied")
	}
	// 1240 CSS px of content, pane aspect preserved exactly (660:800).
	if metrics.Params["width"] != float64(1240) || metrics.Params["height"] != float64(1503) {
		t.Fatalf("layout viewport = %v x %v", metrics.Params["width"], metrics.Params["height"])
	}
	// setDeviceMetricsOverride's `scale` must NOT be used: measured on Chrome 151
	// it shrinks the page inside an unchanged surface (page in the top-left
	// corner, rest blank) instead of shrinking the image.
	if _, hasScale := metrics.Params["scale"]; hasScale {
		t.Fatalf("zoom must not go through metrics scale: %+v", metrics.Params)
	}
	// The frames must stay pane-sized — zooming out costs layout, not bandwidth.
	if err := a.startScreencast(); err != nil {
		t.Fatal(err)
	}
	cast, ok := cdp.last("Page.startScreencast")
	if !ok || cast.Params["maxWidth"] != float64(660) || cast.Params["maxHeight"] != float64(800) {
		t.Fatalf("screencast bounds = %+v", cast.Params)
	}
	// Pointer coordinates live in layout space, so the viewer has to be told.
	if got := drainViewerText(t, v, "viewport"); got["width"] != float64(1240) || got["height"] != float64(1503) {
		t.Fatalf("viewport message = %+v", got)
	}
	a.mu.Lock()
	layout := a.viewport
	a.mu.Unlock()
	if layout.Width != 1240 || !v.validPoint(1200, 1400) {
		t.Fatalf("layout viewport not adopted for input mapping: %+v", layout)
	}
}

// Without fit the page keeps 1:1 with the pane — the responsive-preview case.
func TestBrowserAttachmentWithoutFitKeepsPaneSizedViewport(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	resp := createFakeAttachment(t, m)
	a, _ := m.lookupActive(resp.ID)
	defer m.Delete(resp.ID)
	v := &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 8), done: make(chan struct{})}

	v.handleControl([]byte(`{"type":"viewport","width":660,"height":800}`))
	metrics, _ := cdp.last("Emulation.setDeviceMetricsOverride")
	if metrics.Params["width"] != float64(660) || metrics.Params["height"] != float64(800) {
		t.Fatalf("viewport = %+v", metrics.Params)
	}
	if _, hasScale := metrics.Params["scale"]; hasScale {
		t.Fatalf("1:1 must not carry a scale: %+v", metrics.Params)
	}
	if containsBrowserMethod(cdp.methods(), "Page.getLayoutMetrics") {
		t.Fatal("measured the page even though fit was off")
	}
}

// Pinch zoom on a phone: the page is laid out SMALLER on top of whatever fit
// already decided, so the frame is captured from a layout with fewer CSS pixels
// and the text is re-rendered bigger instead of being interpolated.
func TestBrowserAttachmentPinchZoomShrinksLayout(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	resp := createFakeAttachment(t, m)
	a, _ := m.lookupActive(resp.ID)
	defer m.Delete(resp.ID)
	v := &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 16), done: make(chan struct{})}

	v.handleControl([]byte(`{"type":"viewport","width":660,"height":800,"fit":true,"zoom":1}`))
	measured := countBrowserMethod(cdp.methods(), "Page.getLayoutMetrics")
	if measured != 1 {
		t.Fatalf("fit measured the page %d times", measured)
	}

	v.handleControl([]byte(`{"type":"viewport","width":660,"height":800,"fit":true,"zoom":2}`))
	metrics, ok := cdp.last("Emulation.setDeviceMetricsOverride")
	if !ok || metrics.Params["width"] != float64(620) || metrics.Params["height"] != float64(752) {
		t.Fatalf("zoomed layout = %+v", metrics.Params)
	}
	// Re-measuring an already zoomed page folds each zoom into the next one, so
	// the fit is measured once per pane/fit change and never per pinch.
	if got := countBrowserMethod(cdp.methods(), "Page.getLayoutMetrics"); got != measured {
		t.Fatalf("a pinch re-measured the page (%d -> %d)", measured, got)
	}
	// The emulated pixel ratio stays 1: measured on Chromium 151 the screencast
	// emits CSS-pixel-sized frames and ignores it, so raising it would only make
	// the compositor render pixels no frame carries.
	if metrics.Params["deviceScaleFactor"] != float64(1) {
		t.Fatalf("deviceScaleFactor = %v", metrics.Params["deviceScaleFactor"])
	}
	if got := lastViewerText(t, v, "viewport"); got["width"] != float64(620) || got["height"] != float64(752) {
		t.Fatalf("viewer was not told the zoomed mapping space: %+v", got)
	}
	a.mu.Lock()
	layout := a.viewport
	a.mu.Unlock()
	if layout.Width != 620 || v.validPoint(700, 100) {
		t.Fatalf("pointer mapping not narrowed to the zoomed layout: %+v", layout)
	}

	v.handleControl([]byte(`{"type":"viewport","width":660,"height":800,"fit":true,"zoom":4}`))
	metrics, _ = cdp.last("Emulation.setDeviceMetricsOverride")
	if metrics.Params["width"] != float64(310) {
		t.Fatalf("4x layout = %+v", metrics.Params)
	}
	// The image cap stays the pane's: zooming changes the layout, never the
	// bytes on the wire.
	if err := a.startScreencast(); err != nil {
		t.Fatal(err)
	}
	cast, _ := cdp.last("Page.startScreencast")
	if cast.Params["maxWidth"] != float64(660) || cast.Params["maxHeight"] != float64(800) {
		t.Fatalf("zooming in must not grow the frame: %+v", cast.Params)
	}
}

// Zoom is the viewer's own layout viewport, not page input: a view-only
// attachment must still be able to zoom, exactly as it can already scroll-free
// read and copy. (Input stays refused — that is asserted separately.)
func TestBrowserAttachmentPinchZoomAllowedInViewOnly(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	resp := createFakeAttachment(t, m)
	a, _ := m.lookupActive(resp.ID)
	defer m.Delete(resp.ID)
	a.mu.Lock()
	mode := a.controlMode
	a.mu.Unlock()
	if mode != attachmentControlViewOnly {
		t.Fatalf("a fresh attachment must be view-only, got %q", mode)
	}
	v := &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 16), done: make(chan struct{})}

	v.handleControl([]byte(`{"type":"viewport","width":660,"height":800,"zoom":2}`))
	metrics, ok := cdp.last("Emulation.setDeviceMetricsOverride")
	if !ok || metrics.Params["width"] != float64(330) {
		t.Fatalf("view-only zoom was refused: %+v", metrics.Params)
	}
}

// lastViewerText returns the LAST queued message of the wanted type, draining
// the queue: a run of viewport changes queues one message each, and only the
// newest describes the space the viewer must map pointers into.
func lastViewerText(t *testing.T, v *browserAttachmentViewer, want string) map[string]any {
	t.Helper()
	var found map[string]any
	for {
		select {
		case out := <-v.control:
			var msg map[string]any
			if json.Unmarshal(out.data, &msg) == nil && msg["type"] == want {
				found = msg
			}
		default:
			if found == nil {
				t.Fatalf("no %q message was sent to the viewer", want)
			}
			return found
		}
	}
}

func countBrowserMethod(methods []string, want string) int {
	n := 0
	for _, method := range methods {
		if method == want {
			n++
		}
	}
	return n
}

// Copy: the container's clipboard is unreachable for the user, so the selection
// has to come back over the wire. Allowed in view-only — reading is the point.
func TestBrowserAttachmentCopySelectionWorksInViewOnly(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	resp := createFakeAttachment(t, m)
	a, _ := m.lookupActive(resp.ID)
	defer m.Delete(resp.ID)
	v := &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 8), done: make(chan struct{})}

	v.handleControl([]byte(`{"type":"copy"}`))
	if got := drainViewerText(t, v, "clipboard"); got["text"] != "コピー対象のテキスト" {
		t.Fatalf("clipboard message = %+v", got)
	}

	// Locked stops the screencast, so there is nothing on screen to copy either.
	if _, err := m.SetControlMode(resp.ID, attachmentControlLocked); err != nil {
		t.Fatal(err)
	}
	v.handleControl([]byte(`{"type":"copy"}`))
	if got := drainViewerText(t, v, "protocol-error"); got["code"] != "input_not_allowed" {
		t.Fatalf("locked copy = %+v", got)
	}
}

// drainViewerText returns the first queued viewer message of the wanted type.
func drainViewerText(t *testing.T, v *browserAttachmentViewer, want string) map[string]any {
	t.Helper()
	for {
		select {
		case out := <-v.control:
			var msg map[string]any
			if json.Unmarshal(out.data, &msg) == nil && msg["type"] == want {
				return msg
			}
		default:
			t.Fatalf("no %q message was sent to the viewer", want)
			return nil
		}
	}
}

// The Console's way back to a live attachment when the action link is gone.
func TestBrowserAttachmentListIsNewestFirstAndDropsDeleted(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	defer m.Close()
	targets := []string{"target-1", "target-2"}
	m.config.Discover = func(int, time.Duration) (cdpDiscovery, error) {
		list := make([]browserAttachTarget, 0, len(targets))
		for _, id := range targets {
			list = append(list, browserAttachTarget{TargetID: id, Type: "page", Title: id, URL: "https://example.invalid/" + id})
		}
		return cdpDiscovery{DebuggerURL: "ws://127.0.0.1:9222/devtools/browser/x", Targets: list}, nil
	}
	if got := m.List().Attachments; len(got) != 0 {
		t.Fatalf("empty manager listed %d", len(got))
	}
	first, err := m.Create(browserAttachmentCreateRequest{Port: 9222, TargetID: "target-1"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond) // distinct creation instants
	second, err := m.Create(browserAttachmentCreateRequest{Port: 9222, TargetID: "target-2"})
	if err != nil {
		t.Fatal(err)
	}
	listed := m.List().Attachments
	if len(listed) != 2 || listed[0].ID != second.ID || listed[1].ID != first.ID {
		t.Fatalf("list must be newest first: %+v", listed)
	}
	// Detaching is the user closing the view; it must leave the list, not linger.
	m.Delete(second.ID)
	listed = m.List().Attachments
	if len(listed) != 1 || listed[0].ID != first.ID {
		t.Fatalf("deleted attachment still listed: %+v", listed)
	}
}

// Discovery hands the browser id back so the caller can compare it with line 2
// of its own DevToolsActivePort before it ever attaches.
func TestBrowserAttachDiscoveryExposesBrowserID(t *testing.T) {
	const guid = "c162d83f-b0a3-41d3-9db6-e9f6012c1491"
	m := fakeAttachmentManager(newFakeBrowserCDP(), 0)
	m.config.Discover = func(int, time.Duration) (cdpDiscovery, error) {
		return cdpDiscovery{DebuggerURL: "ws://127.0.0.1:9222/devtools/browser/" + guid, BrowserID: guid}, nil
	}
	defer m.Close()
	resp, err := m.Discover(9222)
	if err != nil {
		t.Fatal(err)
	}
	if resp.BrowserID != guid {
		t.Fatalf("browserId=%q want %q", resp.BrowserID, guid)
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
