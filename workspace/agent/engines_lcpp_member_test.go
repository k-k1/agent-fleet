package main

// docs/log/107 — the member's own LAN llama.cpp connection. These tests pin the four seams
// (harnessEngineToken/Window/Available, lcppModels) reading secrets.Data.Lcpp BEFORE the
// Control Plane's own catalogue, and that an unset connection leaves every one of them on
// EXACTLY today's path (decision 1). No test in this file ever dials a real LAN server —
// every probe target is an httptest.Server.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// lcppMemberSetConn isolates the secrets store in a temp HOME and writes a member connection
// directly (bypassing the HTTP handlers, which are covered separately in
// connections_lcpp_test.go), then clears the short caches this feature added so a preceding
// test's answer cannot leak in.
func lcppMemberSetConn(t *testing.T, url, apiKey string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	// Same isolation lcppMemberNoConn documents: this process inherits its OWN real
	// deployment's Control Plane env, which must not leak into a test unless it deliberately
	// re-sets it (TestHarnessEngineTokenMemberConnWinsOverDeployment does, right after this call).
	t.Setenv("AF_CP_BASE_URL", "")
	t.Setenv("AF_ENGINE_ISSUE_TOKEN", "")
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: url, APIKey: apiKey}
		return nil
	}); err != nil {
		t.Fatalf("seed member connection: %v", err)
	}
	lcppMemberCacheReset()
	harnessEngineWindowCache.Clear()
}

// lcppMemberNoConn is the same isolation, but leaves the store empty — the "member never
// configured anything, and this deployment runs no engines either" case every acceptance
// test starts from. AF_CP_BASE_URL/AF_ENGINE_ISSUE_TOKEN are explicitly cleared: this process
// is itself a live agent-fleet workspace and inherits its own real deployment's values, which
// would otherwise make "nothing configured" silently dial the real Control Plane.
func lcppMemberNoConn(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_CP_BASE_URL", "")
	t.Setenv("AF_ENGINE_ISSUE_TOKEN", "")
	lcppMemberCacheReset()
	harnessEngineWindowCache.Clear()
}

// --- harnessEngineToken ------------------------------------------------------------

// The member's own connection wins even when a deployment engine is ALSO configured
// (AF_CP_BASE_URL set) — decision 1: the member's setting always wins.
func TestHarnessEngineTokenMemberConnWinsOverDeployment(t *testing.T) {
	lcppMemberSetConn(t, "http://192.168.0.113:28080/v1", "sk-member")
	t.Setenv("AF_CP_BASE_URL", "https://cp.example") // present, and must be ignored for "llm"
	t.Setenv("AF_ENGINE_ISSUE_TOKEN", "afei_test")

	conn, ok := harnessEngineToken(context.Background(), "llm", "sess-1")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if conn.BaseURL != "http://192.168.0.113:28080/v1" {
		t.Errorf("BaseURL = %q, want the member's own base ending in /v1 (not re-derived from AF_CP_BASE_URL)", conn.BaseURL)
	}
	if conn.Token != "sk-member" {
		t.Errorf("Token = %q, want the member's own API key", conn.Token)
	}
}

// A stored URL that already ends in /v1 (a member who pasted it straight out of another
// OpenAI-compatible client's config) must not become .../v1/v1.
func TestHarnessEngineTokenNormalizesStoredTrailingV1(t *testing.T) {
	lcppMemberSetConn(t, "http://box:9931/v1", "")
	conn, ok := harnessEngineToken(context.Background(), "llm", "")
	if !ok || conn.BaseURL != "http://box:9931/v1" {
		t.Errorf("BaseURL = %q, ok=%v, want http://box:9931/v1, true", conn.BaseURL, ok)
	}
}

// An empty API key is accepted and passed through as an empty bearer — an unauthenticated
// llama-server on a LAN is a real setup (decision, not an oversight).
func TestHarnessEngineTokenEmptyAPIKeyAllowed(t *testing.T) {
	lcppMemberSetConn(t, "http://box:9931", "")
	conn, ok := harnessEngineToken(context.Background(), "llm", "")
	if !ok || conn.Token != "" {
		t.Errorf("Token = %q, ok=%v, want empty token, true", conn.Token, ok)
	}
}

// No member connection: harnessEngineToken falls all the way through to the deployment path
// exactly as before — no AF_CP_BASE_URL means no token, the "no engines" answer.
func TestHarnessEngineTokenNoMemberConnFallsBackToDeployment(t *testing.T) {
	lcppMemberNoConn(t)
	if _, ok := harnessEngineToken(context.Background(), "llm", ""); ok {
		t.Error("ok = true with neither a member connection nor AF_CP_BASE_URL set")
	}
}

