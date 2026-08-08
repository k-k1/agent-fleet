package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errBrowserPageLimit   = errors.New("browser page limit reached")
	errBrowserNotFound    = errors.New("browser page not found")
	errBrowserAttached    = errors.New("browser page already attached")
	errBrowserViewerLimit = errors.New("browser viewer limit reached")
	errBrowserStart       = errors.New("browser start failed")
	errBrowserNavigate    = errors.New("browser navigation failed")
)

type browserManagerConfig struct {
	MaxPages       int
	DetachedGrace  time.Duration
	ChromiumIdle   time.Duration
	CommandTimeout time.Duration
	FrameInterval  time.Duration
	JPEGQuality    int
	CDPFactory     browserCDPFactory
}

type browserManager struct {
	mu        sync.Mutex
	createMu  sync.Mutex
	config    browserManagerConfig
	cdp       browserCDP
	pages     map[string]*browserPage
	sessions  map[string]*browserPage
	contexts  map[string]*browserPage
	idleTimer *time.Timer
	closed    bool
}

type browserScreencastFrame struct {
	data      string
	sessionID int
	gen       uint64
}

type browserPage struct {
	manager     *browserManager
	id          string
	port        int
	contextID   string
	targetID    string
	sessionID   string
	mainFrameID string
	// viewport is the LAYOUT viewport pointer coordinates live in; a pinch zoom
	// makes it smaller than pane, which stays the size the viewer shows (and the
	// size the screencast image is capped at).
	viewport browserViewport
	pane     browserViewport
	zoom     float64

	mu            sync.Mutex
	url           string
	title         string
	state         string
	viewer        *browserViewer
	reserved      bool
	visible       bool
	expiry        *time.Timer
	latestFrame   chan []byte
	frameEvents   chan browserScreencastFrame
	frameStop     chan struct{}
	frameStopOnce sync.Once
	castMu        sync.Mutex
	casting       bool
	castGen       atomic.Uint64 // active screencast generation; 0 while stopped
	castEpoch     atomic.Uint64 // monotonic source for castGen values
	unreachable   bool
	topRequestID  string
	refreshing    atomic.Bool
}

func defaultBrowserManagerConfig() browserManagerConfig {
	fps := browserConfigInt("AF_BROWSER_MAX_FPS", 12, 1, 30)
	return browserManagerConfig{
		MaxPages:       browserConfigInt("AF_BROWSER_PAGE_LIMIT", 2, 1, 16),
		DetachedGrace:  time.Duration(browserConfigInt("AF_BROWSER_DETACHED_GRACE_SEC", 60, 1, 3600)) * time.Second,
		ChromiumIdle:   time.Duration(browserConfigInt("AF_BROWSER_IDLE_SEC", 120, 1, 3600)) * time.Second,
		CommandTimeout: 5 * time.Second,
		FrameInterval:  time.Second / time.Duration(fps),
		JPEGQuality:    browserConfigInt("AF_BROWSER_JPEG_QUALITY", 70, 1, 100),
		CDPFactory:     launchPipeCDP,
	}
}

var workspaceBrowserManager = newBrowserManager(defaultBrowserManagerConfig())

