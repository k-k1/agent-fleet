package main

import (
	"context"
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
// port contract rests on (docs/53 §53.16), measured 2026-08-08 on Chrome 151:
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
			wantBrowserID = normalizeCDPBrowserID(strings.TrimSpace(lines[1]))
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
	args = append(args, "about:blank")
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
