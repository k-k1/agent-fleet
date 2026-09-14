package main

// engine_member_test.go — ADR 0084 P0-A: what a plain member may see of the fleet's engines.
//
// Every "X is absent" assertion here is checked against a row the SAME engine produces WITH X
// present in some other case in the same test (AGENTS.md「検証」: a positive control before a
// negative one), so a row-building function that silently stopped writing anything would not
// pass these by accident.

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

// newMemberTestEngine wires a managed `image` row with everything memberSourceRow and a.row
// both read: settings (so mode is a real stored value, not the stack default), a catalogue with
// no store behind it (hasModels answers true, same as a CP with no database), a controller, and
// an in-flight counter.
func newMemberTestEngine(t *testing.T, api engineECSAPI, st store.SettingsStore) *engineRuntimeState {
	t.Helper()
	e := newTestImageEngine(t, "http://127.0.0.1:1", api)
	e.settings = st
	e.demand = newEngineDemand(st, engineSettingsFor("image").demandAt, 5*time.Minute)
	e.catalog = newEngineCatalog(nil, "image")
	e.inflight = newEngineInFlight()
	e.ctrl = newEngineController(e.ecs, engineSettingsFor("image"), nil, e.demand, st, nil, testControlCfg())
	return e
}

// TestEngineMemberRowIsSubsetOfAdminRow is decision 3's central claim, asserted against a REAL
// row rather than by construction: every key engineMemberRow keeps has the SAME value as the
// admin row's, for an engine that is actually running with actual demand recorded (so stop_eta
// has an answer to compare, not just an absence on both sides).
func TestEngineMemberRowIsSubsetOfAdminRow(t *testing.T) {
	st := testSettingsStore(t)
	f := &engineTestECS{desired: 1, running: 1}
	e := newMemberTestEngine(t, f, st)
	ctx := t.Context()

	e.demand.record(ctx, 1) // stamps + counts, so stop_eta has an answer on both sides
	e.ctrl.tick(ctx)        // populates the controller's own cached observation
	e.inflight.begin()      // a nonzero in-flight count, so queue is not vacuously {0,0}
	t.Cleanup(e.inflight.end)

	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	admin := a.row(ctx, e)

	full, ok := e.memberSourceRow(ctx)
	if !ok {
		t.Fatal("memberSourceRow refused an engine that is on and has models")
	}
	member := engineMemberRow(full)

	// Positive control: the row is not vacuously empty, and it actually carries a live value
	// (stop_eta) worth comparing — otherwise the subset loop below would pass on nothing.
	if _, ok := member["stop_eta"]; !ok {
		t.Fatalf("member row has no stop_eta with demand just recorded and the engine on-demand: %v", member)
	}
	if len(member) < 5 {
		t.Fatalf("member row = %v, too small to be the real shape", member)
	}
	for k, v := range member {
		if k == "queue" {
			// decision 6-A is new: nothing on the admin row corresponds to it yet.
			continue
		}
		av, present := admin[k]
		if !present {
			t.Errorf("member[%q] = %v, but the admin row has no such key at all — not a subset", k, v)
			continue
		}
		if got, want := jsonOf(t, v), jsonOf(t, av); got != want {
			t.Errorf("member[%q] = %s, admin[%q] = %s — same key, different value", k, got, k, want)
		}
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %v: %v", v, err)
	}
	return string(b)
}