func newBrowserManager(config browserManagerConfig) *browserManager {
	if config.MaxPages <= 0 {
		config.MaxPages = 2
	}
	if config.DetachedGrace <= 0 {
		config.DetachedGrace = 60 * time.Second
	}
	if config.ChromiumIdle <= 0 {
		config.ChromiumIdle = 2 * time.Minute
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
	if config.CDPFactory == nil {
		config.CDPFactory = launchPipeCDP
	}
	return &browserManager{config: config, pages: make(map[string]*browserPage), sessions: make(map[string]*browserPage), contexts: make(map[string]*browserPage)}
}

func (m *browserManager) Create(req browserCreateRequest) (browserPageResponse, error) {
	target, err := browserTargetURL(req.Port, req.Path)
	if err != nil {
		return browserPageResponse{}, err
	}
	viewport, err := normalizeBrowserViewport(req.Viewport)
	if err != nil {
		return browserPageResponse{}, err
	}

	m.createMu.Lock()
	defer m.createMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return browserPageResponse{}, errors.New("browser manager is closed")
	}
	if len(m.pages) >= m.config.MaxPages {
		m.mu.Unlock()
		return browserPageResponse{}, errBrowserPageLimit
	}
	if m.idleTimer != nil {
		m.idleTimer.Stop()
		m.idleTimer = nil
	}
	m.mu.Unlock()

	cdp, err := m.ensureCDP()
	if err != nil {
		return browserPageResponse{}, fmt.Errorf("%w: %v", errBrowserStart, err)
	}
	p := &browserPage{
		manager: m, id: newBrowserID(), port: req.Port, viewport: viewport, pane: viewport, zoom: 1,
		url: target, state: "starting", visible: false,
		latestFrame: make(chan []byte, 1), frameEvents: make(chan browserScreencastFrame, 1), frameStop: make(chan struct{}),
	}
	if err := m.createCDPPage(cdp, p); err != nil {
		m.disposeCDPPage(cdp, p)
		return browserPageResponse{}, fmt.Errorf("%w: %v", errBrowserStart, err)
	}

	m.mu.Lock()
	if m.cdp != cdp || m.closed {
		m.mu.Unlock()
		m.disposeCDPPage(cdp, p)
		return browserPageResponse{}, errors.New("Chromium stopped while creating page")
	}
	m.pages[p.id] = p
	m.sessions[p.sessionID] = p
	m.contexts[p.contextID] = p
	m.mu.Unlock()
	go p.frameLoop()

	// Register ownership before navigation so Fetch.requestPaused can enforce the
	// first document request. Network failures are a normal target-unreachable state.
	var nav struct {
		ErrorText string `json:"errorText"`
	}
	if err := m.call(cdp, p.sessionID, "Page.navigate", map[string]any{"url": target}, &nav); err != nil {
		m.Delete(p.id)
		return browserPageResponse{}, fmt.Errorf("%w: %v", errBrowserNavigate, err)
	}
	if nav.ErrorText != "" {
		p.mu.Lock()
		p.unreachable = true
		p.mu.Unlock()
		p.setState("target-unreachable")
	}
	m.scheduleExpiry(p)
	return browserPageResponse{ID: p.id, Port: p.port, URL: target, State: "starting"}, nil
}

func (m *browserManager) ensureCDP() (browserCDP, error) {
	m.mu.Lock()
	if m.cdp != nil {
		cdp := m.cdp
		m.mu.Unlock()
		return cdp, nil
	}
	m.mu.Unlock()
	cdp, err := m.config.CDPFactory(context.Background())
	if err != nil {
		return nil, err
	}
	if err := m.call(cdp, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		_ = cdp.Close()
		return nil, err
	}
	// Chromium starts with one default about:blank target. It is not a pane and
	// must not remain as an unowned Page beside the per-browserId contexts.
	var targets struct {
		TargetInfos []struct {
			TargetID         string `json:"targetId"`
			Type             string `json:"type"`
			BrowserContextID string `json:"browserContextId"`
		} `json:"targetInfos"`
	}
	if err := m.call(cdp, "", "Target.getTargets", nil, &targets); err != nil {
		_ = cdp.Close()
		return nil, err
	}
	for _, target := range targets.TargetInfos {
		if target.Type == "page" && target.BrowserContextID == "" {
			_ = m.call(cdp, "", "Target.closeTarget", map[string]any{"targetId": target.TargetID}, nil)
		}
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = cdp.Close()
		return nil, errors.New("browser manager is closed")
	}
	if m.cdp != nil {
		existing := m.cdp
		m.mu.Unlock()
		_ = cdp.Close()
		return existing, nil
	}
	m.cdp = cdp
	m.mu.Unlock()
	go m.eventLoop(cdp)
	return cdp, nil
}

