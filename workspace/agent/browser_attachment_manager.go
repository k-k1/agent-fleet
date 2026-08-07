package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

type browserAttachmentManagerConfig struct {
	UnviewedTTL      time.Duration
	ViewerGrace      time.Duration
	HandoffTTL       time.Duration
	DiscoveryTimeout time.Duration
	CommandTimeout   time.Duration
	FrameInterval    time.Duration
	JPEGQuality      int
	Discover         func(int, time.Duration) (cdpDiscovery, error)
	Dial             func(context.Context, int, string) (browserCDP, error)
}

type browserAttachmentManager struct {
	mu          sync.Mutex
	createMu    sync.Mutex
	config      browserAttachmentManagerConfig
	attachments map[string]*browserAttachment
	targets     map[string]string
	closed      bool
}

type browserAttachment struct {
	manager   *browserAttachmentManager
	id        string
	createdAt time.Time
	targetKey string
	targetID  string
	sessionID string
	cdp       browserCDP
	// viewport is the LAYOUT viewport pointer coordinates are expressed in; with
	// zoom-to-fit it is wider than pane, which is what the viewer actually shows.
	viewport browserViewport
	pane     browserViewport
	fit      bool

	mu          sync.Mutex
	state       string
	title       string
	label       string
	url         string
	controlMode string
	handoff     *browserAttachmentHandoffResponse
	viewer      *browserAttachmentViewer
	reserved    bool
	visible     bool
	terminal    bool
	expiresAt   time.Time
	expiry      *time.Timer

	latestFrame chan []byte
	frameEvents chan browserScreencastFrame
	frameStop   chan struct{}
	frameOnce   sync.Once
	castMu      sync.Mutex
	casting     bool
	castGen     atomic.Uint64
	castEpoch   atomic.Uint64
}

func defaultBrowserAttachmentManagerConfig() browserAttachmentManagerConfig {
	fps := browserConfigInt("AF_BROWSER_MAX_FPS", 12, 1, 30)
	return browserAttachmentManagerConfig{
		UnviewedTTL:      10 * time.Minute,
		ViewerGrace:      60 * time.Second,
		HandoffTTL:       30 * time.Minute,
		DiscoveryTimeout: 3 * time.Second,
		CommandTimeout:   5 * time.Second,
		FrameInterval:    time.Second / time.Duration(fps),
		JPEGQuality:      browserConfigInt("AF_BROWSER_JPEG_QUALITY", 70, 1, 100),
		Discover:         discoverCDPTargets,
		Dial:             dialWebSocketCDP,
	}
}

var workspaceBrowserAttachmentManager = newBrowserAttachmentManager(defaultBrowserAttachmentManagerConfig())

func newBrowserAttachmentManager(config browserAttachmentManagerConfig) *browserAttachmentManager {
	if config.UnviewedTTL <= 0 {
		config.UnviewedTTL = 10 * time.Minute
	}
	if config.ViewerGrace <= 0 {
		config.ViewerGrace = time.Minute
	}
	if config.HandoffTTL <= 0 {
		config.HandoffTTL = 30 * time.Minute
	}
	if config.DiscoveryTimeout <= 0 {
		config.DiscoveryTimeout = 3 * time.Second
	}
	if config.CommandTimeout <= 0 {
		config.CommandTimeout = 5 * time.Second
	}
	if config.FrameInterval <= 0 {
		config.FrameInterval = time.Second / 12
	}
	if config.JPEGQuality < 1 || config.JPEGQuality > 100 {
		config.JPEGQuality = 70
	}
	if config.Discover == nil {
		config.Discover = discoverCDPTargets
	}
	if config.Dial == nil {
		config.Dial = dialWebSocketCDP
	}
	return &browserAttachmentManager{
		config: config, attachments: make(map[string]*browserAttachment), targets: make(map[string]string),
	}
}

func (m *browserAttachmentManager) Discover(port int) (browserAttachTargetsResponse, error) {
	discovery, err := m.config.Discover(port, m.config.DiscoveryTimeout)
	if err != nil {
		return browserAttachTargetsResponse{}, err
	}
	return browserAttachTargetsResponse{Targets: discovery.Targets, BrowserID: discovery.BrowserID}, nil
}

