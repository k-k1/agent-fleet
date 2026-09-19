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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// enginePropsSharedOnce and enginePropsShared build the real SQLite store, signing key and one
// allowed membership ONCE per test binary rather than once per test. The other gateway test
// files in this package hold no store at all — props() is the one route that runs the same
// membership/tenant gates serve() does — but that does not make it worth a fresh
// store.OpenSQLite + Migrate for each of the seven tests below: every one of them only READS
// the result, so sharing it costs nothing any of them individually needs.
//
// Not t.TempDir(): that directory is removed when the FIRST test to call it finishes, and the
// other six would then be opening a database file that no longer exists. A plain os.MkdirTemp
// outlives every test in this process instead — and is removed once, by
// enginePropsFixtureCleanup, from TestMain (cli_release_watch_test.go): this workspace's /tmp is
// a persistent disk shared by every session, not a container that vanishes at process exit, so
// one directory per run of this package would otherwise accumulate there forever.
var (
	enginePropsSharedOnce sync.Once
	enginePropsSharedDir  string
	enginePropsShared     struct {
		mgr          *manager
		signKey      []byte
		membershipID string
		err          error
	}
)

// enginePropsFixtureCleanup removes the directory enginePropsFixture built, if it ever built
// one. Safe to call from every test run, including one that never touched a props test at all
// (enginePropsSharedDir is then still "").
func enginePropsFixtureCleanup() {
	if enginePropsSharedDir != "" {
		_ = os.RemoveAll(enginePropsSharedDir)
	}
}

// enginePropsFixture returns the shared store/key/membership, building them on the first call.
// Each test still builds its OWN engine row (enginePropsFixture never touches one) so it can
// vary provider, reachability and lifecycle freely.
func enginePropsFixture(t *testing.T) (mgr *manager, signKey []byte, membershipID string) {
	t.Helper()
	enginePropsSharedOnce.Do(func() {
		dir, err := os.MkdirTemp("", "engine-props-fixture-*")
		if err != nil {
			enginePropsShared.err = err
			return
		}
		enginePropsSharedDir = dir
		st, err := store.OpenSQLite(filepath.Join(dir, "cp.db"))
		if err != nil {
			enginePropsShared.err = err
			return
		}
		ctx := context.Background()
		if err := st.Migrate(ctx); err != nil {
			enginePropsShared.err = err
			return
		}
		tenant, err := st.CreateTenant(ctx, "allowed", "Allowed")
		if err != nil {
			enginePropsShared.err = err
			return
		}
		ident, err := st.UpsertIdentity(ctx, "allowed@example.com", sanitizeUser("allowed@example.com"), "")
		if err != nil {
			enginePropsShared.err = err
			return
		}
		mem, err := st.EnsureMembership(ctx, ident.ID, tenant.ID, "member")
		if err != nil {
			enginePropsShared.err = err
			return
		}
		enginePropsShared.mgr = &manager{store: st}
		enginePropsShared.signKey = engineSignKey([]byte(strings.Repeat("k", 32)))
		enginePropsShared.membershipID = mem.ID
	})
	// Checked OUTSIDE the Once, by every caller: t.Fatalf inside the Do closure would only
	// fail the one test that happened to run the closure (via runtime.Goexit, which sync.Once
	// still marks "done" through), leaving the other six silently reading a zero-value mgr.
	// Each caller failing itself is what makes a setup failure loud on every test rather than
	// on whichever one lost the race to build it.
	if enginePropsShared.err != nil {
		t.Fatalf("shared props fixture: %v", enginePropsShared.err)
	}
	return enginePropsShared.mgr, enginePropsShared.signKey, enginePropsShared.membershipID
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
// for exactly its OWN /engine/{their-key}/props.
//
// The far side's key is deliberately spelled DIFFERENTLY from the local row's ("chat-far" vs.
// "llm"): composing the target from the LOCAL key would happen to land on the right path when
// the two spellings coincide, which is exactly what let a guess pass as "read from the far
// side" before this test forced them apart. base_url is the only place the far key is stated
// (engine_remote_token.go:81-83) — never eng.def.Key, which is this deployment's own.
func TestEnginePropsBorrowedRowAsksTheFarGatewaysOwnRoute(t *testing.T) {
	mgr, signKey, mid := enginePropsFixture(t)

	var paths []string
	var gotAuth string
	far := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/internal/engine/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "afe_far-token",
				"expires_at": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339),
				// The far deployment's OWN spelling for this role, unrelated to the local "llm"
				// key below.
				"base_url": "/engine/chat-far/v1",
			})
		case "/engine/chat-far/props":
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":16384}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
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
		if p == "/engine/chat-far/props" {
			found = true
		}
		if p == "/engine/llm/props" {
			t.Errorf("the far side was asked for %s — that is the LOCAL key guessed at, not the far side's own base_url", p)
		}
		if p == "/engine/chat-far/v1/props" {
			t.Errorf("the far side was asked for %s — the /v1/ segment it stated must be dropped, not kept", p)
		}
	}
	if !found {
		t.Errorf("far side saw %v, want /engine/chat-far/props among them", paths)
	}
	if gotAuth != "Bearer afe_far-token" {
		t.Errorf("far Authorization = %q, want the bought far session token", gotAuth)
	}
	if strings.Contains(gotAuth, "local-key") {
		t.Error("the row's own local apiKey was presented to the far deployment")
	}
}

