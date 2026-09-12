package main

// The GPU an engine buys (ADR 0074, as ADR 0077 decision 8 left it). What is pinned here is what
// a reader of the panel cannot check for themselves:
//
//   - a deployment that declares no ladder never calls AWS about a box. That is decision 3, and
//     it is the only reason it is safe to ship this without an IAM change reaching every
//     existing deployment first;
//   - the demand a class is compared against is a MAXIMUM (one model is in VRAM at a time), and
//     "nobody measured it" never reads as "it fits";
//   - a stored choice is IN FORCE the moment it is stored. ADR 0074 had to write the rung into a
//     capacity provider and could fail half-way, leaving the picker showing a card nothing held;
//     a rung is now the type set of the next purchase, so there is no second copy to keep in
//     step and `class_apply_error` has nothing left to report.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestParseEngineClasses(t *testing.T) {
	list := parseEngineClasses(
		"l4|L4 24GB|21000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26;" +
			"l40s|L40S 48GB|44000|g6e.xlarge|4|30000-65536;" +
			"broken|no types|1000||4-8|1000-2000;" +
			"nonum|bad vram|lots|g6.xlarge|4-8|1000-2000;" +
			"l4|duplicate|21000|g6.xlarge|4-8|15000-65536")
	if len(list) != 2 {
		t.Fatalf("parsed %d rungs, want 2 (the malformed and the duplicate are dropped): %+v", len(list), list)
	}
	l4 := list[0]
	if l4.ID != "l4" || l4.VramMiB != 21000 || l4.UsdPerHour != 1.26 {
		t.Errorf("first rung = %+v", l4)
	}
	if got := strings.Join(l4.Types, ","); got != "g6.xlarge,g5.xlarge" {
		t.Errorf("types = %q", got)
	}
	if l4.VCpuMin != 4 || l4.VCpuMax != 8 || l4.MemMinMiB != 15000 || l4.MemMaxMiB != 65536 {
		t.Errorf("bounds = %+v", l4)
	}
	// A single number is both ends, and an absent price is 0 — never a made-up figure, because
	// the panel prints a price only when the operator declared one.
	if list[1].VCpuMin != 4 || list[1].VCpuMax != 4 {
		t.Errorf("single-number vcpu range = %d-%d", list[1].VCpuMin, list[1].VCpuMax)
	}
	if list[1].UsdPerHour != 0 {
		t.Errorf("undeclared price = %v, want 0", list[1].UsdPerHour)
	}
	if parseEngineClasses("") != nil {
		t.Error("an empty ladder must parse to nothing at all")
	}
}

// A price that does not parse must not cost the rung: it is a label, and dropping the hardware
// to protect a number nothing computes with is the wrong trade.
func TestParseEngineClassesKeepsTheRungWhenOnlyThePriceIsWrong(t *testing.T) {
	list := parseEngineClasses("l4|L4|21000|g6.xlarge|4-8|15000-65536|free")
	if len(list) != 1 || list[0].UsdPerHour != 0 {
		t.Fatalf("got %+v, want the rung kept with no price", list)
	}
}

func TestEngineClassByID(t *testing.T) {
	list := parseEngineClasses("a|A|1|t|1-2|1-2;b|B|2|t|1-2|1-2")
	// "" is the default and the default is the FIRST rung — that is what lets the stack declare
	// one without a second parameter naming it.
	if c, ok := engineClassByID(list, ""); !ok || c.ID != "a" {
		t.Errorf("default = %+v %v", c, ok)
	}
	if c, ok := engineClassByID(list, "b"); !ok || c.ID != "b" {
		t.Errorf("by id = %+v %v", c, ok)
	}
	if _, ok := engineClassByID(list, "zzz"); ok {
		t.Error("an id nobody declared must not resolve")
	}
	if _, ok := engineClassByID(nil, ""); ok {
		t.Error("no ladder must answer no rung")
	}
}