func (m *browserAttachmentManager) Create(req browserAttachmentCreateRequest) (browserAttachmentResponse, error) {
	if err := validateCDPPort(req.Port); err != nil {
		return browserAttachmentResponse{}, err
	}
	if req.TargetID == "" || len(req.TargetID) > browserAttachmentMaxTargetID || !utf8.ValidString(req.TargetID) {
		return browserAttachmentResponse{}, attachmentError(http.StatusBadRequest, "bad_browser_attachment", "targetId is invalid", nil)
	}
	if len(req.Label) > browserAttachmentMaxLabel || !utf8.ValidString(req.Label) {
		return browserAttachmentResponse{}, attachmentError(http.StatusBadRequest, "bad_browser_attachment", "label is invalid", nil)
	}
	wantBrowser := ""
	if req.BrowserID != "" {
		if wantBrowser = normalizeCDPBrowserID(req.BrowserID); wantBrowser == "" {
			return browserAttachmentResponse{}, attachmentError(http.StatusBadRequest, "bad_browser_attachment", "browserId is invalid", nil)
		}
	}
	viewport, err := normalizeAttachmentViewport(req.Viewport)
	if err != nil {
		return browserAttachmentResponse{}, err
	}

	m.createMu.Lock()
	defer m.createMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return browserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_disconnected", "attachment manager is closed", nil)
	}
	targetKey := fmt.Sprintf("%d:%s", req.Port, req.TargetID)
	if _, exists := m.targets[targetKey]; exists {
		m.mu.Unlock()
		return browserAttachmentResponse{}, attachmentError(http.StatusConflict, "browser_already_attached", "target already has an active attachment", nil)
	}
	m.mu.Unlock()

	discovery, err := m.config.Discover(req.Port, m.config.DiscoveryTimeout)
	if err != nil {
		return browserAttachmentResponse{}, err
	}
	// The caller told us which Chromium instance it means. A port collision is
	// silent on the Chromium side (the loser binds the other loopback family), so
	// this is the check that keeps an attach off another session's browser.
	if wantBrowser != "" && !strings.EqualFold(wantBrowser, discovery.BrowserID) {
		return browserAttachmentResponse{}, attachmentError(http.StatusConflict, "cdp_browser_mismatch",
			"port "+strconv.Itoa(req.Port)+" is served by a different Chromium instance than browserId", nil)
	}
	var target *browserAttachTarget
	for i := range discovery.Targets {
		if discovery.Targets[i].TargetID == req.TargetID {
			target = &discovery.Targets[i]
			break
		}
	}
	if target == nil {
		return browserAttachmentResponse{}, attachmentError(http.StatusNotFound, "cdp_target_not_found", "CDP page target does not exist", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.config.CommandTimeout)
	cdp, err := m.config.Dial(ctx, req.Port, discovery.DebuggerURL)
	cancel()
	if err != nil {
		var apiErr *browserAttachmentAPIError
		if errors.As(err, &apiErr) {
			return browserAttachmentResponse{}, err
		}
		return browserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_unreachable", "CDP endpoint is unreachable", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = cdp.Close()
		}
	}()
	if err := m.call(cdp, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		return browserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_disconnected", "Chromium disconnected during attach", err)
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := m.call(cdp, "", "Target.attachToTarget", map[string]any{"targetId": req.TargetID, "flatten": true}, &attached); err != nil || attached.SessionID == "" {
		return browserAttachmentResponse{}, attachmentError(http.StatusNotFound, "cdp_target_not_found", "CDP page target disappeared before attach", err)
	}
	for _, command := range []struct {
		method string
		params any
	}{
		{"Page.enable", nil}, {"Runtime.enable", nil}, {"Network.enable", nil}, {"Log.enable", nil},
		{"Page.setLifecycleEventsEnabled", map[string]any{"enabled": true}},
		{"Emulation.setDeviceMetricsOverride", map[string]any{
			"width": viewport.Width, "height": viewport.Height, "deviceScaleFactor": 1, "mobile": false,
		}},
	} {
		if err := m.call(cdp, attached.SessionID, command.method, command.params, nil); err != nil {
			_ = m.call(cdp, "", "Target.detachFromTarget", map[string]any{"sessionId": attached.SessionID}, nil)
			return browserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_disconnected", "Chromium disconnected during attach", err)
		}
	}
	state := attachmentStateAttached
	if !supportedAttachmentURL(target.URL) {
		state = attachmentStateUnsupportedURL
	}
	a := &browserAttachment{
		manager: m, id: newBrowserAttachmentID(), createdAt: time.Now(), targetKey: targetKey, targetID: req.TargetID,
		sessionID: attached.SessionID, cdp: cdp, viewport: viewport, pane: viewport,
		state: state, title: target.Title, label: req.Label, url: target.URL, controlMode: attachmentControlViewOnly,
		latestFrame: make(chan []byte, 1), frameEvents: make(chan browserScreencastFrame, 1), frameStop: make(chan struct{}),
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = m.call(cdp, "", "Target.detachFromTarget", map[string]any{"sessionId": attached.SessionID}, nil)
		return browserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_disconnected", "attachment manager is closed", nil)
	}
	m.attachments[a.id] = a
	m.targets[targetKey] = a.id
	m.mu.Unlock()
	a.mu.Lock()
	a.armExpiryLocked(m.config.UnviewedTTL)
	resp := a.responseLocked()
	a.mu.Unlock()
	cleanup = false
	go a.frameLoop()
	go m.eventLoop(a)
	return resp, nil
}

