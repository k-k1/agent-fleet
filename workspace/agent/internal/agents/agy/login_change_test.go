package agy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

func writeAgyToken(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(tokenPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath(), []byte(`{"auth_method":"consumer"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// At the handler: a disconnect records the change before it removes the login, refuses
// (and removes nothing) when it cannot record it, and moves the generation when it can.
func TestDisconnectRecordsTheChangeFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeAgyToken(t)
	before := credbind.Generation(routes.RouteAgyLogin)
	dir := paths.AgentConfigDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	rec := httptest.NewRecorder()
	HandleDisconnect(rec, httptest.NewRequest(http.MethodDelete, "/connections/agy", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want a refusal", rec.Code, rec.Body)
	}
	if _, err := os.Stat(tokenPath()); err != nil {
		t.Fatalf("the refused disconnect removed the login: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	HandleDisconnect(rec, httptest.NewRequest(http.MethodDelete, "/connections/agy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	if g := credbind.Generation(routes.RouteAgyLogin); g == "" || g == before {
		t.Errorf("after the disconnect: %q (was %q)", g, before)
	}
}