func (m *browserManager) createCDPPage(cdp browserCDP, p *browserPage) error {
	var contextResult struct {
		BrowserContextID string `json:"browserContextId"`
	}
	if err := m.call(cdp, "", "Target.createBrowserContext", map[string]any{"disposeOnDetach": true}, &contextResult); err != nil {
		return fmt.Errorf("create browser context: %w", err)
	}
	p.contextID = contextResult.BrowserContextID
	if err := m.call(cdp, "", "Browser.setDownloadBehavior", map[string]any{"behavior": "deny", "browserContextId": p.contextID}, nil); err != nil {
		return fmt.Errorf("deny downloads: %w", err)
	}
	var targetResult struct {
		TargetID string `json:"targetId"`
	}
	if err := m.call(cdp, "", "Target.createTarget", map[string]any{
		"url": "about:blank", "browserContextId": p.contextID, "width": p.viewport.Width, "height": p.viewport.Height,
	}, &targetResult); err != nil {
		return fmt.Errorf("create page target: %w", err)
	}
	p.targetID = targetResult.TargetID
	var attachResult struct {
		SessionID string `json:"sessionId"`
	}
	if err := m.call(cdp, "", "Target.attachToTarget", map[string]any{"targetId": p.targetID, "flatten": true}, &attachResult); err != nil {
		return fmt.Errorf("attach page target: %w", err)
	}
	p.sessionID = attachResult.SessionID
	for _, command := range []struct {
		method string
		params any
	}{
		{"Page.enable", nil}, {"Runtime.enable", nil}, {"Log.enable", nil}, {"Network.enable", nil},
		{"Page.setLifecycleEventsEnabled", map[string]any{"enabled": true}},
		{"Page.setInterceptFileChooserDialog", map[string]any{"enabled": true}},
		{"Fetch.enable", map[string]any{"patterns": []map[string]any{{"urlPattern": "*", "requestStage": "Request"}}}},
		{"Emulation.setDeviceMetricsOverride", map[string]any{"width": p.viewport.Width, "height": p.viewport.Height, "deviceScaleFactor": 1, "mobile": false}},
		{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": browserRestrictionScript}},
	} {
		if err := m.call(cdp, p.sessionID, command.method, command.params, nil); err != nil {
			return err
		}
	}
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err := m.call(cdp, p.sessionID, "Page.getFrameTree", nil, &tree); err != nil {
		return err
	}
	p.mainFrameID = tree.FrameTree.Frame.ID
	return nil
}

const browserRestrictionScript = `(() => {
  const denied = () => Promise.reject(new DOMException('Disabled in browser pane', 'NotAllowedError'));
  try { Object.defineProperty(Element.prototype, 'requestFullscreen', {value: denied}); } catch (_) {}
  try { Object.defineProperty(Element.prototype, 'requestPointerLock', {value: () => { throw new DOMException('Disabled in browser pane', 'NotAllowedError'); }}); } catch (_) {}
  try { Object.defineProperty(window, 'open', {value: () => null}); } catch (_) {}
  try { Object.defineProperty(window, 'showOpenFilePicker', {value: denied}); } catch (_) {}
  try { Object.defineProperty(window, 'showSaveFilePicker', {value: denied}); } catch (_) {}
  try { Object.defineProperty(navigator.mediaDevices, 'getUserMedia', {value: denied}); } catch (_) {}
  try { Object.defineProperty(Notification, 'requestPermission', {value: async () => 'denied'}); } catch (_) {}
})();`

func (m *browserManager) Get(id string) (browserPageResponse, bool) {
	m.mu.Lock()
	p := m.pages[id]
	m.mu.Unlock()
	if p == nil {
		return browserPageResponse{}, false
	}
	return p.response(), true
}

func (m *browserManager) Delete(id string) {
	m.mu.Lock()
	p := m.pages[id]
	if p == nil {
		m.mu.Unlock()
		return
	}
	delete(m.pages, id)
	delete(m.sessions, p.sessionID)
	delete(m.contexts, p.contextID)
	cdp := m.cdp
	if len(m.pages) == 0 && cdp != nil && !m.closed {
		m.idleTimer = time.AfterFunc(m.config.ChromiumIdle, func() { m.closeIdle(cdp) })
	}
	m.mu.Unlock()
	workspaceBrowserViewerLeases.release(browserPageViewerLease(p.id))

	p.mu.Lock()
	if p.expiry != nil {
		p.expiry.Stop()
		p.expiry = nil
	}
	v := p.viewer
	p.viewer, p.reserved, p.visible = nil, false, false
	p.state = "disconnected"
	p.mu.Unlock()
	p.stopFrameLoop()
	if v != nil {
		v.enqueueClose(websocketCloseNormal, "page deleted")
	}
	if cdp != nil {
		go m.disposeCDPPage(cdp, p)
	}
}

func (m *browserManager) disposeCDPPage(cdp browserCDP, p *browserPage) {
	if p.targetID != "" {
		_ = m.call(cdp, "", "Target.closeTarget", map[string]any{"targetId": p.targetID}, nil)
	}
	if p.contextID != "" {
		_ = m.call(cdp, "", "Target.disposeBrowserContext", map[string]any{"browserContextId": p.contextID}, nil)
	}
}

func (m *browserManager) closeIdle(cdp browserCDP) {
	m.mu.Lock()
	if m.cdp != cdp || len(m.pages) != 0 || m.closed {
		m.mu.Unlock()
		return
	}
	m.cdp = nil
	m.idleTimer = nil
	m.mu.Unlock()
	_ = cdp.Close()
}

func (m *browserManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	if m.idleTimer != nil {
		m.idleTimer.Stop()
	}
	cdp := m.cdp
	m.cdp = nil
	pages := m.pages
	m.pages = make(map[string]*browserPage)
	m.sessions = make(map[string]*browserPage)
	m.contexts = make(map[string]*browserPage)
	m.mu.Unlock()
	for _, p := range pages {
		p.stopFrameLoop()
		p.notifyFatalState("disconnected", "agent shutting down")
	}
	if cdp != nil {
		_ = cdp.Close()
	}
}

func (m *browserManager) reserve(id string) (*browserPage, error) {
	m.mu.Lock()
	p := m.pages[id]
	m.mu.Unlock()
	if p == nil {
		return nil, errBrowserNotFound
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reserved || p.viewer != nil {
		return nil, errBrowserAttached
	}
	if !workspaceBrowserViewerLeases.acquire(browserPageViewerLease(p.id)) {
		return nil, errBrowserViewerLimit
	}
	p.reserved = true
	if p.expiry != nil {
		p.expiry.Stop()
		p.expiry = nil
	}
	return p, nil
}

func (m *browserManager) attach(p *browserPage, v *browserViewer) bool {
	m.mu.Lock()
	owned := m.pages[p.id] == p
	m.mu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !owned || !p.reserved || p.viewer != nil {
		p.reserved = false
		workspaceBrowserViewerLeases.release(browserPageViewerLease(p.id))
		return false
	}
	p.reserved = false
	p.viewer = v
	p.visible = true
	return true
}

func (m *browserManager) releaseReservation(p *browserPage) {
	p.mu.Lock()
	p.reserved = false
	p.mu.Unlock()
	workspaceBrowserViewerLeases.release(browserPageViewerLease(p.id))
	m.scheduleExpiry(p)
}

func (m *browserManager) detach(p *browserPage, v *browserViewer) {
	p.mu.Lock()
	if p.viewer != v {
		p.mu.Unlock()
		return
	}
	p.viewer = nil
	p.visible = false
	p.mu.Unlock()
	workspaceBrowserViewerLeases.release(browserPageViewerLease(p.id))
	p.stopScreencast()
	m.scheduleExpiry(p)
}

func (m *browserManager) scheduleExpiry(p *browserPage) {
	p.mu.Lock()
	if p.expiry != nil {
		p.expiry.Stop()
	}
	p.expiry = time.AfterFunc(m.config.DetachedGrace, func() { m.Delete(p.id) })
	p.mu.Unlock()
}

func (m *browserManager) call(cdp browserCDP, session, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), m.config.CommandTimeout)
	defer cancel()
	return cdp.Call(ctx, method, params, session, result)
}