func TestEngineVramDemandIsTheMaximumAndKnowsHowWellItKnows(t *testing.T) {
	mib := func(n int64) []store.EngineModelFile {
		return []store.EngineModelFile{{S3Key: "k", Bytes: n * 1024 * 1024}}
	}
	t.Run("declared beats the file size, and the largest wins", func(t *testing.T) {
		need, source, id := engineVramDemand([]store.EngineModel{
			{ID: "small", Enabled: true, VramMiB: 7000},
			{ID: "big", Enabled: true, VramMiB: 21000},
			{ID: "off", Enabled: false, VramMiB: 40000},
		})
		if need != 21000 || source != engineVramDeclared || id != "big" {
			t.Fatalf("got %d %s %s, want the largest ENABLED model's own measurement", need, source, id)
		}
	})
	t.Run("a sum would be wrong", func(t *testing.T) {
		// Five 8 GB models are not a 40 GB demand: the router holds one at a time
		// (`--models-max 1`) and sd-server holds one checkpoint.
		rows := make([]store.EngineModel, 5)
		for i := range rows {
			rows[i] = store.EngineModel{ID: "m", Enabled: true, VramMiB: 8000}
		}
		if need, _, _ := engineVramDemand(rows); need != 8000 {
			t.Fatalf("need = %d, want 8000", need)
		}
	})
	t.Run("file bytes are a floor, and say so", func(t *testing.T) {
		need, source, _ := engineVramDemand([]store.EngineModel{
			{ID: "gguf", Enabled: true, Files: mib(18000)},
		})
		if need != 18000 || source != engineVramFloor {
			t.Fatalf("got %d %s, want a floor of 18000", need, source)
		}
	})
	t.Run("one unmeasured model weakens the whole answer", func(t *testing.T) {
		// The largest KNOWN demand is still reported — a panel needs a number — but it is a
		// floor, because the model nobody measured could be bigger.
		need, source, _ := engineVramDemand([]store.EngineModel{
			{ID: "known", Enabled: true, VramMiB: 9000},
			{ID: "mystery", Enabled: true},
		})
		if need != 9000 || source != engineVramFloor {
			t.Fatalf("got %d %s, want 9000 as a floor", need, source)
		}
	})
	t.Run("nothing declared is unknown, not zero", func(t *testing.T) {
		_, source, _ := engineVramDemand([]store.EngineModel{{ID: "m", Enabled: true}})
		if source != engineVramUnknown {
			t.Fatalf("source = %s, want unknown", source)
		}
	})
	t.Run("a LoRA is not what has to fit", func(t *testing.T) {
		_, source, _ := engineVramDemand([]store.EngineModel{
			{ID: "l", Enabled: true, Kind: engineModelKindLora, VramMiB: 99000},
		})
		if source != engineVramUnknown {
			t.Fatalf("source = %s: a LoRA is an accessory, not the model in VRAM", source)
		}
	})
}

// The unknown must not be turned into a refusal: a deployment where nobody has measured
// anything would ask for a confirmation on every single model, which teaches people to click
// through the one that matters.
func TestEngineClassFits(t *testing.T) {
	c := engineClass{VramMiB: 21000}
	for _, tc := range []struct {
		need int
		want bool
	}{{0, true}, {20000, true}, {21000, true}, {21001, false}} {
		if got := engineClassFits(c, tc.need); got != tc.want {
			t.Errorf("fits(%d) = %v", tc.need, got)
		}
	}
	if !engineClassFits(engineClass{}, 99999) {
		t.Error("a rung that declares no VRAM cannot say anything about fit")
	}
}

// --- the start gate ------------------------------------------------------------------

func newClassTestEngine(t *testing.T, api engineECSAPI, fleet engineFleetAPI, ladder string, st store.Store) *engineRuntimeState {
	t.Helper()
	e := newTestImageEngine(t, "http://127.0.0.1:1", api)
	e.def.LaunchTemplate = "lt-image"
	e.def.Classes = ladder
	e.ecs.roleAttr = engineBoxRole(e.def)
	e.classes = parseEngineOffers("image", ladder)
	e.cluster = "cluster"
	e.settings = st
	e.audit = st
	e.offers = newEngineOfferRun(engineOfferBudgetDefault)
	e.ctrl = nil
	if len(e.classes) > 0 {
		e.fleet = newEngineFleet(fleet, "image", "cluster", "lt-image", []string{"subnet-a"})
	}
	return e
}