func (m *browserAttachmentManager) Get(id string) (browserAttachmentResponse, error) {
	if !validAttachmentID(id) {
		return browserAttachmentResponse{}, attachmentError(http.StatusNotFound, "browser_attachment_not_found", "browser attachment does not exist", nil)
	}
	m.mu.Lock()
	a := m.attachments[id]
	m.mu.Unlock()
	if a == nil {
		return browserAttachmentResponse{}, attachmentError(http.StatusNotFound, "browser_attachment_not_found", "browser attachment does not exist", nil)
	}
	return a.response(), nil
}

// List returns the live attachments, newest first, so the Console can offer a
// way back to one whose action link has scrolled out of the mirror (docs/53
// §53.7). It exposes no more than the pane already shows for a single id.
func (m *browserAttachmentManager) List() browserAttachmentListResponse {
	m.mu.Lock()
	live := make([]*browserAttachment, 0, len(m.attachments))
	for _, a := range m.attachments {
		live = append(live, a)
	}
	m.mu.Unlock()
	sort.Slice(live, func(i, j int) bool { return live[i].createdAt.After(live[j].createdAt) })
	items := make([]browserAttachmentResponse, 0, len(live))
	for _, a := range live {
		items = append(items, a.response())
	}
	return browserAttachmentListResponse{Attachments: items}
}

// Delete is intentionally idempotent and never closes the external Page or
// Chromium process. It detaches only AF's target session and browser socket.
func (m *browserAttachmentManager) Delete(id string) {
	m.mu.Lock()
	a := m.attachments[id]
	if a == nil {
		m.mu.Unlock()
		return
	}
	delete(m.attachments, id)
	delete(m.targets, a.targetKey)
	m.mu.Unlock()
	workspaceBrowserViewerLeases.release(browserAttachmentViewerLease(a.id))

	a.mu.Lock()
	if a.expiry != nil {
		a.expiry.Stop()
		a.expiry = nil
	}
	a.terminal = true
	v := a.viewer
	a.viewer, a.reserved, a.visible = nil, false, false
	a.mu.Unlock()
	a.stopFrameLoop()
	a.stopScreencast()
	if v != nil {
		v.enqueueClose(websocketCloseNormal, "attachment detached")
	}
	if a.sessionID != "" {
		_ = m.call(a.cdp, "", "Target.detachFromTarget", map[string]any{"sessionId": a.sessionID}, nil)
	}
	_ = a.cdp.Close()
}

