package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func imageSizeRequest(t *testing.T, paths ...string) map[string]fsImageSize {
	t.Helper()
	body, _ := json.Marshal(fsImageSizeRequest{Paths: paths})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/fs/imagesize", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handleFSImageSize(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out fsImageSizeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Sizes
}

func writeEncoded(t *testing.T, path string, enc func(io.Writer, image.Image) error, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.SetRGBA(0, 0, color.RGBA{1, 2, 3, 255})
	var buf bytes.Buffer
	if err := enc(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A minimal lossless WebP (VP8L) header: the size lives in 14-bit fields after the signature,
// and DecodeConfig reads nothing past them.
func writeWebP(t *testing.T, path string, w, h int) {
	t.Helper()
	bits := uint32(w-1) | uint32(h-1)<<14
	vp8l := []byte{0x2f, byte(bits), byte(bits >> 8), byte(bits >> 16), byte(bits >> 24)}
	chunk := append([]byte("VP8L"), byte(len(vp8l)), 0, 0, 0)
	chunk = append(chunk, vp8l...)
	if len(vp8l)%2 == 1 {
		chunk = append(chunk, 0)
	}
	riff := append([]byte("RIFF"), 0, 0, 0, 0)
	riff = append(riff, "WEBP"...)
	riff = append(riff, chunk...)
	n := len(riff) - 8
	riff[4], riff[5], riff[6], riff[7] = byte(n), byte(n>>8), byte(n>>16), byte(n>>24)
	if err := os.WriteFile(path, riff, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The four formats the gallery lists, through the same paths a card sends.
func TestImageSizeReadsEachFormat(t *testing.T) {
	root := thumbRoots(t)
	if err := os.MkdirAll(filepath.Join(root, "gen"), 0o700); err != nil {
		t.Fatal(err)
	}
	noisyPNG(t, filepath.Join(root, "gen", "a.png"), 832, 1216, false)
	writeEncoded(t, filepath.Join(root, "gen", "b.jpg"), func(w io.Writer, m image.Image) error { return jpeg.Encode(w, m, nil) }, 640, 480)
	writeEncoded(t, filepath.Join(root, "gen", "c.gif"), func(w io.Writer, m image.Image) error { return gif.Encode(w, m, nil) }, 33, 17)
	writeWebP(t, filepath.Join(root, "gen", "d.webp"), 1024, 768)

	got := imageSizeRequest(t, "gen/a.png", "gen/b.jpg", "gen/c.gif", "gen/d.webp", filepath.Join(root, "gen", "a.png"))
	want := map[string]fsImageSize{
		"gen/a.png":                         {832, 1216},
		"gen/b.jpg":                         {640, 480},
		"gen/c.gif":                         {33, 17},
		"gen/d.webp":                        {1024, 768},
		filepath.Join(root, "gen", "a.png"): {832, 1216}, // keyed as the request spelled it
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %+v, want %+v", k, got[k], v)
		}
	}
}

// The read is the header and no more: a JPEG whose frame header sits behind more metadata
// than the bound answers unknown rather than reading on.
func TestImageSizeReadIsBounded(t *testing.T) {
	root := thumbRoots(t)
	var small bytes.Buffer
	if err := jpeg.Encode(&small, image.NewRGBA(image.Rect(0, 0, 40, 30)), nil); err != nil {
		t.Fatal(err)
	}
	src := small.Bytes() // SOI first, then the rest
	var padded bytes.Buffer
	padded.Write(src[:2])
	// APP segments (each <= 64 KiB) until the header is past the bound.
	seg := make([]byte, 0xfff0)
	for padded.Len() < imageSizeMaxHeader+0x10000 {
		padded.Write([]byte{0xff, 0xe1, byte((len(seg) + 2) >> 8), byte(len(seg) + 2)})
		padded.Write(seg)
	}
	padded.Write(src[2:])
	if err := os.WriteFile(filepath.Join(root, "deep.jpg"), padded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ok.jpg"), src, 0o600); err != nil {
		t.Fatal(err)
	}
	// Sanity: the unbounded read does find the size, so "absent" below is the bound talking.
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(padded.Bytes())); err != nil || cfg.Width != 40 {
		t.Fatalf("fixture: unbounded DecodeConfig = %+v, %v", cfg, err)
	}
	got := imageSizeRequest(t, "deep.jpg", "ok.jpg")
	if _, ok := got["deep.jpg"]; ok {
		t.Errorf("deep.jpg answered %+v: the read went past imageSizeMaxHeader", got["deep.jpg"])
	}
	if got["ok.jpg"] != (fsImageSize{40, 30}) {
		t.Errorf("ok.jpg = %+v", got["ok.jpg"])
	}
}

// No wider than a download: traversal, the denylist, symlinks, non-images and missing files
// are all simply absent.
func TestImageSizeKeepsTheDownloadGate(t *testing.T) {
	root := thumbRoots(t)
	outside := t.TempDir()
	noisyPNG(t, filepath.Join(outside, "secret.png"), 300, 200, false)
	for _, d := range []string{".ssh", "pics"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	noisyPNG(t, filepath.Join(root, ".ssh", "key.png"), 300, 200, false)
	noisyPNG(t, filepath.Join(root, "pics", "ok.png"), 300, 200, false)
	if err := os.Symlink(filepath.Join(outside, "secret.png"), filepath.Join(root, "pics", "link.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pics", "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(root, filepath.Join(outside, "secret.png"))
	got := imageSizeRequest(t,
		"pics/ok.png",
		rel, // ../…/secret.png
		"pics/../../"+filepath.Base(outside)+"/secret.png",
		filepath.Join(outside, "secret.png"), // absolute, outside every read root
		".ssh/key.png",
		"pics/link.png",
		"pics/notes.txt",
		"pics/missing.png",
	)
	if got["pics/ok.png"] != (fsImageSize{300, 200}) {
		t.Errorf("the control case did not answer: %+v", got)
	}
	for k, v := range got {
		if k != "pics/ok.png" {
			t.Errorf("%s answered %+v; it must be absent", k, v)
		}
	}
}

// One request answers at most imageSizeMaxPaths; the rest come back unknown rather than the
// request becoming an unbounded walk.
func TestImageSizeCapsThePathsPerRequest(t *testing.T) {
	root := thumbRoots(t)
	paths := make([]string, 0, imageSizeMaxPaths+20)
	for i := 0; i < imageSizeMaxPaths+20; i++ {
		name := "p" + strconv.Itoa(i) + ".gif"
		writeEncoded(t, filepath.Join(root, name), func(w io.Writer, m image.Image) error { return gif.Encode(w, m, nil) }, 8, 8)
		paths = append(paths, name)
	}
	got := imageSizeRequest(t, paths...)
	if len(got) != imageSizeMaxPaths {
		t.Errorf("%d paths answered, want exactly %d", len(got), imageSizeMaxPaths)
	}
	if _, ok := got[paths[len(paths)-1]]; ok {
		t.Errorf("a path past the cap was answered")
	}
}