func (m *browserManager) eventLoop(cdp browserCDP) {
	for {
		select {
		case ev := <-cdp.Events():
			// The event is no longer queued. Release its byte reservation before
			// processing; at most this one <=8 MiB event remains live in the
			// single event-loop goroutine outside the 32 MiB queue budget.
			ev.releaseQueueBytes()
			m.handleEvent(cdp, ev)
		case err := <-cdp.Done():
			m.handleCrash(cdp, err)
			return
		}
	}
}

func (m *browserManager) handleCrash(cdp browserCDP, err error) {
	m.mu.Lock()
	if m.cdp != cdp {
		m.mu.Unlock()
		return
	}
	m.cdp = nil
	pages := m.pages
	m.pages = make(map[string]*browserPage)
	m.sessions = make(map[string]*browserPage)
	m.contexts = make(map[string]*browserPage)
	m.mu.Unlock()
	if err != nil {
		log.Printf("browser Chromium stopped: %v", err)
	}
	for _, p := range pages {
		p.stopFrameLoop()
		p.notifyFatalState("crashed", "Chromium crashed")
	}
}

func (m *browserManager) pageForSession(session string) *browserPage {
	m.mu.Lock()
	p := m.sessions[session]
	m.mu.Unlock()
	return p
}

func (m *browserManager) pageForTarget(target string) *browserPage {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.pages { // bounded by MaxPages (2 by default)
		if p.targetID == target {
			return p
		}
	}
	return nil
}