// Decision 3, and the reason this can ship before any deployment has the IAM grant: with no
// ladder there is no path from the start path to EC2 at all.
func TestStartGateIsInertWithoutALadder(t *testing.T) {
	f := &fakeFleet{}
	e := newClassTestEngine(t, &engineTestECS{}, f, "", testSettingsStore(t))
	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("gate refused a start with no ladder (%s)", why)
	}
	if f.calls() != 0 {
		t.Fatalf("EC2 was called %d time(s) for a deployment that declares no classes", f.calls())
	}
}

// 🔴 The gate chooses; it does not buy, and it does not touch AWS at all any more (ADR 0077
// decision 8 — the rung IS the request, so there is nothing to apply in advance). What it leaves
// behind is the candidate list the walk will use, in declaration order, with the stored pin at
// the head of it.
func TestStartGateChoosesTheStoredRungAndBuysNothing(t *testing.T) {
	st := testSettingsStore(t)
	f := &fakeFleet{}
	e := newClassTestEngine(t, &engineTestECS{}, f,
		"l4|L4|21000|g6.xlarge|4-8|15000-65536;l40s|L40S|44000|g6e.xlarge|4-8|30000-65536", st)
	if err := st.SetSetting(t.Context(), engineClassSettingKey("image"), "l40s"); err != nil {
		t.Fatal(err)
	}
	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("gate refused (%s)", why)
	}
	if cur, ok := e.offers.current(); !ok || cur.ID != "l40s" {
		t.Fatalf("the walk starts on %+v, want the stored choice to win over the stack's first rung", cur)
	}
	if f.calls() != 0 {
		t.Fatalf("the gate made %d EC2 call(s); buying is the start's, not the gate's", f.calls())
	}
}

// Decision 4: a box of the PREVIOUS rung is still registered, so a start now either exceeds the
// vCPU quota or lands the task straight back on the old card.
func TestStartGateWaitsForTheOldBoxToLeave(t *testing.T) {
	st := testSettingsStore(t)
	api := &engineTestECS{instance: "engine-image", instanceType: "g6.xlarge"}
	e := newClassTestEngine(t, api, &fakeFleet{}, "l40s|L40S|44000|g6e.xlarge|4-8|30000-65536", st)
	ok, why := e.startGate(t.Context())
	if ok || why != engineReasonClassSwapWait {
		t.Fatalf("gate = %v %q, want the start held back", ok, why)
	}
	// The box goes away; the same gate now lets the start through.
	api.instance = ""
	e.ecs.invalidateBox()
	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("gate still refusing after the box left (%s)", why)
	}
}

// A box of the SELECTED rung is not something to wait for — that is the ordinary
// stopped-but-still-draining case ADR 0071 decision 7 calls the cheap start.
func TestStartGateDoesNotWaitForABoxOfTheSameClass(t *testing.T) {
	st := testSettingsStore(t)
	api := &engineTestECS{instance: "engine-image", instanceType: "g6.xlarge"}
	e := newClassTestEngine(t, api, &fakeFleet{}, "l4|L4|21000|g6.xlarge,g5.xlarge|4-8|15000-65536", st)
	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("gate = %v %q, want the start allowed", ok, why)
	}
}

// 🔴 A start already in flight is not re-judged (ADR 0077 decision 1). The offer is chosen, the
// box is bought and the desired count is waiting for it to register; beginning the walk again
// would choose an offer for the second time and buy a second GPU — which is what the three
// hardware rounds of ADR 0075 kept producing.
func TestStartGateDoesNotReopenAWalkThatHasAlreadyBought(t *testing.T) {
	st := testSettingsStore(t)
	f := &fakeFleet{instance: "i-1"}
	e := newClassTestEngine(t, &engineTestECS{}, f,
		"l4|L4|21000|g6.xlarge|4-8|15000-65536;l40s|L40S|44000|g6e.xlarge|4-8|30000-65536", st)
	if ok, _ := e.startGate(t.Context()); !ok {
		t.Fatal("gate refused the first start")
	}
	if err := e.startOnOffer(t.Context()); err == nil {
		t.Fatal("the start did not report that it is waiting for the box")
	}
	bought := len(f.creates)
	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("the gate refused while a box of this very start was registering (%s)", why)
	}
	if err := e.startOnOffer(t.Context()); err == nil {
		t.Fatal("the second start reported success while the box is still registering")
	}
	if len(f.creates) != bought {
		t.Fatalf("%d CreateFleet call(s) for one start, want %d — every extra one is a GPU", len(f.creates), bought)
	}
}

