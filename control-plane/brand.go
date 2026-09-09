package main

// Per-deployment branding: the favicon / PWA icon colour and a short label folded into
// the app name, so several deployments of the SAME image — production, a staging stack,
// a laptop — are told apart in a tab strip and on a phone's home screen instead of
// showing four identical teal cats.
//
// Everything here is decided when the asset is SERVED, never when the Console is built:
// one console bundle ships to every deployment, so a build-time colour would mean one
// image per environment. The Console is static files under consoleDir; the handlers
// below intercept exactly the branded ones and leave the rest to the FileServer.
//
// Why a hue/saturation/lightness shift is enough: the shipped art is flat two-tone —
// brandBaseHex ground, white glyph. White is unsaturated, so it is skipped and comes
// through untouched, and a pixel that is exactly brandBaseHex lands exactly on the
// preset's hex. Anti-aliased edges, being blends of the two, land between them.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// brandBaseHex is the colour the shipped art is actually drawn in (the icons under
// console/public/brand/, index.html's theme-color, the manifest's theme_color). A preset
// is applied as the DELTA from this colour, which is what keeps the relative shading of
// the art intact — so this constant has to track the art, not taste.
const brandBaseHex = "#149ba7"

// brandLabelMax caps AF_BRAND_LABEL. A launcher shows roughly this much before it
// ellipsises, and the label is a prefix precisely so the distinguishing part survives
// the truncation of a narrow browser tab.
const brandLabelMax = 16

// brandGraySat is the saturation below which a pixel counts as ink, not brand colour.
// The glyph is pure white (0) and the art has nothing else desaturated, so anything
// above this is ground to be recoloured.
const brandGraySat = 0.06

// brandPalette is the closed set AF_BRAND_COLOR chooses from. Deliberately a palette and
// not a free hex: these are picked to sit at a similar saturation/lightness to the base,
// so every deployment still looks like the same product.
var brandPalette = []struct{ name, hex string }{
	{"teal", brandBaseHex}, // the shipped art — the default, and a no-op
	{"blue", "#2f6fed"},
	{"violet", "#7c4dff"},
	{"magenta", "#c2447d"},
	{"red", "#d94b4b"},
	{"orange", "#e07a1f"},
	{"green", "#2f9e44"},
	{"slate", "#64748b"},
}

// brandTintedIcons are the console/public/brand/ files served recoloured. The banner and
// the idle artwork are WebP, which the standard library cannot re-encode, so they stay
// as shipped — the icons are what a tab strip and a home screen actually show.
var brandTintedIcons = []string{
	"icon-192.png",
	"icon-512.png",
	"icon-maskable-192.png",
	"icon-maskable-512.png",
	"apple-touch-icon.png",
}

type brandConfig struct {
	colorName string // resolved preset name (always one of brandPalette)
	hex       string // that preset's colour, used for theme-color / theme_color
	label     string // sanitised AF_BRAND_LABEL; empty = the app name is untouched
	// HSL transform from brandBaseHex to hex, applied per pixel.
	hueShift float64
	satScale float64
	lumScale float64
}

// newBrandConfig resolves AF_BRAND_COLOR / AF_BRAND_LABEL. Both are fail-soft: a typo in
// the colour name must not stop a deployment from booting, so it logs the valid names and
// leaves the shipped colour in place.
func newBrandConfig(colorEnv, labelEnv string) brandConfig {
	b := brandConfig{colorName: brandPalette[0].name, hex: brandPalette[0].hex, satScale: 1, lumScale: 1}
	b.label = sanitizeBrandLabel(labelEnv)
	name := strings.ToLower(strings.TrimSpace(colorEnv))
	if name != "" {
		found := false
		for _, p := range brandPalette {
			if p.name == name {
				b.colorName, b.hex, found = p.name, p.hex, true
				break
			}
		}
		if !found {
			log.Printf("AF_BRAND_COLOR=%q is not a known colour; using %s. Valid: %s",
				colorEnv, b.colorName, strings.Join(brandColorNames(), ", "))
		}
	}
	bh, bs, bl := hexToHSL(brandBaseHex)
	th, ts, tl := hexToHSL(b.hex)
	b.hueShift = th - bh
	if bs > 0 {
		b.satScale = ts / bs
	}
	if bl > 0 {
		b.lumScale = tl / bl
	}
	return b
}

func brandColorNames() []string {
	out := make([]string, 0, len(brandPalette))
	for _, p := range brandPalette {
		out = append(out, p.name)
	}
	return out
}

// sanitizeBrandLabel makes an operator-supplied string safe to put in a title, an
// attribute and a launcher name: one line, no control characters, bounded length.
func sanitizeBrandLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > brandLabelMax {
		s = strings.TrimSpace(string(r[:brandLabelMax]))
	}
	return s
}

