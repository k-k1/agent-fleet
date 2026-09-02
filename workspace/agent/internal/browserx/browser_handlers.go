package main

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

const (
	browserWSMaxMessage     = 32 * 1024
	browserWSWriteTimeout   = 5 * time.Second
	websocketCloseNormal    = 1000
	websocketCloseGoingAway = 1001
)

var browserUpgrader = websocket.Upgrader{
	CheckOrigin:       func(*http.Request) bool { return true }, // internal CP-only endpoint
	EnableCompression: true,                                     // base64 スクリーンキャストの帯域を CP↔Agent 間でも削る
}

// chromiumInstallState serializes the one-shot on-demand chromium install a
// lean rootfs triggers on the first pane attach (docs/log/35 §35.7.2-4). While the
// background install runs, page creation answers 503 browser_installing and the
// Console shows "preparing" + retries.
var chromiumInstallState struct {
	sync.Mutex
	running bool
	lastErr error
}

// ensureChromiumForPane checks the pane's chromium is resolvable and, when it
// is absent but a chromium pin exists for this arch, kicks the pinned install
// once in the background. Returns (installing, err): installing=true → tell the
// viewer to wait; err != nil → the previous install attempt failed (surfaced
// once, then cleared so the next create retries).
func ensureChromiumForPane() (bool, error) {
	if _, err := findChromiumBinary(); err == nil {
		return false, nil
	}
	pin := chromiumDefaultPin()
	if pin == "" {
		return false, nil // nothing to install — let Create fail the normal way
	}
	chromiumInstallState.Lock()
	defer chromiumInstallState.Unlock()
	if chromiumInstallState.running {
		return true, nil
	}
	if err := chromiumInstallState.lastErr; err != nil {
		chromiumInstallState.lastErr = nil
		return false, err
	}
	chromiumInstallState.running = true
	go func() {
		err := installChromium(pin)
		chromiumInstallState.Lock()
		chromiumInstallState.running = false
		chromiumInstallState.lastErr = err
		chromiumInstallState.Unlock()
	}()
	return true, nil
}

func handleBrowserPagesCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req browserCreateRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_browser_target", "invalid browser target")
		return
	}
	if installing, err := ensureChromiumForPane(); installing {
		httpx.WriteErr(w, http.StatusServiceUnavailable, "browser_installing", "preparing pinned Chromium (first use only, ~200MB)")
		return
	} else if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "browser_start_failed", "Chromium install failed: "+err.Error())
		return
	}
	resp, err := workspaceBrowserManager.Create(req)
	if err == nil {
		httpx.WriteJSON(w, http.StatusCreated, resp)
		return
	}
	switch {
	case errors.Is(err, errBrowserPageLimit):
		httpx.WriteErr(w, http.StatusTooManyRequests, "browser_page_limit", "browser page limit reached")
	case errors.Is(err, errBrowserStart):
		httpx.WriteErr(w, http.StatusBadGateway, "browser_start_failed", "Chromium could not be started")
	case errors.Is(err, errBrowserNavigate):
		httpx.WriteErr(w, http.StatusBadGateway, "browser_navigation_failed", "Chromium could not start navigation")
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_browser_target", err.Error())
	}
}

func handleBrowserPageGet(w http.ResponseWriter, r *http.Request) {
	resp, ok := workspaceBrowserManager.Get(r.PathValue("id"))
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "browser_not_found", "browser page does not exist")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func handleBrowserPageDelete(w http.ResponseWriter, r *http.Request) {
	workspaceBrowserManager.Delete(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

type browserOutbound struct {
	messageType int
	data        []byte
	closeCode   int
	closeReason string
}

type browserViewer struct {
	conn      *websocket.Conn
	page      *browserPage
	control   chan browserOutbound
	done      chan struct{}
	closeOnce sync.Once
}

func handleBrowserWebSocket(w http.ResponseWriter, r *http.Request) {
	manager := workspaceBrowserManager
	id := r.URL.Query().Get("id")
	if id == "" || len(id) > 128 {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_browser_id", "browser id is required")
		return
	}
	p, err := manager.reserve(id)
	if err != nil {
		if errors.Is(err, errBrowserAttached) || errors.Is(err, errBrowserViewerLimit) {
			httpx.WriteErr(w, http.StatusConflict, "browser_already_attached", "browser page already has a viewer")
		} else {
			httpx.WriteErr(w, http.StatusNotFound, "browser_not_found", "browser page does not exist")
		}
		return
	}
	conn, err := browserUpgrader.Upgrade(w, r, nil)
	if err != nil {
		manager.releaseReservation(p)
		return
	}
	v := &browserViewer{conn: conn, page: p, control: make(chan browserOutbound, 64), done: make(chan struct{})}
	if !manager.attach(p, v) {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "page no longer exists"), time.Now().Add(time.Second))
		_ = conn.Close()
		return
	}
	defer func() {
		v.stop()
		manager.detach(p, v)
		_ = conn.Close()
	}()
	conn.SetReadLimit(browserWSMaxMessage)

	ready := p.readyMessage()
	v.enqueueText(ready)
	writerDone := make(chan struct{})
	go func() {
		v.writeLoop()
		close(writerDone)
	}()
	if err := p.startScreencast(); err != nil {
		v.enqueueTextAndClose(mustBrowserJSON(map[string]any{"type": "state", "state": "crashed"}), websocketCloseGoingAway, "screencast failed")
		<-writerDone
		return
	}

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if mt != websocket.TextMessage {
			v.protocolError("bad_message", "browser control messages must be text JSON")
			continue
		}
		if !v.handleControl(data) {
			break
		}
	}
	v.stop()
	<-writerDone
}