func (m *browserManager) handleEvent(cdp browserCDP, ev browserCDPEvent) {
	if ev.Method == "Target.targetCreated" {
		var v struct {
			TargetInfo struct {
				TargetID         string `json:"targetId"`
				BrowserContextID string `json:"browserContextId"`
				Type             string `json:"type"`
			} `json:"targetInfo"`
		}
		if json.Unmarshal(ev.Params, &v) == nil && v.TargetInfo.BrowserContextID != "" {
			m.mu.Lock()
			p := m.contexts[v.TargetInfo.BrowserContextID]
			m.mu.Unlock()
			if p != nil && v.TargetInfo.Type == "page" && v.TargetInfo.TargetID != p.targetID {
				_ = m.call(cdp, "", "Target.closeTarget", map[string]any{"targetId": v.TargetInfo.TargetID}, nil)
			}
		}
		return
	}
	if ev.Method == "Target.targetDestroyed" || ev.Method == "Target.targetCrashed" {
		var v struct {
			TargetID string `json:"targetId"`
		}
		if json.Unmarshal(ev.Params, &v) == nil {
			if p := m.pageForTarget(v.TargetID); p != nil {
				if ev.Method == "Target.targetCrashed" {
					m.invalidatePage(p, "crashed", "page crashed")
				} else {
					m.invalidatePage(p, "disconnected", "page closed")
				}
			}
		}
		return
	}
	p := m.pageForSession(ev.SessionID)
	if p == nil {
		return
	}
	switch ev.Method {
	case "Fetch.requestPaused":
		m.handleRequestPaused(cdp, p, ev.Params)
	case "Page.frameRequestedNavigation", "Page.frameStartedNavigating":
		m.handleRequestedNavigation(cdp, p, ev.Params)
	case "Page.screencastFrame":
		var v struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
		}
		if json.Unmarshal(ev.Params, &v) == nil {
			p.offerScreencastFrame(v.Data, v.SessionID)
		}
	case "Page.frameNavigated":
		var v struct {
			Frame struct {
				ID       string `json:"id"`
				ParentID string `json:"parentId"`
				URL      string `json:"url"`
			} `json:"frame"`
		}
		if json.Unmarshal(ev.Params, &v) == nil && v.Frame.ParentID == "" {
			p.mu.Lock()
			p.mainFrameID = v.Frame.ID
			if u, err := url.Parse(v.Frame.URL); err == nil && allowedTopLevelBrowserURL(u) {
				p.url = normalizeLoopbackURL(u).String()
				p.mu.Unlock()
				p.refreshNavigation()
			} else {
				safeURL := p.url
				p.mu.Unlock()
				p.notifyJSON(map[string]any{"type": "page-error", "text": "top-level navigation outside loopback was blocked"})
				_ = m.call(cdp, p.sessionID, "Page.navigate", map[string]any{"url": safeURL}, nil)
			}
		}
	case "Network.responseReceived":
		var v struct {
			Type    string `json:"type"`
			FrameID string `json:"frameId"`
		}
		p.mu.Lock()
		mainFrameID := p.mainFrameID
		p.mu.Unlock()
		if json.Unmarshal(ev.Params, &v) == nil && v.Type == "Document" && v.FrameID == mainFrameID {
			p.mu.Lock()
			p.unreachable = false
			p.mu.Unlock()
		}
	case "Network.loadingFailed":
		var v struct {
			Type      string `json:"type"`
			RequestID string `json:"requestId"`
		}
		if json.Unmarshal(ev.Params, &v) == nil && v.Type == "Document" {
			p.mu.Lock()
			isTop := v.RequestID != "" && v.RequestID == p.topRequestID
			if isTop {
				p.unreachable = true
			}
			p.mu.Unlock()
			if isTop {
				p.setState("target-unreachable")
			}
		}
	case "Page.lifecycleEvent":
		var v struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(ev.Params, &v) == nil && (v.Name == "load" || v.Name == "networkIdle") {
			p.markLoaded()
			p.refreshNavigation()
		}
	case "Page.loadEventFired":
		p.markLoaded()
		p.refreshNavigation()
	case "Inspector.targetCrashed", "Target.targetCrashed":
		m.invalidatePage(p, "crashed", "page crashed")
	case "Page.fileChooserOpened":
		_ = m.call(cdp, p.sessionID, "Page.handleFileChooser", map[string]any{"action": "cancel"}, nil)
	case "Runtime.consoleAPICalled":
		p.handleConsoleEvent(ev.Params)
	case "Runtime.exceptionThrown":
		p.handleExceptionEvent(ev.Params)
	}
}