// --- the admin routes ----------------------------------------------------------------

func classAdminAPI(t *testing.T, e *engineRuntimeState, st store.Store) engineAdminAPI {
	t.Helper()
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	return engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
}

func putClass(t *testing.T, a engineAdminAPI, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/api/admin/engines/image/class", strings.NewReader(body))
	r.SetPathValue("key", "image")
	a.putClass(rec, r, store.Identity{ID: "u1"})
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestPutClassStoresTheChoice(t *testing.T) {
	st := testSettingsStore(t)
	f := &fakeFleet{}
	api := &engineTestECS{instance: "engine-image", instanceType: "g6.xlarge"}
	e := newClassTestEngine(t, api, f, "l4|L4|21000|g6.xlarge|4-8|15000-65536;l40s|L40S 48GB|44000|g6e.xlarge|4-8|30000-65536", st)
	a := classAdminAPI(t, e, st)

	code, out := putClass(t, a, `{"class":"l40s"}`)
	if code != http.StatusOK {
		t.Fatalf("put = %d (%v)", code, out)
	}
	if v, _ := st.GetSetting(t.Context(), engineClassSettingKey("image")); v != "l40s" {
		t.Fatalf("stored class = %q — the stored setting is what wins over the stack", v)
	}
	// 🔴 Nothing was bought and nothing was declared to AWS (ADR 0077 decision 8): the stored id
	// IS the declaration, and it reaches hardware at the next purchase. Under ADR 0074 this line
	// was a `UpdateCapacityProvider`, and a failure of it left the picker showing a card nothing
	// held.
	if f.writes() != 0 {
		t.Errorf("%d EC2 write(s) for a stored choice, want none", f.writes())
	}
	// A box of the old rung is up, so the change has not reached anything yet and the panel has
	// to say so rather than reporting success.
	if out["class_replace_pending"] != true {
		t.Errorf("class_replace_pending = %v while a %s box is running", out["class_replace_pending"], api.instanceType)
	}
	if cls, _ := out["class"].(map[string]any); cls == nil || cls["id"] != "l40s" {
		t.Errorf("class in the answer = %v", out["class"])
	}
	if out["class_is_default"] != false {
		t.Errorf("class_is_default = %v — running on a non-default rung must be visible", out["class_is_default"])
	}
	// A rung nobody declared does not exist. This is also what keeps arbitrary instance types
	// from reaching a CreateFleet override.
	if code, _ = putClass(t, a, `{"class":"h100"}`); code != http.StatusBadRequest {
		t.Errorf("undeclared class = %d, want 400", code)
	}
	if v, _ := st.GetSetting(t.Context(), engineClassSettingKey("image")); v != "l40s" {
		t.Errorf("a refused class changed the stored one to %q", v)
	}
}

func TestPutClassIsNotFoundWithoutALadder(t *testing.T) {
	st := testSettingsStore(t)
	e := newClassTestEngine(t, &engineTestECS{}, &fakeFleet{}, "", st)
	code, _ := putClass(t, classAdminAPI(t, e, st), `{"class":"l4"}`)
	if code != http.StatusNotFound {
		t.Fatalf("put on a deployment with no ladder = %d, want 404", code)
	}
}

// Decision 6, at the one moment a human is choosing: enabling a model that does not fit the
// chosen card asks a question, and the same call with confirm_vram goes through.
func TestPutModelAsksBeforeEnablingAModelThatDoesNotFit(t *testing.T) {
	st := testSettingsStore(t)
	e := newClassTestEngine(t, &engineTestECS{}, &fakeFleet{}, "l4|L4|21000|g6.xlarge|4-8|15000-65536", st)
	e.catalog = newEngineCatalog(st, "image")
	ctx := t.Context()
	var models store.EngineModelStore = st
	if err := models.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "flux-dev", Kind: "checkpoint", VramMiB: 40000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := models.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "sdxl", Kind: "checkpoint", VramMiB: 7379,
	}); err != nil {
		t.Fatal(err)
	}
	a := classAdminAPI(t, e, st)

	code, out := putModelReq(t, a, "flux-dev", `{"enabled":true}`)
	if code != http.StatusConflict {
		t.Fatalf("enable = %d (%v), want a question", code, out)
	}
	// The row must be untouched: a refusal that had already written would leave the catalogue
	// saying yes while the answer said no.
	rows, _ := models.ListEngineModels(ctx, "image")
	for _, m := range rows {
		if m.ID == "flux-dev" && m.Enabled {
			t.Fatal("the model was enabled by the call that refused it")
		}
	}
	if code, out = putModelReq(t, a, "flux-dev", `{"enabled":true,"confirm_vram":true}`); code != http.StatusOK {
		t.Fatalf("confirmed enable = %d (%v) — this warns, it does not forbid", code, out)
	}
	// A model that fits is never asked about.
	if code, out = putModelReq(t, a, "sdxl", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("enabling a model that fits = %d (%v)", code, out)
	}
	// And neither is a model nobody has measured: `unknown` is not "too big".
	if err := models.PutEngineModel(ctx, store.EngineModel{Role: "image", ID: "mystery", Kind: "checkpoint"}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()
	if code, out = putModelReq(t, a, "mystery", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("enabling an unmeasured model = %d (%v)", code, out)
	}
}

