package main

// The super-admin toggle for the self-hosted engines (ADR 0071). What is pinned here is the
// thing that made it worth building: the STORED SETTING is the control, and the rest of the
// system — the catalogue, the gateway, the controller — already obeys it. A toggle whose
// effect stops at the ECS desired count would leave an engine "off" that a Workspace still
// writes into opencode's config and still routes to.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
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
	a.get(rec, httptest.NewRequest("GET", "/api/admin/engines", nil), engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
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
		instances:  map[string]string{"arn:ci/i-08a9": "engine-image"},
		registered: at,
	}
	e := newTestImageEngine(t, "http://127.0.0.1:1", f)
	e.settings = st
	e.def.LaunchTemplate = "lt-image"
	e.ecs.roleAttr = engineBoxRole(e.def)
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

	// The box's own clock, which is the one an operator means. It comes from the container
	// instance's registeredAt — EC2 knows when the instance LAUNCHED, which is a different
	// moment: the boot and the agent's registration sit between them.
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

// 🔴 ADR 0072 P2 欠落 6, at the door. The ingest could say nothing about what a file IS within
// the model, so `attach` — this file joins the row that is already there — is the act that
// makes a split model buildable by ingest alone. It is also the one act allowed to name an id
// the catalogue already holds, and everything about it is decided BEFORE the download: nine
// minutes of Fargate is a bad place to learn that a flag was misspelt.
func TestEngineIngestAttachesAPartToAnExistingRow(t *testing.T) {
	st := testSettingsStore(t)
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	ctx := t.Context()
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "flux1-dev-fp8", Kind: "checkpoint", BaseModel: "flux1", Enabled: true,
		Files: []store.EngineModelFile{{Flag: "--diffusion-model", S3Key: "image/diffusion_models/flux1.safetensors"}},
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()
	reg.ing = &engineIngester{
		def:     engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}, SecurityGroups: []string{"sg-1"}},
		cluster: "c", ecs: &fakeIngestECS{}, store: st, models: st,
	}

	post := func(extra string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		body := `{"id":"flux1-dev-fp8","kind":"checkpoint","s3Key":"image/text_encoders/clip_l.safetensors",
		  "license_accepted":true,"source":{"url":"https://example.invalid/clip_l.safetensors",
		  "sha256":"` + strings.Repeat("b", 64) + `"}` + extra + `}`
		r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(body))
		r.SetPathValue("key", "image")
		a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
		return rec.Code, rec.Body.String()
	}

	// Without it, the existing id is still refused — that check is what stops an ingest from
	// upserting a working row's files, licence and enabled flag away.
	if code, body := post(`,"file_flag":"--clip_l"`); code != http.StatusConflict {
		t.Fatalf("a plain ingest onto an existing id = %d, want 409 (%s)", code, body)
	}
	// With it and no role, refused too: the unlabelled slot IS the checkpoint and a row has
	// one, so a second would leave the last writer deciding what the loader gets.
	if code, body := post(`,"attach":true`); code != http.StatusBadRequest {
		t.Fatalf("an attach with no file_flag = %d, want 400 (%s)", code, body)
	}
	// A role this provider does not read is refused rather than stored: the Agent's resolver
	// drops an unknown flag (a catalogue newer than the box has to degrade), so the file would
	// be downloaded, listed on the row and passed to nothing.
	code, body := post(`,"attach":true,"file_flag":"--clip-l"`)
	if code != http.StatusBadRequest || !strings.Contains(body, "--clip_l") {
		t.Fatalf("a misspelt flag = %d, and the refusal does not name the vocabulary: %s", code, body)
	}
	// A role the row already fills.
	if code, body := post(`,"attach":true,"file_flag":"--diffusion-model"`); code != http.StatusConflict {
		t.Fatalf("attaching a second --diffusion-model = %d, want 409 (%s)", code, body)
	}
	// An id nothing holds: an attach names its target, and a typo must not write a row.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(
		`{"id":"typo","kind":"checkpoint","s3Key":"image/text_encoders/clip_l.safetensors","attach":true,
		  "file_flag":"--clip_l","license_accepted":true,
		  "source":{"url":"https://example.invalid/c","sha256":"`+strings.Repeat("b", 64)+`"}}`))
	r.SetPathValue("key", "image")
	a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("an attach to an id nothing holds = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}

	// And the one that is right starts a job — with no family declared, because the row
	// settled that when it was created.
	if code, body := post(`,"attach":true,"file_flag":"--clip_l"`); code != http.StatusOK {
		t.Fatalf("the attach = %d, want 200 (%s)", code, body)
	}
	jobs, err := st.ListEngineIngestJobs(ctx, "image", 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %d (%v) — the refusals above must not have left one", len(jobs), err)
	}
}

// The refusal for a Civitai asset that needs an account has to happen HERE, before RunTask:
// after it, the answer is a 401 nine minutes into a Fargate task and an exit code on the panel
// (ADR 0072 P2 欠落 5).
func TestEngineIngestRefusesACivitaiAssetThatNeedsAnAccount(t *testing.T) {
	a, e, st := engineModelAdminAPI(t)
	civitaiStub(t, http.StatusUnauthorized)
	ecsAPI := &fakeIngestECS{}
	a.reg.ing = &engineIngester{
		def:     engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}, SecurityGroups: []string{"sg-1"}},
		cluster: "c", ecs: ecsAPI, store: st, models: st,
	}
	e.catalog.invalidate()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(
		`{"id":"dreamshaper-8","kind":"checkpoint","s3Key":"image/checkpoints/dreamshaper_8.safetensors",
		  "license_accepted":true,"base_model":"sdxl","source":{"civitai":{"versionId":128713}}}`))
	r.SetPathValue("key", "image")
	a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ingest of a login-walled asset = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "civitai_login_required") {
		t.Errorf("the refusal does not name itself: %s", rec.Body.String())
	}
	if len(ecsAPI.run) != 0 {
		t.Error("a task was started for a download that answers 401")
	}
	if jobs, _ := st.ListEngineIngestJobs(t.Context(), "image", 10); len(jobs) != 0 {
		t.Errorf("a job row was left behind: %+v", jobs)
	}
}

