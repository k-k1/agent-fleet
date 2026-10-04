package main

// The LAN ComfyUI entered from the admin panel (#957, ADR 0076 decision 2's 2026-10-04 addendum):
// precedence against the environment and the table, the key following its URL's source, the key
// never leaving the CP, and the registry following a save without a restart.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// comfyAuthStub is a ComfyUI that records the Authorization header of every health probe.
type comfyAuthStub struct {
	*httptest.Server
	mu    sync.Mutex
	auths []string
}

func newComfyAuthStub(t *testing.T) *comfyAuthStub {
	t.Helper()
	s := &comfyAuthStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *comfyAuthStub) lastAuth(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.auths) == 0 {
		t.Fatalf("%s was never asked anything", s.URL)
	}
	return s.auths[len(s.auths)-1]
}

func (s *comfyAuthStub) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.auths)
}

// comfyPanelManager is a manager with a store and a master key, so the key is really sealed.
func comfyPanelManager(t *testing.T) (*manager, store.Store) {
	t.Helper()
	st := testSettingsStore(t)
	master := sha256.Sum256([]byte("test-master"))
	mgr := &manager{store: st}
	mgr.master32 = master[:]
	mgr.custodian = newLocalCustodian(mgr.master32)
	return mgr, st
}

// comfyPanelEnv clears every engine source, then sets the given ones.
func comfyPanelEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{"AF_ENGINES_SSM_PARAM", "AF_ENGINES_JSON", "AF_COMFY_URL", "AF_COMFY_API_KEY",
		"AF_ENGINE_API_KEY_IMAGE", "AF_LLM_URL", "AF_REMOTE_ENGINE_URL", "AF_REMOTE_ENGINE_TOKEN"} {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func comfyPanelCall(t *testing.T, a engineAdminAPI, method, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(method, "/api/admin/engines/comfy-lan", strings.NewReader(body))
	who := store.Identity{ID: "u-root"}
	switch method {
	case "GET":
		writeJSON(rec, http.StatusOK, a.comfyLanStatus(r.Context()))
	case "PUT":
		a.putComfyLan(rec, r, who)
	case "DELETE":
		a.deleteComfyLan(rec, r, who)
	}
	return rec.Code, rec.Body.String()
}

func comfyStatusOf(t *testing.T, body string) engineComfyLanStatus {
	t.Helper()
	var s engineComfyLanStatus
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("status %q: %v", body, err)
	}
	return s
}

func silenceCatalogPush(t *testing.T) {
	t.Helper()
	orig := notifyEngineCatalogChanged
	notifyEngineCatalogChanged = func(context.Context, *manager, string) {}
	t.Cleanup(func() { notifyEngineCatalogChanged = orig })
}

func TestEngineComfyURLValid(t *testing.T) {
	for in, want := range map[string]string{
		"http://192.0.2.20:8188":         "http://192.0.2.20:8188",
		" https://comfy.lan/proxy/ ":     "https://comfy.lan/proxy",
		"HTTP://192.0.2.20:8188/":        "HTTP://192.0.2.20:8188",
		"http://[2001:db8::1]:8188/base": "http://[2001:db8::1]:8188/base",
	} {
		got, aerr := engineComfyURLValid(in)
		if aerr != nil || got != want {
			t.Errorf("engineComfyURLValid(%q) = %q, %v; want %q", in, got, aerr, want)
		}
	}
	for in, code := range map[string]string{
		"":                            errCodeEngineComfyURLInvalid,
		"192.0.2.20:8188":             errCodeEngineComfyURLInvalid,
		"ftp://192.0.2.20/":           errCodeEngineComfyURLInvalid,
		"file:///etc/passwd":          errCodeEngineComfyURLInvalid,
		"http://":                     errCodeEngineComfyURLInvalid,
		"http://h:8188/?x=1":          errCodeEngineComfyURLInvalid,
		"http://h:8188/#f":            errCodeEngineComfyURLInvalid,
		"http://user:pw@h:8188":       errCodeEngineComfyURLCredentials,
		"https://token@comfy.lan/":    errCodeEngineComfyURLCredentials,
		"javascript:alert(1)":         errCodeEngineComfyURLInvalid,
		"http://h:8188/\x7f":          errCodeEngineComfyURLInvalid,
		"mailto:root@comfy.lan":       errCodeEngineComfyURLInvalid,
		"http://h:8188 http://other/": errCodeEngineComfyURLInvalid,
	} {
		if _, aerr := engineComfyURLValid(in); aerr == nil || aerr.code != code {
			t.Errorf("engineComfyURLValid(%q) = %v, want %s", in, aerr, code)
		}
	}
}