// TestEngineMemberRowStopETAParity is decision 3/4's four refusals (engineStopETA's doc),
// checked on BOTH rows: pinned on, pinned off (where the whole member row disappears — decision
// 5-2), already stopped, and no demand mark yet. Each case also has the case above it in the
// list as its positive control: the same engine, different knob, and the FIRST subtest of each
// pair below establishes that the row-building path is alive at all.
func TestEngineMemberRowStopETAParity(t *testing.T) {
	ctx := context.Background()
	modeKey := engineSettingsFor("image").mode

	newCase := func(t *testing.T, desired, running int32) (*engineRuntimeState, engineAdminAPI, store.SettingsStore) {
		st := testSettingsStore(t)
		f := &engineTestECS{desired: desired, running: running}
		e := newMemberTestEngine(t, f, st)
		reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
		return e, engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}, st
	}

	t.Run("pinned on: never counts down", func(t *testing.T) {
		e, a, st := newCase(t, 1, 1)
		_ = st.SetSetting(ctx, modeKey, engineModeOn)
		e.ctrl.noteMemberSnapshot("running", 1, time.Now())

		admin := a.row(ctx, e)
		if _, ok := admin["stop_eta"]; ok {
			t.Errorf("admin row has stop_eta = %v under mode=on", admin["stop_eta"])
		}
		full, ok := e.memberSourceRow(ctx)
		if !ok {
			t.Fatal("mode=on engine with models must still produce a row")
		}
		if _, ok := full["stop_eta"]; ok {
			t.Errorf("member row has stop_eta = %v under mode=on", full["stop_eta"])
		}
	})

	t.Run("pinned off: the whole row disappears", func(t *testing.T) {
		e, a, st := newCase(t, 0, 0)
		_ = st.SetSetting(ctx, modeKey, engineModeOff)
		e.ctrl.noteMemberSnapshot("stopped", 0, time.Now())

		admin := a.row(ctx, e)
		if _, ok := admin["stop_eta"]; ok {
			t.Errorf("admin row has stop_eta = %v under mode=off", admin["stop_eta"])
		}
		if _, ok := e.memberSourceRow(ctx); ok {
			t.Error("mode=off must produce no member row at all (decision 5-2), not a row without stop_eta")
		}
	})

	t.Run("already stopped: nothing to count down from", func(t *testing.T) {
		e, a, st := newCase(t, 0, 0)
		_ = st.SetSetting(ctx, modeKey, engineModeOnDemand)
		e.demand.record(ctx, 1) // demand IS recorded — up=false is what must suppress it
		e.ctrl.noteMemberSnapshot("stopped", 0, time.Now())

		admin := a.row(ctx, e)
		if _, ok := admin["stop_eta"]; ok {
			t.Errorf("admin row has stop_eta = %v while stopped", admin["stop_eta"])
		}
		full, ok := e.memberSourceRow(ctx)
		if !ok {
			t.Fatal("a stopped on-demand engine with models must still produce a row")
		}
		if _, ok := full["stop_eta"]; ok {
			t.Errorf("member row has stop_eta = %v while stopped", full["stop_eta"])
		}
	})

	t.Run("no demand mark yet: judge nothing", func(t *testing.T) {
		e, a, st := newCase(t, 1, 1)
		_ = st.SetSetting(ctx, modeKey, engineModeOnDemand)
		// Deliberately no e.demand.record/stamp call, and the snapshot set directly rather than
		// through tick() — tick's own first pass would stamp demand as a side effect, which is
		// exactly the case this subtest must NOT exercise.
		e.ctrl.noteMemberSnapshot("running", 1, time.Now())

		admin := a.row(ctx, e)
		if _, ok := admin["stop_eta"]; ok {
			t.Errorf("admin row has stop_eta = %v with no demand mark", admin["stop_eta"])
		}
		full, ok := e.memberSourceRow(ctx)
		if !ok {
			t.Fatal("an on-demand engine with models and no demand mark must still produce a row")
		}
		if _, ok := full["stop_eta"]; ok {
			t.Errorf("member row has stop_eta = %v with no demand mark", full["stop_eta"])
		}
		// Positive control for the whole subtest: state itself IS there, so the row-building
		// path ran and the missing stop_eta above is not just an empty row.
		if full["state"] != "running" {
			t.Fatalf("state = %v, want running — otherwise the stop_eta check above is vacuous", full["state"])
		}
	})
}

