package sessionx

// Regression tests for docs/log/105 §106.2's server-side gate: POST /sessions must refuse an
// lcpp create while the user's own ui-prefs lcppEnabled is explicitly false, must let it
// through when the setting is missing (opt-out default) or explicitly true, and must leave
// every other kind untouched regardless of the setting's value.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// writeLcppEnabledPref writes {"lcppEnabled": v} into the fixture's ui-prefs.json. Call it
// AFTER isolateAgentState, which is what points HOME at a temp dir. Writing the prefs FILE
// rather than calling into uiprefs directly exercises the production read path (ui-prefs.json
// -> uiprefs.LcppEnabled() -> the create-time gate), the same reasoning as
// session_spawn_test.go's spawnLimitPref.
func writeLcppEnabledPref(t *testing.T, v bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(uiprefs.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"lcppEnabled": v})
	if err := os.WriteFile(uiprefs.Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCreateSessionRefusesLcppWhenDisabled is the core positive control for the server-side
// gate itself: with the user's setting explicitly off, POST /sessions kind=lcpp must be
// refused with 403 lcpp_disabled — no tmux/managed-driver involvement needed, since the
// refusal has to fire before any side effect.
func TestCreateSessionRefusesLcppWhenDisabled(t *testing.T) {
	isolateAgentState(t)
	writeLcppEnabledPref(t, false)
	dir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	code, body := roundtrip(t, srv, "POST", "/sessions", map[string]any{"dir": dir, "kind": "lcpp"})
	if code != http.StatusForbidden || !strings.Contains(string(body), "lcpp_disabled") {
		t.Fatalf("status=%d body=%s, want 403 lcpp_disabled", code, body)
	}
}

// TestCreateSessionAllowsLcppWhenSettingMissing is the opt-out default: a fresh deployment
// with no lcppEnabled key at all must keep launching lcpp exactly as it did before this
// change. Needs real tmux for the same reason
// TestCreateSessionDefaultsManagedOnlyKindToManaged does (allocSessionName's collision-retry
// loop never terminates against fakeTmux's has-session stub).
func TestCreateSessionAllowsLcppWhenSettingMissing(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateAgentState(t)
	dir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{"dir": dir, "kind": "lcpp", "model": "test-model"}, http.StatusCreated, &created)
}

// TestCreateSessionAllowsLcppWhenExplicitlyEnabled is the explicit-on twin of the above: the
// setting present and true must behave the same as it being absent.
func TestCreateSessionAllowsLcppWhenExplicitlyEnabled(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateAgentState(t)
	writeLcppEnabledPref(t, true)
	dir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{"dir": dir, "kind": "lcpp", "model": "test-model"}, http.StatusCreated, &created)
}

// TestCreateSessionDisabledLcppLeavesOtherKindsUnaffected is the negative control: turning
// lcpp off must not touch any other kind's create path. shell is picked because, like the
// gate's own real-launch tests, it needs no engine/auth and its create is cheap to observe.
func TestCreateSessionDisabledLcppLeavesOtherKindsUnaffected(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateAgentState(t)
	writeLcppEnabledPref(t, false)
	dir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{"dir": dir, "kind": "shell"}, http.StatusCreated, &created)
}