// tinted reports whether the icons and theme colour differ from what ships in the image.
// The empty hex is the ZERO brandConfig — a config literal that never went through
// newBrandConfig (every test that builds one by hand) — and must read as "not branded",
// not as "recolour towards nothing".
func (b brandConfig) tinted() bool { return b.hex != "" && b.hex != brandBaseHex }

// active reports whether anything at all is branded. When false every asset is served
// byte-for-byte as it ships, through the plain FileServer.
func (b brandConfig) active() bool { return b.tinted() || b.label != "" }

// name folds the label into a product/page name. It PREFIXES, because both places the
// name has to survive — a crowded tab strip and a home-screen launcher — truncate the
// end: "[staging] Agent Fle…" still tells you where you are, "Agent Fleet — Co…" does not.
func (b brandConfig) name(s string) string {
	if b.label == "" {
		return s
	}
	return "[" + b.label + "] " + s
}

// --- assets ----------------------------------------------------------------

// brandIcons serves the recoloured PNGs. Recolouring a 512×512 icon is milliseconds, but
// it happens on every page load, so the result is cached against the file's identity
// (size+mtime) — a console rebuild under the same path therefore invalidates it, which is
// what `npm run dev`'s rebuild-in-place needs.
type brandIcons struct {
	b   brandConfig
	dir string // consoleDir

	mu    sync.Mutex
	cache map[string]brandIcon
}

type brandIcon struct {
	key  string
	body []byte
}

func newBrandIcons(b brandConfig, consoleDir string) *brandIcons {
	return &brandIcons{b: b, dir: consoleDir, cache: map[string]brandIcon{}}
}

