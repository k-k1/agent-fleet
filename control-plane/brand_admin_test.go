package main

import (
	"encoding/json"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func brandTestStore(t *testing.T) *store.SQL {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

// The stored choice is what the assets are served with, and the environment is what is
// left when it is cleared. Both directions, because "the modal wins" and "reset really
// goes back" are different bugs.
func TestBrandResolverPrefersTheStoredOverride(t *testing.T) {
	st := brandTestStore(t)
	res := newBrandResolver(newBrandConfig("orange", "env-label"), st)

	if got := res.get(t.Context()); got.colorName != "orange" || got.label != "env-label" {
		t.Fatalf("with no override the environment must win, got %+v", got)
	}
	if err := res.override(t.Context(), &brandOverride{Color: "violet", Label: "dev"}); err != nil {
		t.Fatal(err)
	}
	// Not "after the cache expires" — override invalidates, so the very next request sees it.
	if got := res.get(t.Context()); got.colorName != "violet" || got.label != "dev" {
		t.Fatalf("the stored override must win at once, got %+v", got)
	}
	// An empty label on a deployment whose environment sets one: the row exists, so the
	// label is genuinely gone. This is the case a two-key store could not express.
	if err := res.override(t.Context(), &brandOverride{Color: "violet"}); err != nil {
		t.Fatal(err)
	}
	if got := res.get(t.Context()); got.label != "" {
		t.Fatalf("a stored empty label must clear the env label, got %q", got.label)
	}
	if err := res.override(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if got := res.get(t.Context()); got.colorName != "orange" || got.label != "env-label" {
		t.Fatalf("clearing must hand the deployment back to the environment, got %+v", got)
	}
}

// A row written by hand (or by an older version) must never take the Console down.
func TestBrandResolverSurvivesAJunkRow(t *testing.T) {
	st := brandTestStore(t)
	res := newBrandResolver(newBrandConfig("red", "env"), st)
	if err := st.SetSetting(t.Context(), brandSettingKey, "{not json"); err != nil {
		t.Fatal(err)
	}
	if got := res.get(t.Context()); got.colorName != "red" {
		t.Fatalf("unparsable row must fall back to the environment, got %+v", got)
	}
	if err := st.SetSetting(t.Context(), brandSettingKey, `{"color":"chartreuse","label":"x"}`); err != nil {
		t.Fatal(err)
	}
	res.mu.Lock()
	res.at = res.at.Add(-2 * brandCacheTTL) // age the cache instead of sleeping
	res.mu.Unlock()
	if got := res.get(t.Context()); got.colorName != "teal" || got.label != "x" {
		t.Fatalf("unknown colour must degrade to teal and keep the label, got %+v", got)
	}
}

// The API and the asset routes have to agree, so this drives both through one mux: save a
// colour, then fetch the icon and the shell that a browser would.
func TestBrandAdminAPIChangesWhatIsServed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"),
		[]byte(`<!doctype html><html><head><title>Agent Fleet — Console</title>`+
			`<meta name="theme-color" content="#149ba7" />`+
			`<meta name="apple-mobile-web-app-title" content="Agent Fleet" /></head><body></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "brand"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "brand", "icon-192.png"),
		pngBytes(t, []color.NRGBA{{0x14, 0x9b, 0xa7, 0xff}}), 0o644); err != nil {
		t.Fatal(err)
	}
	st := brandTestStore(t)
	res := newBrandResolver(newBrandConfig("", ""), st)
	cfg := config{consoleDir: dir, brand: res}
	mux := http.NewServeMux()
	registerStatic(mux, cfg)
	a := brandAdminAPI{memberAuth{nil}, res}

	// The gate is withSuperAdmin in registerBrandAdminRoutes; these call the handlers
	// directly, which is the only way to exercise them without an identity store.
	put := func(body string) brandStatus {
		t.Helper()
		rec := httptest.NewRecorder()
		a.put(rec, httptest.NewRequest("PUT", "/api/admin/brand", strings.NewReader(body)), store.Identity{ID: "u1"})
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s = %d %s", body, rec.Code, rec.Body.String())
		}
		var s brandStatus
		if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	s := put(`{"color":"violet","label":"dev"}`)
	if s.Color != "violet" || s.Label != "dev" || s.Name != "[dev] Agent Fleet" || s.Source != "admin" {
		t.Fatalf("status after save: %+v", s)
	}
	if len(s.Presets) != len(brandPalette) || s.MaxLabel != brandLabelMax {
		t.Errorf("the modal needs the palette and the cap: %+v", s)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "<title>[dev] Agent Fleet") {
		t.Errorf("the shell did not pick the change up without a restart: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/brand/icon-192.png", nil))
	if got, want := decodePNG(t, rec.Body.Bytes())[0], hexNRGBA(t, s.Hex); !nearColor(got, want) {
		t.Errorf("icon still %v, want %v", got, want)
	}

	// Changing the colour again must not serve the previous tint out of the icon cache.
	s = put(`{"color":"orange","label":"dev"}`)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/brand/icon-192.png", nil))
	if got, want := decodePNG(t, rec.Body.Bytes())[0], hexNRGBA(t, s.Hex); !nearColor(got, want) {
		t.Errorf("icon cache served a stale colour: %v, want %v", got, want)
	}

	// A colour outside the palette is refused rather than stored.
	rec = httptest.NewRecorder()
	a.put(rec, httptest.NewRequest("PUT", "/api/admin/brand", strings.NewReader(`{"color":"chartreuse"}`)), store.Identity{ID: "u1"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown colour = %d, want 400", rec.Code)
	}

	// And DELETE hands everything back: unbranded, the shell is the shipped file again.
	rec = httptest.NewRecorder()
	a.del(rec, httptest.NewRequest("DELETE", "/api/admin/brand", nil), store.Identity{ID: "u1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "<title>Agent Fleet — Console") ||
		strings.Contains(rec.Body.String(), "__AF_BRAND") {
		t.Errorf("reset did not return the shipped shell: %s", rec.Body.String())
	}
}

// The label the modal reads back has to be the label the title bar will show.
func TestBrandAdminSanitisesTheStoredLabel(t *testing.T) {
	st := brandTestStore(t)
	a := brandAdminAPI{memberAuth{nil}, newBrandResolver(newBrandConfig("", ""), st)}
	rec := httptest.NewRecorder()
	a.put(rec, httptest.NewRequest("PUT", "/api/admin/brand",
		strings.NewReader(`{"color":"blue","label":"  0123456789abcdefGHIJ  "}`)), store.Identity{ID: "u1"})
	var s brandStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.Label != "0123456789abcdef" {
		t.Fatalf("label = %q", s.Label)
	}
	v, _ := st.GetSetting(t.Context(), brandSettingKey)
	if !strings.Contains(v, `"label":"0123456789abcdef"`) {
		t.Fatalf("stored row = %s", v)
	}
}