// The precedence at boot: a stored panel URL wins over AF_COMFY_URL, and it presents the panel's
// key — or, with no panel key, NO bearer at all, even though AF_COMFY_API_KEY and
// AF_ENGINE_API_KEY_IMAGE are both set (the key follows its URL's source).
func TestComfyPanelWinsOverEnvAtBootAndNeverGetsTheEnvKey(t *testing.T) {
	silenceCatalogPush(t)
	envSrv, panelSrv := newComfyAuthStub(t), newComfyAuthStub(t)
	comfyPanelEnv(t, map[string]string{
		"AF_COMFY_URL": envSrv.URL, "AF_COMFY_API_KEY": "env-secret", "AF_ENGINE_API_KEY_IMAGE": "other-secret",
	})
	mgr, _ := comfyPanelManager(t)
	ctx := context.Background()
	p := newEngineComfyPanel(mgr)

	for _, tc := range []struct{ key, wantAuth string }{
		{"panel-secret", "Bearer panel-secret"},
		{"", ""},
	} {
		if aerr := p.write(ctx, panelSrv.URL, tc.key, "u-root"); aerr != nil {
			t.Fatalf("write: %v", aerr)
		}
		reg := newEngineRegistry(ctx, mgr)
		e := reg.get("image")
		if e == nil || e.def.URL != panelSrv.URL || e.def.origin != engineOriginPanel {
			t.Fatalf("key %q: image row = %+v, want the panel's %s", tc.key, e, panelSrv.URL)
		}
		if !engineHealthy(ctx, e) {
			t.Fatal("the panel's ComfyUI was not reached")
		}
		if got := panelSrv.lastAuth(t); got != tc.wantAuth {
			t.Errorf("panel key %q: the panel's host was sent Authorization %q, want %q", tc.key, got, tc.wantAuth)
		}
	}
	if envSrv.calls() != 0 {
		t.Errorf("the environment's host was asked %d time(s) while the panel holds the role", envSrv.calls())
	}
}

// A CP with no engine at all boots with an empty registry, and a save serves the role at once.
func TestComfyPanelFirstSaveOnACPWithNoEngines(t *testing.T) {
	silenceCatalogPush(t)
	srv := newComfyAuthStub(t)
	comfyPanelEnv(t, nil)
	mgr, _ := comfyPanelManager(t)
	reg := newEngineRegistry(context.Background(), mgr)
	if reg == nil || reg.get("image") != nil {
		t.Fatalf("registry = %+v, want an empty one", reg)
	}
	a := engineAdminAPI{memberAuth{mgr}, reg, mgr.store}
	if code, body := comfyPanelCall(t, a, "PUT", `{"url":"`+srv.URL+`/","key":"k1"}`); code != http.StatusOK {
		t.Fatalf("PUT = %d %s", code, body)
	}
	e := reg.get("image")
	if e == nil || e.def.URL != srv.URL || e.apiKey != "k1" || !e.def.external() {
		t.Fatalf("after the save the image row is %+v", e)
	}
	// Cleared with nothing to fall back to: the role goes away again.
	if code, body := comfyPanelCall(t, a, "DELETE", ""); code != http.StatusOK {
		t.Fatalf("DELETE = %d %s", code, body)
	}
	if reg.get("image") != nil {
		t.Error("the image row outlived the panel value that was its only source")
	}
}

