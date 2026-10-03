package browserx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// withUnavailable makes the workspace look like one whose runtime withholds browser
// features (id != "") or offers them (id == "") for the duration of the test.
func withUnavailable(t *testing.T, id string) {
	t.Helper()
	prev := unavailableRuntime
	unavailableRuntime = func() string { return id }
	t.Cleanup(func() { unavailableRuntime = prev })
}

// browserRouteRequest builds a request that matches pattern ("METHOD /path/{id}").
func browserRouteRequest(pattern string) *http.Request {
	method, path, _ := strings.Cut(pattern, " ")
	path = strings.ReplaceAll(path, "{id}", "x")
	return httptest.NewRequest(method, path+"?port=9222", strings.NewReader("{}"))
}

// Every browser route refuses with the stable code before its handler runs: nothing may
// install Chromium, launch it or dial a CDP port on such a workspace.
func TestBrowserRoutesRefuseWhenUnavailable(t *testing.T) {
	withUnavailable(t, "kubernetes")
	mux := buildMux()
	for _, r := range Routes() {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, browserRouteRequest(r.Pattern))
		var body struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != http.StatusConflict || body.Error.Code != UnavailableCode {
			t.Errorf("%s: status %d code %q, want 409 %s", r.Pattern, rec.Code, body.Error.Code, UnavailableCode)
			continue
		}
		if !strings.Contains(body.Error.Message, "(kubernetes)") || !strings.Contains(body.Error.Message, "sandbox") {
			t.Errorf("%s: message %q does not name the runtime and the reason", r.Pattern, body.Error.Message)
		}
	}
}

// The other runtimes keep today's behaviour: the request reaches the handler.
func TestBrowserRoutesReachHandlersWhenAvailable(t *testing.T) {
	withUnavailable(t, "")
	mux := buildMux()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/browser/pages", strings.NewReader("not json")))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_browser_target") {
		t.Fatalf("POST /browser/pages = %d %s, want the handler's own 400 bad_browser_target", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/browser/attachments", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /browser/attachments = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// The launcher refuses too, and the documented AF_CHROMIUM_NO_SANDBOX escape hatch must not
// turn into the unsandboxed launch that was decided against.
func TestLaunchPipeCDPRefusesWhenUnavailable(t *testing.T) {
	withUnavailable(t, "kubernetes")
	t.Setenv("AF_CHROMIUM_NO_SANDBOX", "1")
	cdp, err := launchPipeCDP(context.Background())
	if cdp != nil {
		_ = cdp.Close()
	}
	if !errors.Is(err, errBrowserUnavailable) {
		t.Fatalf("launchPipeCDP err = %v, want errBrowserUnavailable", err)
	}
	if err := RunBrowserImageSmoke(); err == nil || !strings.HasPrefix(err.Error(), UnavailableCode+": ") {
		t.Fatalf("RunBrowserImageSmoke err = %v, want the %s explanation", err, UnavailableCode)
	}
}

// Where browser features are available the production launcher is not refused and hands
// the sandboxed argument list to the Chromium executable, exactly as before (docker, ECS,
// native). Deterministic: a fake executable records what it was started with.
func TestLaunchPipeCDPDelegatesWhenAvailable(t *testing.T) {
	withUnavailable(t, "")
	t.Setenv("AF_CHROMIUM_NO_SANDBOX", "")
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	fake := filepath.Join(dir, "chromium")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_CHROMIUM_BIN", fake)
	cdp, err := launchPipeCDP(context.Background())
	if err != nil {
		t.Fatalf("launchPipeCDP on an available runtime: %v", err)
	}
	select {
	case <-cdp.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the fake Chromium did not exit")
	}
	_ = cdp.Close()
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("the fake Chromium was not started: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(b)), "\n")
	if !slices.Contains(args, "--remote-debugging-pipe") || !slices.Contains(args, "--headless=new") {
		t.Fatalf("launch args = %q, want the production pipe-CDP arguments", args)
	}
	if slices.Contains(args, "--no-sandbox") {
		t.Fatalf("launch args = %q contain --no-sandbox", args)
	}
}

// The same against a real Chromium, where this host can run its sandbox. The environment is
// judged first and independently of the launcher (a Chromium on PATH, user namespaces
// available); once both hold, any launch or CDP failure is a regression, not a skip.
func TestLaunchPipeCDPStillLaunchesWhenAvailable(t *testing.T) {
	withUnavailable(t, "")
	t.Setenv("AF_CHROMIUM_NO_SANDBOX", "")
	if _, err := findChromiumBinary(); err != nil {
		t.Skip("Chromium is not installed in this test environment")
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare is not installed, so whether user namespaces work cannot be judged")
	}
	if err := exec.Command(unshare, "-U", "true").Run(); err != nil {
		t.Skipf("user namespaces are not available here, so Chromium's sandbox cannot start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cdp, err := launchPipeCDP(ctx)
	if err != nil {
		t.Fatalf("launchPipeCDP: %v", err)
	}
	defer func() { _ = cdp.Close() }()
	var out struct {
		Product string `json:"product"`
	}
	if err := cdp.Call(ctx, "Browser.getVersion", nil, "", &out); err != nil || out.Product == "" {
		t.Fatalf("the sandboxed Chromium did not answer Browser.getVersion: product %q, err %v", out.Product, err)
	}
}
