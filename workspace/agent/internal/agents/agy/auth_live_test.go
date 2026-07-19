package agy

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Live start-flow test against the real agy TUI: a scratch HOME (no token)
// makes agy show the login selector; HandleStart must pick Google OAuth and
// scrape the authorize URL. Stops there — completing needs a real browser-side
// code (covered by the Console E2E). Opt-in like the usage live test:
// AF_AGY_LIVE=1 go test ./internal/agents/agy/ -run Live -v
func TestHandleStartLive(t *testing.T) {
	if os.Getenv("AF_AGY_LIVE") == "" {
		t.Skip("set AF_AGY_LIVE=1 to run the live agy auth start flow")
	}
	t.Setenv("HOME", t.TempDir())

	req := httptest.NewRequest("POST", "/connections/agy/start", strings.NewReader(`{"method":"oauth"}`))
	w := httptest.NewRecorder()
	HandleStart(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var res struct {
		FlowID string `json:"flow_id"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.FlowID == "" || !strings.HasPrefix(res.URL, "https://accounts.google.com/o/oauth2/auth?") {
		t.Fatalf("bad start response: %+v", res)
	}
	t.Logf("flow_id=%s url=%.80s…", res.FlowID, res.URL)
	// The URL must be scraped unwrapped (the flow PTY is 4000 cols wide).
	if strings.ContainsAny(res.URL, " \n") {
		t.Fatalf("URL is wrapped/mangled: %q", res.URL)
	}
	// …and de-duplicated: the OSC-8 hyperlink rendering doubles the URL in the
	// stripped buffer (sanitizeAuthURL) — a doubled state param has no spaces
	// and passes the checks above, so pin the scheme count explicitly.
	if strings.Count(res.URL, "https://") != 1 {
		t.Fatalf("URL not de-duplicated: %q", res.URL)
	}
	// Drop the pending flow so the agy process doesn't linger.
	if f := flows.Take(res.FlowID); f != nil {
		f.Close()
	}
	// The login dir must have been pre-trusted for the onboarding's final screen.
	b, err := os.ReadFile(settingsPath())
	if err != nil {
		t.Fatalf("settings.json not written: %v", err)
	}
	if !strings.Contains(string(b), filepath.Join(stateDir(), "login-flow")) {
		t.Fatalf("login dir not pre-trusted: %s", b)
	}
}

// Live already-connected gate: with the real HOME (signed in), HandleStart must
// refuse instead of hanging on a selector that will never appear.
func TestHandleStartAlreadyConnectedLive(t *testing.T) {
	if os.Getenv("AF_AGY_LIVE") == "" {
		t.Skip("set AF_AGY_LIVE=1 to run the live agy auth gate test")
	}
	if !SignedIn() {
		t.Skip("agy is not signed in")
	}
	req := httptest.NewRequest("POST", "/connections/agy/start", nil)
	w := httptest.NewRecorder()
	HandleStart(w, req)
	if w.Code != 409 {
		t.Fatalf("want 409 already_connected, got %d: %s", w.Code, w.Body.String())
	}
}
