package main

// The super-admin toggle for the self-hosted engines (ADR 0071). What is pinned here is the
// thing that made it worth building: the STORED SETTING is the control, and the rest of the
// system — the catalogue, the gateway, the controller — already obeys it. A toggle whose
// effect stops at the ECS desired count would leave an engine "off" that a Workspace still
// writes into opencode's config and still routes to.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func newAdminTestRegistry(t *testing.T, api engineECSAPI, st store.SettingsStore) (*engineRegistry, *engineRuntimeState) {
	t.Helper()
	e := newTestImageEngine(t, "http://127.0.0.1:1", api)
	// The same wiring newEngineRegistry does: the mode is read from the store, not from a
	// controller. A test that left this nil would be testing the stack default.
	e.settings = st
	e.ctrl = nil // the controller is not what these tests are about, and nil is a valid state
	return &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}, e
}

func adminPut(t *testing.T, a engineAdminAPI, key, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/api/admin/engines/"+key, strings.NewReader(body))
	r.SetPathValue("key", key)
	a.put(rec, r, store.Identity{ID: "u1"})
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// The three modes, and what each one is allowed to do to the desired count. Unlike the TTS
// toggle, OFF stops the box HERE: a GPU is $1.26/hour and there is no undo window worth
// paying for.
func TestEngineAdminModes(t *testing.T) {
	st := testSettingsStore(t)
	f := &engineTestECS{desired: 1, running: 1}
	reg, e := newAdminTestRegistry(t, f, st)
	_ = e
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}

	code, out := adminPut(t, a, "image", `{"mode":"off"}`)
	if code != http.StatusOK {
		t.Fatalf("off = %d (%v)", code, out)
	}
	if out["mode"] != "off" || out["enabled"] != false {
		t.Errorf("after off: mode=%v enabled=%v", out["mode"], out["enabled"])
	}
	// Unlike the TTS toggle there is no undo window, so by the time the answer is written the
	// desired count has already moved. (What a real ECS reports while the task is still going
	// away is engineDisplayState's job, tested on its own below.)
	if out["state"] != "stopped" {
		t.Errorf("after off: state=%v, want stopped", out["state"])
	}
	if f.desired != 0 {
		t.Errorf("after off: desired = %d, want the GPU stopped immediately", f.desired)
	}
	// The setting is the control. Everything else in the system reads THIS.
	if v, _ := st.GetSetting(t.Context(), engineSettingsFor("image").mode); v != "off" {
		t.Fatalf("stored mode = %q, want off — the toggle wrote nothing the rest of the CP can read", v)
	}
	if v, _ := st.GetSetting(t.Context(), engineSettingsFor("image").modeAt); v == "" {
		t.Error("the mode change time was not recorded")
	}

	if code, out = adminPut(t, a, "image", `{"mode":"on"}`); code != http.StatusOK || out["mode"] != "on" {
		t.Fatalf("on = %d %v", code, out)
	}
	if f.desired != 1 {
		t.Errorf("after on: desired = %d, want it started — somebody is waiting for it", f.desired)
	}

	// ondemand hands the desired count back to the controller and touches nothing itself.
	before := f.updates
	if code, out = adminPut(t, a, "image", `{"mode":"ondemand"}`); code != http.StatusOK || out["mode"] != "ondemand" {
		t.Fatalf("ondemand = %d %v", code, out)
	}
	if f.updates != before {
		t.Errorf("ondemand moved the desired count (%d updates), want none", f.updates-before)
	}
}