func (m *browserAttachmentManager) UpdateHandoff(id string, req browserAttachmentHandoffRequest) (browserAttachmentResponse, error) {
	if req.CompletionLabel == "" {
		req.CompletionLabel = "操作完了"
	}
	if req.ControlMode == "" {
		req.ControlMode = attachmentControlUser
	}
	if err := validateHandoffRequest(req); err != nil {
		return browserAttachmentResponse{}, err
	}
	a, err := m.lookupActive(id)
	if err != nil {
		return browserAttachmentResponse{}, err
	}
	a.mu.Lock()
	a.controlMode = req.ControlMode
	a.handoff = &browserAttachmentHandoffResponse{
		Message: req.Message, CompletionLabel: req.CompletionLabel, AllowCancel: req.AllowCancel,
		ControlMode: req.ControlMode, Result: "pending",
	}
	visible := a.visible && a.viewer != nil
	if !visible {
		a.armExpiryLocked(m.config.HandoffTTL)
	}
	resp := a.responseLocked()
	a.mu.Unlock()
	if req.ControlMode == attachmentControlLocked {
		a.stopScreencast()
	} else if visible {
		_ = a.startScreencast()
	}
	a.notifyJSON(map[string]any{"type": "handoff", "handoff": resp.Handoff, "controlMode": req.ControlMode})
	return resp, nil
}

// SetControlMode changes only the attachment's current input/rendering mode.
// Handoff metadata/result and expiry are deliberately left untouched: callers
// use UpdateHandoff when they intend to create or reset a handoff workflow.
func (m *browserAttachmentManager) SetControlMode(id, mode string) (browserAttachmentResponse, error) {
	if !validAttachmentControlMode(mode) {
		return browserAttachmentResponse{}, attachmentError(http.StatusBadRequest, "bad_control_mode", "controlMode must be view-only, user-control, or locked", nil)
	}
	a, err := m.lookupActive(id)
	if err != nil {
		return browserAttachmentResponse{}, err
	}
	a.mu.Lock()
	a.controlMode = mode
	visible := a.visible && a.viewer != nil
	resp := a.responseLocked()
	a.mu.Unlock()
	if mode == attachmentControlLocked {
		a.stopScreencast()
	} else if visible {
		_ = a.startScreencast()
	}
	a.notifyJSON(map[string]any{"type": "control-mode", "controlMode": mode})
	return resp, nil
}

func (m *browserAttachmentManager) SetHandoffResult(id, result string) (browserAttachmentResponse, error) {
	if result != "completed" && result != "cancelled" {
		return browserAttachmentResponse{}, attachmentError(http.StatusBadRequest, "bad_handoff_result", "result must be completed or cancelled", nil)
	}
	a, err := m.lookupActive(id)
	if err != nil {
		return browserAttachmentResponse{}, err
	}
	a.mu.Lock()
	if a.handoff == nil {
		a.mu.Unlock()
		return browserAttachmentResponse{}, attachmentError(http.StatusConflict, "browser_handoff_not_pending", "browser attachment has no pending handoff", nil)
	}
	a.handoff.Result = result
	a.handoff.ControlMode = attachmentControlLocked
	a.controlMode = attachmentControlLocked
	if a.viewer == nil {
		a.armExpiryLocked(m.config.ViewerGrace)
	}
	resp := a.responseLocked()
	a.mu.Unlock()
	a.stopScreencast()
	a.notifyJSON(map[string]any{"type": "handoff", "handoff": resp.Handoff, "controlMode": attachmentControlLocked})
	return resp, nil
}

func (m *browserAttachmentManager) lookupActive(id string) (*browserAttachment, error) {
	if !validAttachmentID(id) {
		return nil, attachmentError(http.StatusNotFound, "browser_attachment_not_found", "browser attachment does not exist", nil)
	}
	m.mu.Lock()
	a := m.attachments[id]
	m.mu.Unlock()
	if a == nil {
		return nil, attachmentError(http.StatusNotFound, "browser_attachment_not_found", "browser attachment does not exist", nil)
	}
	a.mu.Lock()
	terminal := a.terminal
	a.mu.Unlock()
	if terminal {
		return nil, attachmentError(http.StatusBadGateway, "cdp_disconnected", "Chromium is no longer connected", nil)
	}
	return a, nil
}