// A member connection only ever applies to "llm" — an "image" key must still go through the
// deployment path (there is no member-connection concept for image engines).
func TestHarnessEngineTokenMemberConnOnlyAppliesToLLM(t *testing.T) {
	lcppMemberSetConn(t, "http://box:9931", "sk-member")
	if _, ok := harnessEngineToken(context.Background(), "image", ""); ok {
		t.Error("ok = true for key=image with no AF_CP_BASE_URL — the member lcpp connection must not leak into other engine keys")
	}
}

// --- harnessEngineWindow (via lcppMemberWindow) -------------------------------------

func lcppPropsServer(t *testing.T, propsBody, modelsBody string, propsStatus, modelsStatus int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			w.WriteHeader(propsStatus)
			_, _ = w.Write([]byte(propsBody))
		case "/v1/models":
			w.WriteHeader(modelsStatus)
			_, _ = w.Write([]byte(modelsBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The common case (docs/log/106 §10's live LAN run): a single-model llama-server's /props
// already carries a real n_ctx, so /v1/models is never asked.
func TestHarnessEngineWindowMemberConnReadsPropsDirectly(t *testing.T) {
	var modelsHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			_, _ = w.Write([]byte(`{"build_info":"b11067-932a68e06","default_generation_settings":{"n_ctx":24064}}`))
		case "/v1/models":
			modelsHit = true
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	lcppMemberSetConn(t, srv.URL, "")

	if got := harnessEngineWindow(context.Background(), "llm"); got != 24064 {
		t.Errorf("window = %d, want 24064", got)
	}
	if modelsHit {
		t.Error("/v1/models was called even though /props already answered a real window")
	}
}

// The router shape (docs/log/106 §axis 2): /props describes the router (n_ctx=0), and the
// real window has to come from /v1/models' data[].meta.n_ctx instead.
func TestHarnessEngineWindowMemberConnFallsBackToModelsWhenPropsIsWindowless(t *testing.T) {
	srv := lcppPropsServer(t,
		`{"build_info":"b1","role":"router","model_path":"none","default_generation_settings":{"n_ctx":0}}`,
		`{"data":[{"id":"qwen3-coder-30b-a3b","meta":{"n_ctx":65536}}]}`,
		http.StatusOK, http.StatusOK)
	lcppMemberSetConn(t, srv.URL, "")

	if got := harnessEngineWindow(context.Background(), "llm"); got != 65536 {
		t.Errorf("window = %d, want the router fallback's 65536", got)
	}
}

// Neither endpoint answers usefully (box asleep/unreachable): 0, not an error and not a
// hang — harnessEngineWindow's caller (the compaction judgement) treats 0 as "unknown".
func TestHarnessEngineWindowMemberConnUnreachableIsZero(t *testing.T) {
	lcppMemberSetConn(t, "http://127.0.0.1:1", "") // nothing listens here
	if got := harnessEngineWindow(context.Background(), "llm"); got != 0 {
		t.Errorf("window = %d, want 0", got)
	}
}

// --- harnessEngineAvailable ----------------------------------------------------------

func TestHarnessEngineAvailableMemberConn(t *testing.T) {
	lcppMemberSetConn(t, "http://box:9931", "")
	if !harnessEngineAvailable(context.Background(), "llm") {
		t.Error("available = false with a member connection configured")
	}
}

func TestHarnessEngineAvailableNoMemberConnNoDeployment(t *testing.T) {
	lcppMemberNoConn(t)
	if harnessEngineAvailable(context.Background(), "llm") {
		t.Error("available = true with neither a member connection nor a deployment engine")
	}
}

// --- lcppModels (agent_models.go) -----------------------------------------------------

func TestLcppModelsMemberConnListsTheServersOwnModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gemma-4-12b-it-q4_k_m","meta":{"n_ctx":24064}}]}`))
	}))
	t.Cleanup(srv.Close)
	lcppMemberSetConn(t, srv.URL, "")

	got := lcppModels(context.Background())
	if len(got) != 1 || got[0].ID != "gemma-4-12b-it-q4_k_m" || got[0].Label != "gemma-4-12b-it-q4_k_m" {
		t.Errorf("models = %+v", got)
	}
}