// --- router mode: /props answers windowless, /v1/models fills it in (ADR 0093 段0 追补) ----

// The shape this whole follow-up exists for: a router-mode llama-server's
// default_generation_settings describes the ROUTER, not any one model, so n_ctx there is 0.
// props() must then read the model's real window from /v1/models — directly, never through
// serve() — and add it under router_selected_model without disturbing anything /props itself
// said. Measured against a real router deployment (2026-09-20): 262144, in data[].meta.n_ctx.
func TestEnginePropsRouterModeReadsV1ModelsForWindow(t *testing.T) {
	mgr, signKey, mid := enginePropsFixture(t)
	var modelsAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/props":
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":0},"model_path":"none","role":"router"}`))
		case "/v1/models":
			modelsAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen3.8-27b-uncensored-q4_k_m","meta":{"n_ctx":262144}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer up.Close()

	api := &engineTestECS{desired: 1, running: 1}
	eng := newTestEngine(t, up.URL, api)
	eng.apiKey = "engine-secret"
	eng.catalog = &engineCatalog{source: func(context.Context) ([]store.EngineModel, error) {
		return []store.EngineModel{
			{ID: "qwen3.8-27b-uncensored-q4_k_m", Kind: "checkpoint", Enabled: true, Default: true},
		}, nil
	}}
	g := propsGateway(mgr, signKey, eng)

	tok := mintEngineSessionToken(signKey, mid, "sess-1", "llm", time.Now().Add(time.Hour))
	rec := httptest.NewRecorder()
	g.props(rec, propsRequest(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("router props: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		DefaultGenerationSettings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
		RouterSelectedModel struct {
			ID   string `json:"id"`
			NCtx int    `json:"n_ctx"`
		} `json:"router_selected_model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response body did not decode: %v: %s", err, rec.Body.String())
	}
	if out.DefaultGenerationSettings.NCtx != 0 {
		t.Errorf("default_generation_settings.n_ctx = %d, want 0 preserved exactly as the router sent it",
			out.DefaultGenerationSettings.NCtx)
	}
	if out.RouterSelectedModel.NCtx != 262144 {
		t.Fatalf("router_selected_model.n_ctx = %d, want 262144 read from /v1/models", out.RouterSelectedModel.NCtx)
	}
	if out.RouterSelectedModel.ID != "qwen3.8-27b-uncensored-q4_k_m" {
		t.Errorf("router_selected_model.id = %q, want the catalogue's default model", out.RouterSelectedModel.ID)
	}
	if modelsAuth != "Bearer engine-secret" {
		t.Errorf("/v1/models Authorization = %q, want the engine's own apiKey", modelsAuth)
	}
	if api.updates != 0 {
		t.Errorf("ECS saw %d UpdateService call(s) — reading /v1/models directly must never touch the engine's lifecycle", api.updates)
	}
}

// A single-model engine's /props already answers a real window, so props() must never pay a
// second upstream round trip reading /v1/models for it — the positive control for design choice
// (a) over (b) in enginePropsAugmentRouterWindow's doc comment: read /v1/models ONLY when /props
// came back windowless.
func TestEnginePropsSingleModelNeverReadsV1Models(t *testing.T) {
	mgr, signKey, mid := enginePropsFixture(t)
	var sawModels bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			sawModels = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":32768}}`))
	}))
	defer up.Close()

	api := &engineTestECS{desired: 1, running: 1}
	eng := newTestEngine(t, up.URL, api)
	g := propsGateway(mgr, signKey, eng)

	tok := mintEngineSessionToken(signKey, mid, "sess-1", "llm", time.Now().Add(time.Hour))
	rec := httptest.NewRecorder()
	g.props(rec, propsRequest(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("single-model props: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if sawModels {
		t.Error("props() read /v1/models for an engine whose /props already answered a real window")
	}
}

// A borrowed row is never augmented locally: its /v1/models lives on the FAR deployment, and
// reading it directly from this process would mean dialing the far gateway's serve() — exactly
// the demand-record-and-wake path this route exists to avoid. It is relayed exactly as the far
// side answered, windowless or not (a version-skew case this side cannot itself close until the
// far deployment carries this same patch).
func TestEnginePropsBorrowedRowNeverAugmentedLocally(t *testing.T) {
	mgr, signKey, mid := enginePropsFixture(t)

	var farSawModels bool
	far := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/engine/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "afe_far-token",
				"expires_at": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339),
				"base_url":   "/engine/chat-far/v1",
			})
		case "/engine/chat-far/props":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":0},"role":"router"}`))
		case "/engine/chat-far/v1/models":
			farSawModels = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
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
	}
	g := propsGateway(mgr, signKey, eng)

	tok := mintEngineSessionToken(signKey, mid, "sess-1", "llm", time.Now().Add(time.Hour))
	rec := httptest.NewRecorder()
	g.props(rec, propsRequest(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("borrowed router row: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), enginePropsRouterWindowField) {
		t.Errorf("body = %s, a borrowed row must not be augmented locally", rec.Body.String())
	}
	if farSawModels {
		t.Error("this deployment dialed the far side's /v1/models directly — that goes through the far gateway's serve(), the wake path this route exists to avoid")
	}
}