func (m *browserAttachmentManager) call(cdp browserCDP, session, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), m.config.CommandTimeout)
	defer cancel()
	return cdp.Call(ctx, method, params, session, result)
}

func (m *browserAttachmentManager) eventLoop(a *browserAttachment) {
	for {
		select {
		case ev, ok := <-a.cdp.Events():
			if !ok {
				m.markTerminal(a, attachmentStateDisconnected, "Chromium disconnected")
				return
			}
			ev.releaseQueueBytes()
			m.handleEvent(a, ev)
		case <-a.cdp.Done():
			if m.owns(a) {
				m.markTerminal(a, attachmentStateDisconnected, "Chromium disconnected")
			}
			return
		}
	}
}

func (m *browserAttachmentManager) handleEvent(a *browserAttachment, ev browserCDPEvent) {
	switch ev.Method {
	case "Target.targetDestroyed", "Target.targetCrashed":
		var msg struct {
			TargetID string `json:"targetId"`
		}
		if json.Unmarshal(ev.Params, &msg) == nil && msg.TargetID == a.targetID {
			m.markTerminal(a, attachmentStateTargetClosed, "target closed")
		}
	case "Target.detachedFromTarget":
		var msg struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(ev.Params, &msg) == nil && msg.SessionID == a.sessionID {
			m.markTerminal(a, attachmentStateTargetClosed, "target session closed")
		}
	case "Inspector.targetCrashed":
		if ev.SessionID == a.sessionID {
			m.markTerminal(a, attachmentStateTargetClosed, "target crashed")
		}
	case "Page.screencastFrame":
		if ev.SessionID != a.sessionID {
			return
		}
		var msg struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
		}
		if json.Unmarshal(ev.Params, &msg) == nil {
			a.offerFrame(msg.Data, msg.SessionID)
		}
	case "Page.frameNavigated":
		if ev.SessionID != a.sessionID {
			return
		}
		var msg struct {
			Frame struct {
				ParentID string `json:"parentId"`
				URL      string `json:"url"`
			} `json:"frame"`
		}
		if json.Unmarshal(ev.Params, &msg) == nil && msg.Frame.ParentID == "" {
			m.updateNavigation(a, msg.Frame.URL)
		}
	case "Page.lifecycleEvent", "Page.loadEventFired":
		if ev.SessionID == a.sessionID {
			a.refreshNavigation()
		}
	case "Runtime.consoleAPICalled":
		if ev.SessionID == a.sessionID {
			a.handleConsoleEvent(ev.Params)
		}
	case "Runtime.exceptionThrown":
		if ev.SessionID == a.sessionID {
			a.handleExceptionEvent(ev.Params)
		}
	}
}

func (m *browserAttachmentManager) updateNavigation(a *browserAttachment, rawURL string) {
	a.mu.Lock()
	if a.terminal {
		a.mu.Unlock()
		return
	}
	wasUnsupported := a.state == attachmentStateUnsupportedURL
	a.url = truncateBrowserText(rawURL, browserAttachmentMaxURL)
	if !supportedAttachmentURL(rawURL) {
		a.state = attachmentStateUnsupportedURL
	} else if a.viewer != nil {
		a.state = attachmentStateViewerOpen
	} else {
		a.state = attachmentStateAttached
	}
	state, title, urlNow := a.state, a.displayTitleLocked(), a.url
	visible := a.visible && a.viewer != nil
	a.mu.Unlock()
	if state == attachmentStateUnsupportedURL {
		a.stopScreencast()
	} else if wasUnsupported && visible {
		_ = a.startScreencast()
	}
	a.notifyJSON(map[string]any{"type": "navigation", "url": urlNow, "title": title})
	a.notifyJSON(map[string]any{"type": "state", "state": state})
}