// TestEngineMemberRowExternalOmitsState is decision 4: a row this deployment does not manage
// carries no state, stop_eta or idle window, whatever the admin row does. lifecycle staying
// present is the positive control — the row-building path did run, it just declined these three
// keys specifically.
func TestEngineMemberRowExternalOmitsState(t *testing.T) {
	srv := httptest.NewServer(comfyStub())
	t.Cleanup(srv.Close)
	// No settings store: this is decision 4 in isolation, and a nil-backed catalogue answers
	// hasModels true (the same "no database, no false negative" default engine_catalog.go
	// documents) so decision 5-3's refusal never enters into it.
	e := newTestExternalEngine(t, srv.URL, nil)
	e.inflight = newEngineInFlight()

	full, ok := e.memberSourceRow(t.Context())
	if !ok {
		t.Fatal("an external row with models available must still produce a row")
	}
	if full["lifecycle"] != engineLifecycleExternal {
		t.Fatalf("lifecycle = %v, want %q — the row-building path did not run", full["lifecycle"], engineLifecycleExternal)
	}
	for _, k := range []string{"state", "stop_eta", "idle_secs"} {
		if v, present := full[k]; present {
			t.Errorf("external row carries %q = %v, decision 4 says it must not", k, v)
		}
	}
	member := engineMemberRow(full)
	if _, present := member["state"]; present {
		t.Error("engineMemberRow let state through pickKeys for an external row")
	}
}

// TestEngineMemberRowSkipsOffAndHidesEmptyCatalogue is decision 5's cases 2 and 3: a row is
// switched off, or has nothing enabled, must not appear at all — never as a row missing a field.
// The "on, with a model" case is the positive control that runs first.
func TestEngineMemberRowSkipsOffAndHidesEmptyCatalogue(t *testing.T) {
	ctx := context.Background()
	st := testSettingsStore(t)
	f := &engineTestECS{desired: 1, running: 1}
	e := newMemberTestEngine(t, f, st)
	// Wired to the REAL store rather than newMemberTestEngine's nil-backed default, so
	// enabling/disabling the model through the admin API below actually changes what
	// hasModels answers.
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	e.ctrl.noteMemberSnapshot("running", 1, time.Now())

	if code, out := adminModel(t, a, "POST", "image", "",
		`{"id":"sdxl-base-1.0","kind":"checkpoint","base_model":"sdxl",
		  "files":[{"s3Key":"checkpoints/sd_xl_base_1.0.safetensors"}]}`); code != http.StatusOK {
		t.Fatalf("POST model = %d (%v)", code, out)
	}
	if code, out := adminModel(t, a, "PUT", "image", "sdxl-base-1.0", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("PUT model = %d (%v)", code, out)
	}
	e.catalog.invalidate()

	// Positive control: on, with a model enabled, the row is there.
	if _, ok := e.memberSourceRow(ctx); !ok {
		t.Fatal("an engine that is on and has an enabled model must produce a row")
	}

	// Case 3: disable the only model.
	if code, out := adminModel(t, a, "PUT", "image", "sdxl-base-1.0", `{"enabled":false}`); code != http.StatusOK {
		t.Fatalf("PUT model = %d (%v)", code, out)
	}
	e.catalog.invalidate()
	if _, ok := e.memberSourceRow(ctx); ok {
		t.Error("an engine with nothing enabled must produce no row (decision 5-3)")
	}

	// Re-enable, then switch off instead (case 2), to isolate the two refusals from each other.
	if code, out := adminModel(t, a, "PUT", "image", "sdxl-base-1.0", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("PUT model = %d (%v)", code, out)
	}
	e.catalog.invalidate()
	if _, ok := e.memberSourceRow(ctx); !ok {
		t.Fatal("re-enabling the model must restore the row")
	}
	_ = st.SetSetting(ctx, engineSettingsFor("image").mode, engineModeOff)
	if _, ok := e.memberSourceRow(ctx); ok {
		t.Error("mode=off must produce no row (decision 5-2)")
	}
}

