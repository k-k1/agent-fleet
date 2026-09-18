package main

// The two levels of engine_civitai_red.go, and the one thing the Console cannot be trusted
// with: the search route admits a granted tenant_admin, so "the tab is not drawn" has to be
// backed by a refusal.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// offerCivitaiRed answers AF_ENGINE_CIVITAI_RED for one test. The variable and not the
// environment: it is read once at start, so setting the variable is what a deployment that
// offers the source looks like from here.
func offerCivitaiRed(t *testing.T, offered bool) {
	t.Helper()
	old := engineCivitaiRedOffered
	engineCivitaiRedOffered = offered
	t.Cleanup(func() { engineCivitaiRedOffered = old })
}

// The shipped default is that the source does not exist, and the administrator's row only has a
// say where the deployment offers it at all. The list the Console draws its tabs from follows
// the same answer, which is the point of having one.
func TestCivitaiRedGateHasTwoLevels(t *testing.T) {
	st := testSettingsStore(t)
	for _, tc := range []struct {
		name    string
		offered bool
		row     string
		wantOn  bool
		wantSrc int
	}{
		{"a standard build offers nothing", false, "", false, 2},
		{"an off row cannot switch on what is not offered", false, "on", false, 2},
		{"offered and unchosen is on: the stack already asked", true, "", true, 3},
		{"offered and switched off", true, "off", false, 2},
		{"offered and switched back on", true, "on", true, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.row == "" {
				if err := st.DeleteSetting(t.Context(), engineCivitaiRedSetting); err != nil {
					t.Fatalf("clear: %v", err)
				}
			} else if err := st.SetSetting(t.Context(), engineCivitaiRedSetting, tc.row); err != nil {
				t.Fatalf("set: %v", err)
			}
			g := engineCivitaiRed{settings: st, offered: tc.offered}
			if got := g.on(t.Context()); got != tc.wantOn {
				t.Errorf("on = %v, want %v", got, tc.wantOn)
			}
			if got := g.sources(t.Context()); len(got) != tc.wantSrc {
				t.Errorf("sources = %v, want %d of them", got, tc.wantSrc)
			}
			if got := g.available(); got != tc.offered {
				t.Errorf("available = %v, want %v", got, tc.offered)
			}
		})
	}
}

// 🔴 The refusal, not the hidden tab, is the gate: this route is reachable by a tenant_admin
// whose tenant was granted allow_engine_ingest, and by anything else that can POST.
func TestCivitaiRedSearchIsRefusedWhereItIsNotOffered(t *testing.T) {
	a, _, _ := engineModelAdminAPI(t)
	offerCivitaiRed(t, false)
	// Any request at all reaching the upstream is the failure this test exists to catch.
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	oldRed := engineCivitaiRedBase
	engineCivitaiRedBase = srv.URL
	defer func() { engineCivitaiRedBase = oldRed }()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/search?kind=checkpoint",
		strings.NewReader(`{"q":"wai","source":"civitai-red"}`))
	a.browseSearch(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("search = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), errCodeEngineCivitaiRedOff) {
		t.Errorf("body = %s, want %s", rec.Body.String(), errCodeEngineCivitaiRedOff)
	}
	if asked {
		t.Error("the upstream was asked anyway — the refusal has to come before the search")
	}
}

// And where it IS offered the tab works exactly as before, `nsfw=true` and all.
func TestCivitaiRedSearchRunsWhereItIsOffered(t *testing.T) {
	a, _, _ := engineModelAdminAPI(t)
	offerCivitaiRed(t, true)
	asked := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("nsfw")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	oldRed := engineCivitaiRedBase
	engineCivitaiRedBase = srv.URL
	defer func() { engineCivitaiRedBase = oldRed }()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/search?kind=checkpoint",
		strings.NewReader(`{"q":"wai","source":"civitai-red"}`))
	a.browseSearch(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})

	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d %s", rec.Code, rec.Body.String())
	}
	if asked != "true" {
		t.Errorf("nsfw = %q, want true", asked)
	}
}

// The Console reads the tab strip off the engine list, so that is where the gate has to show —
// on the route a granted tenant_admin may call, not only on the super_admin's switch.
func TestEngineListCarriesTheCatalogSources(t *testing.T) {
	st := testSettingsStore(t)
	reg, _ := newAdminTestRegistry(t, &engineTestECS{desired: 0}, st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}

	for _, tc := range []struct {
		offered bool
		want    string
	}{
		{false, "hf,civitai"},
		{true, "hf,civitai,civitai-red"},
	} {
		offerCivitaiRed(t, tc.offered)
		rec := httptest.NewRecorder()
		a.get(rec, httptest.NewRequest("GET", "/api/admin/engines", nil),
			engineIngestGrant{ident: store.Identity{ID: "u1"}})
		var out struct {
			Sources []string `json:"catalog_sources"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got := strings.Join(out.Sources, ","); got != tc.want {
			t.Errorf("offered=%v: catalog_sources = %q, want %q", tc.offered, got, tc.want)
		}
	}
}

func TestCivitaiRedSwitchIsAuditedAndRefusedWhereUnavailable(t *testing.T) {
	st := testSettingsStore(t)
	reg, _ := newAdminTestRegistry(t, &engineTestECS{desired: 0}, st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	put := func(body string) (int, engineCivitaiRedStatus) {
		t.Helper()
		rec := httptest.NewRecorder()
		a.putCivitaiRed(rec, httptest.NewRequest("PUT", "/api/admin/engines/civitai-red",
			strings.NewReader(body)), store.Identity{ID: "u1"})
		var out engineCivitaiRedStatus
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// A deployment that was never given the feature has no switch to move, and the refusal says
	// so rather than writing a row nothing will ever read.
	offerCivitaiRed(t, false)
	if code, _ := put(`{"enabled":true}`); code != http.StatusConflict {
		t.Errorf("put where unavailable = %d, want 409", code)
	}
	if v, _ := st.GetSetting(t.Context(), engineCivitaiRedSetting); v != "" {
		t.Errorf("setting = %q, want nothing written", v)
	}

	offerCivitaiRed(t, true)
	if code, out := put(`{"enabled":false}`); code != http.StatusOK || out.Enabled || !out.Available {
		t.Fatalf("switch off = %d %+v", code, out)
	}
	rec := httptest.NewRecorder()
	a.getCivitaiRed(rec, httptest.NewRequest("GET", "/api/admin/engines/civitai-red", nil),
		store.Identity{ID: "u1"})
	var status engineCivitaiRedStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !status.Available || status.Enabled {
		t.Errorf("status = %+v, want available and off", status)
	}
	if code, out := put(`{"enabled":true}`); code != http.StatusOK || !out.Enabled {
		t.Fatalf("switch on = %d %+v", code, out)
	}

	logs, err := st.ListAuditByTenant(t.Context(), "", 100)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	var moves []string
	for _, l := range logs {
		if l.Action == "engine.civitai_red" {
			moves = append(moves, l.Target)
		}
	}
	// Both presses are on the record, and the refused one is not.
	if len(moves) != 2 {
		t.Fatalf("audited moves = %v, want the two that were allowed", moves)
	}
}