func (m *browserAttachmentManager) markTerminal(a *browserAttachment, state, reason string) {
	if !m.owns(a) {
		return
	}
	a.mu.Lock()
	if a.terminal {
		a.mu.Unlock()
		return
	}
	a.terminal = true
	a.state = state
	v := a.viewer
	a.viewer, a.reserved, a.visible = nil, false, false
	a.armExpiryLocked(m.config.ViewerGrace)
	a.mu.Unlock()
	workspaceBrowserViewerLeases.release(browserAttachmentViewerLease(a.id))
	a.stopFrameLoop()
	if v != nil {
		v.enqueueTextAndClose(mustBrowserJSON(map[string]any{"type": "state", "state": state}), websocketCloseGoingAway, reason)
	}
	_ = a.cdp.Close()
}

func (m *browserAttachmentManager) owns(a *browserAttachment) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.attachments[a.id] == a
}

func (m *browserAttachmentManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	ids := make([]string, 0, len(m.attachments))
	for id := range m.attachments {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Delete(id)
	}
}

func (a *browserAttachment) response() browserAttachmentResponse {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.responseLocked()
}

func (a *browserAttachment) responseLocked() browserAttachmentResponse {
	var handoff *browserAttachmentHandoffResponse
	if a.handoff != nil {
		copy := *a.handoff
		handoff = &copy
	}
	var expiresAt *time.Time
	if !a.expiresAt.IsZero() {
		copy := a.expiresAt
		expiresAt = &copy
	}
	return browserAttachmentResponse{
		ID: a.id, State: a.state, Title: a.displayTitleLocked(), URL: a.url,
		OpenURL: "/open/browser-attachment/" + a.id, ExpiresAt: expiresAt,
		Viewer: a.viewer != nil, ControlMode: a.controlMode, Handoff: handoff,
	}
}

func (a *browserAttachment) displayTitleLocked() string {
	if a.label != "" {
		return a.label
	}
	return a.title
}

func (a *browserAttachment) armExpiryLocked(ttl time.Duration) {
	if a.expiry != nil {
		a.expiry.Stop()
	}
	a.expiresAt = time.Now().UTC().Add(ttl)
	a.expiry = time.AfterFunc(ttl, func() { a.manager.Delete(a.id) })
}

func (a *browserAttachment) offerFrame(data string, sessionID int) {
	gen := a.castGen.Load()
	if gen == 0 {
		return
	}
	frame := browserScreencastFrame{data: data, sessionID: sessionID, gen: gen}
	select {
	case a.frameEvents <- frame:
		return
	default:
	}
	select {
	case <-a.frameEvents:
	default:
	}
	select {
	case a.frameEvents <- frame:
	default:
	}
}

