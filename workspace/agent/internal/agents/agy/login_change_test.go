package agy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
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

// A sign-in flow left open while a disconnect completes: the disconnect does not clear the open
// flow's protection, so the login stays unproven until the flow ends; closing the flow (as
// completion, cancellation and the store's reap all do) records a fresh generation.
func TestDisconnectDuringAnOpenSignIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeAgyToken(t)
	g0 := credbind.Generation(routes.RouteAgyLogin)
	rec := httptest.NewRecorder()
	op, ok := agents.BeginLoginChange(rec, routes.RouteAgyLogin)
	if !ok {
		t.Fatalf("begin: %d %s", rec.Code, rec.Body)
	}
	f, err := agents.StartFlow(exec.Command("sleep", "60"))
	if err != nil {
		t.Fatal(err)
	}
	f.LoginOp = op
	t.Cleanup(f.Close)

	rec = httptest.NewRecorder()
	HandleDisconnect(rec, httptest.NewRequest(http.MethodDelete, "/connections/agy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("disconnect: %d %s", rec.Code, rec.Body)
	}
	if g := credbind.Generation(routes.RouteAgyLogin); g != "" {
		t.Errorf("disconnect finished, sign-in still open: %q, want unproven", g)
	}
	f.Close()
	if g := credbind.Generation(routes.RouteAgyLogin); g == "" || g == g0 {
		t.Errorf("after the sign-in flow ended: %q (was %q)", g, g0)
	}
}
