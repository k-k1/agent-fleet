package main

// engine_external_test.go — the engine somebody else runs (ADR 0076).
//
// What these pin is not "a ComfyUI on the LAN works" — no test in this repository can reach
// one — but the three places the fleet used to assume an engine is an ECS service: the table
// parse, the registry's wiring, and everything that dereferences a row's ECS adapter.

import (
	"bytes"
	"context"
	"encoding/json"

	"net/http"
	"net/http/httptest"

	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// comfyStub is a ComfyUI as far as the gateway is concerned: /system_stats is the health path
// the synthesised row declares, and the native API lives at the root (no /v1).
func comfyStub() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/system_stats" {
			_, _ = w.Write([]byte(`{"system":{"comfyui_version":"0.3.0"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "p1", "seen": r.URL.Path})
	})
}

// newTestExternalEngine is the row AF_COMFY_URL synthesises, wired the way newEngineRegistry
// wires it: a catalogue, a settings store, and nothing else at all.
//
// It goes through engineComfyEnvRow rather than writing the fields out, so a change to what the
// environment synthesises reaches every test below instead of passing against a hand-built row.
func newTestExternalEngine(t *testing.T, url string, settings store.Store) *engineRuntimeState {
	t.Helper()
	t.Setenv("AF_COMFY_URL", url)
	def, _, ok := engineComfyEnvRow()
	if !ok {
		t.Fatalf("engineComfyEnvRow() synthesised no row for %s", url)
	}
	e := &engineRuntimeState{def: def, catalog: newEngineCatalog(nil, def.Key)}
	if settings != nil {
		e.settings, e.catalog = settings, newEngineCatalog(settings, def.Key)
	}
	return e
}

// --- decision 1: the table declares the lifecycle ------------------------------

// A `service` is what a desired count moves on, so a row that declares nobody moves it owes
// none. Everything else stays required: the point of decision 1 is that "external" is written
// down, not derived from a field somebody forgot.
func TestParseEngineTableAllowsAnExternalRowWithoutAService(t *testing.T) {
	raw := `{"engines":[{"key":"image","api":"images","provider":"comfy","lifecycle":"external",
	 "url":"http://192.0.2.20:8188","health":"/system_stats"}]}`
	tab, err := parseEngineTable(raw)
	if err != nil {
		t.Fatalf("an external row without a service was refused: %v", err)
	}
	if len(tab.Engines) != 1 || !tab.Engines[0].external() {
		t.Fatalf("table = %+v", tab.Engines)
	}

	for _, bad := range []string{
		// A managed row still owes a service — the whole reason this check exists.
		`{"engines":[{"key":"image","url":"http://x"}]}`,
		// And an external one still owes the two things nothing can invent.
		`{"engines":[{"lifecycle":"external","url":"http://x"}]}`,
		`{"engines":[{"key":"image","lifecycle":"external"}]}`,
	} {
		if _, err := parseEngineTable(bad); err == nil {
			t.Errorf("parseEngineTable(%s) accepted an incomplete row", bad)
		}
	}
}

// --- decision 2: AF_COMFY_URL, and who wins ------------------------------------

// The whole native/docker case in one test: one environment variable, and the Control Plane
// has an image engine with no AWS anywhere behind it. What is asserted is the ABSENCE of the
// six things decision 1 says an external row does not get — each of them is a client, a
// goroutine or an SSM parameter that a deployment with no credentials cannot have.
func TestEngineRegistryFromComfyURLAloneWiresNoAWS(t *testing.T) {
	t.Setenv("AF_ENGINES_SSM_PARAM", "")
	t.Setenv("AF_ENGINES_JSON", "")
	t.Setenv("AF_COMFY_URL", "http://192.0.2.20:8188")
	t.Setenv("AF_COMFY_API_KEY", "proxy-bearer")

	reg := newEngineRegistry(context.Background(), nil)
	if reg == nil {
		t.Fatal("AF_COMFY_URL alone produced no registry — the gate still demands an engine table")
	}
	e := reg.get("image")
	if e == nil {
		t.Fatalf("no image engine: %+v", reg.byKey)
	}
	if !e.def.external() || e.def.Provider != "comfy" || e.def.api() != engineAPIImages {
		t.Errorf("def = %+v, want an external comfy images row", e.def)
	}
	if e.def.Health != "/system_stats" {
		t.Errorf("health = %q, want /system_stats (60-engines' path for comfy)", e.def.Health)
	}
	// The bearer goes in the field a managed row fills from SSM, because dial and engineHealthy
	// already present that one.
	if e.apiKey != "proxy-bearer" {
		t.Errorf("apiKey = %q, want the value of AF_COMFY_API_KEY", e.apiKey)
	}
	for _, c := range []struct {
		what string
		set  bool
	}{
		{"ecs", e.ecs != nil},
		{"controller", e.ctrl != nil},
		{"demand counter", e.demand != nil},
		{"pending reader", e.pending != nil},
		{"ssm", e.ssm != nil},
		{"active set parameter", e.activeParam != ""},
		{"instance class ladder", len(e.classList()) > 0},
	} {
		if c.set {
			t.Errorf("the external row was given an %s", c.what)
		}
	}
	if e.catalog == nil {
		t.Error("the external row has no catalogue — what it may generate is still ours to declare")
	}
}

// Decision 2's precedence, all three ways. The third is the one the review reversed: an
// environment variable that replaced a MANAGED row would leave its ECS service running with
// nothing left to stop it, which costs $1.26/hour for as long as nobody notices.
func TestEngineTableEnvRowPrecedence(t *testing.T) {
	env := engineDef{
		Key: "image", API: engineAPIImages, Provider: "comfy",
		URL: "http://192.0.2.20:8188", Health: "/system_stats", Lifecycle: engineLifecycleExternal,
	}

	added := engineTableWithEnvRow(nil, env)
	if len(added) != 1 || added[0] != env {
		t.Fatalf("no row on that key: got %+v, want the synthesised one", added)
	}

	was := []engineDef{{Key: "image", URL: "http://192.0.2.9:8188", Lifecycle: engineLifecycleExternal}}
	got := engineTableWithEnvRow(was, env)
	if len(got) != 1 || got[0].URL != env.URL {
		t.Errorf("against an external row: got %+v, want the environment to win", got)
	}
	if was[0].URL != "http://192.0.2.9:8188" {
		t.Error("the table this process started from was rewritten in place")
	}

	managed := []engineDef{{Key: "image", Service: "af-image", URL: "http://image.af.internal:8080"}}
	got = engineTableWithEnvRow(managed, env)
	if len(got) != 1 || got[0].Service != "af-image" || got[0].URL != "http://image.af.internal:8080" {
		t.Errorf("against a managed row: got %+v, want the table to win", got)
	}
}

// --- decision 2: the reloader has nothing to say about an external row ---------

// The synthesised row is not in the SSM table at all, so EVERY branch of apply would fire on
// it whenever any other row changed: a "restart the Control Plane" line about a service the
// table never mentioned, ten seconds after the last one, for ever.
func TestEngineTableReloaderLeavesExternalRowsAlone(t *testing.T) {
	e := newTestExternalEngine(t, "http://192.0.2.20:8188", nil)
	r := &engineTableReloader{name: "/af-ws/engines", reg: &engineRegistry{
		byKey: map[string]*engineRuntimeState{"image": e},
	}}
	var buf bytes.Buffer
	defer captureLog(&buf)()

	// A row that differs in service, url, health and provider — four of the fields
	// engineDefDriftedBeyondClasses names one by one.
	if r.apply(engineTable{Engines: []engineDef{{
		Key: "image", Service: "af-image", URL: "http://image.af.internal:8080",
		Health: "/health", Provider: "sdcpp",
	}}}) {
		t.Error("apply reported a change against an externally managed row")
	}
	// And a table that does not mention it, which is the ordinary case.
	if r.apply(engineTable{}) {
		t.Error("apply reported a change for a row the table never held")
	}
	if buf.Len() != 0 {
		t.Errorf("the reloader wrote %q about an engine it does not own", buf.String())
	}
	if e.def.URL != "http://192.0.2.20:8188" {
		t.Errorf("the row moved to %q", e.def.URL)
	}
}

// --- decision 4: nothing here can start it -------------------------------------

// The forwarding half is unchanged, and this says so: health passes, the request goes upstream
// with no /v1 prefix, and the answer comes back.
func TestEngineGatewayForwardsToAnExternalEngine(t *testing.T) {
	up := httptest.NewServer(comfyStub())
	defer up.Close()
	e := newTestExternalEngine(t, up.URL, nil)
	g := engineGateway{reg: &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/engine/image/v1/prompt", strings.NewReader(`{}`))
	r.SetPathValue("path", "prompt")
	g.plain(rec, r, e, engineSessionClaims{Key: "image"}, testMembership(), []byte(`{}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"seen":"/prompt"`) {
		t.Errorf("body = %s, want the request forwarded to /prompt", rec.Body.String())
	}
}

// And the half decision 4 changes. A LAN box that is not answering does not come up because
// the fleet waited, so the refusal is immediate, is `engine_unavailable` rather than the
// `engine_waking` the provider retries for sixteen minutes, and names the URL an operator can
// go and look at.
func TestEngineGatewayFailsAtOnceWhenAnExternalEngineIsDown(t *testing.T) {
	up := httptest.NewServer(comfyStub())
	url := up.URL
	up.Close() // nothing is listening on it any more
	e := newTestExternalEngine(t, url, nil)
	g := engineGateway{reg: &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/engine/image/v1/prompt", strings.NewReader(`{}`))
	r.SetPathValue("path", "prompt")
	started := time.Now()
	g.plain(rec, r, e, engineSessionClaims{Key: "image"}, testMembership(), []byte(`{}`))
	waited := time.Since(started)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	if out.Error.Code != "engine_unavailable" {
		t.Errorf("code = %q, want engine_unavailable (engine_waking buys 16 minutes of retries "+
			"for a box nothing here can start)", out.Error.Code)
	}
	if !strings.Contains(out.Error.Message, url+"/system_stats") {
		t.Errorf("message = %q, want the health URL the operator has to go and look at", out.Error.Message)
	}
	if !strings.Contains(out.Error.Message, "externally managed") {
		t.Errorf("message = %q, want it to say who owns the engine", out.Error.Message)
	}
	// One health probe and out. The plain path would otherwise hold for 45 seconds.
	if waited > 10*time.Second {
		t.Errorf("the refusal took %s — something waited for a start that cannot happen", waited)
	}
}

// --- decision 5: the row, the modes ---------------------------------------------

// adminRowFor drives the real list handler and returns the one row in it.
func adminRowFor(t *testing.T, a engineAdminAPI) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	a.get(rec, httptest.NewRequest("GET", "/api/admin/engines", nil),
		engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
	var out struct {
		Engines []map[string]any `json:"engines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	if len(out.Engines) != 1 {
		t.Fatalf("engines = %v, want exactly one", out.Engines)
	}
	return out.Engines[0]
}

// Decision 5's contract, written down here because the Console half was built against it in
// another session. What matters as much as the fields that are PRESENT is the six that are
// absent: each of them would otherwise be a number invented about a box this deployment does
// not have, and `idle_secs: 0` in particular is configured to mean "never stops".
func TestEngineAdminRowContractForAnExternalEngine(t *testing.T) {
	st := testSettingsStore(t)
	up := httptest.NewServer(comfyStub())
	defer up.Close()
	e := newTestExternalEngine(t, up.URL, st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, &engineRegistry{
		byKey: map[string]*engineRuntimeState{"image": e},
	}, st}

	row := adminRowFor(t, a)
	if row["managed"] != false {
		t.Errorf("managed = %v, want false", row["managed"])
	}
	if row["lifecycle"] != engineLifecycleExternal {
		t.Errorf("lifecycle = %v, want %q", row["lifecycle"], engineLifecycleExternal)
	}
	if row["url"] != up.URL {
		t.Errorf("url = %v, want %q", row["url"], up.URL)
	}
	if row["warm"] != true {
		t.Errorf("warm = %v, want true — the stub answers /system_stats", row["warm"])
	}
	if row["mode"] != engineModeOn {
		t.Errorf("mode = %v, want on (an engine somebody else runs has no default to be careful about)", row["mode"])
	}
	for _, gone := range []string{
		"state", "desired", "box", "stop_eta", "idle_secs",
		"window_secs", "window_units", "window_counted_secs",
	} {
		if v, ok := row[gone]; ok {
			t.Errorf("%s = %v is on an external row; it can only be invented", gone, v)
		}
	}

	// Decision 8: the answer is cached for ten seconds, so a panel that polls does not turn
	// into a dial loop — and a box that goes away mid-poll does not change the row until then.
	up.Close()
	if again := adminRowFor(t, a); again["warm"] != true {
		t.Errorf("warm = %v on the second read; the 10-second cache did not hold", again["warm"])
	}
}

// On-demand promises to stop the box when nobody wants it, and there is no box here to stop.
// Refused rather than quietly stored: the setting outlives this row's lifecycle.
func TestEngineAdminRefusesOnDemandForAnExternalEngine(t *testing.T) {
	st := testSettingsStore(t)
	e := newTestExternalEngine(t, "http://192.0.2.20:8188", st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, &engineRegistry{
		byKey: map[string]*engineRuntimeState{"image": e},
	}, st}

	code, out := adminPut(t, a, "image", `{"mode":"ondemand"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("PUT ondemand = %d, want 400 (%v)", code, out)
	}
	// off and on are the two that mean something, and they still work.
	for _, mode := range []string{engineModeOff, engineModeOn} {
		if code, out := adminPut(t, a, "image", `{"mode":"`+mode+`"}`); code != http.StatusOK {
			t.Errorf("PUT %s = %d (%v)", mode, code, out)
		}
	}

	// And a value stored before this row was external — or by a client written against the
	// three-valued toggle — reads as `on` rather than as a promise nothing can keep.
	ctx := context.Background()
	if err := st.SetSetting(ctx, engineSettingsFor("image").mode, engineModeOnDemand); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := e.mode(ctx); got != engineModeOn {
		t.Errorf("a stored ondemand reads as %q, want on", got)
	}
}

// --- decision 1: the nil round --------------------------------------------------

// Every handler an external row reaches, in one test, because the thing that breaks a row with
// no ECS adapter is not a wrong answer but a nil dereference — and a panic in one handler is
// invisible from the tests of all the others.
//
// The review read each of these call sites and found only two that were not already nil-safe
// (ensureStarted's `e.ecs.view` and the admin put's start/stop). This is what stops the next
// change from adding a third.
func TestExternalEngineGoesThroughEveryHandler(t *testing.T) {
	ctx := context.Background()
	st := testSettingsStore(t)
	up := httptest.NewServer(comfyStub())
	defer up.Close()

	e := newTestExternalEngine(t, up.URL, st)
	reg := &engineRegistry{
		byKey:   map[string]*engineRuntimeState{"image": e},
		signKey: engineSignKey([]byte(strings.Repeat("k", 32))),
	}
	mgr := &manager{store: st}
	a := engineAdminAPI{memberAuth{mgr}, reg, st}
	g := engineGateway{mgr: mgr, reg: reg}

	// The admin list, and the mode toggle in both directions.
	_ = adminRowFor(t, a)
	for _, mode := range []string{engineModeOff, engineModeOn} {
		if code, out := adminPut(t, a, "image", `{"mode":"`+mode+`"}`); code != http.StatusOK {
			t.Fatalf("PUT %s = %d (%v)", mode, code, out)
		}
	}

	// The catalogue routes: register a model, toggle it, and read the row back.
	if code, out := adminModel(t, a, "POST", "image", "",
		`{"id":"sdxl-base-1.0","kind":"checkpoint","base_model":"sdxl",
		  "files":[{"s3Key":"checkpoints/sd_xl_base_1.0.safetensors"}]}`,
	); code != http.StatusOK {
		t.Fatalf("POST model = %d (%v)", code, out)
	}
	if code, out := adminModel(t, a, "PUT", "image", "sdxl-base-1.0",
		`{"enabled":true,"selected":true}`); code != http.StatusOK {
		t.Fatalf("PUT model = %d (%v)", code, out)
	}
	e.catalog.invalidate()

	// /internal/engine/catalog — what the Workspace Agent reads to know the engine exists.
	tn, err := st.CreateTenant(ctx, "acme", "acme")
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	ident, err := st.UpsertIdentity(ctx, "u@example.invalid", "u-example-invalid", "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	mem, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	rec := httptest.NewRecorder()
	cr := httptest.NewRequest("GET", "/internal/engine/catalog", nil)
	cr.Header.Set("Authorization", "Bearer "+mintEngineIssueToken(reg.signKey, mem.ID))
	g.catalog(rec, cr)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"base_url":"/engine/image/v1"`) {
		t.Errorf("catalog body = %s, want the external engine offered", rec.Body.String())
	}

	// And the gateway itself, end to end through the route's own handler.
	tok := mintEngineSessionToken(reg.signKey, mem.ID, "s-1", "image", time.Now().Add(time.Hour))
	rec = httptest.NewRecorder()
	gr := httptest.NewRequest("POST", "/engine/image/v1/prompt", strings.NewReader(`{}`))
	gr.Header.Set("Authorization", "Bearer "+tok)
	gr.Header.Set("X-AF-Model", "sdxl-base-1.0")
	gr.SetPathValue("key", "image")
	gr.SetPathValue("path", "prompt")
	g.serve(rec, gr)
	if rec.Code != http.StatusOK {
		t.Fatalf("serve = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"prompt_id":"p1"`) {
		t.Errorf("serve body = %s", rec.Body.String())
	}

	// The same route with the engine gone: a refusal, still not a panic.
	up.Close()
	rec = httptest.NewRecorder()
	gr = httptest.NewRequest("POST", "/engine/image/v1/prompt", strings.NewReader(`{}`))
	gr.Header.Set("Authorization", "Bearer "+tok)
	gr.SetPathValue("key", "image")
	gr.SetPathValue("path", "prompt")
	g.serve(rec, gr)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("serve against a dead engine = %d: %s", rec.Code, rec.Body.String())
	}
}