func putModelReq(t *testing.T, a engineAdminAPI, id, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/api/admin/engines/image/models/"+id, strings.NewReader(body))
	r.SetPathValue("key", "image")
	r.SetPathValue("id", id)
	a.putModel(rec, r, store.Identity{ID: "u1"})
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// The controller asks the gate and does not start when it says no. Without this the wait is a
// log line and the box is bought anyway.
func TestControllerHoldsTheStartBackWhenTheGateRefuses(t *testing.T) {
	f := &engineTestECS{desired: 0}
	eng := &engineECS{api: f, key: "image", cluster: "c", service: "s", now: time.Now}
	c := newEngineController(eng, engineSettingsFor("image"), nil, nil, nil, nil, engineControlCfg{
		interval: time.Minute, window: time.Minute, startUnits: 1, idle: time.Hour, deadline: time.Hour,
	})
	c.demand = newEngineDemand(nil, "x", time.Minute)
	c.demand.record(context.Background(), 5)
	c.startGate = func(context.Context) (bool, string) { return false, engineReasonClassSwapWait }
	c.tick(context.Background())
	if f.updates != 0 {
		t.Fatalf("the controller started the engine %d time(s) while the gate said wait", f.updates)
	}
	c.startGate = func(context.Context) (bool, string) { return true, "" }
	c.tick(context.Background())
	if f.updates != 1 {
		t.Fatalf("the controller made %d update(s) once the gate allowed it", f.updates)
	}
}

// The gateway's own start path (a request arriving at a stopped engine) is the third way a box
// gets bought, and it needs the same gate: without it, one request after a class change buys
// the previous rung's box.
//
// It must not fail the request either. The caller is a wait loop, so "not yet" is a nil error
// and the request ends in the retryable 503 the provider already handles.
func TestGatewayStartPathRespectsTheClassGate(t *testing.T) {
	st := testSettingsStore(t)
	api := &engineTestECS{instance: "engine-image", instanceType: "g6.xlarge"}
	e := newClassTestEngine(t, api, &fakeFleet{}, "l40s|L40S|44000|g6e.xlarge|4-8|30000-65536", st)
	if err := e.ensureStarted(t.Context()); err != nil {
		t.Fatalf("ensureStarted = %v, want the wait to continue rather than the request to fail", err)
	}
	if api.updates != 0 {
		t.Fatalf("the gateway started the engine %d time(s) while a %s box was still registered",
			api.updates, api.instanceType)
	}
}
