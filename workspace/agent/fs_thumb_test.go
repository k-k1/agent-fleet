package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Every test here must set HOME: the thumbnail cache lives under it, and a test that
// forgets writes into the developer's real ~/.cache/agent-fleet.
func thumbRoots(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	return root
}

// noisyPNG writes a PNG that does NOT compress away, so it clears thumbMinSourceBytes and
// is a realistic stand-in for a screenshot. A flat colour would encode to a few hundred
// bytes and silently take the "already small" fallback, testing nothing.
func noisyPNG(t *testing.T, path string, w, h int, alpha bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewPCG(1, 2))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if alpha && (x+y)%7 == 0 {
				a = 0
			}
			img.SetRGBA(x, y, color.RGBA{uint8(rng.UintN(256)), uint8(rng.UintN(256)), uint8(rng.UintN(256)), a})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if buf.Len() < thumbMinSourceBytes {
		t.Fatalf("fixture is only %d bytes — under thumbMinSourceBytes, the test would prove nothing", buf.Len())
	}
	return buf.Bytes()
}

func download(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handleFSDownload(rec, httptest.NewRequest("GET", "/api/fs/download?"+query, nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, query)
	}
	return rec
}

// The point of the whole feature: the card gets a fraction of the bytes, at the size it
// actually displays.
func TestFSDownloadThumbShrinksTheImage(t *testing.T) {
	root := thumbRoots(t)
	orig := noisyPNG(t, filepath.Join(root, "shot.png"), 800, 600, false)

	rec := download(t, "path=shot.png&thumb=64")
	body := rec.Body.Bytes()
	if len(body) >= len(orig) {
		t.Fatalf("thumbnail is %d bytes, original %d — no saving", len(body), len(orig))
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg for an opaque source", ct)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("thumbnail does not decode: %v", err)
	}
	// 800x600 at a 64 px long edge is an integer factor of 12.
	if format != "jpeg" || cfg.Width != 66 || cfg.Height != 50 {
		t.Errorf("thumbnail = %s %dx%d, want jpeg 66x50", format, cfg.Width, cfg.Height)
	}
}

// A JPEG would composite transparency onto black, so a source with an alpha channel has
// to come back as PNG even though it costs several times the bytes.
func TestFSDownloadThumbKeepsTransparencyAsPNG(t *testing.T) {
	root := thumbRoots(t)
	noisyPNG(t, filepath.Join(root, "cutout.png"), 800, 600, true)

	rec := download(t, "path=cutout.png&thumb=64")
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png for a source with alpha", ct)
	}
}

// The parameter is advisory. Each of these must answer with the original bytes rather
// than an error, which is what lets the Console ask for a thumbnail unconditionally.
func TestFSDownloadThumbFallsBackToTheOriginal(t *testing.T) {
	root := thumbRoots(t)
	big := noisyPNG(t, filepath.Join(root, "shot.png"), 800, 600, false)

	small := []byte("not really a png, but small")
	if err := os.WriteFile(filepath.Join(root, "small.png"), small, 0o600); err != nil {
		t.Fatal(err)
	}
	// Large enough to be worth scaling, but no decoder will touch it: the decode attempt
	// reads part of the file first, so the served bytes prove the fallback still hands
	// back the WHOLE original and not the tail of it.
	corrupt := bytes.Repeat([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 40<<10)
	if err := os.WriteFile(filepath.Join(root, "corrupt.png"), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	text := bytes.Repeat([]byte("shared notes\n"), 20<<10)
	if err := os.WriteFile(filepath.Join(root, "notes.md"), text, 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		query string
		want  []byte
	}{
		{"no decoder for the format", "path=notes.md&thumb=512", text},
		{"undecodable bytes", "path=corrupt.png&thumb=512", corrupt},
		{"already smaller than the target", "path=small.png&thumb=512", small},
		{"source below the size floor", "path=small.png&thumb=64", small},
		{"edge out of range", "path=shot.png&thumb=99999", big},
		{"edge not a number", "path=shot.png&thumb=big", big},
		{"no thumb asked for", "path=shot.png", big},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := download(t, c.query)
			if !bytes.Equal(rec.Body.Bytes(), c.want) {
				t.Fatalf("body = %d bytes, want the original %d", rec.Body.Len(), len(c.want))
			}
			if cd := rec.Header().Get("Content-Disposition"); !bytes.Contains([]byte(cd), []byte("attachment")) {
				t.Errorf("Content-Disposition = %q, want the original's attachment form", cd)
			}
		})
	}
}