// TestEnginesMemberPayloadKeepsRowsSeparate is decision 11's CP-side half: a role with two
// provider rows must not be folded into one entry here — that summing happens in the Console,
// and it needs each row's own queue count to do it.
func TestEnginesMemberPayloadKeepsRowsSeparate(t *testing.T) {
	ctx := context.Background()
	st := testSettingsStore(t)
	a := newMemberTestEngine(t, &engineTestECS{desired: 1, running: 1}, st)
	a.def.Key = "image"
	a.ctrl.noteMemberSnapshot("running", 1, time.Now())
	a.inflight.begin()
	t.Cleanup(a.inflight.end)

	b := newMemberTestEngine(t, &engineTestECS{desired: 0, running: 0}, st)
	b.def.Key = "comfy-lan"
	b.ctrl.noteMemberSnapshot("stopped", 0, time.Now())

	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": a, "comfy-lan": b}}
	payload := enginesMemberPayload(ctx, reg, tenantLimits{})
	rows, _ := payload["engines"].([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("engines = %v, want 2 separate rows for one role with two providers", rows)
	}
	byKey := map[string]map[string]any{}
	for _, r := range rows {
		byKey[r["key"].(string)] = r
	}
	if byKey["image"] == nil || byKey["comfy-lan"] == nil {
		t.Fatalf("rows = %v, want both keys present", rows)
	}
	if q := byKey["image"]["queue"].(map[string]any); q["count"] != 1 {
		t.Errorf("image queue.count = %v, want 1 — it must not have picked up the other row's count", q["count"])
	}
	if q := byKey["comfy-lan"]["queue"].(map[string]any); q["count"] != 0 {
		t.Errorf("comfy-lan queue.count = %v, want 0", q["count"])
	}
}

// TestEngineInFlightCountedSecsGrace is the wire shape pinned in decision 6 (commit e68fece6):
// `queue.counted_secs` rides while a freshly started process cannot yet vouch for the whole
// count, and disappears — rather than settling on a large, ever-climbing number — once
// engineInFlightGrace has passed. The fresh case is the positive control for the expired one.
func TestEngineInFlightCountedSecsGrace(t *testing.T) {
	f := newEngineInFlight()
	if secs, ok := f.countedSecs(); !ok || secs < 0 {
		t.Fatalf("counted_secs = %d, ok = %v — a freshly started counter must carry it", secs, ok)
	}

	f.since = time.Now().Add(-2 * engineInFlightGrace)
	if secs, ok := f.countedSecs(); ok {
		t.Errorf("counted_secs = %d after the grace window, want it omitted (ok=false)", secs)
	}

	var nilCounter *engineInFlight
	if _, ok := nilCounter.countedSecs(); ok {
		t.Error("a nil counter must report ok=false, not a fabricated value")
	}
}

// TestEngineInFlightRisesAndFalls is decision 6-A, driven through the real gateway: a request
// held against a slow (but reachable) engine counts, and it stops counting the moment the
// answer has gone out — not merely once the handler returns to serve.
func TestEngineInFlightRisesAndFalls(t *testing.T) {
	proceed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/system_stats" {
			_, _ = w.Write([]byte(`{"system":{"comfyui_version":"0.3.0"}}`))
			return
		}
		<-proceed
		_, _ = w.Write([]byte(`{"prompt_id":"p1"}`))
	}))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	st := testSettingsStore(t)
	e := newTestExternalEngine(t, srv.URL, st)
	e.inflight = newEngineInFlight()
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}, signKey: engineSignKey([]byte(strings.Repeat("k", 32)))}
	mgr := &manager{store: st}
	a := engineAdminAPI{memberAuth{mgr}, reg, st}
	g := engineGateway{mgr: mgr, reg: reg}

	if code, out := adminModel(t, a, "POST", "image", "",
		`{"id":"sdxl-base-1.0","kind":"checkpoint","base_model":"sdxl",
		  "files":[{"s3Key":"checkpoints/sd_xl_base_1.0.safetensors"}]}`); code != http.StatusOK {
		t.Fatalf("POST model = %d (%v)", code, out)
	}
	if code, out := adminModel(t, a, "PUT", "image", "sdxl-base-1.0", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("PUT model = %d (%v)", code, out)
	}
	e.catalog.invalidate()

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
	tok := mintEngineSessionToken(reg.signKey, mem.ID, "s-1", "image", time.Now().Add(time.Hour))

	// Positive control: before any request, nothing is held.
	if n := e.inflight.count(); n != 0 {
		t.Fatalf("in-flight = %d before any request, want 0", n)
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/engine/image/v1/prompt", strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+tok)
		r.SetPathValue("key", "image")
		r.SetPathValue("path", "prompt")
		g.serve(rec, r)
		done <- rec
	}()

	deadline := time.Now().Add(5 * time.Second)
	for e.inflight.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := e.inflight.count(); n != 1 {
		t.Fatalf("in-flight = %d while the request is held, want 1", n)
	}

	close(proceed)
	rec := <-done
	if rec.Code != http.StatusOK {
		t.Fatalf("serve = %d: %s", rec.Code, rec.Body.String())
	}
	if n := e.inflight.count(); n != 0 {
		t.Fatalf("in-flight = %d after the answer went out, want 0", n)
	}
	// serve attributes the request on TWO detached goroutines (admission and, on success,
	// recordUsage) — wait for both rather than let either hit the sqlite handle after t.Cleanup
	// closes it (the same ordering trap TestEngineRequestIsAttributedEvenWhenTheEngineNeverAnswers's
	// helper exists for).
	waitForAttribution(t, st, "image", func(r []store.EngineMembershipHourRow) bool {
		return len(r) == 1 && r[0].Requests == 1 && r[0].OKRequests == 1
	})
}

