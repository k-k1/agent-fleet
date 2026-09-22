package sessionx

// Regression tests for docs/log/109: unlike codex/opencode, lcpp has no CLI-picked "own
// default". This is not because llama-server itself demands a model name on every request —
// a single-model instance ignores the request's own `model` field entirely (measured live,
// docs/log/107's 2026-09-21 addendum) — it is the Agent's own requirement (a router
// deployment DOES dispatch on it, and the id is the only record of which catalog entry a
// session means). So POST /sessions kind=lcpp with no model used to sail straight through to
// worktree creation and the managed driver, and the driver then failed the very first turn
// with only the user's own prompt left in the store (svcnyrc's reproduction). These pin the
// create-time guard (before any side effect), the live-catalog membership check for an
// explicit id, and that every other kind is left untouched.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestCreateSessionRefusesLcppWithEmptyModelBeforeSideEffects is the core positive control:
// no model at all must be refused with 400 bad_model, and — the "before any side effect" half
// — must leave no session meta behind at all (ListMetas stays exactly as it was).
func TestCreateSessionRefusesLcppWithEmptyModelBeforeSideEffects(t *testing.T) {
	isolateAgentState(t)
	dir := t.TempDir()
	before := len(session.ListMetas())

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	code, body := roundtrip(t, srv, "POST", "/sessions", map[string]any{"dir": dir, "kind": "lcpp"})
	if code != http.StatusBadRequest || !strings.Contains(string(body), "bad_model") {
		t.Fatalf("status=%d body=%s, want 400 bad_model", code, body)
	}
	if got := len(session.ListMetas()); got != before {
		t.Fatalf("ListMetas() grew from %d to %d — a meta was written despite the refusal", before, got)
	}
}

// TestCreateSessionRefusesLcppWithBlankModelBeforeSideEffects is the same guard against a
// model that is present on the wire but blank/whitespace-only — a direct POST or an older
// Console build could still send `"model": ""` or `"model": " "` explicitly, and
// strings.TrimSpace is what the guard actually checks.
func TestCreateSessionRefusesLcppWithBlankModelBeforeSideEffects(t *testing.T) {
	isolateAgentState(t)
	dir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	code, body := roundtrip(t, srv, "POST", "/sessions", map[string]any{"dir": dir, "kind": "lcpp", "model": "   "})
	if code != http.StatusBadRequest || !strings.Contains(string(body), "bad_model") {
		t.Fatalf("status=%d body=%s, want 400 bad_model", code, body)
	}
}

// TestCreateSessionAcceptsLcppWithConcreteModel is the negative control for the guard above:
// a real model id must still create the session. LcppLiveModels is left nil (as it always is
// outside main's own init), so this also pins that "no catalog to check against" is not
// itself treated as a rejection.
func TestCreateSessionAcceptsLcppWithConcreteModel(t *testing.T) {
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
	do(t, srv, "POST", "/sessions", map[string]any{"dir": dir, "kind": "lcpp", "model": "qwen3-8b"}, http.StatusCreated, &created)
	meta, ok := session.ReadMeta(created.Name)
	if !ok || meta.Model != "qwen3-8b" {
		t.Fatalf("meta = %+v, ok=%v, want Model=qwen3-8b", meta, ok)
	}
}

// TestCreateSessionAllowsEmptyModelForOtherKinds is the negative control for the guard's
// scope: it must fire for lcpp alone. Every other kind's "" still means "the CLI's own pick"
// (agentModels.ts's resolveModel doc comment) and must keep working exactly as before.
func TestCreateSessionAllowsEmptyModelForOtherKinds(t *testing.T) {
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
	do(t, srv, "POST", "/sessions", map[string]any{"dir": dir, "kind": "claude"}, http.StatusCreated, &created)
}

// TestCreateSessionValidatesLcppExplicitModelAgainstLiveCatalog wires LcppLiveModels (normally
// set only by main's own init, engines.go) to a fake catalog and confirms an explicit id is
// checked the same way codex/opencode/copilot's own live catalogs already are: an unknown id
// is refused with the nearest candidates, and a known one is accepted as-is.
func TestCreateSessionValidatesLcppExplicitModelAgainstLiveCatalog(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateAgentState(t)
	prev := LcppLiveModels
	t.Cleanup(func() { LcppLiveModels = prev })
	LcppLiveModels = func(ctx context.Context) []agents.ModelChoice {
		return []agents.ModelChoice{{ID: "qwen3-8b", Label: "qwen3-8b"}}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	code, body := roundtrip(t, srv, "POST", "/sessions", map[string]any{"dir": t.TempDir(), "kind": "lcpp", "model": "not-a-model"})
	if code != http.StatusBadRequest || !strings.Contains(string(body), "qwen3-8b") {
		t.Fatalf("status=%d body=%s, want 400 naming qwen3-8b as the nearest candidate", code, body)
	}

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{"dir": t.TempDir(), "kind": "lcpp", "model": "qwen3-8b"}, http.StatusCreated, &created)
}

// TestCreateSessionAcceptsLcppExplicitModelWhenCatalogUnreachable is requirement 4's own
// case: LcppLiveModels wired but answering with nothing (the member connection unreachable,
// or a deployment with no engines) must NOT be treated as "the model does not exist" —
// resolveLiveModel's own rule (an empty catalog passes an explicit id through unchanged)
// applies to lcpp exactly as it does to every other live-catalog kind.
func TestCreateSessionAcceptsLcppExplicitModelWhenCatalogUnreachable(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateAgentState(t)
	prev := LcppLiveModels
	t.Cleanup(func() { LcppLiveModels = prev })
	LcppLiveModels = func(ctx context.Context) []agents.ModelChoice { return nil }

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{"dir": t.TempDir(), "kind": "lcpp", "model": "whatever-the-member-set"}, http.StatusCreated, &created)
}
