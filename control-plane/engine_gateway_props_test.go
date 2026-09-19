package main

// engine_gateway_props_test.go — ADR 0093 phase 0 (docs/log/99 §4.11): GET /engine/{key}/props,
// the one bounce that lets a caller learn the window llama-server actually started with.
//
// What is pinned:
//   - the same five gates serve() runs, in the same order, reached through the real mux handler
//     (not a lower-level function that skips membership/tenant resolution — those are exactly
//     what this route shares with serve() and must not diverge from);
//   - provider is a sixth gate this route adds of its own: only llamacpp has a /props to read;
//   - a box that is not answering gets a 503 WITHOUT this route ever starting it — no ECS
//     UpdateService call, which is the observable proof that ensureStarted was never reached;
//   - a box that is answering gets the upstream body relayed as it stands.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// enginePropsFixture is a real SQLite store with one membership in a tenant nobody has
// restricted (ADR 0084 decision 7: no limits row resolves to allowed), which is everything
// props() needs from the store side. Each test builds its own engine row so it can vary
// provider, reachability and lifecycle.
func enginePropsFixture(t *testing.T) (mgr *manager, signKey []byte, membershipID string) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := t.Context()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	tenant, err := st.CreateTenant(ctx, "allowed", "Allowed")
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	ident, err := st.UpsertIdentity(ctx, "allowed@example.com", sanitizeUser("allowed@example.com"), "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	mem, err := st.EnsureMembership(ctx, ident.ID, tenant.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	return &manager{store: st}, engineSignKey([]byte(strings.Repeat("k", 32))), mem.ID
}

func propsGateway(mgr *manager, signKey []byte, eng *engineRuntimeState) engineGateway {
	return engineGateway{mgr: mgr, reg: &engineRegistry{
		byKey:   map[string]*engineRuntimeState{"llm": eng},
		signKey: signKey,
	}}
}

func propsRequest(tok string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/engine/llm/props", nil)
	r.SetPathValue("key", "llm")
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	return r
}

// --- auth: no token, a broken token, another engine's token ----------------------