// --- decision 2: the `engines` stream must not carry a value that ticks on its own ------------

// TestEventsStreamEnginesSnapshotThenSilence is the regression test decision 2 explicitly asks
// for: stop_eta is an absolute RFC3339 timestamp, never a countdown, so an engine whose state has
// not actually changed must produce exactly one frame no matter how many ticks pass — including
// ticks that span real wall-clock time (eventsTestEnv's tick is 5ms; several of them elapse here).
// A field computed as "seconds until stop_eta" would instead re-serialize to a different number
// on every tick and break this immediately.
func TestEventsStreamEnginesSnapshotThenSilence(t *testing.T) {
	stub := newEventsStub(`{"sessions":[]}`)
	a, res := eventsTestEnv(t, stub)
	st := testSettingsStore(t)
	e := newMemberTestEngine(t, &engineTestECS{desired: 1, running: 1}, st)
	e.demand.record(context.Background(), 1) // gives stop_eta an actual value to hold stable
	e.ctrl.noteMemberSnapshot("running", 1, time.Now())
	a.engines = &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}

	// Five ticks over real time with nothing about the engine changing.
	frames, _ := runStream(t, a, res, stub, 5)
	if len(frames["engines"]) != 1 {
		t.Fatalf("engines frames = %d, want 1 (snapshot once, then suppressed)", len(frames["engines"]))
	}
	var payload struct {
		Engines []map[string]any `json:"engines"`
	}
	if err := json.Unmarshal(frames["engines"][0], &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Engines) != 1 || payload.Engines[0]["key"] != "image" {
		t.Fatalf("engines payload = %v", payload)
	}
	if _, ok := payload.Engines[0]["stop_eta"]; !ok {
		t.Fatalf("engines payload has no stop_eta to hold stable: %v", payload.Engines[0])
	}
}

// TestEventsStreamEnginesPushesChange is the positive control for the test above: when the
// engine's own observed state actually changes, the stream DOES emit again. Without this, a
// suppression bug that dropped every "engines" frame would pass the stability test above for the
// wrong reason.
func TestEventsStreamEnginesPushesChange(t *testing.T) {
	stub := newEventsStub(`{"sessions":[]}`)
	a, res := eventsTestEnv(t, stub)
	st := testSettingsStore(t)
	e := newMemberTestEngine(t, &engineTestECS{desired: 1, running: 1}, st)
	e.ctrl.noteMemberSnapshot("running", 1, time.Now())
	a.engines = &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}

	go func() {
		stub.waitPolls(2, time.Now().Add(25*time.Second))
		e.ctrl.noteMemberSnapshot("stopped", 0, time.Now())
	}()
	frames, _ := runStream(t, a, res, stub, 6)
	if got := len(frames["engines"]); got != 2 {
		t.Fatalf("engines frames = %d, want 2 (snapshot + the state change)", got)
	}
	var second struct {
		Engines []map[string]any `json:"engines"`
	}
	if err := json.Unmarshal(frames["engines"][1], &second); err != nil {
		t.Fatal(err)
	}
	if second.Engines[0]["state"] != "stopped" {
		t.Fatalf("second engines frame = %v, want state=stopped", second.Engines[0])
	}
}