// A save swaps the row live; the cached health answer of the old URL does not survive it; and
// clearing the panel goes back to AF_COMFY_URL with the environment's own key.
func TestComfyPanelSaveSwapsTheRowLiveAndClearFallsBackToEnv(t *testing.T) {
	silenceCatalogPush(t)
	envSrv, panelSrv := newComfyAuthStub(t), newComfyAuthStub(t)
	comfyPanelEnv(t, map[string]string{"AF_COMFY_URL": envSrv.URL, "AF_COMFY_API_KEY": "env-secret"})
	mgr, st := comfyPanelManager(t)
	ctx := context.Background()
	reg := newEngineRegistry(ctx, mgr)
	a := engineAdminAPI{memberAuth{mgr}, reg, st}

	before := reg.get("image")
	if before == nil || before.def.URL != envSrv.URL || before.apiKey != "env-secret" {
		t.Fatalf("boot row = %+v", before)
	}
	if !before.warm(ctx) {
		t.Fatal("the environment's ComfyUI is up and warm() said otherwise")
	}
	if s := comfyStatusOf(t, mustOK(t, a, "GET", "")); s.Source != "env" || !s.Available || s.URL != envSrv.URL || !s.EnvKeySet {
		t.Errorf("status before = %+v", s)
	}

	// The new URL is down. If the old row's ten-second warm cache were kept, the panel would go
	// on reporting the old host's "warm".
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()
	mustOK(t, a, "PUT", `{"url":"`+downURL+`"}`)
	e := reg.get("image")
	if e == nil || e == before || e.def.URL != downURL {
		t.Fatalf("after PUT the row is not a fresh one for %s (same object: %v)", downURL, e == before)
	}
	if e.apiKey != "" {
		t.Errorf("a panel URL saved without a key presents %q - the environment's key leaked onto it", e.apiKey)
	}
	if e.warm(ctx) {
		t.Error("warm() answered the old URL's cached health for the new one")
	}

	mustOK(t, a, "PUT", `{"url":"`+panelSrv.URL+`","key":"panel-secret"}`)
	if !engineHealthy(ctx, reg.get("image")) || panelSrv.lastAuth(t) != "Bearer panel-secret" {
		t.Fatalf("the panel's host was not asked with the panel's key")
	}
	// Key kept when the form sends none, cleared on request.
	mustOK(t, a, "PUT", `{"url":"`+panelSrv.URL+`"}`)
	if got := reg.get("image").apiKey; got != "panel-secret" {
		t.Errorf("a save without a key dropped the stored one: %q", got)
	}
	s := comfyStatusOf(t, mustOK(t, a, "PUT", `{"url":"`+panelSrv.URL+`","clear_key":true}`))
	if got := reg.get("image").apiKey; got != "" || s.PanelKeySet {
		t.Errorf("clear_key left key %q / panel_key_set %v", got, s.PanelKeySet)
	}
	if s.Source != "panel" || s.URL != panelSrv.URL || s.UpdatedBy != "u-root" {
		t.Errorf("status after = %+v", s)
	}

	mustOK(t, a, "DELETE", "")
	back := reg.get("image")
	if back == nil || back.def.URL != envSrv.URL || back.apiKey != "env-secret" || back.def.origin != engineOriginEnv {
		t.Fatalf("after DELETE the row is %+v, want AF_COMFY_URL's with its key", back)
	}
}

func mustOK(t *testing.T, a engineAdminAPI, method, body string) string {
	t.Helper()
	code, out := comfyPanelCall(t, a, method, body)
	if code != http.StatusOK {
		t.Fatalf("%s %s = %d %s", method, body, code, out)
	}
	return out
}