func (a *browserAttachment) frameLoop() {
	for {
		select {
		case <-a.frameStop:
			return
		case event := <-a.frameEvents:
			if event.gen != a.castGen.Load() {
				continue
			}
			if frame, err := base64.StdEncoding.DecodeString(event.data); err == nil {
				a.mu.Lock()
				visible := a.visible && a.viewer != nil
				a.mu.Unlock()
				if visible {
					select {
					case a.latestFrame <- frame:
					default:
						select {
						case <-a.latestFrame:
						default:
						}
						select {
						case a.latestFrame <- frame:
						default:
						}
					}
				}
			}
			timer := time.NewTimer(a.manager.config.FrameInterval)
			select {
			case <-a.frameStop:
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
			if event.gen == a.castGen.Load() {
				_ = a.manager.call(a.cdp, a.sessionID, "Page.screencastFrameAck", map[string]any{"sessionId": event.sessionID}, nil)
			}
		}
	}
}

func (a *browserAttachment) stopFrameLoop() {
	a.frameOnce.Do(func() { close(a.frameStop) })
}

func (a *browserAttachment) startScreencast() error {
	a.castMu.Lock()
	defer a.castMu.Unlock()
	if a.casting {
		return nil
	}
	a.mu.Lock()
	blocked := a.terminal || a.state == attachmentStateUnsupportedURL || a.controlMode == attachmentControlLocked
	// Cap the IMAGE at the pane, not the layout viewport: zoom-to-fit lays the
	// page out wider on purpose, and the frame is scaled back down anyway.
	image := a.pane
	if image.Width < 1 || image.Height < 1 {
		image = a.viewport
	}
	a.mu.Unlock()
	if blocked {
		return nil
	}
	err := a.manager.call(a.cdp, a.sessionID, "Page.startScreencast", map[string]any{
		"format": "jpeg", "quality": a.manager.config.JPEGQuality,
		"maxWidth": image.Width, "maxHeight": image.Height,
	}, nil)
	if err == nil {
		a.casting = true
		a.castGen.Store(a.castEpoch.Add(1))
	}
	return err
}

func (a *browserAttachment) stopScreencast() {
	a.castMu.Lock()
	defer a.castMu.Unlock()
	if !a.casting {
		return
	}
	a.castGen.Store(0)
	_ = a.manager.call(a.cdp, a.sessionID, "Page.stopScreencast", nil, nil)
	a.casting = false
}

func (a *browserAttachment) restartScreencast() {
	a.castMu.Lock()
	casting := a.casting
	a.castMu.Unlock()
	if casting {
		a.stopScreencast()
		_ = a.startScreencast()
	}
}

func (a *browserAttachment) refreshNavigation() {
	var history struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID    int    `json:"id"`
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"entries"`
	}
	if a.manager.call(a.cdp, a.sessionID, "Page.getNavigationHistory", nil, &history) != nil ||
		history.CurrentIndex < 0 || history.CurrentIndex >= len(history.Entries) {
		return
	}
	entry := history.Entries[history.CurrentIndex]
	a.mu.Lock()
	a.url = truncateBrowserText(entry.URL, browserAttachmentMaxURL)
	a.title = truncateBrowserText(entry.Title, browserAttachmentMaxTitle)
	urlNow, title := a.url, a.displayTitleLocked()
	a.mu.Unlock()
	a.notifyJSON(map[string]any{
		"type": "navigation", "url": urlNow, "title": title,
		"canBack": history.CurrentIndex > 0, "canForward": history.CurrentIndex+1 < len(history.Entries),
	})
}

func (a *browserAttachment) notifyJSON(value any) {
	a.mu.Lock()
	v := a.viewer
	a.mu.Unlock()
	if v != nil {
		v.enqueueText(mustBrowserJSON(value))
	}
}

func (a *browserAttachment) handleConsoleEvent(raw json.RawMessage) {
	var msg struct {
		Type string `json:"type"`
		Args []struct {
			Type        string `json:"type"`
			Value       any    `json:"value"`
			Description string `json:"description"`
		} `json:"args"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	parts := make([]string, 0, len(msg.Args))
	for _, arg := range msg.Args {
		switch {
		case arg.Value != nil:
			parts = append(parts, fmt.Sprint(arg.Value))
		case arg.Description != "":
			parts = append(parts, arg.Description)
		default:
			parts = append(parts, arg.Type)
		}
	}
	level := msg.Type
	if level == "warning" {
		level = "warn"
	}
	if level != "error" && level != "warn" && level != "info" && level != "debug" {
		level = "log"
	}
	a.notifyJSON(map[string]any{"type": "console", "level": level, "text": truncateBrowserText(joinBrowserParts(parts), browserMaxConsoleText)})
}

func (a *browserAttachment) handleExceptionEvent(raw json.RawMessage) {
	var msg struct {
		ExceptionDetails struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	text := msg.ExceptionDetails.Exception.Description
	if text == "" {
		text = msg.ExceptionDetails.Text
	}
	a.notifyJSON(map[string]any{"type": "page-error", "text": truncateBrowserText(text, browserMaxConsoleText)})
}

func joinBrowserParts(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, part := range parts[1:] {
		result += " " + part
	}
	return result
}

func newBrowserAttachmentID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return browserAttachmentIDPrefix + hex.EncodeToString(b)
}