func (p *browserPage) readyMessage() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return mustBrowserJSON(map[string]any{
		"type": "ready", "version": 1, "url": p.url, "title": p.title,
		"width": p.viewport.Width, "height": p.viewport.Height,
	})
}

func (v *browserViewer) enqueueText(data []byte) {
	select {
	case v.control <- browserOutbound{messageType: websocket.TextMessage, data: data}:
	default:
		// Console/log events are bounded and intentionally not persisted. Preserve
		// navigation/state best-effort rather than letting a slow viewer stop CDP.
	}
}

func (v *browserViewer) enqueueTextAndClose(data []byte, code int, reason string) {
	select {
	case v.control <- browserOutbound{messageType: websocket.TextMessage, data: data, closeCode: code, closeReason: reason}:
	default:
		v.stop()
	}
}

func (v *browserViewer) enqueueClose(code int, reason string) {
	select {
	case v.control <- browserOutbound{messageType: websocket.CloseMessage, closeCode: code, closeReason: reason}:
	default:
		v.stop()
	}
}

func (v *browserViewer) stop() {
	v.closeOnce.Do(func() {
		close(v.done)
		if v.conn != nil {
			_ = v.conn.Close()
		}
	})
}

func (v *browserViewer) writeLoop() {
	ticker := time.NewTicker(v.page.manager.config.FrameInterval)
	defer ticker.Stop()
	for {
		// Control events take priority over lossy frames.
		select {
		case out := <-v.control:
			if !v.writeOutbound(out) {
				return
			}
			continue
		default:
		}
		select {
		case <-v.done:
			return
		case out := <-v.control:
			if !v.writeOutbound(out) {
				return
			}
		case <-ticker.C:
			select {
			case frame := <-v.page.latestFrame:
				_ = v.conn.SetWriteDeadline(time.Now().Add(browserWSWriteTimeout))
				if err := v.conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
					v.stop()
					return
				}
			default:
			}
		}
	}
}

func (v *browserViewer) writeOutbound(out browserOutbound) bool {
	_ = v.conn.SetWriteDeadline(time.Now().Add(browserWSWriteTimeout))
	if out.messageType == websocket.CloseMessage {
		_ = v.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(out.closeCode, out.closeReason))
		v.stop()
		return false
	}
	if err := v.conn.WriteMessage(out.messageType, out.data); err != nil {
		v.stop()
		return false
	}
	if out.closeCode != 0 {
		_ = v.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(out.closeCode, out.closeReason))
		v.stop()
		return false
	}
	return true
}