// The launch menu must never block or error on an unreachable member connection — it reads
// exactly like "no models", the same contract catalog-backed kinds already have (docs/log/54).
func TestLcppModelsMemberConnUnreachableIsEmptyNotError(t *testing.T) {
	lcppMemberSetConn(t, "http://127.0.0.1:1", "")
	got := lcppModels(context.Background())
	if len(got) != 0 {
		t.Errorf("models = %+v, want empty", got)
	}
}

// --- acceptance: unset connection reproduces today's behavior exactly (decision 1) -------

// With no member connection at all, every one of the four seams must behave EXACTLY as it
// did before this feature existed: no AF_CP_BASE_URL means no token/window/availability, and
// lcppModels reads the (here, empty) catalogue — never the member-connection branch.
func TestNoMemberConnReproducesPreExistingBehaviorExactly(t *testing.T) {
	lcppMemberNoConn(t)

	if _, ok := harnessEngineToken(context.Background(), "llm", ""); ok {
		t.Error("harnessEngineToken: ok = true with nothing configured")
	}
	if got := harnessEngineWindow(context.Background(), "llm"); got != 0 {
		t.Errorf("harnessEngineWindow = %d, want 0", got)
	}
	if harnessEngineAvailable(context.Background(), "llm") {
		t.Error("harnessEngineAvailable: true with nothing configured")
	}
	if got := lcppModels(context.Background()); got != nil {
		t.Errorf("lcppModels = %+v, want nil", got)
	}
}

// --- lcppMemberReachable (the "reachable" field's underlying observation) ------------------

// lcppMemberSetConn/lcppMemberNoConn already call lcppMemberCacheReset, which clears the
// observation too — so every test below starts from "unknown", not whatever a preceding test
// in this file left behind.

// A real fetch through lcppModels (the launch-menu path, unrelated to a check button) records
// success.
func TestLcppMemberFetchModelsCachedRecordsReachableOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	t.Cleanup(srv.Close)
	lcppMemberSetConn(t, srv.URL, "")

	if _, known := lcppMemberObservedReachable(); known {
		t.Fatal("known = true before any fetch happened")
	}
	lcppModels(context.Background())
	ok, known := lcppMemberObservedReachable()
	if !known || !ok {
		t.Errorf("ok=%v known=%v, want true, true", ok, known)
	}
}

// An unreachable box records a false observation, not a missing one — "unknown" and
// "unreachable" are different facts, and only the latter follows a real, failed attempt.
func TestLcppMemberFetchModelsCachedRecordsReachableOnFailure(t *testing.T) {
	lcppMemberSetConn(t, "http://127.0.0.1:1", "") // nothing listens here
	lcppModels(context.Background())
	ok, known := lcppMemberObservedReachable()
	if !known || ok {
		t.Errorf("ok=%v known=%v, want false, true", ok, known)
	}
}

// A cache hit (within lcppMemberModelsCacheTTL) must NOT overwrite the observation — the
// recorded fact is "the last real dial", not "the last time this function was called".
func TestLcppMemberFetchModelsCachedCacheHitDoesNotReRecord(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError) // every real dial after the first would flip ok to false
	}))
	t.Cleanup(srv.Close)
	lcppMemberSetConn(t, srv.URL, "")

	// Seed one successful observation directly, then force a "would-be" real dial to be a
	// server error: since it lands inside the cache TTL, lcppModels must serve the cached
	// (successful) answer and never call the server a second time.
	lcppMemberRecordReachable(true)
	lcppMemberModelsCache.mu.Lock()
	lcppMemberModelsCache.at = time.Now()
	lcppMemberModelsCache.value = []lcppMemberModel{{ID: "m1"}}
	lcppMemberModelsCache.mu.Unlock()

	lcppModels(context.Background())
	if hits != 0 {
		t.Errorf("hits = %d, want 0 (cache hit must not dial)", hits)
	}
	ok, known := lcppMemberObservedReachable()
	if !known || !ok {
		t.Errorf("ok=%v known=%v, want true, true (the seeded observation, untouched)", ok, known)
	}
}

// Changing the connection (a fresh lcppMemberSetConn/PUT) drops the PREVIOUS connection's
// observation back to unknown — a stale true/false about a URL that no longer applies must not
// carry over onto the new one.
func TestLcppMemberCacheResetClearsReachableObservation(t *testing.T) {
	lcppMemberSetConn(t, "http://box:9931", "")
	lcppMemberRecordReachable(true)
	if _, known := lcppMemberObservedReachable(); !known {
		t.Fatal("setup: observation not recorded")
	}

	lcppMemberCacheReset()
	if _, known := lcppMemberObservedReachable(); known {
		t.Error("known = true after lcppMemberCacheReset, want false (unknown)")
	}
}
