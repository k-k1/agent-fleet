package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrandConfigDefaultsAreInert(t *testing.T) {
	b := newBrandConfig("", "")
	if b.active() || b.tinted() {
		t.Fatalf("unset env must leave everything as shipped: %+v", b)
	}
	if got := b.name("Agent Fleet"); got != "Agent Fleet" {
		t.Fatalf("name = %q, want it untouched", got)
	}
	// An unknown colour is fail-soft: the deployment boots on the shipped art.
	if u := newBrandConfig("chartreuse", ""); u.tinted() || u.colorName != "teal" {
		t.Fatalf("unknown colour must fall back to teal, got %+v", u)
	}
}

func TestBrandLabelSanitised(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  staging  ", "staging"},
		{"a\tb\nc", "a b c"},
		{"pro\x00d", "pro d"},
		{"0123456789abcdefGHIJ", "0123456789abcdef"}, // capped at brandLabelMax
	} {
		if got := sanitizeBrandLabel(tc.in); got != tc.want {
			t.Errorf("sanitizeBrandLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The label prefixes, because a tab strip and a launcher both truncate the END.
func TestBrandNamePrefixes(t *testing.T) {
	b := newBrandConfig("violet", "dev")
	if got := b.name("Agent Fleet — Console"); got != "[dev] Agent Fleet — Console" {
		t.Fatalf("name = %q", got)
	}
}

// The whole colour scheme rests on this: a pixel painted in the art's own colour must
// come out as exactly the preset's hex, and white ink must not move at all.
func TestTintPNGMapsBaseColourOntoPreset(t *testing.T) {
	base := color.NRGBA{0x14, 0x9b, 0xa7, 0xff}
	src := pngBytes(t, []color.NRGBA{
		base,
		{0xff, 0xff, 0xff, 0xff}, // the glyph
		{0, 0, 0, 0},             // transparent padding
	})
	for _, name := range brandColorNames() {
		b := newBrandConfig(name, "")
		out, err := b.tintPNG(src)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		px := decodePNG(t, out)
		want := hexNRGBA(t, b.hex)
		if !nearColor(px[0], want) {
			t.Errorf("%s: ground %v, want %v (%s)", name, px[0], want, b.hex)
		}
		if px[1] != (color.NRGBA{0xff, 0xff, 0xff, 0xff}) {
			t.Errorf("%s: white glyph moved to %v", name, px[1])
		}
		if px[2].A != 0 {
			t.Errorf("%s: transparent pixel became opaque", name)
		}
	}
}

// Positive control for the test above: with the default (teal) the bytes are handed back
// untouched, so a tint that silently did nothing could not pass as a match.
func TestTintPNGDefaultIsByteIdentical(t *testing.T) {
	src := pngBytes(t, []color.NRGBA{{0x14, 0x9b, 0xa7, 0xff}})
	out, err := newBrandConfig("teal", "").tintPNG(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src, out) {
		t.Fatal("the default colour must not re-encode the shipped file")
	}
}

// The rewrite is anchored to tags in the REAL index.html. If someone moves one, this is
// where it is caught — not on a phone that installed a wrongly named app.
func TestRewriteIndexHTMLHitsEveryAnchorInTheRealShell(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "console", "index.html"))
	if err != nil {
		t.Fatalf("console/index.html: %v", err)
	}
	b := newBrandConfig("violet", "dev")
	out, hits := b.rewriteIndexHTML(src)
	if hits != brandIndexAnchors {
		t.Fatalf("hits = %d, want %d — an anchor moved out from under the rewrite", hits, brandIndexAnchors)
	}
	s := string(out)
	for _, want := range []string{
		"<title>[dev] Agent Fleet",
		`content="` + b.hex + `"`,
		`name="apple-mobile-web-app-title" content="[dev] Agent Fleet"`,
		`window.__AF_BRAND={"label":"dev"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rewritten shell is missing %q", want)
		}
	}
	if strings.Contains(s, brandBaseHex) {
		t.Error("the shipped teal survived somewhere in the shell")
	}
}

// A label is operator-supplied text that lands inside a <script>. It must not be able to
// close it.
func TestBootScriptEscapesLabel(t *testing.T) {
	b := newBrandConfig("red", "</script><x>")
	if strings.Contains(b.bootScript(), "</script><x>") {
		t.Fatalf("label was injected raw: %s", b.bootScript())
	}
}

func TestRewriteManifest(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "console", "public", "manifest.webmanifest"))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	b := newBrandConfig("orange", "stg")
	out, err := b.rewriteManifest(src)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if !strings.HasPrefix(m["name"].(string), "[stg] ") || !strings.HasPrefix(m["short_name"].(string), "[stg] ") {
		t.Errorf("names not branded: %v / %v", m["name"], m["short_name"])
	}
	if m["theme_color"] != b.hex {
		t.Errorf("theme_color = %v, want %s", m["theme_color"], b.hex)
	}
	// Everything else has to survive the round trip — the share target above all, since
	// losing it silently breaks Android's 共有シート route into the memo composer.
	if _, ok := m["share_target"].(map[string]any)["params"]; !ok {
		t.Error("share_target.params did not survive the rewrite")
	}
	if len(m["icons"].([]any)) != 4 {
		t.Errorf("icons = %v", m["icons"])
	}
}

// End to end over the real route table: the branded shell, manifest and icon come back
// from the paths the browser actually asks for.
func TestBrandedRoutesServeThroughTheMux(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"),
		[]byte(`<!doctype html><html><head><title>Agent Fleet — Console</title>`+
			`<meta name="theme-color" content="#149ba7" />`+
			`<meta name="apple-mobile-web-app-title" content="Agent Fleet" /></head><body></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.webmanifest"),
		[]byte(`{"name":"Agent Fleet — Console","short_name":"Agent Fleet","theme_color":"#149ba7"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "brand"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "brand", "icon-192.png"),
		pngBytes(t, []color.NRGBA{{0x14, 0x9b, 0xa7, 0xff}}), 0o644); err != nil {
		t.Fatal(err)
	}

	b := newBrandConfig("violet", "dev")
	mux := http.NewServeMux()
	registerStatic(mux, config{consoleDir: dir, brand: b})

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		return rec
	}
	if s := get("/").Body.String(); !strings.Contains(s, "<title>[dev] Agent Fleet") || !strings.Contains(s, b.hex) {
		t.Errorf("shell not branded: %s", s)
	}
	if s := get("/manifest.webmanifest").Body.String(); !strings.Contains(s, "[dev] Agent Fleet") || !strings.Contains(s, b.hex) {
		t.Errorf("manifest not branded: %s", s)
	}
	rec := get("/brand/icon-192.png")
	if got, want := decodePNG(t, rec.Body.Bytes())[0], hexNRGBA(t, b.hex); !nearColor(got, want) {
		t.Errorf("icon pixel = %v, want %v", got, want)
	}

	// The control: unbranded, the same paths fall through to the FileServer untouched.
	plain := http.NewServeMux()
	registerStatic(plain, config{consoleDir: dir})
	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest("GET", "/brand/icon-192.png", nil))
	if !bytes.Equal(rec.Body.Bytes(), pngBytes(t, []color.NRGBA{{0x14, 0x9b, 0xa7, 0xff}})) {
		t.Error("an unbranded deployment must serve the shipped bytes")
	}
}

func TestHSLRoundTrip(t *testing.T) {
	for _, c := range []color.NRGBA{{0x14, 0x9b, 0xa7, 0xff}, {0x7c, 0x4d, 0xff, 0xff}, {0, 0, 0, 0xff}, {0xff, 0xff, 0xff, 0xff}} {
		h, s, l := rgbToHSL(c.R, c.G, c.B)
		r, g, b := hslToRGB(h, s, l)
		if math.Abs(float64(r)-float64(c.R)) > 1 || math.Abs(float64(g)-float64(c.G)) > 1 || math.Abs(float64(b)-float64(c.B)) > 1 {
			t.Errorf("%v -> hsl(%.1f,%.2f,%.2f) -> %d,%d,%d", c, h, s, l, r, g, b)
		}
	}
}

// --- helpers ---------------------------------------------------------------

// pngBytes encodes a 1×N image, one pixel per entry.
func pngBytes(t *testing.T, px []color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, len(px), 1))
	for i, c := range px {
		img.SetNRGBA(i, 0, c)
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodePNG(t *testing.T, b []byte) []color.NRGBA {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]color.NRGBA, img.Bounds().Dx())
	for i := range out {
		out[i] = color.NRGBAModel.Convert(img.At(img.Bounds().Min.X+i, img.Bounds().Min.Y)).(color.NRGBA)
	}
	return out
}

func hexNRGBA(t *testing.T, hex string) color.NRGBA {
	t.Helper()
	var r, g, b uint8
	if _, err := fmt.Sscanf(strings.TrimPrefix(hex, "#"), "%02x%02x%02x", &r, &g, &b); err != nil {
		t.Fatalf("bad hex %q: %v", hex, err)
	}
	return color.NRGBA{r, g, b, 0xff}
}

// nearColor allows the one-LSB drift of an RGB -> HSL -> RGB round trip.
func nearColor(a, b color.NRGBA) bool {
	d := func(x, y uint8) bool { return math.Abs(float64(x)-float64(y)) <= 1 }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B) && a.A == b.A
}
