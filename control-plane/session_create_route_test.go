package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// POST /api/sessions refuses an explicit route selection the snapshot cannot prove valid before
// the Agent is asked (ADR 0128 decision 12), and passes a create with no selection untouched. No
// harness declares a route yet, so every well-formed selection is refused.
func TestSessionCreateRefusesRouteSelection(t *testing.T) {
	f := newHandoffLife(t)
	rc := f.recipientWorkspace()
	for name, tc := range map[string]struct {
		body map[string]any
		code string
	}{
		"known route, kind declares none": {map[string]any{"kind": "claude", "route": map[string]any{"route": "anthropic-api"}}, "route_unsupported"},
		"no kind means claude":            {map[string]any{"route": map[string]any{"route": "anthropic-api", "connection": "c1"}}, "route_unsupported"},
		"another kind":                    {map[string]any{"kind": "codex", "route": map[string]any{"route": "openai-api"}}, "route_unsupported"},
		"unknown route":                   {map[string]any{"kind": "claude", "route": map[string]any{"route": "nope"}}, "bad_route"},
		"empty selection":                 {map[string]any{"kind": "claude", "route": map[string]any{}}, "bad_route"},
		"connection only":                 {map[string]any{"kind": "claude", "route": map[string]any{"connection": "c1"}}, "bad_route"},
		"not an object":                   {map[string]any{"kind": "claude", "route": "anthropic-api"}, "bad_route"},
	} {
		tc.body["dir"] = "/repos/app"
		rec := rc.create(t, tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+tc.code+`"`) {
			t.Errorf("%s: %d %s, want 400 %s", name, rec.Code, rec.Body.String(), tc.code)
		}
	}
	if len(rc.bodies) != 0 {
		t.Fatalf("a refused route reached the Agent: %v", rc.bodies)
	}
	// The invariant: no selection, or a null one, is forwarded exactly as before.
	for _, body := range []map[string]any{
		{"kind": "claude", "dir": "/repos/app"},
		{"kind": "codex", "dir": "/repos/app", "route": nil},
	} {
		if rec := rc.create(t, body); rec.Code != http.StatusCreated {
			t.Errorf("%v: %d %s, want the create forwarded", body, rec.Code, rec.Body.String())
		}
	}
	if len(rc.bodies) != 2 {
		t.Errorf("Agent saw %d creates, want 2", len(rc.bodies))
	}
}

func TestScheduleRouteSelection(t *testing.T) {
	api, _, mv := newSchedAPITest(t)
	const base = `"spec_kind":"cron","spec":"0 9 * * *","prompt":"x"`
	for name, b := range map[string]string{
		"known route, kind declares none": `{` + base + `,"route":"anthropic-api"}`,
		"on another kind":                 `{` + base + `,"agent_kind":"codex","route":"openai-api"}`,
		"unknown route":                   `{` + base + `,"route":"nope"}`,
		"connection without route":        `{` + base + `,"route_connection":"c1"}`,
		"on an assistant schedule":        `{` + base + `,"session_mode":"assistant","route":"anthropic-api"}`,
		"reuse mode":                      `{` + base + `,"session_mode":"reuse","reuse_target":"s1","route":"anthropic-api"}`,
	} {
		rec := doJSON(api.create, mv, "POST", b, "")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "route") {
			t.Errorf("create %s -> %d %s, want 400 naming the route", name, rec.Code, rec.Body.String())
		}
	}
	// No selection: created, and read back with no route key.
	rec := doJSON(api.create, mv, "POST", `{`+base+`}`, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create without a route -> %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"route`) {
		t.Errorf("a schedule with no selection reads back with one: %s", rec.Body.String())
	}
	var dto scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	for name, patch := range map[string]string{
		"route":      `{"route":"anthropic-api"}`,
		"connection": `{"route_connection":"c1"}`,
		"unknown":    `{"route":"nope"}`,
	} {
		up := doJSON(api.update, mv, "PATCH", patch, dto.ID)
		if up.Code != http.StatusBadRequest || !strings.Contains(up.Body.String(), "route") {
			t.Errorf("update %s -> %d %s, want 400", name, up.Code, up.Body.String())
		}
	}
	if up := doJSON(api.update, mv, "PATCH", `{"route":""}`, dto.ID); up.Code != http.StatusOK {
		t.Errorf("clearing the route -> %d %s", up.Code, up.Body.String())
	}
}

// A fire of a row that carries a selection the snapshot does not accept is refused before the
// Workspace is touched, in every session mode: new, reuse (first create and rotation) alike.
func TestFireRefusesRouteSelection(t *testing.T) {
	for _, mode := range []string{"new", "reuse"} {
		for _, kind := range []string{"", "claude", "codex"} {
			// A zero wakeFirer would panic on the first store call: the refusal has to come first.
			_, _, err := (&wakeFirer{}).fire(t.Context(), store.Schedule{ID: "s", SessionMode: mode, AgentKind: kind,
				Route: "anthropic-api", RouteConnection: "c1"}, time.Now())
			if err == nil || !strings.Contains(err.Error(), "route_unsupported") {
				t.Errorf("fire %s/%q = %v, want a route_unsupported refusal", mode, kind, err)
			}
		}
	}
}

// The bodies a fire sends carry the selection when the row has one, and no route key when not.
func TestScheduleBodiesCarryRoute(t *testing.T) {
	slot := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	sch := store.Schedule{ID: "s", SessionMode: "new", AgentKind: "claude", Prompt: "p"}
	for name, build := range map[string]func(store.Schedule) []byte{
		"new":   func(s store.Schedule) []byte { return buildInjectBody(s, slot) },
		"reuse": func(s store.Schedule) []byte { return buildReuseCreateBody(s, slot, "t") },
	} {
		var plain map[string]any
		_ = json.Unmarshal(build(sch), &plain)
		if _, has := plain["route"]; has {
			t.Errorf("%s: a row with no selection sends a route: %v", name, plain["route"])
		}
		sch2 := sch
		sch2.Route, sch2.RouteConnection = "anthropic-api", "c1"
		var with struct {
			Route map[string]string `json:"route"`
		}
		_ = json.Unmarshal(build(sch2), &with)
		if with.Route["route"] != "anthropic-api" || with.Route["connection"] != "c1" {
			t.Errorf("%s: route = %v, want the row's selection", name, with.Route)
		}
	}
}

// The selection survives the store round trip, and a row written without one reads back empty.
func TestScheduleRouteStoreRoundTrip(t *testing.T) {
	api, _, mv := newSchedAPITest(t)
	st := api.store
	rec := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x"}`, "")
	var dto scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	sch, ok, err := st.GetSchedule(t.Context(), dto.ID)
	if err != nil || !ok || sch.Route != "" || sch.RouteConnection != "" {
		t.Fatalf("default row: %+v %v %v", sch, ok, err)
	}
	sch.Route, sch.RouteConnection = "anthropic-api", "c1"
	if err := st.UpdateSchedule(t.Context(), sch); err != nil {
		t.Fatal(err)
	}
	got, _, _ := st.GetSchedule(t.Context(), dto.ID)
	if got.Route != "anthropic-api" || got.RouteConnection != "c1" {
		t.Errorf("stored selection = %q/%q", got.Route, got.RouteConnection)
	}
}
