package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