func (v *browserViewer) handleControl(data []byte) bool {
	var envelope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Type == "" {
		v.protocolError("bad_json", "invalid browser control message")
		return true
	}
	switch envelope.Type {
	case "viewport":
		var msg struct {
			Type   string  `json:"type"`
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
			Zoom   float64 `json:"zoom"`
		}
		if json.Unmarshal(data, &msg) != nil {
			v.protocolError("bad_viewport", "invalid viewport")
			return true
		}
		pane, err := normalizeBrowserViewport(browserViewportRequest{Width: msg.Width, Height: msg.Height, DeviceScaleFactor: 1})
		if err != nil {
			v.protocolError("bad_viewport", err.Error())
			return true
		}
		zoom := normalizeBrowserZoom(msg.Zoom)
		layout := zoomedLayout(pane, zoom)
		v.page.mu.Lock()
		unchanged := v.page.pane == pane && v.page.zoom == zoom
		v.page.pane = pane
		v.page.zoom = zoom
		v.page.viewport = layout
		v.page.mu.Unlock()
		if unchanged {
			// Console re-sends its current size right after attaching. Restarting the
			// screencast for an identical viewport only churns the cast and risks
			// mixing old and new frames, so skip the redundant device-metrics and
			// stop/start round-trip entirely.
			return true
		}
		if !v.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": layout.Width, "height": layout.Height, "deviceScaleFactor": 1, "mobile": false}, nil) {
			return true
		}
		// A pinch zoom lays the page out smaller than the pane, so pointer
		// coordinates no longer live in the space the viewer asked for.
		v.enqueueText(mustBrowserJSON(map[string]any{"type": "viewport", "width": layout.Width, "height": layout.Height}))
		v.page.restartScreencastForResize()
	case "mouse":
		v.handleMouse(data)
	case "wheel":
		v.handleWheel(data)
	case "key":
		v.handleKey(data)
	case "text":
		var msg struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(data, &msg) != nil || msg.Text == "" || len(msg.Text) > browserMaxTextBytes || !utf8.ValidString(msg.Text) {
			v.protocolError("bad_text", "text must be non-empty valid UTF-8")
			return true
		}
		v.call("Input.insertText", map[string]any{"text": msg.Text}, nil)
	case "copy":
		// The page runs in the container, so its clipboard is unreachable for the
		// user; hand the selection back over the wire instead (docs/log/53 §53.7).
		var result struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if v.call("Runtime.evaluate", map[string]any{"expression": browserSelectionExpression, "returnByValue": true}, &result) {
			v.enqueueText(mustBrowserJSON(map[string]any{
				"type": "clipboard",
				"text": truncateBrowserText(result.Result.Value, browserMaxSelectionBytes),
			}))
		}
	case "navigate":
		var msg struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(data, &msg) != nil {
			v.protocolError("bad_navigation", "invalid navigation")
			return true
		}
		v.page.mu.Lock()
		base := v.page.url
		v.page.mu.Unlock()
		target, err := browserPathURL(base, msg.Path)
		if err != nil {
			v.protocolError("bad_navigation", err.Error())
			return true
		}
		v.page.setState("loading")
		var result struct {
			ErrorText string `json:"errorText"`
		}
		if v.call("Page.navigate", map[string]any{"url": target}, &result) && result.ErrorText != "" {
			v.page.setState("target-unreachable")
		}
	case "reload":
		var msg struct {
			IgnoreCache bool `json:"ignoreCache"`
		}
		if json.Unmarshal(data, &msg) != nil {
			v.protocolError("bad_reload", "invalid reload")
			return true
		}
		v.page.setState("loading")
		v.call("Page.reload", map[string]any{"ignoreCache": msg.IgnoreCache}, nil)
	case "history":
		v.handleHistory(data)
	case "visibility":
		var msg struct {
			Visible *bool `json:"visible"`
		}
		if json.Unmarshal(data, &msg) != nil || msg.Visible == nil {
			v.protocolError("bad_visibility", "visible must be boolean")
			return true
		}
		v.setVisible(*msg.Visible)
	default:
		v.protocolError("unknown_type", "unknown browser control type")
	}
	return true
}

func (v *browserViewer) handleMouse(data []byte) {
	var msg struct {
		Event      string  `json:"event"`
		X          float64 `json:"x"`
		Y          float64 `json:"y"`
		Button     string  `json:"button"`
		Buttons    int     `json:"buttons"`
		Modifiers  int     `json:"modifiers"`
		ClickCount int     `json:"clickCount"`
	}
	if json.Unmarshal(data, &msg) != nil || !v.validPoint(msg.X, msg.Y) || !validModifiers(msg.Modifiers) || msg.Buttons < 0 || msg.Buttons > 31 || msg.ClickCount < 0 || msg.ClickCount > 3 {
		v.protocolError("bad_mouse", "invalid mouse event")
		return
	}
	eventType := map[string]string{"move": "mouseMoved", "down": "mousePressed", "up": "mouseReleased"}[msg.Event]
	if eventType == "" || !validMouseButton(msg.Button) {
		v.protocolError("bad_mouse", "unsupported mouse event or button")
		return
	}
	params := map[string]any{
		"type": eventType, "x": msg.X, "y": msg.Y, "button": msg.Button,
		"buttons": msg.Buttons, "modifiers": msg.Modifiers, "clickCount": msg.ClickCount,
	}
	// Only the pointer-rate events skip the reply (see post): a press, a release
	// or a keystroke is one event per user action and can afford the round trip,
	// which keeps anything read straight afterwards consistent with it.
	if eventType == "mouseMoved" {
		v.post("Input.dispatchMouseEvent", params)
		return
	}
	v.call("Input.dispatchMouseEvent", params, nil)
}

func (v *browserViewer) handleWheel(data []byte) {
	var raw struct {
		X         float64 `json:"x"`
		Y         float64 `json:"y"`
		DeltaX    float64 `json:"deltaX"`
		DeltaY    float64 `json:"deltaY"`
		Modifiers int     `json:"modifiers"`
	}
	if json.Unmarshal(data, &raw) != nil || !v.validPoint(raw.X, raw.Y) || math.IsNaN(raw.DeltaX) || math.IsInf(raw.DeltaX, 0) || math.IsNaN(raw.DeltaY) || math.IsInf(raw.DeltaY, 0) || !validModifiers(raw.Modifiers) {
		v.protocolError("bad_wheel", "invalid wheel event")
		return
	}
	v.post("Input.dispatchMouseEvent", map[string]any{
		"type": "mouseWheel", "x": raw.X, "y": raw.Y, "deltaX": raw.DeltaX, "deltaY": raw.DeltaY, "modifiers": raw.Modifiers,
	})
}

func (v *browserViewer) handleKey(data []byte) {
	var msg struct {
		Event     string `json:"event"`
		Key       string `json:"key"`
		Code      string `json:"code"`
		Modifiers int    `json:"modifiers"`
		Repeat    bool   `json:"repeat"`
	}
	if json.Unmarshal(data, &msg) != nil || (msg.Event != "down" && msg.Event != "up") || msg.Key == "" || len(msg.Key) > 128 || len(msg.Code) > 128 || !utf8.ValidString(msg.Key) || !validModifiers(msg.Modifiers) {
		v.protocolError("bad_key", "invalid key event")
		return
	}
	v.call("Input.dispatchKeyEvent",
		browserKeyEventParams(msg.Event == "down", msg.Key, msg.Code, msg.Modifiers, msg.Repeat), nil)
}

func (v *browserViewer) handleHistory(data []byte) {
	var msg struct {
		Direction string `json:"direction"`
	}
	if json.Unmarshal(data, &msg) != nil || (msg.Direction != "back" && msg.Direction != "forward") {
		v.protocolError("bad_history", "direction must be back or forward")
		return
	}
	var history struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if !v.call("Page.getNavigationHistory", nil, &history) {
		return
	}
	index := history.CurrentIndex
	if msg.Direction == "back" {
		index--
	} else {
		index++
	}
	if index < 0 || index >= len(history.Entries) {
		return
	}
	v.page.setState("loading")
	v.call("Page.navigateToHistoryEntry", map[string]any{"entryId": history.Entries[index].ID}, nil)
}

func (v *browserViewer) setVisible(visible bool) {
	v.page.mu.Lock()
	if v.page.viewer != v {
		v.page.mu.Unlock()
		return
	}
	v.page.visible = visible
	if visible && v.page.expiry != nil {
		v.page.expiry.Stop()
		v.page.expiry = nil
	}
	v.page.mu.Unlock()
	if visible {
		if err := v.page.startScreencast(); err != nil {
			v.protocolError("screencast_failed", "could not resume browser rendering")
		}
	} else {
		v.page.stopScreencast()
		v.page.manager.scheduleExpiry(v.page)
	}
}

func (v *browserViewer) call(method string, params, result any) bool {
	v.page.manager.mu.Lock()
	cdp := v.page.manager.cdp
	owned := v.page.manager.pages[v.page.id] == v.page
	v.page.manager.mu.Unlock()
	if cdp == nil || !owned || v.page.manager.call(cdp, v.page.sessionID, method, params, result) != nil {
		v.protocolError("browser_command_failed", "browser command failed")
		return false
	}
	return true
}

// post dispatches an INPUT command without waiting for Chromium's reply (see
// browserCDPCore.Send). Control messages are handled on the viewer's read loop,
// so waiting per finger movement is what made a swipe queue up behind Chromium's
// frame-paced acks. A page that has gone away still refuses input.
func (v *browserViewer) post(method string, params any) {
	v.page.manager.mu.Lock()
	cdp := v.page.manager.cdp
	owned := v.page.manager.pages[v.page.id] == v.page
	v.page.manager.mu.Unlock()
	if cdp == nil || !owned || cdp.Send(method, params, v.page.sessionID) != nil {
		v.protocolError("browser_command_failed", "browser command failed")
	}
}

func (v *browserViewer) protocolError(code, message string) {
	v.enqueueText(mustBrowserJSON(map[string]any{"type": "protocol-error", "code": code, "message": message}))
}

func (v *browserViewer) validPoint(x, y float64) bool {
	if x < 0 || y < 0 || math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return false
	}
	v.page.mu.Lock()
	viewport := v.page.viewport
	v.page.mu.Unlock()
	return x <= float64(viewport.Width) && y <= float64(viewport.Height)
}

func validModifiers(v int) bool { return v >= 0 && v <= 15 }
func validMouseButton(v string) bool {
	switch v {
	case "none", "left", "middle", "right", "back", "forward":
		return true
	default:
		return false
	}
}

func mustBrowserJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
