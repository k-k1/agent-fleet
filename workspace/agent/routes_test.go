package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// docs/log/23 P0-2: smoke tests over the real route table (buildMux) + the real
// httpx.RequireToken gate, in an isolated HOME. Regression detectors for handler
// moves — status + known JSON keys only, no docker / tmux / CLI involved.

func smokeHandler(t *testing.T) http.Handler {
	t.Helper()
	isolateAgentConfigDirs(t)
	t.Setenv("AGENT_TOKEN", "smoke-token")
	return httpx.RequireToken(buildMux())
}

// isolateAgentConfigDirs points HOME **and every config dir that is pinned by its own
// environment variable** at one throwaway tree.
//
// HOME alone is not enough, and the gap is not theoretical: paths.ClaudeConfigDir()
// honours $CLAUDE_CONFIG_DIR, which production sets to /var/lib/af/claude (a dedicated
// mount outside home). So a test that only isolated HOME and then hit
// POST /mcp-servers — which materializes the registry into every CLI's config — wrote
// its fixture server into the developer's REAL .claude.json. It was found there on
// 2026-08-09 as a live `wiki` → https://mcp.example.com/mcp entry, straight out of
// mcp_servers_test.go.
//
// Worse than the stray row: the ownership ledger (mcp-managed.json) DID land in the
// temp HOME, so af never learned it wrote that name — the row became an orphan no
// later materialize is allowed to remove (docs/log/48 §8.2), and only a hand-run
// `claude mcp remove` clears it.
//
// The other kinds escaped only by luck: CODEX_HOME / COPILOT_HOME / KIRO_HOME /
// XDG_CONFIG_HOME are unset in this container, so they resolved under the temp HOME.
// Set them here too rather than depend on that.
func isolateAgentConfigDirs(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for k, v := range map[string]string{
		"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"),
		"CODEX_HOME":        filepath.Join(home, ".codex"),
		"COPILOT_HOME":      filepath.Join(home, ".copilot"),
		"KIRO_HOME":         filepath.Join(home, ".kiro"),
		"XDG_CONFIG_HOME":   filepath.Join(home, ".config"),
		"XDG_DATA_HOME":     filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME":    filepath.Join(home, ".cache"),
		"XDG_STATE_HOME":    filepath.Join(home, ".local", "state"),
	} {
		t.Setenv(k, v)
	}
}

func smokeDo(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// /healthz stays open (startup readiness probe) even with the token gate armed.
func TestSmokeHealthzOpen(t *testing.T) {
	h := smokeHandler(t)
	w := smokeDo(t, h, "GET", "/healthz", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("healthz: %d %s", w.Code, w.Body.String())
	}
}

// Every other route requires the CP-injected bearer; a missing or wrong token
// 401s with the shared error shape {error:{code,message}}.
func TestSmokeTokenGate(t *testing.T) {
	h := smokeHandler(t)
	for _, tok := range []string{"", "wrong"} {
		w := smokeDo(t, h, "GET", "/env/ui-prefs", tok, "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: want 401 got %d", tok, w.Code)
		}
		var got struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Error.Code != "unauthorized" {
			t.Fatalf("token %q: error shape %s (err=%v)", tok, w.Body.String(), err)
		}
	}
}

// ui-prefs round-trip: the lightest authenticated route pair (pure fs, opaque JSON).
func TestSmokeUIPrefsRoundTrip(t *testing.T) {
	h := smokeHandler(t)
	if w := smokeDo(t, h, "GET", "/env/ui-prefs", "smoke-token", ""); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("initial prefs: %d %q", w.Code, w.Body.String())
	}
	if w := smokeDo(t, h, "PUT", "/env/ui-prefs", "smoke-token", `{"theme":"dark"}`); w.Code != http.StatusOK {
		t.Fatalf("put prefs: %d %s", w.Code, w.Body.String())
	}
	w := smokeDo(t, h, "GET", "/env/ui-prefs", "smoke-token", "")
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got["theme"] != "dark" {
		t.Fatalf("read back: %d %s (err=%v)", w.Code, w.Body.String(), err)
	}
}

// /assistants serves the code-injected builtin personas without any user files.
func TestSmokeAssistantsBuiltins(t *testing.T) {
	h := smokeHandler(t)
	w := smokeDo(t, h, "GET", "/assistants", "smoke-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("assistants: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "\"id\"") {
		t.Fatalf("assistants payload has no ids: %s", w.Body.String())
	}
}

func TestFSFilePutRouteRegistered(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/fs/file", nil)
	_, pattern := buildMux().Handler(req)
	if pattern != "PUT /fs/file" {
		t.Fatalf("route pattern=%q", pattern)
	}
}

// The collection route must not be swallowed by the {id} route — the Console's
// recovery list is the only entry left when the action link is lost (docs/log/53 §53.7).
func TestBrowserAttachmentListRouteRegistered(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/browser/attachments", nil)
	_, pattern := buildMux().Handler(req)
	if pattern != "GET /browser/attachments" {
		t.Fatalf("route pattern=%q", pattern)
	}
}

func TestBrowserAttachmentControlModeRouteRegistered(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/browser/attachments/ba_0123456789abcdef0123456789abcdef/control-mode", nil)
	_, pattern := buildMux().Handler(req)
	if pattern != "POST /browser/attachments/{id}/control-mode" {
		t.Fatalf("route pattern=%q", pattern)
	}
}

// The retarget pair must not be swallowed by the {id} route either — same
// concern as the list route above, one level deeper.
func TestBrowserAttachmentSiblingTargetsRouteRegistered(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/browser/attachments/ba_0123456789abcdef0123456789abcdef/targets", nil)
	_, pattern := buildMux().Handler(req)
	if pattern != "GET /browser/attachments/{id}/targets" {
		t.Fatalf("route pattern=%q", pattern)
	}
}

func TestBrowserAttachmentRetargetRouteRegistered(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/browser/attachments/ba_0123456789abcdef0123456789abcdef/retarget", nil)
	_, pattern := buildMux().Handler(req)
	if pattern != "POST /browser/attachments/{id}/retarget" {
		t.Fatalf("route pattern=%q", pattern)
	}
}