func TestEnginePropsNoToken401(t *testing.T) {
	mgr, signKey, _ := enginePropsFixture(t)
	eng := newTestEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	g := propsGateway(mgr, signKey, eng)

	rec := httptest.NewRecorder()
	g.props(rec, propsRequest(""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unauthenticated") {
		t.Errorf("body = %s, want the unauthenticated code", rec.Body.String())
	}
}

func TestEnginePropsBrokenToken401(t *testing.T) {
	mgr, signKey, _ := enginePropsFixture(t)
	eng := newTestEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	g := propsGateway(mgr, signKey, eng)

	rec := httptest.NewRecorder()
	g.props(rec, propsRequest("not-a-real-token"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("broken token: status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
}

// A token minted for a DIFFERENT engine must be refused exactly like serve()'s claims.Key !=
// key check (:476) — the token is valid, signed, unexpired, and still not good for this route.
func TestEnginePropsWrongEngineToken401(t *testing.T) {
	mgr, signKey, mid := enginePropsFixture(t)
	eng := newTestEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	g := propsGateway(mgr, signKey, eng)

	tok := mintEngineSessionToken(signKey, mid, "sess-1", "image", time.Now().Add(time.Hour))
	rec := httptest.NewRecorder()
	g.props(rec, propsRequest(tok))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-engine token: status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
}

// --- provider: only llamacpp has a /props to read ---------------------------------

func TestEnginePropsNonLlamacppProvider404(t *testing.T) {
	mgr, signKey, mid := enginePropsFixture(t)
	eng := newTestEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	eng.def.Provider = "comfy"
	g := propsGateway(mgr, signKey, eng)

	tok := mintEngineSessionToken(signKey, mid, "sess-1", "llm", time.Now().Add(time.Hour))
	rec := httptest.NewRecorder()
	g.props(rec, propsRequest(tok))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("comfy provider: status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "engine_no_props") {
		t.Errorf("body = %s, want the engine_no_props code", rec.Body.String())
	}
}

// --- a box that is not answering: 503, and NEVER a wake ---------------------------

// The engine's own URL is closed before the request, so this is exactly a stopped box's Cloud
// Map name not resolving: nothing there to answer. The positive control below proves the same
// route succeeds when something IS listening, so this 503 is not merely "the test URL is
// wrong".
func TestEnginePropsSleepingEngine503NeverWakes(t *testing.T) {
	mgr, signKey, mid := enginePropsFixture(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":32768}}`))
	}))
	url := up.URL
	up.Close() // closed before any request: nothing is listening there anymore

	api := &engineTestECS{desired: 0}
	eng := newTestEngine(t, url, api)
	g := propsGateway(mgr, signKey, eng)

	tok := mintEngineSessionToken(signKey, mid, "sess-1", "llm", time.Now().Add(time.Hour))
	rec := httptest.NewRecorder()
	g.props(rec, propsRequest(tok))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("sleeping engine: status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "engine_unavailable") {
		t.Errorf("body = %s, want the engine_unavailable code", rec.Body.String())
	}
	// The whole point of decision 7: this route reads whatever is already running, and never
	// buys a box to answer the question. ensureStarted is the only thing that ever calls
	// UpdateService, so this failing to move is the proof it was never reached.
	if api.updates != 0 {
		t.Errorf("ECS saw %d UpdateService call(s) — a read of /props must never start the engine", api.updates)
	}
	if api.desired != 0 {
		t.Errorf("desired count = %d, want 0 — /props must never buy a box", api.desired)
	}
}

// --- a box that IS answering: the upstream body relayed as it stands --------------

// The positive control: same route, a real listener, and the field the whole ADR is about —
// default_generation_settings.n_ctx — has to come through unmodified.
func TestEnginePropsWarmEngineRelaysUpstream(t *testing.T) {
	mgr, signKey, mid := enginePropsFixture(t)
	var gotPath string
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":32768},"total_slots":1}`))
	}))
	defer up.Close()

	api := &engineTestECS{desired: 1, running: 1}
	eng := newTestEngine(t, up.URL, api)
	eng.apiKey = "engine-secret"
	g := propsGateway(mgr, signKey, eng)

	tok := mintEngineSessionToken(signKey, mid, "sess-1", "llm", time.Now().Add(time.Hour))
	rec := httptest.NewRecorder()
	g.props(rec, propsRequest(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("warm engine: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/props" {
		t.Errorf("upstream path = %q, want /props (no /v1/ prefix)", gotPath)
	}
	if gotAuth != "Bearer engine-secret" {
		t.Errorf("upstream Authorization = %q, want the engine's own apiKey", gotAuth)
	}
	var out struct {
		DefaultGenerationSettings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response body did not decode: %v: %s", err, rec.Body.String())
	}
	if out.DefaultGenerationSettings.NCtx != 32768 {
		t.Errorf("n_ctx = %d, want 32768 — the upstream body must be relayed, not rebuilt", out.DefaultGenerationSettings.NCtx)
	}
	if api.updates != 0 {
		t.Errorf("ECS saw %d UpdateService call(s) — reading /props must never touch the engine's lifecycle", api.updates)
	}
}

// --- a borrowed row: the far gateway's OWN /engine/{key}/props, never guessed -----

// Mirrors engine_gateway_remote_test.go's shape (ADR 0079 decision 4): the far side is asked
// for exactly /engine/llm/props, never /engine/llm/v1/props and never this deployment's own
// engine URL directly.
func TestEnginePropsBorrowedRowAsksTheFarGatewaysOwnRoute(t *testing.T) {
	mgr, signKey, mid := enginePropsFixture(t)

	var paths []string
	var gotAuth string
	far := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/internal/engine/token" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "afe_far-token",
				"expires_at": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339),
				"base_url":   "/engine/llm/v1",
			})
			return
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":16384}}`))
	}))
	defer far.Close()

	remotes := &engineRemotes{base: far.URL, token: "afei_borrower", byKey: map[string]*engineRemote{}}
	rem := remotes.forKey("llm")
	eng := &engineRuntimeState{
		def: engineDef{
			Key: "llm", API: engineAPIChat, Provider: "llamacpp",
			URL: far.URL, Lifecycle: engineLifecycleRemote,
		},
		remote: rem,
		apiKey: "local-key-that-must-not-travel",
	}
	g := propsGateway(mgr, signKey, eng)

	tok := mintEngineSessionToken(signKey, mid, "sess-1", "llm", time.Now().Add(time.Hour))
	rec := httptest.NewRecorder()
	g.props(rec, propsRequest(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("borrowed row: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"n_ctx":16384`) {
		t.Errorf("body = %s, want the far side's n_ctx relayed", rec.Body.String())
	}
	found := false
	for _, p := range paths {
		if p == "/engine/llm/props" {
			found = true
		}
		if p == "/engine/llm/v1/props" {
			t.Errorf("the far side was asked for %s — the local /v1/ prefix must never apply to a borrowed row's /props", p)
		}
	}
	if !found {
		t.Errorf("far side saw %v, want /engine/llm/props among them", paths)
	}
	if gotAuth != "Bearer afe_far-token" {
		t.Errorf("far Authorization = %q, want the bought far session token", gotAuth)
	}
	if strings.Contains(gotAuth, "local-key") {
		t.Error("the row's own local apiKey was presented to the far deployment")
	}
}