// The key never comes back: not from GET, not from PUT's answer, not in the audit log, and not
// in clear in the store.
func TestComfyPanelKeyNeverLeaves(t *testing.T) {
	silenceCatalogPush(t)
	comfyPanelEnv(t, nil)
	mgr, st := comfyPanelManager(t)
	ctx := context.Background()
	reg := newEngineRegistry(ctx, mgr)
	a := engineAdminAPI{memberAuth{mgr}, reg, st}
	const secret = "s3cr3t-bearer-value"

	put := mustOK(t, a, "PUT", `{"url":"http://192.0.2.20:8188","key":"`+secret+`"}`)
	get := mustOK(t, a, "GET", "")
	for what, body := range map[string]string{"PUT": put, "GET": get} {
		if strings.Contains(body, secret) {
			t.Errorf("%s answered the key: %s", what, body)
		}
	}
	if s := comfyStatusOf(t, get); !s.PanelKeySet {
		t.Errorf("GET does not say a key is set: %s", get)
	}
	enc, _ := st.GetSetting(ctx, engineComfyKeySetting)
	if enc == "" || strings.Contains(enc, secret) {
		t.Errorf("stored key = %q, want it sealed", enc)
	}
	mustOK(t, a, "PUT", `{"url":"http://192.0.2.21:8188","clear_key":true}`)
	mustOK(t, a, "DELETE", "")
	rows, err := st.ListAuditByTenant(ctx, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, r := range rows {
		if r.Action != "engine.comfy_lan" {
			continue
		}
		seen = append(seen, r.Target)
		if strings.Contains(r.Target+r.Detail, secret) {
			t.Errorf("the audit log carries the key: %+v", r)
		}
	}
	want := []string{
		"url=(none) was=http://192.0.2.21:8188 key=cleared",
		"url=http://192.0.2.21:8188 was=http://192.0.2.20:8188 key=cleared",
		"url=http://192.0.2.20:8188 was=(none) key=set",
	}
	// Same-second rows have no order in the log, so compare as sets.
	sort.Strings(seen)
	sort.Strings(want)
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Errorf("audit targets =\n%s\nwant\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}

// The URL is refused before anything is sealed or written.
func TestComfyPanelRefusesABadURLBeforeStoringAnything(t *testing.T) {
	silenceCatalogPush(t)
	comfyPanelEnv(t, nil)
	mgr, st := comfyPanelManager(t)
	ctx := context.Background()
	reg := newEngineRegistry(ctx, mgr)
	a := engineAdminAPI{memberAuth{mgr}, reg, st}
	for _, body := range []string{
		`{"url":"http://admin:pw@192.0.2.20:8188","key":"k"}`,
		`{"url":"ftp://192.0.2.20","key":"k"}`,
		`{"url":"","key":"k"}`,
	} {
		if code, out := comfyPanelCall(t, a, "PUT", body); code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d %s, want 400", body, code, out)
		}
	}
	for _, k := range []string{engineComfyURLSetting, engineComfyKeySetting} {
		if v, _ := st.GetSetting(ctx, k); v != "" {
			t.Errorf("%s = %q after refused saves", k, v)
		}
	}
	if reg.get("image") != nil {
		t.Error("a refused save registered an image row")
	}
}

// A managed table row wins over the panel: 409, nothing stored, the row untouched.
func TestComfyPanelDoesNotReplaceAManagedRow(t *testing.T) {
	silenceCatalogPush(t)
	comfyPanelEnv(t, nil)
	mgr, st := comfyPanelManager(t)
	managed := &engineRuntimeState{
		def: engineDef{Key: "image", API: engineAPIImages, Provider: "comfy", Service: "af-image",
			URL: "http://image.af.internal:8188"},
		ecs: &engineECS{key: "image"},
	}
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": managed}}
	reg.buildComfy = func(d engineDef) *engineRuntimeState { return &engineRuntimeState{def: d} }
	a := engineAdminAPI{memberAuth{mgr}, reg, st}

	code, body := comfyPanelCall(t, a, "PUT", `{"url":"http://192.0.2.20:8188","key":"k"}`)
	if code != http.StatusConflict || !strings.Contains(body, errCodeEngineComfyManaged) {
		t.Fatalf("PUT over a managed row = %d %s, want 409 %s", code, body, errCodeEngineComfyManaged)
	}
	if reg.get("image") != managed {
		t.Error("the managed row was replaced")
	}
	if v, _ := st.GetSetting(context.Background(), engineComfyURLSetting); v != "" {
		t.Errorf("the refused URL was stored: %q", v)
	}
	if s := comfyStatusOf(t, mustOK(t, a, "GET", "")); s.Available || s.Source != "table" {
		t.Errorf("status = %+v, want unavailable with source table", s)
	}
	if reg.applyComfyPanel("http://192.0.2.20:8188", "") || reg.get("image") != managed {
		t.Error("applyComfyPanel replaced a managed row")
	}
}

// Only a super_admin reaches the routes: a member and a tenant_admin are refused all three.
func TestComfyPanelRoutesRefuseNonSuperAdmins(t *testing.T) {
	silenceCatalogPush(t)
	comfyPanelEnv(t, nil)
	st, mgr, _, _, _ := networkFixture(t)
	ctx := context.Background()
	if _, err := st.UpsertIdentity(ctx, "root@acme.co.jp", "root-acme-co-jp", "super_admin"); err != nil {
		t.Fatal(err)
	}
	reg := newEngineRegistry(ctx, mgr)
	mux := http.NewServeMux()
	registerEngineAdminRoutes(mux, config{mgr: mgr}, reg)
	call := func(method, email, body string) int {
		r := httptest.NewRequest(method, "/api/admin/engines/comfy-lan", strings.NewReader(body))
		r.Header.Set("X-Forwarded-Email", email)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Code
	}
	body := `{"url":"http://192.0.2.20:8188","key":"k"}`
	for _, email := range []string{"yamada@acme.co.jp", "boss@acme.co.jp"} {
		for _, m := range []string{"PUT", "DELETE"} {
			if code := call(m, email, body); code != http.StatusForbidden {
				t.Errorf("%s by %s = %d, want 403", m, email, code)
			}
		}
	}
	if v, _ := st.GetSetting(ctx, engineComfyURLSetting); v != "" {
		t.Fatalf("a refused caller stored %q", v)
	}
	if code := call("PUT", "root@acme.co.jp", body); code != http.StatusOK {
		t.Fatalf("PUT by the super_admin = %d - the refusals above prove nothing", code)
	}
}

// The state rides on GET /api/admin/engines for the operator alone: a tenant_admin granted the
// ingest sees the engine list without it.
func TestComfyPanelStateIsOnTheEngineListForTheOperatorOnly(t *testing.T) {
	silenceCatalogPush(t)
	comfyPanelEnv(t, nil)
	mgr, st := comfyPanelManager(t)
	reg := newEngineRegistry(context.Background(), mgr)
	a := engineAdminAPI{memberAuth{mgr}, reg, st}
	mustOK(t, a, "PUT", `{"url":"http://192.0.2.20:8188","key":"s3cr3t"}`)
	list := func(super bool) map[string]any {
		rec := httptest.NewRecorder()
		a.get(rec, httptest.NewRequest("GET", "/api/admin/engines", nil),
			engineIngestGrant{ident: store.Identity{ID: "u1"}, super: super})
		if strings.Contains(rec.Body.String(), "s3cr3t") {
			t.Fatalf("the engine list carries the key: %s", rec.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	got, ok := list(true)["comfy_lan"].(map[string]any)
	if !ok || got["source"] != "panel" || got["panel_key_set"] != true {
		t.Errorf("operator's comfy_lan = %v", got)
	}
	if v, ok := list(false)["comfy_lan"]; ok {
		t.Errorf("a tenant_admin's engine list carries comfy_lan: %v", v)
	}
}
