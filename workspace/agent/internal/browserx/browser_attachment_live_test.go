package browserx

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestBrowserAttachmentLiveExternalOwner is opt-in because it starts the real
// system Chromium. It verifies the product discovery/WebSocket adapter and the
// ownership boundary: AF detach must leave the external Page and process alive.
func TestBrowserAttachmentLiveExternalOwner(t *testing.T) {
	if os.Getenv("AF_CHROMIUM_ATTACH_LIVE") != "1" {
		t.Skip("set AF_CHROMIUM_ATTACH_LIVE=1 to run the real external-owner Chromium test")
	}
	bin, err := findChromiumBinary()
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Attach fixture</title><button>OK</button>`)
	}))
	defer fixture.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	profile := t.TempDir()
	cmd := exec.CommandContext(ctx, bin,
		"--headless=new", "--no-sandbox", "--no-first-run", "--disable-dev-shm-usage",
		"--remote-debugging-address=127.0.0.1", "--remote-debugging-port="+strconv.Itoa(port),
		"--user-data-dir="+profile, fixture.URL)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- cmd.Wait() }()
	t.Cleanup(func() {
		cancel()
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		select {
		case <-processDone:
		case <-time.After(3 * time.Second):
		}
	})

	var discovery cdpDiscovery
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		discovery, err = discoverCDPTargets(port, time.Second)
		if err == nil && len(discovery.Targets) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil || len(discovery.Targets) == 0 {
		t.Fatalf("discover live Chromium: targets=%v err=%v", discovery.Targets, err)
	}
	var target browserAttachTarget
	for _, candidate := range discovery.Targets {
		if strings.TrimRight(candidate.URL, "/") == strings.TrimRight(fixture.URL, "/") || candidate.Title == "Attach fixture" {
			target = candidate
			break
		}
	}
	if target.TargetID == "" {
		t.Fatalf("fixture target not found: %+v", discovery.Targets)
	}

	m := newBrowserAttachmentManager(defaultBrowserAttachmentManagerConfig())
	resp, err := m.Create(browserAttachmentCreateRequest{
		Port: port, TargetID: target.TargetID,
		Viewport: browserViewportRequest{Width: 800, Height: 600, DeviceScaleFactor: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.lookupActive(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.viewer = &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 8), done: make(chan struct{})}
	a.visible = true
	a.state = attachmentStateViewerOpen
	a.mu.Unlock()
	if err := a.startScreencast(); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-a.latestFrame:
		if len(frame) < 4 {
			t.Fatalf("short live screencast frame: %d bytes", len(frame))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("live Chromium produced no screencast frame")
	}
	m.Delete(resp.ID)

	after, err := discoverCDPTargets(port, time.Second)
	if err != nil {
		t.Fatalf("external Chromium died on detach: %v", err)
	}
	for _, candidate := range after.Targets {
		if candidate.TargetID == target.TargetID {
			return
		}
	}
	t.Fatalf("external Page was closed on detach: before=%s after=%+v", target.TargetID, after.Targets)
}

// TestBrowserAttachmentLivePortCollision pins the Chromium behaviour the whole
// port contract rests on (docs/log/53 §53.16), measured 2026-08-08 on Chrome 151:
// a second Chromium told to use an already-taken --remote-debugging-port does
// NOT fail — it silently binds the other loopback family and runs on. Discovery
// dials 127.0.0.1, so without a guard the second session attaches to the FIRST
// session's browser. If a future Chromium starts failing that launch instead,
// this test tells us the guard's premise changed.
func TestBrowserAttachmentLivePortCollision(t *testing.T) {
	if os.Getenv("AF_CHROMIUM_ATTACH_LIVE") != "1" {
		t.Skip("set AF_CHROMIUM_ATTACH_LIVE=1 to run the real two-Chromium collision test")
	}
	bin, err := findChromiumBinary()
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	first := startLiveChromium(t, bin, strconv.Itoa(port))
	waitForLiveCDP(t, port)
	second := startLiveChromium(t, bin, strconv.Itoa(port))

	// The premise: the loser keeps running rather than exiting on a taken port.
	time.Sleep(2 * time.Second)
	if !liveProcessAlive(second) {
		t.Fatal("premise changed: the second Chromium now exits on a taken port — revisit the ambiguity guard")
	}
	if owners, ok := cdpPortListeners(port); !ok || len(owners) < 2 {
		t.Fatalf("two Chromium processes must be visible on the port: owners=%+v ok=%v", owners, ok)
	}
	if _, err := discoverCDPTargets(port, 2*time.Second); asAttachmentAPIError(err).Code != "cdp_port_ambiguous" {
		t.Fatalf("a contended port must be refused, got %v", err)
	}
	_ = first

	// The prescribed launch instead: port 0, then read the port and the instance
	// GUID back out of DevToolsActivePort.
	profile := t.TempDir()
	startLiveChromium(t, bin, "0", "--user-data-dir="+profile)
	var activePort int
	var wantBrowserID string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		if readErr == nil && len(lines) == 2 {
			activePort, _ = strconv.Atoi(strings.TrimSpace(lines[0]))
			wantBrowserID = NormalizeCDPBrowserID(strings.TrimSpace(lines[1]))
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if activePort == 0 || wantBrowserID == "" {
		t.Fatal("--remote-debugging-port=0 must publish port and browser GUID via DevToolsActivePort")
	}
	discovery, err := discoverCDPTargets(activePort, 3*time.Second)
	if err != nil {
		t.Fatalf("discover auto-assigned port %d: %v", activePort, err)
	}
	if discovery.BrowserID != wantBrowserID {
		t.Fatalf("browserId=%q want DevToolsActivePort's %q", discovery.BrowserID, wantBrowserID)
	}

	m := newBrowserAttachmentManager(defaultBrowserAttachmentManagerConfig())
	defer m.Close()
	if len(discovery.Targets) == 0 {
		t.Fatal("auto-assigned Chromium exposed no page target")
	}
	_, err = m.Create(browserAttachmentCreateRequest{
		Port: activePort, TargetID: discovery.Targets[0].TargetID,
		BrowserID: "11111111-2222-3333-4444-555555555555",
		Viewport:  browserViewportRequest{Width: 800, Height: 600, DeviceScaleFactor: 1},
	})
	if asAttachmentAPIError(err).Code != "cdp_browser_mismatch" {
		t.Fatalf("a foreign browserId must be refused against a live endpoint, got %v", err)
	}
}

// TestBrowserAttachmentLiveZoomToFit proves the zoom-out against a real
// Chromium: the fake CDP cannot tell us whether setDeviceMetricsOverride's
// `scale` really keeps the screencast at pane size while the page lays out
// wider. A min-width desktop page in a narrow pane is the case the user hits.
func TestBrowserAttachmentLiveZoomToFit(t *testing.T) {
	if os.Getenv("AF_CHROMIUM_ATTACH_LIVE") != "1" {
		t.Skip("set AF_CHROMIUM_ATTACH_LIVE=1 to run the real zoom-to-fit test")
	}
	bin, err := findChromiumBinary()
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	// Edge markers: a correct fit puts the page's own left and right edges on the
	// frame's left and right edges. Frame SIZE alone proves nothing — a page drawn
	// small in the corner of a pane-sized frame passes that check and is exactly
	// what the user reported seeing.
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Wide fixture</title>`+
			`<body style="margin:0;background:#fff">`+
			`<div style="min-width:1240px;height:1500px;display:flex">`+
			`<div style="width:20%;background:#00ff00"></div>`+
			`<div style="flex:1;background:#ffffff"></div>`+
			`<div style="width:20%;background:#ff0000"></div>`+
			`</div>`)
	}))
	defer fixture.Close()

	profile := t.TempDir()
	startLiveChromium(t, bin, "0", "--user-data-dir="+profile, fixture.URL)
	port, _ := liveDevToolsPort(t, profile)
	discovery, err := discoverCDPTargets(port, 3*time.Second)
	if err != nil || len(discovery.Targets) == 0 {
		t.Fatalf("discover: %v targets=%d", err, len(discovery.Targets))
	}
	m := newBrowserAttachmentManager(defaultBrowserAttachmentManagerConfig())
	defer m.Close()
	resp, err := m.Create(browserAttachmentCreateRequest{
		Port: port, TargetID: discovery.Targets[0].TargetID,
		Viewport: browserViewportRequest{Width: 660, Height: 800, DeviceScaleFactor: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.lookupActive(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.viewer = &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 8), done: make(chan struct{})}
	a.visible = true
	a.state = attachmentStateViewerOpen
	viewer := a.viewer
	a.mu.Unlock()
	if err := a.startScreencast(); err != nil {
		t.Fatal(err)
	}
	// Attach models a page the user is ALREADY looking at: wait for the fixture to
	// lay out before asking for a fit, or the measurement reads a blank document
	// and correctly decides nothing needs zooming.
	waitForLiveContentWidth(t, m, a, 1240)

	viewer.handleControl([]byte(`{"type":"viewport","width":660,"height":800,"fit":true}`))

	// The page must now be laid out at the content width, not clipped to 660.
	var evaluated struct {
		Result struct {
			Value float64 `json:"value"`
		} `json:"result"`
	}
	if err := m.call(a.cdp, a.sessionID, "Runtime.evaluate",
		map[string]any{"expression": "innerWidth", "returnByValue": true}, &evaluated); err != nil {
		t.Fatal(err)
	}
	if evaluated.Result.Value < 1200 {
		t.Fatalf("innerWidth=%v — the page was not zoomed out to fit", evaluated.Result.Value)
	}

	// ...while the frames stay pane-sized AND the page fills them.
	deadline := time.After(8 * time.Second)
	for {
		var frame []byte
		select {
		case frame = <-a.latestFrame:
		case <-deadline:
			t.Fatal("no screencast frame showed the zoomed page")
		}
		img, err := jpeg.Decode(bytes.NewReader(frame))
		if err != nil {
			t.Fatalf("decode screencast frame: %v", err)
		}
		size := img.Bounds().Size()
		if size.X > 700 || size.Y > 840 {
			t.Fatalf("frame %dx%d is not pane-sized — zoom cost bandwidth", size.X, size.Y)
		}
		// The page must FILL the frame: green band at the far left, red at the
		// far right. Blank on the right means it was drawn small in the corner.
		left := img.At(size.X/40, size.Y/2)
		right := img.At(size.X-1-size.X/40, size.Y/2)
		if isGreenish(left) && isReddish(right) {
			m.Delete(resp.ID)
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the page never filled the frame: left=%v right=%v (frame %dx%d)", left, right, size.X, size.Y)
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestBrowserAttachmentLivePinchZoom measures what pinch zoom rests on, which no
// recorded CDP call can prove: pinching in from zoom-to-fit really RE-LAYS OUT
// the page (innerWidth divides by the zoom) and the frame is captured from that
// new layout, filling it edge to edge. That is the difference between this and
// magnifying the image — the fitted frame has already lost the pixels a
// magnifier would have to invent.
//
// It also pins the sharpness ceiling: at the zoom that lands the layout on the
// pane the frame is pane-sized, i.e. 1:1. (Zooming further is allowed and is
// then a plain upscale — the screencast ignores the emulated device pixel ratio,
// measured on Chromium 151 and noted in zoomedLayout.)
func TestBrowserAttachmentLivePinchZoom(t *testing.T) {
	if os.Getenv("AF_CHROMIUM_ATTACH_LIVE") != "1" {
		t.Skip("set AF_CHROMIUM_ATTACH_LIVE=1 to run the real pinch zoom test")
	}
	bin, err := findChromiumBinary()
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	// A wide site, so the pane starts zoomed OUT to fit — the state a phone user
	// actually pinches out of. Edge markers again: the page must FILL the zoomed
	// frame, not sit small in a corner of it.
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Zoom fixture</title>`+
			`<body style="margin:0;background:#fff">`+
			`<div style="min-width:1240px;height:3000px;display:flex">`+
			`<div style="width:20%;background:#00ff00"></div>`+
			`<div style="flex:1;background:#ffffff"></div>`+
			`<div style="width:20%;background:#ff0000"></div>`+
			`</div>`)
	}))
	defer fixture.Close()

	profile := t.TempDir()
	startLiveChromium(t, bin, "0", "--user-data-dir="+profile, fixture.URL)
	port, _ := liveDevToolsPort(t, profile)
	discovery, err := discoverCDPTargets(port, 3*time.Second)
	if err != nil || len(discovery.Targets) == 0 {
		t.Fatalf("discover: %v targets=%d", err, len(discovery.Targets))
	}
	m := newBrowserAttachmentManager(defaultBrowserAttachmentManagerConfig())
	defer m.Close()
	resp, err := m.Create(browserAttachmentCreateRequest{
		Port: port, TargetID: discovery.Targets[0].TargetID,
		Viewport: browserViewportRequest{Width: 660, Height: 800, DeviceScaleFactor: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.lookupActive(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Delete(resp.ID)
	a.mu.Lock()
	a.viewer = &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 8), done: make(chan struct{})}
	a.visible = true
	a.state = attachmentStateViewerOpen
	viewer := a.viewer
	a.mu.Unlock()
	if err := a.startScreencast(); err != nil {
		t.Fatal(err)
	}

	// Attach models a page the user is ALREADY looking at: let the fixture lay out
	// before pinching. Restarting the screencast inside the initial navigation's
	// commit window is rejected by Chromium and the cast would simply stay dead.
	waitForLiveContentWidth(t, m, a, 1240)

	// Fit puts 1240 CSS px in the 660 px pane (a third of life size on a phone);
	// pinching 2x lays it out at 620 — one CSS pixel per pane pixel.
	viewer.handleControl([]byte(`{"type":"viewport","width":660,"height":800,"fit":true,"zoom":2}`))

	// Polled: measured here, Blink still reports the PREVIOUS innerWidth for a few
	// tens of milliseconds after setDeviceMetricsOverride returns, so a single read
	// right after the call sees the old layout and would fail a working zoom.
	var layout string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		var evaluated struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if err := m.call(a.cdp, a.sessionID, "Runtime.evaluate",
			map[string]any{"expression": "innerWidth + 'x' + innerHeight", "returnByValue": true}, &evaluated); err != nil {
			t.Fatal(err)
		}
		layout = evaluated.Result.Value
		if layout == "620x752" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if layout != "620x752" {
		t.Fatalf("zoomed layout = %q, want 620x752 (1240x1503 fitted, pinched 2x)", layout)
	}

	// The fixture is a rigid 1240 px site, so the marker bands are the giveaway:
	// fitted, the frame carries the WHOLE page and ends in the red band; zoomed to
	// life size it carries the left half, so the right sample is the page's own
	// white middle. Frames only flow on a visual change, so a retry re-arms the
	// cast rather than waiting for a repaint that will never come.
	var size image.Point
	var left, right color.Color
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		select {
		case frame := <-a.latestFrame:
			img, err := jpeg.Decode(bytes.NewReader(frame))
			if err != nil {
				t.Fatalf("decode screencast frame: %v", err)
			}
			size = img.Bounds().Size()
			left = img.At(size.X/40, size.Y/2)
			// Sampled inside the content, not in the scrollbar gutter at the edge.
			right = img.At(size.X-1-size.X/12, size.Y/2)
			if isGreenish(left) && !isReddish(right) {
				// Captured FROM the 620 px layout — life size for a 660 px pane. A
				// magnified fitted frame would still be a 660 px capture of 1240 px.
				if size.X < 590 || size.X > 660 {
					t.Fatalf("zoomed frame is %dx%d — not captured from the zoomed layout", size.X, size.Y)
				}
				return
			}
		case <-time.After(time.Second):
			a.restartScreencast()
		}
	}
	t.Fatalf("no frame showed the zoomed page: left=%v right=%v (frame %v)", left, right, size)
}

// TestBrowserAttachmentLiveInputDoesNotBlockOnAcks guards the throughput of the
// input path, which is what a swipe on a phone actually stresses.
//
// Chromium answers an Input.dispatch* command only after it has processed the
// event, and that is frame-paced: waiting for each reply cost ~16 ms per event
// (measured), while a finger produces 60-120 events a second. The viewer handles
// control messages on its read loop, so the whole swipe queued up behind those
// acks and the page kept scrolling long after the finger stopped. Pointer-rate
// events are therefore dispatched without waiting.
//
// Keystrokes and clicks deliberately still wait: they are one event per user
// action, and TestBrowserPlainASCIIKeyTypingLandsInForm reads the DOM right after
// typing — that ordering is only guaranteed by the reply.
func TestBrowserAttachmentLiveInputDoesNotBlockOnAcks(t *testing.T) {
	if os.Getenv("AF_CHROMIUM_ATTACH_LIVE") != "1" {
		t.Skip("set AF_CHROMIUM_ATTACH_LIVE=1 to run the real input throughput test")
	}
	bin, err := findChromiumBinary()
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Tall fixture</title>`+
			`<body style="margin:0"><div style="height:20000px;background:linear-gradient(#fff,#000)"></div>`)
	}))
	defer fixture.Close()

	profile := t.TempDir()
	startLiveChromium(t, bin, "0", "--user-data-dir="+profile, fixture.URL)
	port, _ := liveDevToolsPort(t, profile)
	discovery, err := discoverCDPTargets(port, 3*time.Second)
	if err != nil || len(discovery.Targets) == 0 {
		t.Fatalf("discover: %v targets=%d", err, len(discovery.Targets))
	}
	m := newBrowserAttachmentManager(defaultBrowserAttachmentManagerConfig())
	defer m.Close()
	resp, err := m.Create(browserAttachmentCreateRequest{
		Port: port, TargetID: discovery.Targets[0].TargetID,
		Viewport: browserViewportRequest{Width: 390, Height: 800, DeviceScaleFactor: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.lookupActive(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Delete(resp.ID)
	a.mu.Lock()
	a.viewer = &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 64), done: make(chan struct{})}
	a.visible = true
	a.state = attachmentStateViewerOpen
	a.controlMode = attachmentControlUser
	viewer := a.viewer
	a.mu.Unlock()
	waitForLiveContentWidth(t, m, a, 390)

	// 40 wheel events is about a third of a second of finger movement.
	const events = 40
	start := time.Now()
	for i := 0; i < events; i++ {
		viewer.handleControl([]byte(`{"type":"wheel","x":195,"y":400,"deltaX":0,"deltaY":40,"modifiers":0}`))
	}
	wheelSpent := time.Since(start)
	start = time.Now()
	for i := 0; i < events; i++ {
		viewer.handleControl([]byte(`{"type":"mouse","event":"move","x":195,"y":400,"button":"none","buttons":0,"modifiers":0,"clickCount":0}`))
	}
	moveSpent := time.Since(start)
	// Measured: ~4 ms for either burst, against ~625 ms when each event waited for
	// its ack. The bound is deliberately loose — it only has to fail if the wait
	// comes back.
	if wheelSpent > 150*time.Millisecond || moveSpent > 150*time.Millisecond {
		t.Fatalf("input is waiting for Chromium again: %d wheels took %v, %d moves took %v", events, wheelSpent, events, moveSpent)
	}

	// ...and the events really arrived: 40 x 40 px of scrolling.
	var scrolled float64
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		var out struct {
			Result struct {
				Value float64 `json:"value"`
			} `json:"result"`
		}
		if err := m.call(a.cdp, a.sessionID, "Runtime.evaluate",
			map[string]any{"expression": "scrollY", "returnByValue": true}, &out); err != nil {
			t.Fatal(err)
		}
		scrolled = out.Result.Value
		if scrolled >= events*40 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("scrollY reached only %v of %d px — fire-and-forget lost input", scrolled, events*40)
}

func isGreenish(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return g > 0x8000 && r < 0x8000 && b < 0x8000
}

func isReddish(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0x8000 && g < 0x8000 && b < 0x8000
}

// waitForLiveContentWidth blocks until the page reports at least want CSS px of
// content, i.e. it has actually laid out.
func waitForLiveContentWidth(t *testing.T, m *browserAttachmentManager, a *browserAttachment, want float64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var metrics struct {
			CSSContentSize struct {
				Width float64 `json:"width"`
			} `json:"cssContentSize"`
		}
		if err := m.call(a.cdp, a.sessionID, "Page.getLayoutMetrics", nil, &metrics); err == nil &&
			metrics.CSSContentSize.Width >= want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the fixture never laid out to %v CSS px", want)
}

// TestBrowserAttachmentLiveScrolling covers what a user does with a page they
// can see but not scroll: the wheel, the arrow / PgDn keys, and dragging the
// scrollbar. All three were broken at once and for different reasons, so each is
// asserted against a real Chromium rather than against a recorded CDP call.
func TestBrowserAttachmentLiveScrolling(t *testing.T) {
	if os.Getenv("AF_CHROMIUM_ATTACH_LIVE") != "1" {
		t.Skip("set AF_CHROMIUM_ATTACH_LIVE=1 to run the real scrolling test")
	}
	bin, err := findChromiumBinary()
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Scroll fixture</title>`+
			`<body style="margin:0"><div style="min-width:1600px;height:6000px;background:linear-gradient(#fff,#333)"></div>`+
			`<textarea id="t" style="position:fixed;left:0;top:0">0123456789</textarea>`)
	}))
	defer fixture.Close()

	profile := t.TempDir()
	startLiveChromium(t, bin, "0", "--user-data-dir="+profile, fixture.URL)
	port, _ := liveDevToolsPort(t, profile)
	discovery, err := discoverCDPTargets(port, 3*time.Second)
	if err != nil || len(discovery.Targets) == 0 {
		t.Fatalf("discover: %v", err)
	}
	m := newBrowserAttachmentManager(defaultBrowserAttachmentManagerConfig())
	defer m.Close()
	resp, err := m.Create(browserAttachmentCreateRequest{
		Port: port, TargetID: discovery.Targets[0].TargetID,
		Viewport: browserViewportRequest{Width: 800, Height: 600, DeviceScaleFactor: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Delete(resp.ID)
	a, err := m.lookupActive(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetControlMode(resp.ID, attachmentControlUser); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.viewer = &browserAttachmentViewer{attachment: a, control: make(chan browserOutbound, 32), done: make(chan struct{})}
	a.visible = true
	a.state = attachmentStateViewerOpen
	viewer := a.viewer
	a.mu.Unlock()
	waitForLiveContentWidth(t, m, a, 1600)

	// Side-effect expressions return undefined, so the value is decoded loosely.
	number := func(expr string) float64 {
		t.Helper()
		var out struct {
			Result struct {
				Value any `json:"value"`
			} `json:"result"`
		}
		if err := m.call(a.cdp, a.sessionID, "Runtime.evaluate",
			map[string]any{"expression": expr, "returnByValue": true}, &out); err != nil {
			t.Fatal(err)
		}
		value, _ := out.Result.Value.(float64)
		return value
	}
	settle := func(expr string, want float64) float64 {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		var last float64
		for time.Now().Before(deadline) {
			if last = number(expr); last >= want {
				return last
			}
			time.Sleep(100 * time.Millisecond)
		}
		return last
	}

	// Wheel.
	viewer.handleControl([]byte(`{"type":"wheel","x":400,"y":300,"deltaX":0,"deltaY":400,"modifiers":0}`))
	if got := settle("scrollY", 1); got < 1 {
		t.Fatalf("the wheel did not scroll the page: scrollY=%v", got)
	}
	_ = number("scrollTo(0,0)")

	// Keyboard: a navigation key has no text, so it only acts when the event
	// carries a virtual key code.
	viewer.handleControl([]byte(`{"type":"key","event":"down","key":"PageDown","code":"PageDown","modifiers":0,"repeat":false}`))
	viewer.handleControl([]byte(`{"type":"key","event":"up","key":"PageDown","code":"PageDown","modifiers":0,"repeat":false}`))
	if got := settle("scrollY", 1); got < 1 {
		t.Fatalf("PageDown did not scroll the page: scrollY=%v", got)
	}
	_ = number("scrollTo(0,0)")
	viewer.handleControl([]byte(`{"type":"key","event":"down","key":"ArrowDown","code":"ArrowDown","modifiers":0,"repeat":false}`))
	viewer.handleControl([]byte(`{"type":"key","event":"up","key":"ArrowDown","code":"ArrowDown","modifiers":0,"repeat":false}`))
	if got := settle("scrollY", 1); got < 1 {
		t.Fatalf("ArrowDown did not scroll the page: scrollY=%v", got)
	}

	// Caret movement inside a field — the same missing key code stopped the
	// caret from moving at all.
	_ = number("(()=>{const t=document.getElementById('t');t.focus();t.setSelectionRange(5,5);return 0})()")
	viewer.handleControl([]byte(`{"type":"key","event":"down","key":"ArrowLeft","code":"ArrowLeft","modifiers":0,"repeat":false}`))
	viewer.handleControl([]byte(`{"type":"key","event":"up","key":"ArrowLeft","code":"ArrowLeft","modifiers":0,"repeat":false}`))
	deadline := time.Now().Add(5 * time.Second)
	var caret float64 = 5
	for time.Now().Before(deadline) {
		if caret = number("document.getElementById('t').selectionStart"); caret < 5 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if caret != 4 {
		t.Fatalf("ArrowLeft did not move the caret in the textarea: selectionStart=%v", caret)
	}
	_ = number("(()=>{document.getElementById('t').blur();scrollTo(0,0);return 0})()")

	// Scrollbar drag: press the horizontal thumb and move right with the button
	// held. A move sent as button "none" is a hover and does nothing.
	y := 600 - 4
	viewer.handleControl([]byte(`{"type":"mouse","event":"move","x":40,"y":` + strconv.Itoa(y) + `,"button":"none","buttons":0,"modifiers":0,"clickCount":0}`))
	viewer.handleControl([]byte(`{"type":"mouse","event":"down","x":40,"y":` + strconv.Itoa(y) + `,"button":"left","buttons":1,"modifiers":0,"clickCount":1}`))
	for _, x := range []int{120, 200, 280} {
		viewer.handleControl([]byte(`{"type":"mouse","event":"move","x":` + strconv.Itoa(x) + `,"y":` + strconv.Itoa(y) + `,"button":"left","buttons":1,"modifiers":0,"clickCount":0}`))
		time.Sleep(60 * time.Millisecond)
	}
	viewer.handleControl([]byte(`{"type":"mouse","event":"up","x":280,"y":` + strconv.Itoa(y) + `,"button":"left","buttons":0,"modifiers":0,"clickCount":1}`))
	if got := settle("scrollX", 1); got < 1 {
		t.Fatalf("dragging the horizontal scrollbar did not scroll: scrollX=%v", got)
	}
}

// liveDevToolsPort reads the port and browser GUID Chromium publishes for a
// --remote-debugging-port=0 launch.
func liveDevToolsPort(t *testing.T, profile string) (int, string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		if err == nil && len(lines) == 2 {
			port, convErr := strconv.Atoi(strings.TrimSpace(lines[0]))
			if convErr == nil && port > 0 {
				return port, NormalizeCDPBrowserID(strings.TrimSpace(lines[1]))
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Chromium never published DevToolsActivePort")
	return 0, ""
}

// startLiveChromium launches a headless Chromium on the given remote-debugging
// port ("0" = let it pick) and kills the process group at test end.
func startLiveChromium(t *testing.T, bin, port string, extra ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	args := append([]string{
		"--headless=new", "--no-sandbox", "--no-first-run", "--disable-dev-shm-usage",
		"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=" + port,
	}, extra...)
	if !strings.Contains(strings.Join(extra, " "), "--user-data-dir=") {
		args = append(args, "--user-data-dir="+t.TempDir())
	}
	// The caller may pass a URL as the last extra arg; otherwise open a blank page.
	if len(extra) == 0 || !strings.HasPrefix(extra[len(extra)-1], "http") {
		args = append(args, "about:blank")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		cancel()
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})
	return cmd
}

func waitForLiveCDP(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := discoverCDPTargets(port, time.Second); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Chromium never answered CDP on port %d", port)
}

func liveProcessAlive(cmd *exec.Cmd) bool {
	return cmd.Process != nil && cmd.Process.Signal(syscall.Signal(0)) == nil
}