// 🔴 ADR 0072 P2 欠落 10. Declaring a family clears `base_model_missing` — and NOTHING looked
// at whether the row held the files that family's template reads, so the row came out of the
// fix looking healthier and generating just as little.
//
// Measured on af-sandbox: `flux1-dev` was one unflagged 22.2 GiB file in `image/checkpoints/`,
// and the flux1 template reads a diffusion model, two text encoders and a VAE — no answer in
// the family selector could have saved that row, and after any answer the panel said nothing.
func TestEngineAdminRowMustHoldWhatItsFamilyReads(t *testing.T) {
	st := testSettingsStore(t)
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	ctx := t.Context()
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "flux1-dev", Kind: "checkpoint",
		Files: []store.EngineModelFile{{S3Key: "image/checkpoints/flux1-dev.safetensors"}},
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()

	// Declaring the family is ACCEPTED — it is an improvement to a broken row, and refusing it
	// would leave the row both broken and unfixable.
	code, out := adminModel(t, a, "PUT", "image", "flux1-dev", `{"base_model":"flux1"}`)
	if code != http.StatusOK {
		t.Fatalf("declaring the family = %d (%v)", code, out)
	}
	// But the panel does not go quiet: the mark that replaces `base_model_missing` names the
	// files the row still needs.
	rowOf := func(out map[string]any, id string) map[string]any {
		t.Helper()
		rows, _ := out["model_rows"].([]any)
		for _, r := range rows {
			m, _ := r.(map[string]any)
			if m["id"] == id {
				return m
			}
		}
		t.Fatalf("no row %s in %v", id, out)
		return nil
	}
	mr := rowOf(out, "flux1-dev")
	if mr["base_model_missing"] == true {
		t.Error("the family did not stick")
	}
	missing, _ := mr["files_missing"].([]any)
	// All four: the one file this row does have is an unflagged checkpoint, and flux1's
	// template reads no such thing — which is why no answer in the selector could save it.
	if len(missing) != 4 {
		t.Fatalf("files_missing = %v, want every file the flux1 template reads", mr["files_missing"])
	}

	// And switching it on is refused, without a confirm: this is not a risk to weigh, it is
	// comfyBuildGraph refusing before it dials anything.
	code, out = adminModel(t, a, "PUT", "image", "flux1-dev", `{"enabled":true}`)
	if code != http.StatusConflict {
		t.Fatalf("enabling a row that cannot generate = %d (%v), want 409", code, out)
	}
	msg, _ := out["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "--t5xxl") {
		t.Errorf("the refusal does not name what is missing: %s", msg)
	}
	if code, out = adminModel(t, a, "PUT", "image", "flux1-dev", `{"selected":true}`); code != http.StatusConflict {
		t.Errorf("selecting it = %d (%v), want the same refusal — select enables too", code, out)
	}

	// 🔴 The positive control. A row that HAS what its family reads goes on, and a single-file
	// SDXL row is complete with the one unflagged file — a guard that refused everything would
	// pass every assertion above.
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "sdxl-base-1.0", Kind: "checkpoint", BaseModel: "sdxl",
		Files: []store.EngineModelFile{{S3Key: "image/checkpoints/sd_xl_base_1.0.safetensors"}},
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()
	if code, out = adminModel(t, a, "PUT", "image", "sdxl-base-1.0", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("enabling a complete row = %d (%v), want 200", code, out)
	}
	if _, marked := rowOf(out, "sdxl-base-1.0")["files_missing"]; marked {
		t.Error("a complete single-file row is marked as missing something")
	}
	// A LoRA has no template and no requirements; refusing one would block a registration that
	// has nothing to do with graphs.
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "detailed-eyes", Kind: "lora", BaseModel: "SDXL 1.0",
		Files: []store.EngineModelFile{{S3Key: "image/loras/detailed_eyes.safetensors"}},
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()
	if code, out = adminModel(t, a, "PUT", "image", "detailed-eyes", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("enabling a LoRA = %d (%v), want 200", code, out)
	}
}

// 🔴 ADR 0072 P2 欠落 6 の帰結. `?purge=1` handed the forgotten row's S3 keys straight to
// MODE=delete without asking whether anything else pointed at them — and decision 2 shares
// `text_encoders/` ON PURPOSE: SD3.5 and FLUX.1 read the same T5-XXL and CLIP-L, so one ingest
// is referenced from both rows. Measured on af-sandbox: `clip_l.safetensors` was pointed at by
// both `tmp-flux-clip-l` and `flux1-dev-fp8`, and purging the first would have broken the
// second silently, at the next cold start.
//
// Both directions are pinned, because "kept everything" and "checked nothing" look identical
// from the outside: the shared file survives, and the one nothing else uses is deleted.
func TestEngineModelPurgeSparesFilesAnotherRowUses(t *testing.T) {
	a, e, st := engineModelAdminAPI(t)
	ctx := t.Context()
	shared := "image/text_encoders/clip_l.safetensors"
	own := "image/diffusion_models/flux1-dev-fp8.safetensors"
	for _, m := range []store.EngineModel{
		{Role: "image", ID: "tmp-flux-clip-l", Kind: "checkpoint", BaseModel: "flux1",
			Files: []store.EngineModelFile{{Flag: "--clip_l", S3Key: shared}}},
		{Role: "image", ID: "flux1-dev-fp8", Kind: "checkpoint", BaseModel: "flux1", Enabled: true,
			Files: []store.EngineModelFile{
				{Flag: "--diffusion-model", S3Key: own},
				{Flag: "--clip_l", S3Key: shared},
			}},
	} {
		if err := st.PutEngineModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	e.catalog.invalidate()
	ecsAPI := &fakeIngestECS{}
	a.reg.ing = &engineIngester{
		def:     engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}, SecurityGroups: []string{"sg-1"}},
		cluster: "c", ecs: ecsAPI, store: st, models: st,
	}

	deleted := func() string {
		t.Helper()
		if len(ecsAPI.run) == 0 {
			return ""
		}
		for _, c := range ecsAPI.run[len(ecsAPI.run)-1].Overrides.ContainerOverrides {
			for _, kv := range c.Environment {
				if aws.ToString(kv.Name) == "KEY" {
					return aws.ToString(kv.Value)
				}
			}
		}
		return ""
	}
	purge := func(id string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("DELETE", "/api/admin/engines/image/models/"+id+"?purge=1", nil)
		r.SetPathValue("key", "image")
		r.SetPathValue("id", id)
		a.deleteModel(rec, r, store.Identity{ID: "u1"})
		if rec.Code != http.StatusOK {
			t.Fatalf("forget %s = %d (%s)", id, rec.Code, rec.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		note, _ := out["purge"].(string)
		return note
	}

	// The throwaway row's only file is the shared one, so NOTHING is deleted — and the answer
	// names the row that is keeping it, because "kept" with no name is not actionable.
	note := purge("tmp-flux-clip-l")
	if len(ecsAPI.run) != 0 {
		t.Fatalf("a delete task was started for a file another row uses: %q", deleted())
	}
	if !strings.Contains(note, shared) || !strings.Contains(note, "flux1-dev-fp8") {
		t.Errorf("the answer does not say what survived or why: %q", note)
	}
	e.catalog.invalidate()

	// 🔴 The positive control. With the last reference gone, the same file IS deleted — without
	// this half, a check that refused every purge would pass the test above.
	note = purge("flux1-dev-fp8")
	if len(ecsAPI.run) != 1 {
		t.Fatalf("nothing was deleted for a row nothing else references (note %q)", note)
	}
	got := strings.Fields(deleted())
	sort.Strings(got)
	if want := []string{own, shared}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("deleted %v, want both files of the last row that referenced them", got)
	}
}