func (ic *brandIcons) serve(w http.ResponseWriter, r *http.Request) {
	name := path.Base(r.URL.Path)
	full := filepath.Join(ic.dir, "brand", name)
	st, err := os.Stat(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	key := fmt.Sprintf("%d-%d", st.Size(), st.ModTime().UnixNano())
	ic.mu.Lock()
	hit, ok := ic.cache[name]
	ic.mu.Unlock()
	if !ok || hit.key != key {
		src, err := os.ReadFile(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		body, err := ic.b.tintPNG(src)
		if err != nil {
			// Fail-soft: an icon in a format we cannot re-encode is a wrong colour,
			// not a missing icon.
			log.Printf("brand: %s not recoloured (%v)", name, err)
			body = src
		}
		hit = brandIcon{key: key, body: body}
		ic.mu.Lock()
		ic.cache[name] = hit
		ic.mu.Unlock()
	}
	w.Header().Set("Content-Type", "image/png")
	// Same policy as the FileServer this handler stands in front of: everything outside
	// /assets/ is no-store, so a colour change lands on the next load.
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, st.ModTime(), bytes.NewReader(hit.body))
}

// tintPNG shifts every saturated pixel from the base hue to the preset's.
func (b brandConfig) tintPNG(src []byte) ([]byte, error) {
	if !b.tinted() {
		return src, nil
	}
	img, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	bnd := img.Bounds()
	out := image.NewNRGBA(bnd)
	for y := bnd.Min.Y; y < bnd.Max.Y; y++ {
		for x := bnd.Min.X; x < bnd.Max.X; x++ {
			// NRGBA (not RGBA): the maths below is on straight, not
			// alpha-premultiplied, components.
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if c.A != 0 {
				h, s, l := rgbToHSL(c.R, c.G, c.B)
				if s > brandGraySat {
					c.R, c.G, c.B = hslToRGB(
						math.Mod(h+b.hueShift+360, 360),
						clamp01(s*b.satScale),
						clamp01(l*b.lumScale),
					)
				}
			}
			out.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// --- documents -------------------------------------------------------------

// The three places index.html states the brand. Matched by attribute rather than by the
// whole line so ordinary edits to the file (or Vite's build output) do not silently stop
// the rewrite; brandRewriteIndex reports which ones fired and the test asserts all three
// hit the real console/index.html.
var (
	brandTitleRe      = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	brandThemeColorRe = regexp.MustCompile(`(?i)(<meta\s+name="theme-color"\s+content=")([^"]*)(")`)
	brandAppTitleRe   = regexp.MustCompile(`(?i)(<meta\s+name="apple-mobile-web-app-title"\s+content=")([^"]*)(")`)
)

// rewriteIndexHTML brands the Console shell: the tab title, the PWA theme colour, the iOS
// home-screen name, plus a window.__AF_BRAND the app itself reads (src/lib/brand.ts) for
// the titles it sets at run time.
//
// The count is for the caller's drift check — 3 means every anchor was found.
func (b brandConfig) rewriteIndexHTML(src []byte) ([]byte, int) {
	if !b.active() {
		return src, 0
	}
	s := string(src)
	hits := 0
	if m := brandTitleRe.FindStringSubmatchIndex(s); m != nil {
		s = s[:m[2]] + html.EscapeString(b.name(html.UnescapeString(s[m[2]:m[3]]))) + s[m[3]:]
		hits++
	}
	if brandThemeColorRe.MatchString(s) {
		s = brandThemeColorRe.ReplaceAllString(s, "${1}"+b.hex+"${3}")
		hits++
	}
	if m := brandAppTitleRe.FindStringSubmatchIndex(s); m != nil {
		s = s[:m[4]] + html.EscapeString(b.name(html.UnescapeString(s[m[4]:m[5]]))) + s[m[5]:]
		hits++
	}
	if i := strings.LastIndex(s, "</head>"); i >= 0 {
		s = s[:i] + b.bootScript() + s[i:]
	}
	return []byte(s), hits
}

// bootScript hands the branding to the app before the bundle loads. json.Marshal escapes
// "<", ">" and "&" as unicode escapes, so an operator's label cannot close the script
// element.
func (b brandConfig) bootScript() string {
	j, err := json.Marshal(struct {
		Label string `json:"label"`
		Color string `json:"color"`
		Name  string `json:"name"`
	}{b.label, b.hex, b.name("Agent Fleet")})
	if err != nil {
		return ""
	}
	return "<script>window.__AF_BRAND=" + string(j) + ";</script>\n"
}

// rewriteManifest brands the PWA manifest — the install prompt's name, the launcher's
// short name and the colour Android paints the task switcher with.
//
// background_color is deliberately untouched: it is the neutral splash background the
// app paints before first render, not a brand colour.
func (b brandConfig) rewriteManifest(src []byte) ([]byte, error) {
	if !b.active() {
		return src, nil
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber() // a future numeric member must not come back as 1e+06
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	for _, k := range []string{"name", "short_name"} {
		if s, ok := m[k].(string); ok {
			m[k] = b.name(s)
		}
	}
	if _, ok := m["theme_color"].(string); ok {
		m["theme_color"] = b.hex
	}
	return json.Marshal(m)
}

// brandIndexAnchors is how many of index.html's brand anchors rewriteIndexHTML expects to
// find. Fewer means the shell moved a tag out from under the rewrite and the deployment
// is now half-branded — silent unless it is said out loud, hence the log.
const brandIndexAnchors = 3

var brandDriftOnce sync.Once

// serveConsoleIndex writes the Console shell. Unbranded it is http.ServeFile, byte for
// byte what the build produced; branded it is the rewrite above, produced per request
// because the file is served no-store anyway and `npm run dev` rebuilds it in place.
func serveConsoleIndex(w http.ResponseWriter, r *http.Request, cfg config) {
	idx := filepath.Join(cfg.consoleDir, "index.html")
	if !cfg.brand.active() {
		http.ServeFile(w, r, idx)
		return
	}
	src, err := os.ReadFile(idx)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	out, hits := cfg.brand.rewriteIndexHTML(src)
	if hits < brandIndexAnchors {
		brandDriftOnce.Do(func() {
			log.Printf("brand: only %d/%d anchors found in index.html — branding is incomplete",
				hits, brandIndexAnchors)
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(out)
}

// serveConsoleManifest writes the PWA manifest with the deployment's name and colour.
func serveConsoleManifest(w http.ResponseWriter, r *http.Request, cfg config) {
	src, err := os.ReadFile(filepath.Join(cfg.consoleDir, "manifest.webmanifest"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	out, err := cfg.brand.rewriteManifest(src)
	if err != nil {
		log.Printf("brand: manifest not branded (%v)", err)
		out = src
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
}

// --- colour maths ----------------------------------------------------------

func hexToHSL(hex string) (h, s, l float64) {
	var r, g, bl uint8
	if _, err := fmt.Sscanf(strings.TrimPrefix(hex, "#"), "%02x%02x%02x", &r, &g, &bl); err != nil {
		return 0, 0, 0
	}
	return rgbToHSL(r, g, bl)
}

func rgbToHSL(r8, g8, b8 uint8) (h, s, l float64) {
	r, g, b := float64(r8)/255, float64(g8)/255, float64(b8)/255
	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	l = (max + min) / 2
	d := max - min
	if d == 0 {
		return 0, 0, l
	}
	s = d / (1 - math.Abs(2*l-1))
	switch max {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h, s, l
}

func hslToRGB(h, s, l float64) (uint8, uint8, uint8) {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return to8(r + m), to8(g + m), to8(b + m)
}

func to8(v float64) uint8 { return uint8(math.Round(clamp01(v) * 255)) }

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }
