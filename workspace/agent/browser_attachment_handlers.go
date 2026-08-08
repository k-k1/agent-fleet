package main

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

func handleBrowserAttachTargets(w http.ResponseWriter, r *http.Request) {
	port, err := parseCDPPort(r.URL.Query().Get("port"))
	if err != nil {
		writeBrowserAttachmentError(w, err)
		return
	}
	resp, err := workspaceBrowserAttachmentManager.Discover(port)
	if err != nil {
		writeBrowserAttachmentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func handleBrowserAttachmentCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req browserAttachmentCreateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_browser_attachment", "invalid browser attachment request")
		return
	}
	// label is MCP-only metadata. Keep it out of the fixed public REST body;
	// base64 also keeps arbitrary UTF-8 out of the internal HTTP header value.
	if encoded := r.Header.Get(browserAttachmentLabelHeader); encoded != "" {
		label, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_browser_attachment", "invalid browser attachment label")
			return
		}
		req.Label = string(label)
	}
	resp, err := workspaceBrowserAttachmentManager.Create(req)
	if err != nil {
		writeBrowserAttachmentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, resp)
}

func handleBrowserAttachmentList(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, workspaceBrowserAttachmentManager.List())
}

func handleBrowserAttachmentGet(w http.ResponseWriter, r *http.Request) {
	resp, err := workspaceBrowserAttachmentManager.Get(r.PathValue("id"))
	if err != nil {
		writeBrowserAttachmentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func handleBrowserAttachmentDelete(w http.ResponseWriter, r *http.Request) {
	workspaceBrowserAttachmentManager.Delete(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func handleBrowserAttachmentHandoff(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req browserAttachmentHandoffRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_browser_handoff", "invalid browser handoff request")
		return
	}
	resp, err := workspaceBrowserAttachmentManager.UpdateHandoff(r.PathValue("id"), req)
	if err != nil {
		writeBrowserAttachmentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func handleBrowserAttachmentControlMode(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var req browserAttachmentControlModeRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_control_mode", "invalid browser control mode request")
		return
	}
	resp, err := workspaceBrowserAttachmentManager.SetControlMode(r.PathValue("id"), req.ControlMode)
	if err != nil {
		writeBrowserAttachmentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func handleBrowserAttachmentHandoffResult(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var req browserAttachmentHandoffResultRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_handoff_result", "invalid handoff result")
		return
	}
	resp, err := workspaceBrowserAttachmentManager.SetHandoffResult(r.PathValue("id"), req.Result)
	if err != nil {
		writeBrowserAttachmentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func writeBrowserAttachmentError(w http.ResponseWriter, err error) {
	apiErr := asAttachmentAPIError(err)
	httpx.WriteErr(w, apiErr.Status, apiErr.Code, apiErr.Message)
}

func parseCDPPort(value string) (int, error) {
	if value == "" {
		return 0, attachmentError(http.StatusBadRequest, "bad_cdp_port", "port is required", nil)
	}
	port := 0
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, attachmentError(http.StatusBadRequest, "bad_cdp_port", "port must be an integer", nil)
		}
		port = port*10 + int(c-'0')
		if port > 65535 {
			break
		}
	}
	if err := validateCDPPort(port); err != nil {
		return 0, err
	}
	return port, nil
}

func handleBrowserAttachmentWebSocket(w http.ResponseWriter, r *http.Request) {
	m := workspaceBrowserAttachmentManager
	a, err := m.reserveViewer(r.URL.Query().Get("id"))
	if err != nil {
		writeBrowserAttachmentError(w, err)
		return
	}
	conn, err := browserUpgrader.Upgrade(w, r, nil)
	if err != nil {
		m.releaseViewerReservation(a)
		return
	}
	v := &browserAttachmentViewer{
		conn: conn, attachment: a, control: make(chan browserOutbound, 64), done: make(chan struct{}),
	}
	if !m.attachViewer(a, v) {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "attachment no longer exists"),
			nowPlusSecond())
		_ = conn.Close()
		return
	}
	defer func() {
		v.stop()
		m.detachViewer(a, v)
		_ = conn.Close()
	}()
	conn.SetReadLimit(browserWSMaxMessage)
	v.enqueueText(a.readyMessage())
	writerDone := make(chan struct{})
	go func() { v.writeLoop(); close(writerDone) }()
	if err := a.startScreencast(); err != nil {
		v.enqueueTextAndClose(mustBrowserJSON(map[string]any{"type": "state", "state": attachmentStateDisconnected}), websocketCloseGoingAway, "screencast failed")
		<-writerDone
		return
	}
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if mt != websocket.TextMessage {
			v.protocolError("bad_message", "browser attachment control messages must be text JSON")
			continue
		}
		if !v.handleControl(data) {
			break
		}
	}
	v.stop()
	<-writerDone
}

func nowPlusSecond() time.Time { return time.Now().Add(time.Second) }

type browserAttachmentViewer struct {
	conn       *websocket.Conn
	attachment *browserAttachment
	control    chan browserOutbound
	done       chan struct{}
	closeOnce  sync.Once
}

func (m *browserAttachmentManager) reserveViewer(id string) (*browserAttachment, error) {
	a, err := m.lookupActive(id)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reserved || a.viewer != nil {
		return nil, attachmentError(http.StatusConflict, "browser_already_attached", "browser attachment already has a viewer", nil)
	}
	if !workspaceBrowserViewerLeases.acquire(browserAttachmentViewerLease(a.id)) {
		return nil, attachmentError(http.StatusConflict, "browser_already_attached", "workspace browser viewer limit reached", nil)
	}
	a.reserved = true
	if a.expiry != nil {
		a.expiry.Stop()
		a.expiry = nil
		a.expiresAt = time.Time{}
	}
	return a, nil
}

func (m *browserAttachmentManager) releaseViewerReservation(a *browserAttachment) {
	a.mu.Lock()
	a.reserved = false
	if !a.terminal {
		a.armExpiryLocked(m.config.ViewerGrace)
	}
	a.mu.Unlock()
	workspaceBrowserViewerLeases.release(browserAttachmentViewerLease(a.id))
}

func (m *browserAttachmentManager) attachViewer(a *browserAttachment, v *browserAttachmentViewer) bool {
	if !m.owns(a) {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.terminal || !a.reserved || a.viewer != nil {
		a.reserved = false
		workspaceBrowserViewerLeases.release(browserAttachmentViewerLease(a.id))
		return false
	}
	a.reserved = false
	a.viewer = v
	a.visible = true
	if a.state != attachmentStateUnsupportedURL {
		a.state = attachmentStateViewerOpen
	}
	return true
}

func (m *browserAttachmentManager) detachViewer(a *browserAttachment, v *browserAttachmentViewer) {
	a.mu.Lock()
	if a.viewer != v {
		a.mu.Unlock()
		return
	}
	a.viewer = nil
	a.visible = false
	if !a.terminal && a.state != attachmentStateUnsupportedURL {
		a.state = attachmentStateAttached
	}
	if !a.terminal {
		ttl := m.config.ViewerGrace
		if a.handoff != nil && a.handoff.Result == "pending" {
			ttl = m.config.HandoffTTL
		}
		a.armExpiryLocked(ttl)
	}
	a.mu.Unlock()
	workspaceBrowserViewerLeases.release(browserAttachmentViewerLease(a.id))
	a.stopScreencast()
}

func (a *browserAttachment) readyMessage() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return mustBrowserJSON(map[string]any{
		"type": "ready", "version": 1, "state": a.state, "url": a.url, "title": a.displayTitleLocked(),
		"width": a.viewport.Width, "height": a.viewport.Height,
		"controlMode": a.controlMode, "handoff": a.handoff,
	})
}

func (v *browserAttachmentViewer) enqueueText(data []byte) {
	select {
	case v.control <- browserOutbound{messageType: websocket.TextMessage, data: data}:
	default:
	}
}

func (v *browserAttachmentViewer) enqueueTextAndClose(data []byte, code int, reason string) {
	select {
	case v.control <- browserOutbound{messageType: websocket.TextMessage, data: data, closeCode: code, closeReason: reason}:
	default:
		v.stop()
	}
}

func (v *browserAttachmentViewer) enqueueClose(code int, reason string) {
	select {
	case v.control <- browserOutbound{messageType: websocket.CloseMessage, closeCode: code, closeReason: reason}:
	default:
		v.stop()
	}
}

func (v *browserAttachmentViewer) stop() {
	v.closeOnce.Do(func() {
		close(v.done)
		if v.conn != nil {
			_ = v.conn.Close()
		}
	})
}

func (v *browserAttachmentViewer) writeLoop() {
	ticker := time.NewTicker(v.attachment.manager.config.FrameInterval)
	defer ticker.Stop()
	for {
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
			case frame := <-v.attachment.latestFrame:
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

func (v *browserAttachmentViewer) writeOutbound(out browserOutbound) bool {
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

func (v *browserAttachmentViewer) handleControl(data []byte) bool {
	var envelope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Type == "" {
		v.protocolError("bad_json", "invalid browser attachment control message")
		return true
	}
	if envelope.Type == "visibility" {
		var msg struct {
			Visible *bool `json:"visible"`
		}
		if json.Unmarshal(data, &msg) != nil || msg.Visible == nil {
			v.protocolError("bad_visibility", "visible must be boolean")
			return true
		}
		v.setVisible(*msg.Visible)
		return true
	}
	if envelope.Type == "viewport" {
		v.handleViewport(data)
		return true
	}
	// Copying what the user can already read is not "operating the page", so it
	// is allowed in view-only — the whole point of the pane is that they can read
	// it. Only a locked attachment (screencast stopped) refuses.
	if envelope.Type == "copy" {
		v.handleCopySelection()
		return true
	}
	v.attachment.mu.Lock()
	mode := v.attachment.controlMode
	state := v.attachment.state
	v.attachment.mu.Unlock()
	if mode != attachmentControlUser || state == attachmentStateUnsupportedURL {
		v.protocolError("input_not_allowed", "browser attachment is not in user-control mode")
		return true
	}
	switch envelope.Type {
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
	case "reload":
		var msg struct {
			IgnoreCache bool `json:"ignoreCache"`
		}
		if json.Unmarshal(data, &msg) != nil {
			v.protocolError("bad_reload", "invalid reload")
			return true
		}
		v.call("Page.reload", map[string]any{"ignoreCache": msg.IgnoreCache}, nil)
	case "history":
		v.handleHistory(data)
	default:
		// In particular, navigate is intentionally absent from this namespace.
		v.protocolError("unknown_type", "unknown browser attachment control type")
	}
	return true
}

func (v *browserAttachmentViewer) handleViewport(data []byte) {
	var msg struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
		Fit    bool    `json:"fit"`
		Zoom   float64 `json:"zoom"`
	}
	if json.Unmarshal(data, &msg) != nil {
		v.protocolError("bad_viewport", "invalid viewport")
		return
	}
	pane, err := normalizeBrowserViewport(browserViewportRequest{Width: msg.Width, Height: msg.Height, DeviceScaleFactor: 1})
	if err != nil {
		v.protocolError("bad_viewport", err.Error())
		return
	}
	zoom := normalizeBrowserZoom(msg.Zoom)
	v.attachment.mu.Lock()
	// base is the layout BEFORE the pinch zoom (the pane, or the fitted width).
	// Keeping it lets a pinch skip the re-measure below: measuring the content of
	// an already zoomed page would fold each zoom into the next one and the view
	// would drift with every gesture.
	sameShape := v.attachment.pane == pane && v.attachment.fit == msg.Fit && v.attachment.base.Width > 0
	unchanged := sameShape && v.attachment.zoom == zoom
	base := v.attachment.base
	v.attachment.pane = pane
	v.attachment.fit = msg.Fit
	v.attachment.zoom = zoom
	v.attachment.mu.Unlock()
	if unchanged {
		return
	}
	if !sameShape {
		if !v.applyMetrics(pane) {
			return
		}
		base = pane
		if msg.Fit {
			if fitted, ok := v.measureFit(pane); ok {
				base = fitted
			}
		}
		v.attachment.mu.Lock()
		v.attachment.base = base
		v.attachment.viewport = pane
		v.attachment.mu.Unlock()
	}
	layout := zoomedLayout(base, zoom)
	if !v.applyMetrics(layout) {
		// Leave the metrics that did apply in place rather than a half-applied
		// zoom; viewport already names the layout those metrics describe.
		return
	}
	v.attachment.mu.Lock()
	v.attachment.viewport = layout
	v.attachment.mu.Unlock()
	// Pointer coordinates are in layout space, so the viewer has to be told the
	// size it must map into — it only ever asked for the pane's size.
	v.enqueueText(mustBrowserJSON(map[string]any{"type": "viewport", "width": layout.Width, "height": layout.Height}))
	v.attachment.restartScreencast()
}

// applyMetrics emulates a layout viewport of the given size.
//
// Deliberately WITHOUT setDeviceMetricsOverride's `scale`: measured on Chrome
// 151, that parameter shrinks the page INSIDE an unchanged surface — the page
// ends up drawn small in the top-left corner with the rest blank — rather than
// shrinking the produced image. The screencast's own maxWidth/maxHeight already
// scales the frame down to the pane (a 1240x1503 layout arrives as a 660x800
// frame), which is the scaling we actually want.
//
// deviceScaleFactor stays 1 for the same kind of reason: the screencast ignores
// it (measured — see zoomedLayout), so raising it for a zoomed layout would only
// make the compositor render pixels no frame ever carries.
func (v *browserAttachmentViewer) applyMetrics(vp browserViewport) bool {
	return v.call("Emulation.setDeviceMetricsOverride",
		map[string]any{"width": vp.Width, "height": vp.Height, "deviceScaleFactor": 1, "mobile": false}, nil)
}

// measureFit answers the layout viewport that makes the page's own content fit
// the pane, so the image can be scaled back down to it. A desktop site with a
// min-width otherwise renders clipped with its own horizontal scrollbar inside a
// pane that is simply narrower than the site was designed for.
//
// The aspect ratio is kept EXACTLY equal to the pane's, so the canvas (which
// stretches the frame to fill the pane) never distorts and pointer coordinates
// stay a plain linear map. One pass only: re-measuring after a reflow could
// oscillate, and the second guess is not better than the first. The caller must
// have the pane's own metrics applied — that is the layout being measured.
func (v *browserAttachmentViewer) measureFit(pane browserViewport) (browserViewport, bool) {
	var metrics struct {
		CSSContentSize struct {
			Width float64 `json:"width"`
		} `json:"cssContentSize"`
		ContentSize struct {
			Width float64 `json:"width"`
		} `json:"contentSize"`
	}
	if !v.call("Page.getLayoutMetrics", nil, &metrics) {
		return browserViewport{}, false
	}
	content := metrics.CSSContentSize.Width
	if content <= 0 {
		content = metrics.ContentSize.Width
	}
	return fitLayoutViewport(pane, content)
}

// handleCopySelection hands the page's current selection back to the viewer so
// the Console can put it on the USER's clipboard. Ctrl+C inside the pane cannot
// do it: the keystroke would copy into the container's clipboard, which nobody
// can reach. The expression is fixed and read-only, and the text is never
// logged or persisted — same rule as the page title and URL (docs/53 §53.10).
func (v *browserAttachmentViewer) handleCopySelection() {
	v.attachment.mu.Lock()
	locked := v.attachment.controlMode == attachmentControlLocked
	unsupported := v.attachment.state == attachmentStateUnsupportedURL
	v.attachment.mu.Unlock()
	if locked || unsupported {
		v.protocolError("input_not_allowed", "browser attachment is locked")
		return
	}
	var result struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if !v.call("Runtime.evaluate", map[string]any{
		"expression":    browserSelectionExpression,
		"returnByValue": true,
	}, &result) {
		return
	}
	v.enqueueText(mustBrowserJSON(map[string]any{
		"type": "clipboard",
		"text": truncateBrowserText(result.Result.Value, browserMaxSelectionBytes),
	}))
}

func (v *browserAttachmentViewer) handleMouse(data []byte) {
	var msg struct {
		Event      string  `json:"event"`
		X          float64 `json:"x"`
		Y          float64 `json:"y"`
		Button     string  `json:"button"`
		Buttons    int     `json:"buttons"`
		Modifiers  int     `json:"modifiers"`
		ClickCount int     `json:"clickCount"`
	}
	if json.Unmarshal(data, &msg) != nil || !v.validPoint(msg.X, msg.Y) || !validModifiers(msg.Modifiers) ||
		msg.Buttons < 0 || msg.Buttons > 31 || msg.ClickCount < 0 || msg.ClickCount > 3 {
		v.protocolError("bad_mouse", "invalid mouse event")
		return
	}
	eventType := map[string]string{"move": "mouseMoved", "down": "mousePressed", "up": "mouseReleased"}[msg.Event]
	if eventType == "" || !validMouseButton(msg.Button) {
		v.protocolError("bad_mouse", "unsupported mouse event or button")
		return
	}
	v.call("Input.dispatchMouseEvent", map[string]any{"type": eventType, "x": msg.X, "y": msg.Y, "button": msg.Button, "buttons": msg.Buttons, "modifiers": msg.Modifiers, "clickCount": msg.ClickCount}, nil)
}

func (v *browserAttachmentViewer) handleWheel(data []byte) {
	var msg struct {
		X         float64 `json:"x"`
		Y         float64 `json:"y"`
		DeltaX    float64 `json:"deltaX"`
		DeltaY    float64 `json:"deltaY"`
		Modifiers int     `json:"modifiers"`
	}
	if json.Unmarshal(data, &msg) != nil || !v.validPoint(msg.X, msg.Y) || math.IsNaN(msg.DeltaX) || math.IsInf(msg.DeltaX, 0) || math.IsNaN(msg.DeltaY) || math.IsInf(msg.DeltaY, 0) || !validModifiers(msg.Modifiers) {
		v.protocolError("bad_wheel", "invalid wheel event")
		return
	}
	v.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": msg.X, "y": msg.Y, "deltaX": msg.DeltaX, "deltaY": msg.DeltaY, "modifiers": msg.Modifiers}, nil)
}

func (v *browserAttachmentViewer) handleKey(data []byte) {
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

func (v *browserAttachmentViewer) handleHistory(data []byte) {
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
	if index >= 0 && index < len(history.Entries) {
		v.call("Page.navigateToHistoryEntry", map[string]any{"entryId": history.Entries[index].ID}, nil)
	}
}

func (v *browserAttachmentViewer) setVisible(visible bool) {
	a := v.attachment
	a.mu.Lock()
	if a.viewer != v {
		a.mu.Unlock()
		return
	}
	a.visible = visible
	mode := a.controlMode
	if visible && a.expiry != nil {
		a.expiry.Stop()
		a.expiry = nil
		a.expiresAt = time.Time{}
	}
	if !a.terminal && a.state != attachmentStateUnsupportedURL {
		if visible {
			a.state = attachmentStateViewerOpen
		} else {
			a.state = attachmentStateAttached
		}
	}
	if !visible && !a.terminal {
		ttl := a.manager.config.ViewerGrace
		if a.handoff != nil && a.handoff.Result == "pending" {
			ttl = a.manager.config.HandoffTTL
		}
		a.armExpiryLocked(ttl)
	}
	a.mu.Unlock()
	if visible && mode != attachmentControlLocked {
		_ = a.startScreencast()
	} else {
		a.stopScreencast()
	}
}

func (v *browserAttachmentViewer) call(method string, params, result any) bool {
	a := v.attachment
	a.mu.Lock()
	terminal := a.terminal
	a.mu.Unlock()
	if terminal || a.manager.call(a.cdp, a.sessionID, method, params, result) != nil {
		v.protocolError("browser_command_failed", "browser attachment command failed")
		if !terminal {
			go a.manager.markTerminal(a, attachmentStateDisconnected, "Chromium disconnected")
		}
		return false
	}
	return true
}

func (v *browserAttachmentViewer) protocolError(code, message string) {
	v.enqueueText(mustBrowserJSON(map[string]any{"type": "protocol-error", "code": code, "message": message}))
}

func (v *browserAttachmentViewer) validPoint(x, y float64) bool {
	if x < 0 || y < 0 || math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return false
	}
	v.attachment.mu.Lock()
	viewport := v.attachment.viewport
	v.attachment.mu.Unlock()
	return x <= float64(viewport.Width) && y <= float64(viewport.Height)
}
