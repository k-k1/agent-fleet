package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestBrowserLiveW2W3W4 starts the real Workspace Agent browser handlers with
// a real Chromium process, relays them through the real Control Plane browser
// API, and drives that relay with the Console's real BrowserController.
//
// It is opt-in because normal Control Plane CI does not install Chromium or the
// Console node_modules. The Workspace image/E2E job can enable it after those
// prerequisites are available.
func TestBrowserLiveW2W3W4(t *testing.T) {
	if os.Getenv("AF_BROWSER_LIVE_E2E") != "1" {
		t.Skip("W5 live browser E2E is disabled")
	}
	chromium := os.Getenv("AF_CHROMIUM_BIN")
	if chromium == "" {
		t.Fatal("AF_CHROMIUM_BIN is required")
	}

	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>W5 live browser</title><input autofocus oninput="console.log('input:'+this.value)">`)
	}))
	defer app.Close()
	appPort := portFromURL(t, app.URL)

	agentURL, stopAgent := startLiveBrowserAgent(t, chromium)
	defer stopAgent()
	env := newBrowserTestEnv(t, browserTestRuntime{
		endpoint: agentURL,
		token:    "w5-live-token",
		state:    "running",
	})
	cp := httptest.NewServer(env.mux)
	defer cp.Close()

	consoleDir, err := filepath.Abs("../console")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("npm", "exec", "vitest", "run", "src/features/browser/live-integration.test.ts", "--maxWorkers=1")
	cmd.Dir = consoleDir
	cmd.Env = append(os.Environ(),
		"AF_BROWSER_CP_E2E_URL="+cp.URL,
		"AF_BROWSER_TARGET_PORT="+strconv.Itoa(appPort),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Console live browser integration: %v\n%s", err, output)
	}
	t.Logf("Console live browser integration passed:\n%s", output)
}

func startLiveBrowserAgent(t *testing.T, chromium string) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(root, "workspace", "agent")
	bin := filepath.Join(t.TempDir(), "workspace-agent-live.test")
	build := exec.Command("go", "test", "-c", "-o", bin, ".")
	build.Dir = agentDir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Agent test server: %v\n%s", err, output)
	}

	cmd := exec.Command(bin, "-test.run", "^TestBrowserLiveServerHelper$", "-test.v")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	cmd.Env = append(os.Environ(),
		"AF_BROWSER_LIVE_SERVER=1",
		"AF_BROWSER_LIVE_ADDR="+addr,
		"AF_CHROMIUM_BIN="+chromium,
		"AF_CHROMIUM_NO_SANDBOX=1",
		"AGENT_TOKEN=w5-live-token",
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	endpoint := "http://" + addr
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(endpoint + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return endpoint, func() {
					_ = stdin.Close()
					if err := cmd.Wait(); err != nil {
						t.Errorf("Agent live server stopped with %v\n%s", err, logs.String())
					}
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = stdin.Close()
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	t.Fatalf("Agent live server did not become ready\n%s", logs.String())
	return "", func() {}
}

func portFromURL(t *testing.T, raw string) int {
	t.Helper()
	parts := strings.Split(raw, ":")
	port, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		t.Fatalf("target URL %q: %v", raw, err)
	}
	return port
}
