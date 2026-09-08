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