// 🔴 ADR 0072 P6 R2. The admin row said what a model IS but not what it was DECLARED as: the
// files were base names, and the S3 keys, the flags, the sizes and the args were nowhere on
// the wire. Since P6 the catalogue is the only declaration in the deployment, so a row that
// was forgotten could be rebuilt only by whoever had kept a copy — and on the real deployment
// one was, from a note, because it happened to be a single-file GGUF. A FLUX.1 row is four
// keys with four flags and no note would have held it.
//
// So the row answers `file_rows` and `args` in the shape the register route reads, and this
// is the round trip that has to keep working: read the row, forget it, post the same JSON,
// get the same declaration back.
func TestEngineAdminRowCanBePostedBackToRebuildIt(t *testing.T) {
	a, e, st := engineModelAdminAPI(t)
	ctx := t.Context()
	e.def.Provider = "" // sdcpp-shaped: no family vocabulary to satisfy on the way back in
	body := `{"id":"flux1-dev-fp8","kind":"checkpoint",
	  "files":[
	    {"flag":"--diffusion-model","s3Key":"image/diffusion_models/flux1-dev-fp8.safetensors","bytes":11901933568},
	    {"flag":"--clip_l","s3Key":"image/text_encoders/clip_l.safetensors","bytes":246144152},
	    {"flag":"--t5xxl","s3Key":"image/text_encoders/t5xxl_fp8_e4m3fn.safetensors","bytes":4893934592},
	    {"flag":"--vae","s3Key":"image/vae/ae.safetensors","bytes":335304388}],
	  "args":["--offload-to-cpu","--type","q8_0"],
	  "sizes":["1024x1024","1216x832"],"description":"FLUX.1 dev, fp8, split",
	  "vram_mib":16571,"license":"other","license_name":"flux-1-dev-non-commercial-license",
	  "license_url":"https://huggingface.co/black-forest-labs/FLUX.1-dev","precision":"fp8"}`
	if code, out := adminModel(t, a, "POST", "image", "", body); code != http.StatusOK {
		t.Fatalf("register = %d (%v)", code, out)
	}
	declared := func() store.EngineModel {
		t.Helper()
		rows, err := st.ListEngineModels(ctx, "image")
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range rows {
			if m.ID == "flux1-dev-fp8" {
				// The parts a re-registration cannot and must not carry back: when the row was
				// created, and by whom the licence was accepted. Compared out rather than
				// silently ignored — see the note below on what the round trip is FOR.
				m.CreatedAt, m.UpdatedAt = "", ""
				return m
			}
		}
		t.Fatal("the row is gone")
		return store.EngineModel{}
	}
	before := declared()
	if len(before.Files) != 4 {
		t.Fatalf("the seed did not land: %+v", before.Files)
	}

	// What a super_admin can actually read back.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/admin/engines", nil)
	a.get(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
	var listed struct {
		Engines []struct {
			ModelRows []map[string]any `json:"model_rows"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("list: %v (%s)", err, rec.Body.String())
	}
	var got map[string]any
	for _, m := range listed.Engines[0].ModelRows {
		if m["id"] == "flux1-dev-fp8" {
			got = m
		}
	}
	if got == nil {
		t.Fatal("the row is not in the admin answer at all")
	}

	// Forget it — the situation this exists for — and post back exactly what was read.
	restore := func(row map[string]any) (int, store.EngineModel) {
		t.Helper()
		// 404 is fine: a refused restore leaves nothing to forget, and that is the state the
		// next attempt starts from.
		if code, out := adminModel(t, a, "DELETE", "image", "flux1-dev-fp8", ""); code != http.StatusOK &&
			code != http.StatusNotFound {
			t.Fatalf("forget = %d (%v)", code, out)
		}
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		code, out := adminModel(t, a, "POST", "image", "", string(raw))
		if code != http.StatusOK {
			return code, store.EngineModel{}
		}
		_ = out
		return code, declared()
	}
	code, after := restore(got)
	if code != http.StatusOK {
		t.Fatalf("posting the row back = %d — the whole point is that this works", code)
	}

	// Everything a DECLARATION is. Not `enabled`/`selected` (a re-registered row is disabled on
	// purpose — "the file is in the bucket" and "members may use it" are different facts) and
	// not the licence acceptance, which is a record of a human act and not a field to copy.
	if fmt.Sprint(after.Files) != fmt.Sprint(before.Files) {
		t.Errorf("files did not survive the round trip:\n before %+v\n after  %+v", before.Files, after.Files)
	}
	for _, c := range []struct{ what, before, after string }{
		{"args", fmt.Sprint(before.Args), fmt.Sprint(after.Args)},
		{"sizes", fmt.Sprint(before.Sizes), fmt.Sprint(after.Sizes)},
		{"kind", before.Kind, after.Kind},
		{"description", before.Description, after.Description},
		{"precision", before.Precision, after.Precision},
		{"licence", before.License + "/" + before.LicenseName + "/" + before.LicenseURL,
			after.License + "/" + after.LicenseName + "/" + after.LicenseURL},
		{"vram", fmt.Sprint(before.VramMiB), fmt.Sprint(after.VramMiB)},
	} {
		if c.before != c.after {
			t.Errorf("%s did not survive: %q -> %q", c.what, c.before, c.after)
		}
	}

	// 🔴 The positive controls. Without them, a test posting a body the handler ignored would
	// pass just as happily — the row would be rebuilt by whatever else is in the JSON.
	//
	// Take `file_rows` out and there is nothing to rebuild from: what is left is `files`, the
	// base names, which are not keys (two directories hold `model.safetensors`). The route
	// says so rather than registering a row with no files or inventing paths.
	without := map[string]any{}
	for k, v := range got {
		without[k] = v
	}
	delete(without, "file_rows")
	if code, lost := restore(without); code != http.StatusBadRequest {
		t.Errorf("a body with no file_rows = %d and rebuilt %+v — the files are coming from"+
			" somewhere other than the round trip", code, lost.Files)
	}
	// And with the flags stripped, the same four keys come back UNLABELLED: a row ComfyUI can
	// make nothing of, which is the failure the flags exist to prevent and which must not be
	// papered over by anything reconstructing them.
	flagless := map[string]any{}
	for k, v := range got {
		flagless[k] = v
	}
	stripped := []map[string]any{}
	for _, f := range got["file_rows"].([]any) {
		fm := f.(map[string]any)
		stripped = append(stripped, map[string]any{"s3Key": fm["s3Key"], "bytes": fm["bytes"]})
	}
	flagless["file_rows"] = stripped
	code, lost := restore(flagless)
	if code != http.StatusOK {
		t.Fatalf("a flagless body = %d, want it accepted (an unflagged row is legal — it is a"+
			" whole checkpoint)", code)
	}
	if fmt.Sprint(lost.Files) == fmt.Sprint(before.Files) {
		t.Error("dropping every flag changed nothing — the flags are not coming from the body")
	}
	for _, f := range lost.Files {
		if f.Flag != "" {
			t.Errorf("a flag survived a body that carried none: %+v", f)
		}
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

// --- the negative prompt (ADR 0072 follow-up) ---------------------------------

// The row's own negative prompt is edited in place, like base_model and for the same reason: a
// split model is four S3 keys, and re-registering all of them to change one sentence is an edit
// nobody makes twice. The empty string is a real value — "stop declaring one" — which is why the
// field is a pointer on the wire.
func TestEngineAdminEditsAModelNegativePrompt(t *testing.T) {
	a, e, st := engineModelAdminAPI(t)
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: "sdxl-base-1.0", Kind: "checkpoint", BaseModel: "sdxl", Enabled: true,
		Files: []store.EngineModelFile{{S3Key: "image/checkpoints/sd_xl_base_1.0.safetensors"}},
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()

	code, _ := adminModel(t, a, "PUT", "image", "sdxl-base-1.0", `{"negative_prompt":"extra fingers, text"}`)
	if code != http.StatusOK {
		t.Fatalf("PUT negative_prompt = %d", code)
	}
	rows, _ := st.ListEngineModels(t.Context(), "image")
	if len(rows) != 1 || rows[0].NegativePrompt != "extra fingers, text" {
		t.Fatalf("stored row = %+v, want the negative prompt", rows)
	}
	// It reaches the Agent through the catalogue, which is the only path that matters: a value
	// stored and not carried is a setting that does nothing.
	row := engineCatalogRowFor(e.def, rows, "", "")
	models, _ := row["model_rows"].([]map[string]any)
	if len(models) != 1 || models[0]["negative"] != "extra fingers, text" {
		t.Errorf("catalogue row = %v, want the negative on the wire", row["model_rows"])
	}
	// ...and back to nothing, which the Agent answers with its own default rather than with an
	// empty negative prompt.
	if code, _ := adminModel(t, a, "PUT", "image", "sdxl-base-1.0", `{"negative_prompt":""}`); code != http.StatusOK {
		t.Fatalf("clearing it = %d", code)
	}
	rows, _ = st.ListEngineModels(t.Context(), "image")
	if rows[0].NegativePrompt != "" {
		t.Errorf("negative prompt = %q, want it cleared", rows[0].NegativePrompt)
	}
}

// The engine-wide exclusion list: one text box, applied to every request on this engine. Set,
// carried on the catalogue, and cleared by the same route — there is no DELETE, because
// "excluded: nothing" is a value typed into the box it was typed out of.
func TestEngineAdminSetsTheEngineExclusionList(t *testing.T) {
	a, e, _ := engineModelAdminAPI(t)
	put := func(body string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("PUT", "/api/admin/engines/image/negative", strings.NewReader(body))
		r.SetPathValue("key", "image")
		a.putNegative(rec, r, store.Identity{ID: "u1"})
		return rec.Code, rec.Body.String()
	}
	if code, body := put(`{"negative":"explicit, gore"}`); code != http.StatusOK {
		t.Fatalf("PUT negative = %d (%s)", code, body)
	}
	if got := e.negativeAlways(t.Context()); got != "explicit, gore" {
		t.Errorf("negativeAlways = %q, want what was just set", got)
	}
	row := engineCatalogRowFor(e.def, []store.EngineModel{
		{Role: "image", ID: "sdxl-base-1.0", Enabled: true, BaseModel: "sdxl"},
	}, "", e.negativeAlways(t.Context()))
	if row["negative_always"] != "explicit, gore" {
		t.Errorf("catalogue row = %v, want the exclusion list on the wire", row)
	}
	// A paragraph is refused rather than stored: this string rides on every catalogue answer to
	// every workspace, so its size is a cost every session pays.
	if code, body := put(`{"negative":"` + strings.Repeat("x", engineNegativeMaxRunes+1) + `"}`); code != http.StatusBadRequest {
		t.Fatalf("an over-long list = %d, want 400 (%s)", code, body)
	}
	if got := e.negativeAlways(t.Context()); got != "explicit, gore" {
		t.Errorf("a refused write changed the stored value to %q", got)
	}
	// Cleared, and then the wire says nothing at all rather than an empty string.
	if code, _ := put(`{"negative":"  "}`); code != http.StatusOK {
		t.Fatal("clearing the list was refused")
	}
	if got := e.negativeAlways(t.Context()); got != "" {
		t.Errorf("negativeAlways = %q, want it cleared", got)
	}
	empty := engineCatalogRowFor(e.def, []store.EngineModel{
		{Role: "image", ID: "sdxl-base-1.0", Enabled: true, BaseModel: "sdxl"},
	}, "", e.negativeAlways(t.Context()))
	if _, ok := empty["negative_always"]; ok {
		t.Errorf("catalogue row = %v, want the key absent when nothing is excluded", empty)
	}
}

// --- the window and the VRAM measurement (ADR 0079 live run, 2026-09-13) ------

// engineWindowTestEngine is an engine with a ladder, so the VRAM guard has a rung to compare
// against, and a real store, so the edit can be read back.
func engineWindowTestEngine(t *testing.T) (engineAdminAPI, *engineRuntimeState, store.Store) {
	t.Helper()
	st := testSettingsStore(t)
	e := newClassTestEngine(t, &engineTestECS{}, &fakeFleet{}, "l4|L4|21000|g6.xlarge|4-8|15000-65536", st)
	e.catalog = newEngineCatalog(st, "image")
	return classAdminAPI(t, e, st), e, st
}

// The numbers are the ones the live run produced. A 17 GiB model with this geometry wants a
// 1024 MiB KV cache at 16384 tokens (18432 MiB in all, which the 21000 MiB rung holds) and a
// 16384 MiB one at 262144 (33792 MiB, which it does not) — the difference between an engine that
// starts and `ggml_backend_cuda_buffer_type_alloc_buffer: cudaMalloc failed: out of memory`.
const engineWindowTestBytes = 17 << 30 // 17 GiB of weights = 17408 MiB

func engineWindowTestRow(id string, enabled bool, ctxTokens int) store.EngineModel {
	return store.EngineModel{
		Role: "image", ID: id, Kind: "checkpoint", Enabled: enabled,
		Files:         []store.EngineModelFile{{S3Key: "image/checkpoints/" + id + ".gguf", Bytes: engineWindowTestBytes}},
		ContextTokens: ctxTokens, MaxOutputTokens: 4096,
		KVLayers: 64, KVHeadsKV: 2, KVKeyLen: 128, KVValueLen: 128,
		Source: "hf:vendor/" + id, LicenseName: "apache-2.0", LicenseAcceptedBy: "u0",
	}
}

// The window is editable in place, and the pair moves together. Before this the only way to
// correct a context_tokens was to register the whole row again — which lands it disabled and
// drops the licence acceptance the ingest recorded.
func TestEngineAdminEditsAModelWindow(t *testing.T) {
	a, e, st := engineWindowTestEngine(t)
	if err := st.PutEngineModel(t.Context(), engineWindowTestRow("qwen3", false, 262144)); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()

	code, out := putModelReq(t, a, "qwen3", `{"context_tokens":16384,"max_output_tokens":2048}`)
	if code != http.StatusOK {
		t.Fatalf("PUT context_tokens = %d (%v), want 200", code, out)
	}
	rows, _ := st.ListEngineModels(t.Context(), "image")
	if len(rows) != 1 || rows[0].ContextTokens != 16384 || rows[0].MaxOutputTokens != 2048 {
		t.Fatalf("stored row = %+v, want the window written", rows)
	}
	// ⚠️ Nothing else may move: that is the whole reason this is a targeted column write rather
	// than a round trip through the register route.
	if rows[0].Source != "hf:vendor/qwen3" || rows[0].LicenseAcceptedBy != "u0" || len(rows[0].Files) != 1 {
		t.Errorf("the ingest's own fields were lost: %+v", rows[0])
	}

	// Half a body is a real edit: the cap is raised against the window already declared.
	e.catalog.invalidate()
	if code, out = putModelReq(t, a, "qwen3", `{"max_output_tokens":8192}`); code != http.StatusOK {
		t.Fatalf("PUT max_output_tokens alone = %d (%v), want 200", code, out)
	}
	rows, _ = st.ListEngineModels(t.Context(), "image")
	if rows[0].ContextTokens != 16384 || rows[0].MaxOutputTokens != 8192 {
		t.Fatalf("stored row = %+v, want the window kept and the cap raised", rows[0])
	}

	// 🔴 Clearing the window clears the cap with it. The row answers a cap only alongside a
	// window (engineAdminModelRow), so one left behind is stored, invisible, and still read by
	// whatever starts the engine.
	e.catalog.invalidate()
	if code, out = putModelReq(t, a, "qwen3", `{"context_tokens":0}`); code != http.StatusOK {
		t.Fatalf("clearing the window = %d (%v)", code, out)
	}
	rows, _ = st.ListEngineModels(t.Context(), "image")
	if rows[0].ContextTokens != 0 || rows[0].MaxOutputTokens != 0 {
		t.Errorf("stored row = %+v, want both cleared", rows[0])
	}

	e.catalog.invalidate()
	if code, _ = putModelReq(t, a, "qwen3", `{"context_tokens":-1}`); code != http.StatusBadRequest {
		t.Errorf("a negative window = %d, want 400", code)
	}
	if code, _ = putModelReq(t, a, "nope", `{"context_tokens":4096}`); code != http.StatusNotFound {
		t.Errorf("a window on a row that does not exist = %d, want 404", code)
	}
}

// 🔥 The gap the live run fell into. engineVramGuard only ever ran on the way IN — enabling a
// model — so an ALREADY enabled row's context window could be raised to anything and nobody
// looked. That is the exact operation that bought an L4 and then died four minutes later.
func TestEngineAdminAsksBeforeRaisingTheWindowOfALoadedModel(t *testing.T) {
	a, e, st := engineWindowTestEngine(t)
	if err := st.PutEngineModel(t.Context(), engineWindowTestRow("qwen3", true, 16384)); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()

	code, out := putModelReq(t, a, "qwen3", `{"context_tokens":262144,"max_output_tokens":8192}`)
	if code != http.StatusConflict {
		t.Fatalf("raising an enabled row's window past the card = %d (%v), want 409", code, out)
	}
	// The refusal must leave the catalogue as it was, like the one on the way in.
	rows, _ := st.ListEngineModels(t.Context(), "image")
	if rows[0].ContextTokens != 16384 {
		t.Fatalf("the window was written by the call that refused it: %+v", rows[0])
	}
	if err, _ := out["error"].(map[string]any); err == nil || err["code"] != errCodeEngineVramConfirm {
		t.Fatalf("error = %v, want %s", out["error"], errCodeEngineVramConfirm)
	}

	// This warns, it does not forbid: quantisation and --offload-to-cpu are real (ADR 0074
	// decision 6), and the same call with the confirmation goes through.
	e.catalog.invalidate()
	if code, out = putModelReq(t, a, "qwen3", `{"context_tokens":262144,"max_output_tokens":8192,"confirm_vram":true}`); code != http.StatusOK {
		t.Fatalf("the confirmed edit = %d (%v), want 200", code, out)
	}
	rows, _ = st.ListEngineModels(t.Context(), "image")
	if rows[0].ContextTokens != 262144 {
		t.Fatalf("stored row = %+v, want the confirmed window", rows[0])
	}
	// And the way back is never asked about: a window that fits again is not a question.
	e.catalog.invalidate()
	if code, out = putModelReq(t, a, "qwen3", `{"context_tokens":16384,"max_output_tokens":4096}`); code != http.StatusOK {
		t.Fatalf("lowering it again = %d (%v), want 200", code, out)
	}

	// A row nothing would load costs nothing to get wrong, and asking about it teaches people to
	// click through the question that matters.
	if err := st.PutEngineModel(t.Context(), engineWindowTestRow("shelf", false, 16384)); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()
	if code, out = putModelReq(t, a, "shelf", `{"context_tokens":262144,"max_output_tokens":8192}`); code != http.StatusOK {
		t.Fatalf("raising a DISABLED row's window = %d (%v), want 200", code, out)
	}
}

// The operator's own measurement, written and withdrawn — and guarded forward like the window,
// because declaring 40 GB on a row the engine is already loading is the same act.
func TestEngineAdminEditsAModelVram(t *testing.T) {
	a, e, st := engineWindowTestEngine(t)
	if err := st.PutEngineModel(t.Context(), engineWindowTestRow("qwen3", true, 16384)); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()

	if code, out := putModelReq(t, a, "qwen3", `{"vram_mib":19000}`); code != http.StatusOK {
		t.Fatalf("PUT vram_mib = %d (%v), want 200", code, out)
	}
	rows, _ := st.ListEngineModels(t.Context(), "image")
	if rows[0].VramMiB != 19000 {
		t.Fatalf("stored row = %+v, want the measurement", rows[0])
	}
	// A declared number OUTRANKS the floor, so the answer the panel reads changes with it.
	if need, src := engineModelVramNeed(rows[0]); need != 19000 || src != engineVramDeclared {
		t.Errorf("need = %d/%s, want 19000/declared", need, src)
	}

	e.catalog.invalidate()
	code, out := putModelReq(t, a, "qwen3", `{"vram_mib":40000}`)
	if code != http.StatusConflict {
		t.Fatalf("declaring more than the card holds on a loaded row = %d (%v), want 409", code, out)
	}
	rows, _ = st.ListEngineModels(t.Context(), "image")
	if rows[0].VramMiB != 19000 {
		t.Fatalf("the refused call wrote anyway: %+v", rows[0])
	}
	e.catalog.invalidate()
	if code, out = putModelReq(t, a, "qwen3", `{"vram_mib":40000,"confirm_vram":true}`); code != http.StatusOK {
		t.Fatalf("the confirmed measurement = %d (%v), want 200", code, out)
	}

	// 0 WITHDRAWS it and puts the row back on the floor its files imply — which here is the
	// weights plus the KV cache the declared window needs.
	e.catalog.invalidate()
	if code, out = putModelReq(t, a, "qwen3", `{"vram_mib":0}`); code != http.StatusOK {
		t.Fatalf("withdrawing the measurement = %d (%v), want 200", code, out)
	}
	rows, _ = st.ListEngineModels(t.Context(), "image")
	if rows[0].VramMiB != 0 {
		t.Fatalf("stored row = %+v, want the measurement withdrawn", rows[0])
	}
	if need, src := engineModelVramNeed(rows[0]); src != engineVramWeightsKV || need != 17408+1024 {
		t.Errorf("need = %d/%s, want 18432/weights_kv", need, src)
	}
	e.catalog.invalidate()
	if code, _ = putModelReq(t, a, "qwen3", `{"vram_mib":-1}`); code != http.StatusBadRequest {
		t.Errorf("a negative measurement = %d, want 400", code)
	}
}

// A borrowed catalogue is a MIRROR of the far deployment's (ADR 0079 decision 7), so the new
// fields are refused with the rest — before the body is even read.
func TestEngineAdminRefusesAWindowEditOnABorrowedRow(t *testing.T) {
	st := testSettingsStore(t)
	rem := &engineRemote{key: "image", tokens: map[string]engineRemoteToken{}}
	e := &engineRuntimeState{
		def: engineDef{Key: "image", API: engineAPIImages, Provider: "comfy",
			URL: "https://af.example.invalid", Lifecycle: engineLifecycleRemote},
		settings: st,
		catalog:  newEngineCatalog(nil, "image"),
		remote:   rem,
	}
	e.catalog.source = rem.catalogSource()
	a := engineAdminAPI{memberAuth{&manager{store: st}}, &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}, st}

	for _, body := range []string{`{"context_tokens":4096,"max_output_tokens":1024}`, `{"vram_mib":19000}`} {
		code, out := putModelReq(t, a, "m1", body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s on a borrowed engine = %d (%v), want 400", body, code, out)
		}
		err, _ := out["error"].(map[string]any)
		if err == nil || err["code"] != errCodeEngineNotOurs {
			t.Errorf("%s code = %v, want %s", body, out["error"], errCodeEngineNotOurs)
		}
	}
}

// 🔴 The other half of ADR 0072 P2 欠落 6, and the one the code confessed to in its own refusal:
// `engineAttachAllowed` answers "forget the row, or take this in as its own" to a file whose
// flag is already filled. Wanting a t5xxl at another quantisation is not a reason to lose a
// row's licence acceptance, family, params, enabled state and provenance — and the row's own
// CHECKPOINT could not be changed by any ingest at all, because an attach requires a flag.
//
// Everything here is decided before RunTask, for the same reason the attach gate is: nine
// minutes of Fargate is a bad place to learn that a slot was empty.
func TestEngineIngestReplacesAFileOfAnExistingRow(t *testing.T) {
	st := testSettingsStore(t)
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	ctx := t.Context()
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "flux1-dev-fp8", Kind: "checkpoint", BaseModel: "flux1", Enabled: true,
		Files: []store.EngineModelFile{
			{Flag: "--diffusion-model", S3Key: "image/diffusion_models/flux1.safetensors"},
			{Flag: "--t5xxl", S3Key: "image/text_encoders/t5xxl_fp16.safetensors"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	// A row whose one file is UNLABELLED — the whole checkpoint, the slot no attach can reach.
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "sdxl-base-1.0", Kind: "checkpoint", BaseModel: "sdxl",
		Files: []store.EngineModelFile{{S3Key: "image/checkpoints/sd_xl_base_1.0.safetensors"}},
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()
	reg.ing = &engineIngester{
		def:     engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}, SecurityGroups: []string{"sg-1"}},
		cluster: "c", ecs: &fakeIngestECS{}, store: st, models: st,
	}

	post := func(id, extra string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		body := `{"id":"` + id + `","kind":"checkpoint","s3Key":"image/text_encoders/t5xxl_fp8.safetensors",
		  "license_accepted":true,"source":{"url":"https://example.invalid/t5xxl_fp8.safetensors",
		  "sha256":"` + strings.Repeat("c", 64) + `"}` + extra + `}`
		r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(body))
		r.SetPathValue("key", "image")
		a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
		return rec.Code, rec.Body.String()
	}

	// A slot the row does not fill is NOT quietly created: that is the other act, and turning one
	// into the other is how a mistyped flag becomes a row with two checkpoints. The refusal names
	// the act that was meant.
	code, body := post("flux1-dev-fp8", `,"replace":true,"file_flag":"--clip_l"`)
	if code != http.StatusBadRequest || !strings.Contains(body, "as a part") {
		t.Fatalf("replacing an empty slot = %d, and the refusal does not point at the other act: %s", code, body)
	}
	// Both at once is not a request the CP may pick a winner for: their preconditions are
	// opposite — one needs the slot free, the other needs it filled.
	if code, body := post("flux1-dev-fp8", `,"replace":true,"attach":true,"file_flag":"--t5xxl"`); code != http.StatusBadRequest {
		t.Fatalf("attach and replace together = %d, want 400 (%s)", code, body)
	}
	// An id nothing holds, same as the attach gate: a typo must not write a row.
	if code, body := post("typo", `,"replace":true,"file_flag":"--t5xxl"`); code != http.StatusNotFound {
		t.Fatalf("replacing in an id nothing holds = %d, want 404 (%s)", code, body)
	}
	// 🔴 The asymmetry that is the whole point. An attach with no flag is refused — the
	// unlabelled slot is THE checkpoint and a row has one — and a replace with no flag is the
	// only way that file has ever been changeable.
	if code, body := post("sdxl-base-1.0", `,"attach":true`); code != http.StatusBadRequest {
		t.Fatalf("an attach with no file_flag = %d, want 400 (%s)", code, body)
	}
	if code, body := post("sdxl-base-1.0", `,"replace":true`); code != http.StatusOK {
		t.Fatalf("replacing a row's own checkpoint = %d, want 200 (%s)", code, body)
	}
	// And the flagged one, with no family declared: the row settled that when it was created.
	if code, body := post("flux1-dev-fp8", `,"replace":true,"file_flag":"--t5xxl"`); code != http.StatusOK {
		t.Fatalf("the replace = %d, want 200 (%s)", code, body)
	}
	jobs, err := st.ListEngineIngestJobs(ctx, "image", 10)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("jobs = %d (%v) — the refusals above must not have left one", len(jobs), err)
	}
	// Nothing about either row changed at the door: the swap happens when the download finishes.
	rows, _ := st.ListEngineModels(ctx, "image")
	for _, m := range rows {
		if m.ID == "flux1-dev-fp8" && (!m.Enabled || len(m.Files) != 2) {
			t.Errorf("the row was touched before the download: enabled=%v files=%+v", m.Enabled, m.Files)
		}
	}
}

// --- registering a key the ingest history still holds -------------------------

// 🔴 `POST /models` is how a file that is ALREADY in the bucket is registered, and the panel now
// offers it on a finished ingest job's key — the bytes are staged, so "take it in again" would
// be an ingest of something that is already here (forgetting a row without ?purge=1 leaves the
// object: measured on the dev deployment 2026-09-09, 491 MB outlived its row).
//
// What this pins is the field that makes the rebuilt row equal to the one the ingest wrote:
// WHERE it came from. Without it the round trip loses exactly what migration
// 0060_engine_model_source.sql exists to keep — an id is short and readable and does not say
// which vendor published the model, and once the job row is gone nothing else does.
func TestRegisteringAModelRecordsWhereItCameFrom(t *testing.T) {
	a, _, st := engineModelAdminAPI(t)
	code, out := adminModel(t, a, "POST", "image", "", `{
	  "id":"sd35-medium","kind":"checkpoint","base_model":"sd35",
	  "files":[{"s3Key":"image/checkpoints/sd3.5_medium.safetensors"}],
	  "source":"hf:stabilityai/stable-diffusion-3.5-medium/sd3.5_medium.safetensors",
	  "license_name":"stabilityai-ai-community"}`)
	if code != http.StatusOK {
		t.Fatalf("register = %d (%v)", code, out)
	}
	rows, err := st.ListEngineModels(t.Context(), "image")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v (err %v)", rows, err)
	}
	m := rows[0]
	if m.Source != "hf:stabilityai/stable-diffusion-3.5-medium/sd3.5_medium.safetensors" {
		t.Errorf("source = %q — the rebuilt row lost which repository it came from", m.Source)
	}
	// 🔴 And the ACCEPTANCE is not rebuilt with it. It is the record of a human act on the row
	// the ingest created (ADR 0072 decision 10); the person registering this key may be
	// somebody else entirely, and writing their id here would forge a signature. The licence
	// TEXT is theirs to state and the verdict is still read OFF it, exactly as the ingest does.
	if m.LicenseAcceptedBy != "" || m.LicenseAcceptedAt != "" || m.LicenseAcceptedTenant != "" {
		t.Errorf("a registration invented a licence acceptance: by=%q at=%q tenant=%q",
			m.LicenseAcceptedBy, m.LicenseAcceptedAt, m.LicenseAcceptedTenant)
	}
	if m.CommercialUse == "" {
		t.Error("the commercial-use verdict was not read off the licence, so this door loses the mark the other one records")
	}
	if m.Enabled {
		t.Error("a registered row arrived enabled")
	}
	// A row registered with no source says nothing rather than something: the seed and every
	// row that predates the column are in exactly that state.
	if code, out := adminModel(t, a, "POST", "image", "", `{
	  "id":"sdxl-base-1.0","kind":"checkpoint","base_model":"sdxl",
	  "files":[{"s3Key":"image/checkpoints/sd_xl_base_1.0.safetensors"}]}`); code != http.StatusOK {
		t.Fatalf("register without a source = %d (%v)", code, out)
	}
	rows, _ = st.ListEngineModels(t.Context(), "image")
	for _, m := range rows {
		if m.ID == "sdxl-base-1.0" && m.Source != "" {
			t.Errorf("a row registered with no source claims %q", m.Source)
		}
	}
}

// A borrowed role has no bucket and no active set on this side (ADR 0079), so a replace here
// would stage bytes for an engine that will never read them — and the row it would rewrite is
// the far deployment's to change.
//
// 🔴 The control is an EXTERNAL row, not a managed one. Paired with a managed row this passes
// for an implementation that refuses every unmanaged engine, which is exactly the LAN ComfyUI
// of ADR 0076: `managed:false` with a lifecycle that is not `remote`, and it takes models in
// like any other.
func TestEngineIngestReplaceIsRefusedForABorrowedRoleAndAllowedForAnExternalOne(t *testing.T) {
	body := func() string {
		return `{"id":"m1","kind":"gguf","s3Key":"llm/new.gguf","replace":true,"license_accepted":true,
		  "source":{"url":"https://example.invalid/new.gguf","sha256":"` + strings.Repeat("d", 64) + `"}}`
	}
	call := func(t *testing.T, e *engineRuntimeState, st store.Store) (int, string) {
		t.Helper()
		reg := &engineRegistry{byKey: map[string]*engineRuntimeState{e.def.Key: e}}
		a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
		reg.ing = &engineIngester{
			def:     engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}, SecurityGroups: []string{"sg-1"}},
			cluster: "c", ecs: &fakeIngestECS{}, store: st, models: st,
		}
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/admin/engines/"+e.def.Key+"/ingest", strings.NewReader(body()))
		r.SetPathValue("key", e.def.Key)
		a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
		return rec.Code, rec.Body.String()
	}

	// Borrowed: refused, and it says which deployment owns the catalogue.
	st := testSettingsStore(t)
	borrowed := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	borrowed.settings, borrowed.ctrl = st, nil
	borrowed.def.Key = "image"
	borrowed.def.Lifecycle = engineLifecycleRemote
	borrowed.def.URL = "https://far.invalid"
	borrowed.catalog = newEngineCatalog(st, "image")
	if code, msg := call(t, borrowed, st); code != http.StatusBadRequest || !strings.Contains(msg, errCodeEngineNotOurs) {
		t.Fatalf("replace on a borrowed role = %d (%s), want 400 engine_not_ours", code, msg)
	}

	// 🔴 The pair. An external engine — the LAN ComfyUI an operator runs on their own box — is
	// also `managed:false`, and it stages files in this deployment's bucket like any other. It
	// must get past this gate; what it fails on next is its own business (no such row).
	st2 := testSettingsStore(t)
	external := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	external.settings, external.ctrl = st2, nil
	external.def.Key = "image"
	external.def.Lifecycle = engineLifecycleExternal
	external.catalog = newEngineCatalog(st2, "image")
	if code, msg := call(t, external, st2); strings.Contains(msg, errCodeEngineNotOurs) {
		t.Fatalf("an external engine was refused as borrowed = %d (%s)", code, msg)
	}
}

// The panel builds that registration from a job row, so the job row has to say what the file
// was taken in AS. Both halves are silent failures if guessed: a LoRA registered as a checkpoint
// is a row the engine can be told to start with and cannot load, and a text encoder registered
// with no flag becomes the checkpoint of its own row.
func TestIngestJobRowSaysWhatTheFileWasTakenInAs(t *testing.T) {
	job := store.EngineIngestJob{
		ID: "j1", Role: "image", ModelID: "clip-l", S3Key: "image/text_encoders/clip_l.safetensors",
		State: store.EngineIngestDone,
	}
	spec, _ := json.Marshal(engineIngestRequest{Kind: "checkpoint", FileFlag: "--clip_l"})
	job.Spec = string(spec)
	row := engineIngestJobRow(job)
	if row["kind"] != "checkpoint" || row["file_flag"] != "--clip_l" {
		t.Errorf("row = %v, want the kind and the file's role", row)
	}
	// 🔴 And nothing ELSE of the spec reaches the wire. It carries the licence acceptance and
	// the resolved download URL, neither of which a job list is the place for.
	for _, k := range []string{"spec", "AcceptedBy", "accepted_by", "Resolved", "license"} {
		if _, ok := row[k]; ok {
			t.Errorf("the job row leaked %q: %v", k, row)
		}
	}
	// A job taken in before these fields existed decodes with them empty, and says nothing
	// rather than guessing — the form then opens with one fewer answer filled in.
	job.Spec = `{"Role":"image","ModelID":"clip-l"}`
	if row := engineIngestJobRow(job); row["kind"] != nil || row["file_flag"] != nil {
		t.Errorf("an old job invented an answer: %v", row)
	}
	job.Spec = "not json at all"
	if row := engineIngestJobRow(job); row["kind"] != nil || row["file_flag"] != nil {
		t.Errorf("an unreadable spec invented an answer: %v", row)
	}
}