// The mode decides whether the engine is IN /internal/engine/catalog at all, and the Agent
// caches that answer for ten minutes — so this route has to PUSH the change to running
// Workspaces exactly as enabling a model does. Without it, `off` leaves every open session
// offering an engine that now answers 503 engine_off, and `on` hides a ready one from
// generate_image, for up to the whole TTL. (Measured on af-sandbox, ADR 0072 P2 実機検証.)
func TestEngineAdminModePushesTheCatalogue(t *testing.T) {
	pushed := make(chan string, 4)
	orig := notifyEngineCatalogChanged
	notifyEngineCatalogChanged = func(_ context.Context, _ *manager, key string) { pushed <- key }
	t.Cleanup(func() { notifyEngineCatalogChanged = orig })

	st := testSettingsStore(t)
	reg, _ := newAdminTestRegistry(t, &engineTestECS{desired: 1, running: 1}, st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}

	// All three, because each reaches the end of the handler by a different path and the mode
	// is stored on every one of them.
	for _, mode := range []string{"off", "ondemand", "on"} {
		if code, out := adminPut(t, a, "image", `{"mode":"`+mode+`"}`); code != http.StatusOK {
			t.Fatalf("mode=%s: %d (%v)", mode, code, out)
		}
		select {
		case key := <-pushed:
			if key != "image" {
				t.Errorf("mode=%s pushed %q, want image", mode, key)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("mode=%s never told running sessions the catalogue changed", mode)
		}
	}
}

// The gateway and the catalogue are where "off" has to be felt. Without this the toggle
// would be a setting nobody consults.
func TestEngineAdminOffTakesItOutOfTheCatalogueAndRefusesRouting(t *testing.T) {
	st := testSettingsStore(t)
	reg, e := newAdminTestRegistry(t, &engineTestECS{desired: 1, running: 1}, st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}

	g := engineGateway{mgr: &manager{store: st}, reg: reg}
	// A positive control first: with the engine on, it IS in the catalogue. Without this a
	// broken catalogue call would make the negative below pass for the wrong reason.
	if n := len(catalogKeys(t, g)); n != 1 {
		t.Fatalf("catalogue has %d engines before the toggle, want 1", n)
	}

	if code, _ := adminPut(t, a, "image", `{"mode":"off"}`); code != http.StatusOK {
		t.Fatalf("off = %d", code)
	}
	if keys := catalogKeys(t, g); len(keys) != 0 {
		t.Errorf("catalogue still offers %v after off — a Workspace would keep writing it into opencode's config", keys)
	}
	if got := e.mode(context.Background()); got != engineModeOff {
		t.Errorf("mode = %q, want off", got)
	}
}

func catalogKeys(t *testing.T, g engineGateway) []string {
	t.Helper()
	var keys []string
	for _, e := range g.reg.list() {
		if e.mode(context.Background()) != engineModeOff {
			keys = append(keys, e.def.Key)
		}
	}
	return keys
}

func TestEngineAdminRefusesWhatItCannotMean(t *testing.T) {
	st := testSettingsStore(t)
	reg, _ := newAdminTestRegistry(t, &engineTestECS{}, st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}

	if code, _ := adminPut(t, a, "image", `{"mode":"maybe"}`); code != http.StatusBadRequest {
		t.Errorf("unknown mode = %d, want 400 rather than one of the three by accident", code)
	}
	// An empty body must not read as "switch it off" — that is the most expensive possible
	// misreading of a missing field.
	if code, _ := adminPut(t, a, "image", `{}`); code != http.StatusBadRequest {
		t.Errorf("empty body = %d, want 400", code)
	}
	if code, _ := adminPut(t, a, "nosuch", `{"mode":"on"}`); code != http.StatusNotFound {
		t.Errorf("unknown engine = %d, want 404", code)
	}
	// A client written against the two-valued toggle still works.
	if code, out := adminPut(t, a, "image", `{"enabled":false}`); code != http.StatusOK || out["mode"] != "off" {
		t.Errorf("legacy {enabled:false} = %d %v", code, out)
	}
}

func TestEngineAdminListsEveryEngine(t *testing.T) {
	st := testSettingsStore(t)
	reg, _ := newAdminTestRegistry(t, &engineTestECS{desired: 0}, st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}

	rec := httptest.NewRecorder()
	a.get(rec, httptest.NewRequest("GET", "/api/admin/engines", nil), store.Identity{ID: "u1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d", rec.Code)
	}
	var out struct {
		Engines []map[string]any `json:"engines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Engines) != 1 {
		t.Fatalf("engines = %v", out.Engines)
	}
	row := out.Engines[0]
	for _, k := range []string{"key", "api", "provider", "models", "mode", "enabled", "managed"} {
		if _, ok := row[k]; !ok {
			t.Errorf("row is missing %q: %v", k, row)
		}
	}
	// The default with nothing stored: a managed engine is on-demand, never "on". Leaving a
	// $1.26/hour box running because nobody picked a value is what these ADRs exist to avoid.
	if row["mode"] != engineModeOnDemand {
		t.Errorf("mode = %v with nothing stored, want ondemand", row["mode"])
	}
}

// The status fields the panel draws (ADR 0071 P1.5). The property under test is not that each
// field is present — it is that a field the CP has no answer for is ABSENT rather than filled
// with a plausible-looking value. A blank on that screen reads as "unknown"; a zero or a
// fallback date reads as fact and gets acted on against a $1.26/hour GPU.
func TestEngineAdminRowOmitsWhatItCannotAnswer(t *testing.T) {
	st := testSettingsStore(t)
	at := time.Date(2026, 9, 8, 4, 3, 0, 0, time.UTC)
	f := &fakeTTSECS{
		svc:        &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1},
		instances:  map[string]string{"arn:ci/i-08a9": "af-engines-image"},
		registered: at,
	}
	e := newTestImageEngine(t, "http://127.0.0.1:1", f)
	e.settings = st
	e.ecs.capacityProvider = "af-engines-image"
	e.demand = newEngineDemand(st, engineSettingsFor("image").demandAt, 5*time.Minute)
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	ctx := t.Context()

	// Nothing has ever asked for it: no demand mark, so no stop time. The controller judges
	// nothing on that pass either (engineReasonFirstPass), so there is genuinely no answer.
	row := a.row(ctx, e)
	if _, ok := row["stop_eta"]; ok {
		t.Errorf("stop_eta = %v with no demand mark, want it absent", row["stop_eta"])
	}
	if _, ok := row["last_demand"]; ok {
		t.Errorf("last_demand = %v before anything asked, want it absent", row["last_demand"])
	}
	// The window is always reported as the length it actually is. A client that hard-codes
	// "the last 5 minutes" is wrong the moment AF_ENGINE_IMAGE_WINDOW_SEC is set.
	if row["window_secs"] != 300 {
		t.Errorf("window_secs = %v, want 300", row["window_secs"])
	}
	// ⚠️ And how much of that window this process can speak for. The count is in memory only,
	// so without this the panel states a confident 0 for an engine somebody is using through a
	// control plane that started a minute ago.
	if _, ok := row["window_counted_secs"]; !ok {
		t.Error("window_counted_secs is missing — the rolling count is unqualified, and a CP " +
			"replaced mid-conversation reports 0 requests as if it were a measurement")
	}

	// Now somebody asks for it. Under on-demand and up, the stop time appears.
	e.demand.record(ctx, 1)
	row = a.row(ctx, e)
	if row["last_demand"] == nil {
		t.Error("last_demand is missing after a request")
	}
	eta, ok := row["stop_eta"].(string)
	if !ok {
		t.Fatalf("stop_eta = %v, want the time the controller will stop it", row["stop_eta"])
	}
	if got, err := time.Parse(time.RFC3339, eta); err != nil {
		t.Errorf("stop_eta %q is not RFC3339: %v", eta, err)
	} else if d := time.Until(got); d < 14*time.Minute || d > 15*time.Minute {
		// idle 900 s from newTestImageEngine, and the panel must reach the same moment the
		// controller does.
		t.Errorf("stop_eta is %v away, want the engine's 15-minute idle window", d)
	}

	// Pinned on, the engine does not stop by itself, so the countdown must vanish rather than
	// promise a saving that will not arrive.
	if code, _ := adminPut(t, a, "image", `{"mode":"on"}`); code != http.StatusOK {
		t.Fatalf("on failed")
	}
	if row = a.row(ctx, e); row["stop_eta"] != nil {
		t.Errorf("stop_eta = %v while pinned on, want it absent", row["stop_eta"])
	}

	// The box's own clock, which is the one an operator means. It comes from ECS's
	// registeredAt and NOT from `ec2 describe-instances`, whose unfiltered listing does not
	// contain a Managed Instances box at all (ADR 0071, P1 の実測 2).
	box, ok := row["box"].(map[string]any)
	if !ok {
		t.Fatalf("box = %v, want the container instance", row["box"])
	}
	if box["id"] != "i-08a9" || box["since"] != at.Format(time.RFC3339) {
		t.Errorf("box = %v, want i-08a9 started at %v", box, at.Format(time.RFC3339))
	}
}

// The one place the reported state must differ from what ECS says. A real cluster keeps
// reporting "running" for the minute or so after desired hits 0, and a panel that echoed that
// back would be telling the administrator the opposite of the button they just pressed —
// the same class of defect as reporting a setting as reality.
func TestEngineDisplayStateDoesNotContradictTheButton(t *testing.T) {
	for _, tc := range []struct{ raw, mode, want string }{
		{"running", engineModeOff, "stopping"},
		{"starting", engineModeOff, "stopping"},
		{"stopped", engineModeOff, "stopped"},
		{"running", engineModeOn, "running"},
		{"running", engineModeOnDemand, "running"},
	} {
		if got := engineDisplayState(tc.raw, tc.mode); got != tc.want {
			t.Errorf("engineDisplayState(%q, %q) = %q, want %q", tc.raw, tc.mode, got, tc.want)
		}
	}
}

// --- the model catalogue routes (ADR 0072) ------------------------------------

// engineModelAdminAPI is the admin API wired to a real store, so the catalogue routes exercise
// the exclusivity the store enforces rather than a stub that agrees with them.
func engineModelAdminAPI(t *testing.T) (engineAdminAPI, *engineRuntimeState, store.Store) {
	t.Helper()
	st := testSettingsStore(t)
	reg, e := newAdminTestRegistry(t, &engineTestECS{}, st)
	e.catalog = newEngineCatalog(st, "image")
	// No ssm and no activeParam: publishActiveSet is then a no-op, which is what a CP with an
	// inline table does. What is under test here is the ROW, not the publish.
	return engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}, e, st
}

func adminModel(t *testing.T, a engineAdminAPI, method, key, id, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	path := "/api/admin/engines/" + key + "/models"
	if id != "" {
		path += "/" + id
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.SetPathValue("key", key)
	r.SetPathValue("id", id)
	switch method {
	case "POST":
		a.postModel(rec, r, store.Identity{ID: "u1"})
	case "PUT":
		a.putModel(rec, r, store.Identity{ID: "u1"})
	case "DELETE":
		a.deleteModel(rec, r, store.Identity{ID: "u1"})
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// 🔴 An ingest onto an id the catalogue already holds is refused BEFORE the task starts.
//
// The row is written by PutEngineModel, which is an upsert on (role, id) — right for the seed
// and for registering a staged file, and here it would replace a WORKING row's files, licence
// and sha256 with the new ones and set enabled=false. The engine would quietly lose the
// checkpoint it starts with, minutes after a button was pressed, with nothing linking the two.
//
// And it must refuse before RunTask, not after: the alternative spends the download first and
// then either overwrites the row or strands gigabytes in the bucket with no row to reach them.
func TestEngineIngestRefusesAnIdTheCatalogueAlreadyHas(t *testing.T) {
	a, e, st := engineModelAdminAPI(t)
	ctx := context.Background()
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "sdxl-base-1.0", Kind: "checkpoint", Enabled: true, Selected: true,
		Files: []store.EngineModelFile{{S3Key: "image/checkpoints/sd_xl_base_1.0.safetensors"}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e.catalog.invalidate()
	// A deployment that CAN ingest — otherwise the earlier "no ingest task here" refusal is
	// what gets tested, and this check would never be reached.
	a.reg.ing = &engineIngester{
		def:     engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}, SecurityGroups: []string{"sg-1"}},
		cluster: "c", ecs: &fakeIngestECS{fail: "not reached"}, store: st, models: st,
	}

	rec := httptest.NewRecorder()
	body := `{"id":"sdxl-base-1.0","kind":"checkpoint","s3Key":"image/checkpoints/other.safetensors",
	  "license_accepted":true,"source":{"url":"https://example.invalid/other.safetensors",
	  "sha256":"` + strings.Repeat("a", 64) + `"}}`
	r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(body))
	r.SetPathValue("key", "image")
	a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("ingest onto an existing id = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "model_id_exists") {
		t.Errorf("the refusal does not name itself: %s", rec.Body.String())
	}
	// The seeded row is untouched: still enabled, still pointing at its own file.
	rows, _ := st.ListEngineModels(ctx, "image")
	for _, m := range rows {
		if m.ID != "sdxl-base-1.0" {
			continue
		}
		if !m.Enabled || len(m.Files) == 0 || m.Files[0].S3Key != "image/checkpoints/sd_xl_base_1.0.safetensors" {
			t.Errorf("the existing row was disturbed: enabled=%v files=%v", m.Enabled, m.Files)
		}
	}
	// A DIFFERENT id on the same engine is not what this refuses — it gets past the check and
	// fails later for its own reasons (this deployment's registry declares no ingest task).
	rec2 := httptest.NewRecorder()
	body2 := strings.Replace(body, `"id":"sdxl-base-1.0"`, `"id":"sdxl-base-1.1"`, 1)
	r2 := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(body2))
	r2.SetPathValue("key", "image")
	a.postIngest(rec2, r2, engineIngestGrant{ident: store.Identity{ID: "u1"}})
	if rec2.Code == http.StatusConflict {
		t.Errorf("a free id was refused as a duplicate: %s", rec2.Body.String())
	}
}

// The whole P0 flow through the API: register a staged file, enable it, make it the one the
// engine starts with — and see the previous one stop being it. This is the sequence behind
// ADR 0072 P0's definition of done, minus the GPU.
func TestEngineAdminModelLifecycle(t *testing.T) {
	a, e, st := engineModelAdminAPI(t)
	ctx := context.Background()
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "sdxl-base-1.0", Kind: "checkpoint", Enabled: true, Selected: true,
		Files: []store.EngineModelFile{{S3Key: "image/checkpoints/sd_xl_base_1.0.safetensors"}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e.catalog.invalidate()

	code, out := adminModel(t, a, "POST", "image", "", `{"id":"jugg","kind":"checkpoint",
	  "files":[{"s3Key":"image/checkpoints/juggernaut_xl_v9.safetensors"}],
	  "description":"a photographic SDXL fine-tune","license":"creativeml-openrail-m"}`)
	if code != http.StatusOK {
		t.Fatalf("register = %d (%v)", code, out)
	}
	rows, _ := st.ListEngineModels(ctx, "image")
	var jugg store.EngineModel
	for _, m := range rows {
		if m.ID == "jugg" {
			jugg = m
		}
	}
	// ⚠️ Registered means STAGED, not offered. "The file is in the bucket" and "members may use
	// it" are different facts, and the second is a separate press.
	if jugg.ID == "" || jugg.Enabled || jugg.Selected {
		t.Fatalf("a registered row must arrive disabled: %+v", jugg)
	}
	if jugg.License != "creativeml-openrail-m" {
		t.Errorf("licence was dropped: %+v", jugg)
	}
	// 🔴 The verdict is READ FROM the licence on this route too, not only on the ingest. A row
	// registered by hand was the one place the panel could not say "non-commercial" about a
	// model it says it about when the same file arrives through the other door.
	if jugg.CommercialUse != "yes" {
		t.Errorf("openrail-m read as commercial_use=%q", jugg.CommercialUse)
	}

	// Selecting is what an administrator presses to change the checkpoint, and it is exclusive.
	if code, out = adminModel(t, a, "PUT", "image", "jugg", `{"selected":true}`); code != http.StatusOK {
		t.Fatalf("select = %d (%v)", code, out)
	}
	rows, _ = st.ListEngineModels(ctx, "image")
	var selected []string
	for _, m := range rows {
		if m.Selected {
			selected = append(selected, m.ID)
		}
	}
	if len(selected) != 1 || selected[0] != "jugg" {
		t.Fatalf("selection after the switch = %v", selected)
	}
	// The answer carries the whole engine row, so the panel does not have to reproduce the
	// exclusivity rule client-side.
	mr, _ := out["model_rows"].([]any)
	if len(mr) != 2 {
		t.Fatalf("model_rows = %v", out["model_rows"])
	}
	if out["has_models"] != true {
		t.Errorf("has_models = %v", out["has_models"])
	}

	// Forgetting a row leaves the FILE alone — the CP has no s3:DeleteObject (review R3) — and
	// an unknown id is a 404 rather than a silent success.
	if code, _ = adminModel(t, a, "DELETE", "image", "sdxl-base-1.0", ""); code != http.StatusOK {
		t.Fatalf("forget = %d", code)
	}
	if code, _ = adminModel(t, a, "DELETE", "image", "sdxl-base-1.0", ""); code != http.StatusNotFound {
		t.Errorf("forgetting twice = %d, want 404", code)
	}
	if code, _ = adminModel(t, a, "PUT", "image", "nope", `{"enabled":true}`); code != http.StatusNotFound {
		t.Errorf("unknown model = %d, want 404", code)
	}
}

// An empty body must not be read as "switch it off", and a row with no file must not be
// created: it would be a catalogue entry the sidecar can never sync, visible as an engine that
// starts and idles.
func TestEngineAdminModelRefusesAnEmptyBody(t *testing.T) {
	a, _, _ := engineModelAdminAPI(t)
	if code, _ := adminModel(t, a, "PUT", "image", "x", `{}`); code != http.StatusBadRequest {
		t.Errorf("empty patch = %d, want 400", code)
	}
	if code, _ := adminModel(t, a, "POST", "image", "", `{"id":"x"}`); code != http.StatusBadRequest {
		t.Errorf("no files = %d, want 400", code)
	}
	if code, _ := adminModel(t, a, "POST", "image", "", `{"files":[{"s3Key":"a"}]}`); code != http.StatusBadRequest {
		t.Errorf("no id = %d, want 400", code)
	}
}

// A comfy engine picks its workflow graph from the model's family and REFUSES to guess one, so
// a catalogue row without a valid family is a row that will be registered, enabled, offered in
// generate_image's `model` enum — and then fail at generation, after a cold start. This is the
// refusal moved to where the operator can act on it (ADR 0072 decision 2, P2 実機検証).
func TestEngineAdminComfyDemandsADeclaredFamily(t *testing.T) {
	st := testSettingsStore(t)
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings = st
	e.ctrl = nil
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	e.catalog = newEngineCatalog(st, "image")

	body := func(extra string) string {
		return `{"id":"m1","kind":"checkpoint","files":[{"s3Key":"image/checkpoints/m.safetensors"}]` + extra + `}`
	}

	// No family at all — the shape the SEED writes, and the shape every row had before this.
	code, out := adminModel(t, a, "POST", "image", "", body(""))
	if code != http.StatusBadRequest {
		t.Fatalf("a checkpoint with no family = %d (%v), want 400 — it cannot generate", code, out)
	}
	msg, _ := out["error"].(map[string]any)["message"].(string)
	for _, want := range []string{"flux2-klein", "comfy"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q, so it does not say what to declare: %s", want, msg)
		}
	}

	// Civitai's display name. The ingest path used to store exactly this, which is why a row
	// could look complete in the panel and still refuse to generate.
	if code, out = adminModel(t, a, "POST", "image", "", body(`,"base_model":"SDXL 1.0"`)); code != http.StatusBadRequest {
		t.Fatalf("base_model=\"SDXL 1.0\" = %d (%v), want 400 — that is a display name, not a family", code, out)
	}

	// The declared family.
	if code, out = adminModel(t, a, "POST", "image", "", body(`,"base_model":"sdxl"`)); code != http.StatusOK {
		t.Fatalf("base_model=sdxl = %d (%v), want 200", code, out)
	}

	// ⚠️ A LoRA is exempt: its base_model is a compatibility target for a later phase, not a
	// workflow template, so an upstream spelling is legitimate and refusing it would block a
	// registration that has nothing to do with graphs.
	lora := `{"id":"w1","kind":"lora","files":[{"s3Key":"image/loras/w.safetensors"}]}`
	if code, out = adminModel(t, a, "POST", "image", "", lora); code != http.StatusOK {
		t.Fatalf("a LoRA with no family = %d (%v), want 200", code, out)
	}
}

// The panel cannot work either of these out on its own: which families this provider knows
// (there is no list anywhere else), and which existing rows predate the rule above.
func TestEngineAdminRowCarriesTheFamilyVocabulary(t *testing.T) {
	st := testSettingsStore(t)
	comfy := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	comfy.settings, comfy.ctrl = st, nil
	comfy.catalog = newEngineCatalog(st, "image")
	// A row of the shape the seed writes: complete in every way a panel can see, except that
	// ComfyUI cannot use it.
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: "seeded", Kind: "checkpoint", Enabled: true,
		Files: []store.EngineModelFile{{S3Key: "image/checkpoints/sd_xl_base_1.0.safetensors"}},
	}); err != nil {
		t.Fatal(err)
	}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, &engineRegistry{byKey: map[string]*engineRuntimeState{"image": comfy}}, st}
	row := a.row(t.Context(), comfy)
	fams, _ := row["base_models"].([]string)
	if len(fams) == 0 {
		t.Fatal("no base_models on a comfy row — the Console has nothing to build a selector from")
	}
	rows, _ := row["model_rows"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("model_rows = %d, want the seeded row", len(rows))
	}
	if rows[0]["base_model_missing"] != true {
		t.Errorf("the seeded row is not flagged: %v — it looks like a working row until a cold start says otherwise", rows[0])
	}

	// sdcpp holds one checkpoint and never reads the family, so offering a choice there would
	// ask for a decision that changes nothing.
	sd := newTestImageEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	sd.settings, sd.ctrl = st, nil
	sd.catalog = newEngineCatalog(st, "image")
	sdRow := a.row(t.Context(), sd)
	if _, ok := sdRow["base_models"]; ok {
		t.Error("sdcpp was given a family vocabulary")
	}
	sdRows, _ := sdRow["model_rows"].([]map[string]any)
	if len(sdRows) > 0 && sdRows[0]["base_model_missing"] == true {
		t.Error("sdcpp flagged a row for a family it never reads")
	}
}

// Giving an EXISTING row its family. Before this the only way was to register the whole row
// again — which lands it disabled and, for a split model, means re-typing three S3 keys to
// change one word. A seeded row is always in this state, because the seed cannot know a family.
func TestEngineAdminModelBaseModelPatch(t *testing.T) {
	st := testSettingsStore(t)
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	a := engineAdminAPI{memberAuth{&manager{store: st}}, &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}, st}

	seeded := store.EngineModel{
		Role: "image", ID: "seeded", Kind: "checkpoint", Enabled: true, Selected: true,
		Files: []store.EngineModelFile{{S3Key: "image/checkpoints/sd_xl_base_1.0.safetensors"}},
		// Everything a licence acceptance and an ingest wrote down. A read-modify-write through
		// the register route would have to carry all of it back out and in again.
		Source: "hf:stabilityai/x", LicenseName: "openrail", LicenseAcceptedBy: "u0",
	}
	if err := st.PutEngineModel(t.Context(), seeded); err != nil {
		t.Fatal(err)
	}

	// A family that names no template is refused, exactly as at registration.
	code, out := adminModel(t, a, "PUT", "image", "seeded", `{"base_model":"SDXL 1.0"}`)
	if code != http.StatusBadRequest {
		t.Fatalf(`base_model="SDXL 1.0" = %d (%v), want 400`, code, out)
	}

	if code, out = adminModel(t, a, "PUT", "image", "seeded", `{"base_model":"sdxl"}`); code != http.StatusOK {
		t.Fatalf("base_model=sdxl = %d (%v), want 200", code, out)
	}
	rows, err := st.ListEngineModels(t.Context(), "image")
	if err != nil || len(rows) != 1 {
		t.Fatalf("list = %v, %v", rows, err)
	}
	if rows[0].BaseModel != "sdxl" {
		t.Errorf("base_model = %q, want sdxl", rows[0].BaseModel)
	}
	// ⚠️ Nothing ELSE may move. The whole point of a targeted update is that a licence
	// acceptance and a source survive a one-word correction.
	if !rows[0].Enabled || !rows[0].Selected {
		t.Errorf("the row was disabled or deselected by a family change: %+v", rows[0])
	}
	if rows[0].Source != "hf:stabilityai/x" || rows[0].LicenseAcceptedBy != "u0" {
		t.Errorf("the ingest's own fields were lost: source=%q acceptedBy=%q", rows[0].Source, rows[0].LicenseAcceptedBy)
	}
	if len(rows[0].Files) != 1 || rows[0].Files[0].S3Key != "image/checkpoints/sd_xl_base_1.0.safetensors" {
		t.Errorf("the files were lost: %+v", rows[0].Files)
	}

	// The row stops being flagged, which is what the panel reads.
	mr, _ := a.row(t.Context(), e)["model_rows"].([]map[string]any)
	if len(mr) != 1 || mr[0]["base_model_missing"] == true {
		t.Errorf("still flagged after the correction: %v", mr)
	}

	// A LoRA is exempt here as it is at registration: its base_model is a compatibility target.
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: "w1", Kind: "lora",
		Files: []store.EngineModelFile{{S3Key: "image/loras/w.safetensors"}},
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()
	if code, out = adminModel(t, a, "PUT", "image", "w1", `{"base_model":"SDXL 1.0"}`); code != http.StatusOK {
		t.Fatalf("a LoRA's base_model = %d (%v), want 200", code, out)
	}
}

// The licence a hand-registered row carries decides its commercial-use verdict, and an ABSENT
// licence leaves the verdict absent too (ADR 0072 decision 10).
//
// 🔴 The empty case is the point. "Nobody recorded a licence" and "recorded, and the terms could
// not be read" are different facts: the first is what a seeded row and every row staged before
// this field existed are in, and writing `unknown` there would claim the question was asked. The
// panel draws them differently — "licence not recorded" against the licence's own name.
func TestEngineAdminModelReadsTheVerdictOffTheLicence(t *testing.T) {
	a, _, st := engineModelAdminAPI(t)
	ctx := context.Background()

	for _, tc := range []struct{ id, body, want string }{
		{"nc", `"license_name":"flux-1-dev-non-commercial-license",`, "no"},
		{"ok", `"license_name":"apache-2.0",`, "yes"},
		{"odd", `"license_name":"some-house-licence-v3",`, "unknown"},
		{"none", "", ""},
	} {
		body := `{"id":"` + tc.id + `","kind":"checkpoint",` + tc.body +
			`"files":[{"s3Key":"image/checkpoints/` + tc.id + `.safetensors"}]}`
		if code, out := adminModel(t, a, "POST", "image", "", body); code != http.StatusOK {
			t.Fatalf("register %s = %d (%v)", tc.id, code, out)
		}
		rows, _ := st.ListEngineModels(ctx, "image")
		var got string
		found := false
		for _, m := range rows {
			if m.ID == tc.id {
				got, found = m.CommercialUse, true
			}
		}
		if !found {
			t.Fatalf("%s was not registered", tc.id)
		}
		if got != tc.want {
			t.Errorf("%s: commercial_use = %q, want %q", tc.id, got, tc.want)
		}
	}
}
