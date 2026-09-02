package browserx

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
	port      int
	// targetKey, targetID, sessionID and cdp all describe which CDP target/
	// connection this attachment currently points at. They start out set once
	// by Create and never change for most attachments' lifetime, but Retarget
	// swaps all four together (under mu) to move the SAME attachment id onto a
	// different target. Because of that, any reader outside of Retarget's own
	// goroutine — frameLoop, a viewer's input dispatch, a superseded eventLoop
	// generation — must go through currentSession()/mu rather than reading
	// these fields directly, even though they look like simple value fields.
	targetKey string
	targetID  string
	sessionID string
	cdp       browserCDP
	// viewport is the LAYOUT viewport pointer coordinates are expressed in; with
	// zoom-to-fit it is wider than pane, and with a pinch zoom it is narrower.
	// pane is what the viewer actually shows; base is the layout before the pinch
	// zoom, kept so a pinch does not re-measure an already zoomed page.
	viewport browserViewport
	pane     browserViewport
	base     browserViewport
	fit      bool
	zoom     float64

	// opMu serializes the two operations that tear down or replace this
	// attachment's CDP session end to end (Delete, Retarget) so they can never
	// interleave with each other on the same attachment.
	opMu sync.Mutex

	mu          sync.Mutex
	state       string
	title       string
	label       string
	url         string
	controlMode string
	handoff     *browserAttachmentHandoffResponse
	// handoffSession is the session to notify once a human resolves the CURRENT
	// handoff (empty when UpdateHandoff was called without one — see
	// browser_handoff_ledger.go). Set alongside handoff, so it always matches
	// whichever handoff round is live.
	handoffSession string
	viewer         *browserAttachmentViewer
	reserved       bool
	visible        bool
	terminal       bool
	expiresAt      time.Time
	expiry         *time.Timer

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
		// The link is typically handed off through chat (agent creates it, reports
		// it, the human reads the message and clicks later), not opened the instant
		// it's created, so this needs real slack. 30m matches HandoffTTL below.
		UnviewedTTL:      time.Duration(browserConfigInt("AF_BROWSER_ATTACH_UNVIEWED_TTL_SEC", 1800, 30, 24*3600)) * time.Second,
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

var WorkspaceBrowserAttachmentManager = newBrowserAttachmentManager(defaultBrowserAttachmentManagerConfig())

