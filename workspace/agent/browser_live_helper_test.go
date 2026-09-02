package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// TestBrowserLiveServerHelper exposes the real Agent browser routes to the W5
// cross-component test. It is inert in the normal suite and runs in a separate
// test process so the Control Plane test can exercise the actual HTTP/WS boundary.
func TestBrowserLiveServerHelper(t *testing.T) {
	if os.Getenv("AF_BROWSER_LIVE_SERVER") != "1" {
		t.Skip("W5 live browser helper is disabled")
	}
	addr := os.Getenv("AF_BROWSER_LIVE_ADDR")
	if addr == "" {
		t.Fatal("AF_BROWSER_LIVE_ADDR is required")
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	config := browserx.DefaultBrowserManagerConfig()
	if os.Getenv("AF_BROWSER_LIVE_ALLOW_NO_SANDBOX") == "1" {
		config.CDPFactory = browserx.LaunchPipeCDPWithoutSandboxForTest
	}
	manager := browserx.NewBrowserManager(config)
	previous := browserx.WorkspaceBrowserManager
	browserx.WorkspaceBrowserManager = manager
	defer func() {
		browserx.WorkspaceBrowserManager = previous
		manager.Close()
	}()

	server := &http.Server{Handler: httpx.RequireToken(buildMux()), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()

	// The parent owns stdin. Closing it is the clean shutdown signal and avoids
	// teaching production Agent code about integration-test lifecycle controls.
	_, _ = io.Copy(io.Discard, os.Stdin)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	if err := <-done; err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