func (m *browserManager) invalidatePage(p *browserPage, state, reason string) {
	m.mu.Lock()
	if m.pages[p.id] != p {
		m.mu.Unlock()
		return
	}
	delete(m.pages, p.id)
	delete(m.sessions, p.sessionID)
	delete(m.contexts, p.contextID)
	cdp := m.cdp
	if len(m.pages) == 0 && cdp != nil && !m.closed {
		m.idleTimer = time.AfterFunc(m.config.ChromiumIdle, func() { m.closeIdle(cdp) })
	}
	m.mu.Unlock()
	p.stopFrameLoop()
	p.notifyFatalState(state, reason)
	if cdp != nil {
		go m.disposeCDPPage(cdp, p)
	}
}

func (m *browserManager) handleRequestPaused(cdp browserCDP, p *browserPage, raw json.RawMessage) {
	var v struct {
		RequestID    string `json:"requestId"`
		NetworkID    string `json:"networkId"`
		FrameID      string `json:"frameId"`
		ResourceType string `json:"resourceType"`
		Request      struct {
			URL string `json:"url"`
		} `json:"request"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	p.mu.Lock()
	mainFrameID := p.mainFrameID
	p.mu.Unlock()
	top := v.ResourceType == "Document" && v.FrameID == mainFrameID
	blocked := forbiddenBrowserResource(v.Request.URL)
	params := map[string]any{"requestId": v.RequestID}
	if top {
		u, err := url.Parse(v.Request.URL)
		if err != nil || !allowedTopLevelBrowserURL(u) {
			blocked = true
		} else if normalized := normalizeLoopbackURL(u).String(); normalized != v.Request.URL {
			params["url"] = normalized
		}
		if !blocked {
			p.mu.Lock()
			p.unreachable = false
			p.topRequestID = v.NetworkID
			p.mu.Unlock()
			p.setState("loading")
		}
	}
	if blocked {
		_ = m.call(cdp, p.sessionID, "Fetch.failRequest", map[string]any{"requestId": v.RequestID, "errorReason": "BlockedByClient"}, nil)
		if top {
			p.notifyJSON(map[string]any{"type": "page-error", "text": "top-level navigation outside loopback was blocked"})
			p.setState("ready")
		}
		return
	}
	_ = m.call(cdp, p.sessionID, "Fetch.continueRequest", params, nil)
}

func (m *browserManager) handleRequestedNavigation(cdp browserCDP, p *browserPage, raw json.RawMessage) {
	var v struct {
		FrameID string `json:"frameId"`
		URL     string `json:"url"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	p.mu.Lock()
	mainFrameID := p.mainFrameID
	p.mu.Unlock()
	u, err := url.Parse(v.URL)
	if v.FrameID != mainFrameID || (err == nil && allowedTopLevelBrowserURL(u)) {
		return
	}
	_ = m.call(cdp, p.sessionID, "Page.stopLoading", nil, nil)
	p.notifyJSON(map[string]any{"type": "page-error", "text": "top-level navigation outside loopback was blocked"})
}

func (p *browserPage) markLoaded() {
	p.mu.Lock()
	unreachable := p.unreachable
	p.mu.Unlock()
	if unreachable {
		p.setState("target-unreachable")
	} else {
		p.setState("ready")
	}
}

func (p *browserPage) response() browserPageResponse {
	p.mu.Lock()
	defer p.mu.Unlock()
	return browserPageResponse{ID: p.id, Port: p.port, URL: p.url, Title: p.title, State: p.state}
}

func (p *browserPage) setState(state string) {
	p.mu.Lock()
	if p.state == state {
		p.mu.Unlock()
		return
	}
	p.state = state
	p.mu.Unlock()
	p.notifyJSON(map[string]any{"type": "state", "state": state})
}

func (p *browserPage) notifyJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	p.mu.Lock()
	viewer := p.viewer
	p.mu.Unlock()
	if viewer != nil {
		viewer.enqueueText(b)
	}
}

func (p *browserPage) notifyFatalState(state, reason string) {
	p.mu.Lock()
	p.state = state
	if p.expiry != nil {
		p.expiry.Stop()
	}
	v := p.viewer
	p.viewer, p.reserved, p.visible = nil, false, false
	p.mu.Unlock()
	workspaceBrowserViewerLeases.release(browserPageViewerLease(p.id))
	if v != nil {
		b, _ := json.Marshal(map[string]any{"type": "state", "state": state})
		v.enqueueTextAndClose(b, websocketCloseGoingAway, reason)
	}
}