func newBrowserAttachmentManager(config browserAttachmentManagerConfig) *browserAttachmentManager {
	if config.UnviewedTTL <= 0 {
		config.UnviewedTTL = 30 * time.Minute
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

func (m *browserAttachmentManager) Discover(port int) (BrowserAttachTargetsResponse, error) {
	discovery, err := m.config.Discover(port, m.config.DiscoveryTimeout)
	if err != nil {
		return BrowserAttachTargetsResponse{}, err
	}
	return BrowserAttachTargetsResponse{Targets: discovery.Targets, BrowserID: discovery.BrowserID}, nil
}

// ListSiblingTargets answers "what else could this attachment switch to" for
// Retarget's picker: the other targets on the SAME Chromium instance. The
// port itself never crosses this boundary (an attachment response never
// includes it — see BrowserAttachmentResponse), so this is the only way a
// caller who only holds an attachment id can discover candidates at all.
func (m *browserAttachmentManager) ListSiblingTargets(id string) (browserAttachmentSiblingTargetsResponse, error) {
	a, err := m.lookupActive(id)
	if err != nil {
		return browserAttachmentSiblingTargetsResponse{}, err
	}
	a.mu.Lock()
	port, currentTargetID := a.port, a.targetID
	a.mu.Unlock()
	discovery, err := m.config.Discover(port, m.config.DiscoveryTimeout)
	if err != nil {
		return browserAttachmentSiblingTargetsResponse{}, err
	}
	targets := make([]browserAttachmentSiblingTarget, 0, len(discovery.Targets))
	for _, t := range discovery.Targets {
		targets = append(targets, browserAttachmentSiblingTarget{
			TargetID: t.TargetID, Title: t.Title, URL: t.URL, Current: t.TargetID == currentTargetID,
		})
	}
	return browserAttachmentSiblingTargetsResponse{Targets: targets}, nil
}

func (m *browserAttachmentManager) Create(req browserAttachmentCreateRequest) (BrowserAttachmentResponse, error) {
	if err := validateCDPPort(req.Port); err != nil {
		return BrowserAttachmentResponse{}, err
	}
	if req.TargetID == "" || len(req.TargetID) > browserAttachmentMaxTargetID || !utf8.ValidString(req.TargetID) {
		return BrowserAttachmentResponse{}, attachmentError(http.StatusBadRequest, "bad_browser_attachment", "targetId is invalid", nil)
	}
	if len(req.Label) > BrowserAttachmentMaxLabel || !utf8.ValidString(req.Label) {
		return BrowserAttachmentResponse{}, attachmentError(http.StatusBadRequest, "bad_browser_attachment", "label is invalid", nil)
	}
	wantBrowser := ""
	if req.BrowserID != "" {
		if wantBrowser = NormalizeCDPBrowserID(req.BrowserID); wantBrowser == "" {
			return BrowserAttachmentResponse{}, attachmentError(http.StatusBadRequest, "bad_browser_attachment", "browserId is invalid", nil)
		}
	}
	viewport, err := normalizeAttachmentViewport(req.Viewport)
	if err != nil {
		return BrowserAttachmentResponse{}, err
	}

	m.createMu.Lock()
	defer m.createMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return BrowserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_disconnected", "attachment manager is closed", nil)
	}
	targetKey := fmt.Sprintf("%d:%s", req.Port, req.TargetID)
	if _, exists := m.targets[targetKey]; exists {
		m.mu.Unlock()
		return BrowserAttachmentResponse{}, attachmentError(http.StatusConflict, "browser_already_attached", "target already has an active attachment", nil)
	}
	m.mu.Unlock()

	discovery, err := m.config.Discover(req.Port, m.config.DiscoveryTimeout)
	if err != nil {
		return BrowserAttachmentResponse{}, err
	}
	// The caller told us which Chromium instance it means. A port collision is
	// silent on the Chromium side (the loser binds the other loopback family), so
	// this is the check that keeps an attach off another session's browser.
	if wantBrowser != "" && !strings.EqualFold(wantBrowser, discovery.BrowserID) {
		return BrowserAttachmentResponse{}, attachmentError(http.StatusConflict, "cdp_browser_mismatch",
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
		return BrowserAttachmentResponse{}, attachmentError(http.StatusNotFound, "cdp_target_not_found", "CDP page target does not exist", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.config.CommandTimeout)
	cdp, err := m.config.Dial(ctx, req.Port, discovery.DebuggerURL)
	cancel()
	if err != nil {
		var apiErr *browserAttachmentAPIError
		if errors.As(err, &apiErr) {
			return BrowserAttachmentResponse{}, err
		}
		return BrowserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_unreachable", "CDP endpoint is unreachable", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = cdp.Close()
		}
	}()
	if err := m.call(cdp, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		return BrowserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_disconnected", "Chromium disconnected during attach", err)
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := m.call(cdp, "", "Target.attachToTarget", map[string]any{"targetId": req.TargetID, "flatten": true}, &attached); err != nil || attached.SessionID == "" {
		return BrowserAttachmentResponse{}, attachmentError(http.StatusNotFound, "cdp_target_not_found", "CDP page target disappeared before attach", err)
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
			return BrowserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_disconnected", "Chromium disconnected during attach", err)
		}
	}
	state := attachmentStateAttached
	if !supportedAttachmentURL(target.URL) {
		state = attachmentStateUnsupportedURL
	}
	a := &browserAttachment{
		manager: m, id: newBrowserAttachmentID(), createdAt: time.Now(), port: req.Port, targetKey: targetKey, targetID: req.TargetID,
		sessionID: attached.SessionID, cdp: cdp, viewport: viewport, pane: viewport, base: viewport, zoom: 1,
		state: state, title: target.Title, label: req.Label, url: target.URL, controlMode: attachmentControlViewOnly,
		latestFrame: make(chan []byte, 1), frameEvents: make(chan browserScreencastFrame, 1), frameStop: make(chan struct{}),
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = m.call(cdp, "", "Target.detachFromTarget", map[string]any{"sessionId": attached.SessionID}, nil)
		return BrowserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_disconnected", "attachment manager is closed", nil)
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
	go m.eventLoop(a, cdp, attached.SessionID, req.TargetID)
	return resp, nil
}

// Retarget switches an existing attachment onto a different CDP target on the
// same port, keeping its id (and so its already-open Console pane / handoff
// link) instead of forcing a close-and-reattach cycle. This exists because a
// script that drives many tabs in turn (e.g. posting several drafts) used to
// mean closing the pane and asking the agent to mint a brand new attachment
// link for every tab switch.
func (m *browserAttachmentManager) Retarget(id string, req browserAttachmentRetargetRequest) (BrowserAttachmentResponse, error) {
	if req.TargetID == "" || len(req.TargetID) > browserAttachmentMaxTargetID || !utf8.ValidString(req.TargetID) {
		return BrowserAttachmentResponse{}, attachmentError(http.StatusBadRequest, "bad_browser_attachment", "targetId is invalid", nil)
	}
	wantBrowser := ""
	if req.BrowserID != "" {
		if wantBrowser = NormalizeCDPBrowserID(req.BrowserID); wantBrowser == "" {
			return BrowserAttachmentResponse{}, attachmentError(http.StatusBadRequest, "bad_browser_attachment", "browserId is invalid", nil)
		}
	}
	a, err := m.lookupActive(id)
	if err != nil {
		return BrowserAttachmentResponse{}, err
	}

	// Excludes a concurrent Delete on the same attachment (see Delete's own
	// opMu comment); a Retarget that loses that race sees a.terminal below.
	a.opMu.Lock()
	defer a.opMu.Unlock()

	a.mu.Lock()
	if a.terminal {
		a.mu.Unlock()
		return BrowserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_disconnected", "Chromium is no longer connected", nil)
	}
	port, oldTargetKey, oldTargetID, oldSessionID, oldCDP, viewport := a.port, a.targetKey, a.targetID, a.sessionID, a.cdp, a.viewport
	a.mu.Unlock()

	if req.TargetID == oldTargetID {
		return a.response(), nil
	}

	m.createMu.Lock()
	defer m.createMu.Unlock()
	newTargetKey := fmt.Sprintf("%d:%s", port, req.TargetID)
	m.mu.Lock()
	if _, exists := m.targets[newTargetKey]; exists {
		m.mu.Unlock()
		return BrowserAttachmentResponse{}, attachmentError(http.StatusConflict, "browser_already_attached", "target already has an active attachment", nil)
	}
	m.mu.Unlock()

	discovery, err := m.config.Discover(port, m.config.DiscoveryTimeout)
	if err != nil {
		return BrowserAttachmentResponse{}, err
	}
	if wantBrowser != "" && !strings.EqualFold(wantBrowser, discovery.BrowserID) {
		return BrowserAttachmentResponse{}, attachmentError(http.StatusConflict, "cdp_browser_mismatch",
			"port "+strconv.Itoa(port)+" is served by a different Chromium instance than browserId", nil)
	}
	var target *browserAttachTarget
	for i := range discovery.Targets {
		if discovery.Targets[i].TargetID == req.TargetID {
			target = &discovery.Targets[i]
			break
		}
	}
	if target == nil {
		return BrowserAttachmentResponse{}, attachmentError(http.StatusNotFound, "cdp_target_not_found", "CDP page target does not exist", nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), m.config.CommandTimeout)
	newCDP, err := m.config.Dial(ctx, port, discovery.DebuggerURL)
	cancel()
	if err != nil {
		var apiErr *browserAttachmentAPIError
		if errors.As(err, &apiErr) {
			return BrowserAttachmentResponse{}, err
		}
		return BrowserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_unreachable", "CDP endpoint is unreachable", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = newCDP.Close()
		}
	}()
	if err := m.call(newCDP, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		return BrowserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_disconnected", "Chromium disconnected during attach", err)
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := m.call(newCDP, "", "Target.attachToTarget", map[string]any{"targetId": req.TargetID, "flatten": true}, &attached); err != nil || attached.SessionID == "" {
		return BrowserAttachmentResponse{}, attachmentError(http.StatusNotFound, "cdp_target_not_found", "CDP page target disappeared before attach", err)
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
		if err := m.call(newCDP, attached.SessionID, command.method, command.params, nil); err != nil {
			_ = m.call(newCDP, "", "Target.detachFromTarget", map[string]any{"sessionId": attached.SessionID}, nil)
			return BrowserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_disconnected", "Chromium disconnected during attach", err)
		}
	}

	// A real Chromium crash/close on the OLD target could have raced us in and
	// already torn a down while we were off doing the discover/dial/attach
	// dance above (none of which touches a). Bail out rather than resurrecting
	// a deleted attachment with a freshly attached session nobody will ever use.
	a.mu.Lock()
	if a.terminal {
		a.mu.Unlock()
		return BrowserAttachmentResponse{}, attachmentError(http.StatusBadGateway, "cdp_disconnected", "Chromium is no longer connected", nil)
	}
	a.mu.Unlock()

	// Past this point we're committed. Stop the OLD session's cast (its own
	// generation check already drops any in-flight frame once this returns),
	// then swap the session fields together under mu so every other reader
	// (frameLoop, viewer input, response()) only ever observes the fully-old
	// or fully-new attachment, never a torn mix.
	a.stopScreencast()

	newState := attachmentStateAttached
	if !supportedAttachmentURL(target.URL) {
		newState = attachmentStateUnsupportedURL
	}
	a.mu.Lock()
	a.targetKey, a.targetID, a.sessionID, a.cdp = newTargetKey, req.TargetID, attached.SessionID, newCDP
	a.title, a.url = target.Title, target.URL
	// zoom-to-fit and pinch-zoom baselines are measurements of the OLD page's
	// content; they mean nothing for the new one, so reset to the plain
	// requested viewport and let the Console re-measure if it wants zoom-to-fit.
	a.zoom, a.base, a.pane, a.fit = 1, a.viewport, a.viewport, false
	if newState == attachmentStateUnsupportedURL {
		a.state = attachmentStateUnsupportedURL
	} else if a.viewer != nil {
		a.state = attachmentStateViewerOpen
	} else {
		a.state = attachmentStateAttached
	}
	visible := a.visible && a.viewer != nil
	resp := a.responseLocked()
	a.mu.Unlock()

	m.mu.Lock()
	delete(m.targets, oldTargetKey)
	m.targets[newTargetKey] = a.id
	m.mu.Unlock()

	cleanup = false
	go m.eventLoop(a, newCDP, attached.SessionID, req.TargetID)

	// Best-effort teardown of the abandoned session; the old eventLoop's own
	// endSessionIfCurrent check will see a has already moved on and no-op
	// rather than tearing a down, however this connection ends.
	if oldSessionID != "" {
		_ = m.call(oldCDP, "", "Target.detachFromTarget", map[string]any{"sessionId": oldSessionID}, nil)
	}
	_ = oldCDP.Close()

	if visible {
		_ = a.startScreencast()
	}
	// Reuse the exact "ready" shape the initial WS connect sends: the Console
	// side already knows how to reset its canvas/overlay/title/url on that
	// message, so a retarget can drive the same code path instead of a
	// bespoke "retargeted" message type.
	a.mu.Lock()
	v := a.viewer
	a.mu.Unlock()
	if v != nil {
		v.enqueueText(a.readyMessage())
	}
	return resp, nil
}

func (m *browserAttachmentManager) Get(id string) (BrowserAttachmentResponse, error) {
	if !validAttachmentID(id) {
		return BrowserAttachmentResponse{}, attachmentError(http.StatusNotFound, "browser_attachment_not_found", "browser attachment does not exist", nil)
	}
	m.mu.Lock()
	a := m.attachments[id]
	m.mu.Unlock()
	if a == nil {
		return BrowserAttachmentResponse{}, attachmentError(http.StatusNotFound, "browser_attachment_not_found", "browser attachment does not exist", nil)
	}
	return a.response(), nil
}

// List returns the live attachments, newest first, so the Console can offer a
// way back to one whose action link has scrolled out of the mirror (docs/log/53
// §53.7). It exposes no more than the pane already shows for a single id.
func (m *browserAttachmentManager) List() browserAttachmentListResponse {
	m.mu.Lock()
	live := make([]*browserAttachment, 0, len(m.attachments))
	for _, a := range m.attachments {
		live = append(live, a)
	}
	m.mu.Unlock()
	sort.Slice(live, func(i, j int) bool { return live[i].createdAt.After(live[j].createdAt) })
	items := make([]BrowserAttachmentResponse, 0, len(live))
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

	// Excludes a concurrent Retarget on the same attachment: whichever of the
	// two gets here first runs to completion before the other proceeds (a
	// Retarget that loses the race sees a.terminal below and bails out).
	a.opMu.Lock()
	defer a.opMu.Unlock()

	a.mu.Lock()
	if a.expiry != nil {
		a.expiry.Stop()
		a.expiry = nil
	}
	a.terminal = true
	v := a.viewer
	cdp, sessionID := a.cdp, a.sessionID
	a.viewer, a.reserved, a.visible = nil, false, false
	a.mu.Unlock()
	a.stopFrameLoop()
	a.stopScreencast()
	if v != nil {
		v.enqueueClose(websocketCloseNormal, "attachment detached")
	}
	if sessionID != "" {
		_ = m.call(cdp, "", "Target.detachFromTarget", map[string]any{"sessionId": sessionID}, nil)
	}
	_ = cdp.Close()
}

func (m *browserAttachmentManager) UpdateHandoff(id string, req browserAttachmentHandoffRequest) (BrowserAttachmentResponse, error) {
	if req.CompletionLabel == "" {
		req.CompletionLabel = "操作完了"
	}
	if req.ControlMode == "" {
		req.ControlMode = attachmentControlUser
	}
	if err := validateHandoffRequest(req); err != nil {
		return BrowserAttachmentResponse{}, err
	}
	a, err := m.lookupActive(id)
	if err != nil {
		return BrowserAttachmentResponse{}, err
	}
	a.mu.Lock()
	a.controlMode = req.ControlMode
	a.handoff = &browserAttachmentHandoffResponse{
		Message: req.Message, CompletionLabel: req.CompletionLabel, AllowCancel: req.AllowCancel,
		ControlMode: req.ControlMode, Result: "pending",
	}
	a.handoffSession = req.SessionName
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
	// Durable BEFORE returning: a crash between this and SetHandoffResult must
	// still leave a row for the startup sweep to find once a result exists.
	RecordBrowserHandoffRequested(req.SessionName, a.id, req.Message)
	return resp, nil
}

// SetControlMode changes only the attachment's current input/rendering mode.
// Handoff metadata/result and expiry are deliberately left untouched: callers
// use UpdateHandoff when they intend to create or reset a handoff workflow.
func (m *browserAttachmentManager) SetControlMode(id, mode string) (BrowserAttachmentResponse, error) {
	if !validAttachmentControlMode(mode) {
		return BrowserAttachmentResponse{}, attachmentError(http.StatusBadRequest, "bad_control_mode", "controlMode must be view-only, user-control, or locked", nil)
	}
	a, err := m.lookupActive(id)
	if err != nil {
		return BrowserAttachmentResponse{}, err
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

func (m *browserAttachmentManager) SetHandoffResult(id, result string) (BrowserAttachmentResponse, error) {
	if result != "completed" && result != "cancelled" {
		return BrowserAttachmentResponse{}, attachmentError(http.StatusBadRequest, "bad_handoff_result", "result must be completed or cancelled", nil)
	}
	a, err := m.lookupActive(id)
	if err != nil {
		return BrowserAttachmentResponse{}, err
	}
	a.mu.Lock()
	if a.handoff == nil {
		a.mu.Unlock()
		return BrowserAttachmentResponse{}, attachmentError(http.StatusConflict, "browser_handoff_not_pending", "browser attachment has no pending handoff", nil)
	}
	a.handoff.Result = result
	a.handoff.ControlMode = attachmentControlLocked
	a.controlMode = attachmentControlLocked
	sessionName := a.handoffSession
	if a.viewer == nil {
		a.armExpiryLocked(m.config.ViewerGrace)
	}
	resp := a.responseLocked()
	a.mu.Unlock()
	a.stopScreencast()
	a.notifyJSON(map[string]any{"type": "handoff", "handoff": resp.Handoff, "controlMode": attachmentControlLocked})
	// Deliver the result back into the requesting session's conversation
	// (docs/log/53 完了通知節). Off the request goroutine: a human's button click
	// must return immediately, not block on resuming a stopped session or on
	// the CLI's own delivery-confirmation round trip (up to 45s — see
	// agentSendToSession). The ledger row already on disk (RecordBrowserHandoffRequested)
	// is what makes this crash-safe: undeliveredBrowserHandoffs retries at the
	// next Agent start if this goroutine never gets to run.
	if row, ok := ResolveBrowserHandoff(sessionName, a.id, result); ok {
		go DeliverBrowserHandoff(sessionName, row)
	}
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

// eventLoop drains one CDP browser connection for that connection's entire
// lifetime. Retarget does not mutate a running eventLoop in place — it starts
// a brand new one on the new connection and lets this one keep running until
// its own (now abandoned) connection ends. At that point it must not assume
// it is still a's active session: Retarget's own detach+close of the old
// connection would otherwise be indistinguishable from a real Chromium
// disconnect and wrongly tear the (already-moved-on) attachment down. See
// endSessionIfCurrent.
func (m *browserAttachmentManager) eventLoop(a *browserAttachment, cdp browserCDP, sessionID, targetID string) {
	for {
		select {
		case ev, ok := <-cdp.Events():
			if !ok {
				m.endSessionIfCurrent(a, sessionID, "Chromium disconnected")
				return
			}
			ev.releaseQueueBytes()
			m.handleEvent(a, sessionID, targetID, ev)
		case <-cdp.Done():
			m.endSessionIfCurrent(a, sessionID, "Chromium disconnected")
			return
		}
	}
}

// endSessionIfCurrent marks a terminal only if sessionID is still what a's
// live session actually is. A stale eventLoop generation (superseded by a
// Retarget) must not be able to kill the attachment its old connection no
// longer represents.
func (m *browserAttachmentManager) endSessionIfCurrent(a *browserAttachment, sessionID, reason string) {
	if _, cur := a.currentSession(); cur != sessionID {
		return
	}
	m.markTerminal(a, attachmentStateDisconnected, reason)
}

func (m *browserAttachmentManager) handleEvent(a *browserAttachment, sessionID, targetID string, ev browserCDPEvent) {
	switch ev.Method {
	case "Target.targetDestroyed", "Target.targetCrashed":
		var msg struct {
			TargetID string `json:"targetId"`
		}
		if json.Unmarshal(ev.Params, &msg) == nil && msg.TargetID == targetID {
			m.markTerminal(a, attachmentStateTargetClosed, "target closed")
		}
	case "Target.detachedFromTarget":
		var msg struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(ev.Params, &msg) == nil && msg.SessionID == sessionID {
			m.markTerminal(a, attachmentStateTargetClosed, "target session closed")
		}
	case "Inspector.targetCrashed":
		if ev.SessionID == sessionID {
			m.markTerminal(a, attachmentStateTargetClosed, "target crashed")
		}
	case "Page.screencastFrame":
		if ev.SessionID != sessionID {
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
		if ev.SessionID != sessionID {
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
		if ev.SessionID == sessionID {
			a.refreshNavigation()
		}
	case "Runtime.consoleAPICalled":
		if ev.SessionID == sessionID {
			a.handleConsoleEvent(ev.Params)
		}
	case "Runtime.exceptionThrown":
		if ev.SessionID == sessionID {
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
	cdp := a.cdp
	a.viewer, a.reserved, a.visible = nil, false, false
	a.armExpiryLocked(m.config.ViewerGrace)
	a.mu.Unlock()
	workspaceBrowserViewerLeases.release(browserAttachmentViewerLease(a.id))
	a.stopFrameLoop()
	if v != nil {
		v.enqueueTextAndClose(mustBrowserJSON(map[string]any{"type": "state", "state": state}), websocketCloseGoingAway, reason)
	}
	_ = cdp.Close()
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

func (a *browserAttachment) response() BrowserAttachmentResponse {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.responseLocked()
}

func (a *browserAttachment) responseLocked() BrowserAttachmentResponse {
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
	return BrowserAttachmentResponse{
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
				cdp, sessionID := a.currentSession()
				_ = a.manager.call(cdp, sessionID, "Page.screencastFrameAck", map[string]any{"sessionId": event.sessionID}, nil)
			}
		}
	}
}

func (a *browserAttachment) stopFrameLoop() {
	a.frameOnce.Do(func() { close(a.frameStop) })
}

// currentSession returns a consistent snapshot of the CDP connection and
// session id currently backing this attachment. Retarget swaps both together
// under mu; every other reader (frameLoop, viewer input dispatch, a stale
// eventLoop generation) must go through this rather than reading a.cdp /
// a.sessionID directly, or it risks a torn read racing Retarget's write.
func (a *browserAttachment) currentSession() (browserCDP, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cdp, a.sessionID
}

func (a *browserAttachment) startScreencast() error {
	a.castMu.Lock()
	defer a.castMu.Unlock()
	if a.casting {
		return nil
	}
	// An attach target is owned by something else (a Playwright script, another
	// tab the human has open) and is very often not the foreground tab: Chromium
	// throttles paints on background tabs, so Page.startScreencast succeeds but
	// no screencastFrame ever arrives. Bring it to front so it actually renders;
	// best-effort, ignore failure (the target may not support activation, and
	// startScreencast below still gets a real error if the page is truly gone).
	cdp, sessionID := a.currentSession()
	_ = a.manager.call(cdp, sessionID, "Page.bringToFront", nil, nil)

	// A viewer can attach while the target page is still committing its first
	// navigation (about:blank -> target), the same transient window documented
	// on the owned-page path in browser_manager.go. Retry briefly on that one
	// error; any other error, or an attachment that has gone away, returns
	// immediately. See screencastFrameNotActive.
	//
	// The generation is published BEFORE the command for the reason spelled out on
	// browserPage.startScreencast: a frame that arrives while gen is still 0 is
	// dropped without an ACK, and Chromium then stops capturing for good. An attach
	// target is the worse case — it is somebody else's page, so nothing is animating
	// it into producing a replacement frame.
	var err error
	for attempt := 0; attempt < 12; attempt++ {
		a.mu.Lock()
		blocked := a.terminal || a.state == attachmentStateUnsupportedURL || a.controlMode == attachmentControlLocked
		// Cap the IMAGE at the pane, not the layout viewport: zoom-to-fit lays the
		// page out wider on purpose, and the frame is scaled back down anyway.
		image := a.pane
		if image.Width < 1 || image.Height < 1 {
			image = a.viewport
		}
		cdp, sessionID = a.cdp, a.sessionID
		a.mu.Unlock()
		if blocked {
			return nil
		}
		if !a.manager.owns(a) {
			return errBrowserNotFound
		}
		a.castGen.Store(a.castEpoch.Add(1))
		err = a.manager.call(cdp, sessionID, "Page.startScreencast", map[string]any{
			"format": "jpeg", "quality": a.manager.config.JPEGQuality,
			"maxWidth": image.Width, "maxHeight": image.Height,
		}, nil)
		if err == nil {
			a.casting = true
			return nil
		}
		a.castGen.Store(0)
		if !screencastFrameNotActive(err) {
			return err
		}
		time.Sleep(40 * time.Millisecond)
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
	cdp, sessionID := a.currentSession()
	_ = a.manager.call(cdp, sessionID, "Page.stopScreencast", nil, nil)
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
	cdp, sessionID := a.currentSession()
	var history struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID    int    `json:"id"`
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"entries"`
	}
	if a.manager.call(cdp, sessionID, "Page.getNavigationHistory", nil, &history) != nil ||
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