// Decoding is ~100 ms and a multiple of the image in RAM, so the second card to ask for
// the same picture must not pay it again.
func TestFSDownloadThumbServesTheCache(t *testing.T) {
	root := thumbRoots(t)
	path := filepath.Join(root, "shot.png")
	noisyPNG(t, path, 800, 600, false)

	first := download(t, "path=shot.png&thumb=64").Body.Bytes()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	key := thumbCacheKey("shot.png", st.Size(), st.ModTime(), 64)
	cached, err := os.ReadFile(thumbCacheFile(key, "image/jpeg"))
	if err != nil {
		t.Fatalf("nothing cached under the file's identity: %v", err)
	}
	if !bytes.Equal(cached, first) {
		t.Fatal("cached bytes differ from what was served")
	}

	// Overwrite the entry: a second request that returns the sentinel proves the cache
	// is read, not just written (identical output would prove nothing — the encoder is
	// deterministic).
	sentinel := []byte("cached-thumbnail-bytes")
	if err := os.WriteFile(thumbCacheFile(key, "image/jpeg"), sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := download(t, "path=shot.png&thumb=64").Body.Bytes(); !bytes.Equal(got, sentinel) {
		t.Fatalf("second request re-decoded instead of reading the cache (%d bytes)", len(got))
	}

	// The key carries size and mtime, so a rewritten file must miss rather than serve
	// the previous picture.
	noisyPNG(t, path, 640, 480, false)
	if got := download(t, "path=shot.png&thumb=64").Body.Bytes(); bytes.Equal(got, sentinel) {
		t.Fatal("a rewritten file still served the stale cache entry")
	}
}

// Generated images are named by the tool, not the user, so two `shot.png` under different
// directories with the same size and mtime is an ordinary situation — and a cache keyed on
// the base name would show one card the other card's picture.
func TestFSDownloadThumbKeysOnTheWholePath(t *testing.T) {
	root := thumbRoots(t)
	for _, dir := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	noisyPNG(t, filepath.Join(root, "a", "shot.png"), 800, 600, false)
	// Deliberately a DIFFERENT picture at the same size and mtime.
	noisyPNG(t, filepath.Join(root, "b", "shot.png"), 640, 800, false)
	when := time.Unix(1_700_000_000, 0)
	for _, dir := range []string{"a", "b"} {
		if err := os.Chtimes(filepath.Join(root, dir, "shot.png"), when, when); err != nil {
			t.Fatal(err)
		}
	}
	// The identity that matters, held equal except for the directory: two files that
	// differ only there must not share an entry.
	if thumbCacheKey("a/shot.png", 4096, when, 512) == thumbCacheKey("b/shot.png", 4096, when, 512) {
		t.Fatal("cache key ignores the directory")
	}
	first := download(t, "path=a/shot.png&thumb=64").Body.Bytes()
	second := download(t, "path=b/shot.png&thumb=64").Body.Bytes()
	if bytes.Equal(first, second) {
		t.Fatal("two different pictures with the same base name served the same thumbnail")
	}
}

func TestThumbEdgeRejectsOutOfRange(t *testing.T) {
	cases := map[string]int{"": 0, "0": 0, "63": 0, "64": 64, "512": 512, "1024": 1024, "1025": 0, "-1": 0, "abc": 0, "512.5": 0}
	for in, want := range cases {
		if got := thumbEdge(in); got != want {
			t.Errorf("thumbEdge(%q) = %d, want %d", in, got, want)
		}
	}
}