func (p *browserPage) enqueueFrame(frame []byte) {
	p.mu.Lock()
	visible := p.visible && p.viewer != nil
	p.mu.Unlock()
	if !visible {
		return
	}
	select {
	case p.latestFrame <- frame:
	default:
		select {
		case <-p.latestFrame:
		default:
		}
		select {
		case p.latestFrame <- frame:
		default:
		}
	}
}

// offerScreencastFrame hands the newest raw screencast frame to frameLoop through
// a latest-only, non-blocking buffer. Real Chromium keeps several frames in flight
// before it observes an ACK, so a burst of frames must replace the pending one
// rather than be treated as a fatal protocol error. Frames that arrive while the
// cast is stopped (gen 0) or that belong to a superseded generation are dropped
// here so a stop/start restart never mixes old and new casts.
func (p *browserPage) offerScreencastFrame(data string, sessionID int) {
	gen := p.castGen.Load()
	if gen == 0 {
		return
	}
	frame := browserScreencastFrame{data: data, sessionID: sessionID, gen: gen}
	select {
	case p.frameEvents <- frame:
		return
	default:
	}
	// Buffer full: drop the older pending frame and keep only the newest.
	select {
	case <-p.frameEvents:
	default:
	}
	select {
	case p.frameEvents <- frame:
	default:
	}
}

// frameLoop is the sole base64 decoder and screencast ACK producer for a Page.
// It decodes only the latest buffered frame and paces one ACK per FrameInterval:
// Chromium throttles capture toward that ACK rate once its in-flight limit is
// reached, while any extra in-flight frames are absorbed by the latest-only buffer
// instead of crashing the Page. Frames from a superseded screencast generation are
// dropped without an ACK so a stop/start restart never acknowledges a stale cast.
func (p *browserPage) frameLoop() {
	for {
		select {
		case <-p.frameStop:
			return
		case event := <-p.frameEvents:
			if event.gen != p.castGen.Load() {
				continue
			}
			if frame, err := base64.StdEncoding.DecodeString(event.data); err == nil {
				p.enqueueFrame(frame)
			}
			timer := time.NewTimer(p.manager.config.FrameInterval)
			select {
			case <-p.frameStop:
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
			if event.gen != p.castGen.Load() {
				continue
			}
			p.manager.mu.Lock()
			cdp := p.manager.cdp
			owned := p.manager.pages[p.id] == p
			p.manager.mu.Unlock()
			if cdp != nil && owned {
				_ = p.manager.call(cdp, p.sessionID, "Page.screencastFrameAck", map[string]any{"sessionId": event.sessionID}, nil)
			}
		}
	}
}

func (p *browserPage) stopFrameLoop() {
	if p.frameStop == nil {
		return
	}
	p.frameStopOnce.Do(func() { close(p.frameStop) })
}

// screencastFrameNotActive reports whether a Page.startScreencast error is the
// transient "the main frame is not live yet" rejection that Chromium raises when
// the call lands in the window where the page target is swapping from its initial
// about:blank document to the navigated document. It is not a fatal condition: the
// frame goes live within a few tens of milliseconds and the cast then arms cleanly.
func screencastFrameNotActive(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Not attached to an active page")
}

func (p *browserPage) startScreencast() error {
	p.castMu.Lock()
	defer p.castMu.Unlock()
	if p.casting {
		return nil
	}
	// A viewer attaches right after POST /browser/pages returns, which is while the
	// page is still committing its first navigation (about:blank -> target). If the
	// very first Page.startScreencast lands in that commit window Chromium rejects it
	// with "Not attached to an active page". A fast, low-latency viewer attach (the
	// normal Console path against a fast target) can hit this reliably and would
	// otherwise crash the whole pane with zero frames. Retry briefly on that one
	// transient error; the main frame goes live within a few tens of milliseconds.
	// Any other error, or a page that has gone away, is returned immediately.
	var err error
	for attempt := 0; attempt < 12; attempt++ {
		p.manager.mu.Lock()
		cdp := p.manager.cdp
		owned := p.manager.pages[p.id] == p
		p.manager.mu.Unlock()
		if cdp == nil || !owned {
			return errBrowserNotFound
		}
		p.mu.Lock()
		// Cap the IMAGE at the pane, not the layout viewport: a pinch zoom lays the
		// page out smaller on purpose and renders it at a higher device pixel ratio,
		// and capping at the layout would throw exactly those pixels away.
		image := p.pane
		if image.Width < 1 || image.Height < 1 {
			image = p.viewport
		}
		p.mu.Unlock()
		err = p.manager.call(cdp, p.sessionID, "Page.startScreencast", map[string]any{
			"format": "jpeg", "quality": p.manager.config.JPEGQuality, "maxWidth": image.Width, "maxHeight": image.Height,
		}, nil)
		if err == nil {
			p.casting = true
			// Publish a fresh generation so frames from any prior cast are dropped and
			// only this cast's frames are decoded and acknowledged.
			p.castGen.Store(p.castEpoch.Add(1))
			return nil
		}
		if !screencastFrameNotActive(err) {
			return err
		}
		time.Sleep(40 * time.Millisecond)
	}
	return err
}

func (p *browserPage) stopScreencast() {
	p.castMu.Lock()
	defer p.castMu.Unlock()
	if !p.casting {
		return
	}
	// Retire the generation first so late in-flight frames are dropped, not acked.
	p.castGen.Store(0)
	p.manager.mu.Lock()
	cdp := p.manager.cdp
	p.manager.mu.Unlock()
	if cdp != nil {
		_ = p.manager.call(cdp, p.sessionID, "Page.stopScreencast", nil, nil)
	}
	p.casting = false
}

// restartScreencastForResize re-arms the screencast so its maxWidth/maxHeight track
// a new viewport. It only restarts an already-running cast; when nothing is casting
// (for example a hidden viewer) the next startScreencast picks up the new size on
// its own, so this avoids starting a cast no one is watching.
func (p *browserPage) restartScreencastForResize() {
	p.castMu.Lock()
	casting := p.casting
	p.castMu.Unlock()
	if !casting {
		return
	}
	p.stopScreencast()
	_ = p.startScreencast()
}

func (p *browserPage) refreshNavigation() {
	if !p.refreshing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer p.refreshing.Store(false)
		p.manager.mu.Lock()
		cdp := p.manager.cdp
		p.manager.mu.Unlock()
		if cdp == nil {
			return
		}
		var history struct {
			CurrentIndex int `json:"currentIndex"`
			Entries      []struct {
				ID    int    `json:"id"`
				URL   string `json:"url"`
				Title string `json:"title"`
			} `json:"entries"`
		}
		if p.manager.call(cdp, p.sessionID, "Page.getNavigationHistory", nil, &history) != nil || len(history.Entries) == 0 {
			return
		}
		entry := history.Entries[history.CurrentIndex]
		p.mu.Lock()
		if u, err := url.Parse(entry.URL); err == nil && allowedTopLevelBrowserURL(u) {
			p.url = normalizeLoopbackURL(u).String()
		}
		p.title = truncateBrowserText(entry.Title, 1024)
		urlNow, title := p.url, p.title
		p.mu.Unlock()
		p.notifyJSON(map[string]any{
			"type": "navigation", "url": urlNow, "title": title,
			"canBack": history.CurrentIndex > 0, "canForward": history.CurrentIndex+1 < len(history.Entries),
		})
	}()
}

func (p *browserPage) handleConsoleEvent(raw json.RawMessage) {
	var v struct {
		Type      string  `json:"type"`
		Timestamp float64 `json:"timestamp"`
		Args      []struct {
			Type        string `json:"type"`
			Value       any    `json:"value"`
			Description string `json:"description"`
		} `json:"args"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	parts := make([]string, 0, len(v.Args))
	for _, arg := range v.Args {
		if arg.Value != nil {
			parts = append(parts, fmt.Sprint(arg.Value))
		} else if arg.Description != "" {
			parts = append(parts, arg.Description)
		} else {
			parts = append(parts, arg.Type)
		}
	}
	level := v.Type
	if level != "error" && level != "warning" && level != "warn" && level != "info" && level != "debug" {
		level = "log"
	}
	if level == "warning" {
		level = "warn"
	}
	ts := time.UnixMilli(int64(v.Timestamp * 1000)).UTC().Format(time.RFC3339Nano)
	p.notifyJSON(map[string]any{"type": "console", "level": level, "text": truncateBrowserText(strings.Join(parts, " "), browserMaxConsoleText), "ts": ts})
}

func (p *browserPage) handleExceptionEvent(raw json.RawMessage) {
	var v struct {
		ExceptionDetails struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	text := v.ExceptionDetails.Exception.Description
	if text == "" {
		text = v.ExceptionDetails.Text
	}
	p.notifyJSON(map[string]any{"type": "page-error", "text": truncateBrowserText(text, browserMaxConsoleText)})
}

func newBrowserID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func browserConfigInt(key string, fallback, min, max int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n >= min && n <= max {
		return n
	}
	return fallback
}
