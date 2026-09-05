package main

// Downscaled previews for /fs/download (`&thumb=<max edge>`), so a card that shows a
// picture 190x240 CSS px wide does not pull the original bytes to get there. Measured on
// the images codex actually shares: a 1024x1536 / 3.2 MB PNG becomes a 341x512 / ~60 KB
// JPEG, and the mirror panel in the report that prompted this went from ~13 MB to ~0.4 MB.
//
// Rules that keep it safe to say yes to:
//
//   - The parameter is advisory. Anything that cannot be scaled — a format with no
//     decoder in the standard library, an image already small enough, a picture so large
//     that decoding it would cost this container real memory — serves the ORIGINAL bytes.
//     A caller therefore never has to ask whether a thumbnail is possible, and an older
//     Agent that ignores the parameter behaves like the fallback.
//   - Decoding is the expensive part (~100 ms and width*height*4 bytes for the RGBA), so
//     results go to a disk cache keyed by the file's identity and at most a couple of
//     decodes run at once. The host is shared and memory-constrained.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // decoder registration for image.Decode; gif is never written back
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Bounds on the requested edge. 512 is what the Console asks for; the range only
	// exists so a hand-written query cannot ask for a 1-pixel or a 4K "thumbnail".
	thumbMinEdge = 64
	thumbMaxEdge = 1024
	// Below this the original is already thumbnail-sized: decoding and re-encoding it
	// would spend ~100 ms of CPU to save a few KB.
	thumbMinSourceBytes = 128 << 10
	// Decoding is width*height*4 bytes of RGBA plus the decoder's own buffers. 40 MP is
	// ~160 MB and already generous for a shared screenshot; beyond it the original is
	// served rather than risk the container's memory cap on a preview.
	thumbMaxPixels = 40 << 20
	// JPEG quality for opaque sources. 80 is where the size curve flattens for the
	// screenshots and renders agents share.
	thumbJPEGQuality = 80
)

// thumbSem caps concurrent decodes. A transcript can mount a dozen cards at once and
// every one of them would otherwise decode in parallel on a memory-capped container.
var thumbSem = make(chan struct{}, 2)

// thumbEdge reads the `thumb` query value: the longest edge the caller wants, or 0 when
// it asked for none. An out-of-range or unparsable value is treated as "none" rather than
// an error — the parameter is advisory and a download must not fail over it.
func thumbEdge(raw string) int {
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < thumbMinEdge || n > thumbMaxEdge {
		return 0
	}
	return n
}

// thumbDecodable reports whether we have a decoder for this extension. Deliberately
// narrower than imageContentType: webp/avif/bmp/ico have no decoder in the standard
// library, and svg is text that the browser scales for free.
func thumbDecodable(name string) bool {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")) {
	case "png", "apng", "jpg", "jpeg", "jfif", "gif":
		return true
	}
	return false
}

// thumbnail returns the downscaled bytes for an already-opened file plus their content
// type. ok=false means "serve the original" and is the answer to every problem here —
// there is no error path a caller has to render.
//
// src is left with its offset wherever the decode attempt stopped; a caller that falls
// back needs no rewind, because http.ServeContent seeks for the size and back again.
func thumbnail(src io.ReadSeeker, display string, size int64, modTime time.Time, edge int) (out []byte, contentType string, ok bool) {
	if edge == 0 || !thumbDecodable(display) || size < thumbMinSourceBytes {
		return nil, "", false
	}
	// Keyed on the whole path, not the base name: two `shot.png` in different directories
	// with the same size and mtime are not far-fetched among generated images, and the
	// cache would hand one card the other's picture.
	key := thumbCacheKey(display, size, modTime, edge)
	if cached, ct, ok := readThumbCache(key); ok {
		return cached, ct, true
	}

	thumbSem <- struct{}{}
	defer func() { <-thumbSem }()

	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, "", false
	}
	cfg, _, err := image.DecodeConfig(src)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, "", false
	}
	if int64(cfg.Width)*int64(cfg.Height) > thumbMaxPixels {
		return nil, "", false
	}
	// Integer factor only: the box average below needs whole source pixels per output
	// pixel, and a factor of 1 means the picture is already at or below the target.
	factor := longEdge(cfg.Width, cfg.Height) / edge
	if factor < 2 {
		return nil, "", false
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, "", false
	}
	img, _, err := image.Decode(src)
	if err != nil {
		return nil, "", false
	}
	small := boxDownscale(img, factor)

	var buf bytes.Buffer
	if opaque(img) {
		if err := jpeg.Encode(&buf, small, &jpeg.Options{Quality: thumbJPEGQuality}); err != nil {
			return nil, "", false
		}
		contentType = "image/jpeg"
	} else {
		// PNG is 5-8x the JPEG for the same picture, but a JPEG would composite the
		// transparent parts onto black — a UI screenshot with a rounded corner or a
		// cut-out logo would come back visibly wrong.
		if err := png.Encode(&buf, small); err != nil {
			return nil, "", false
		}
		contentType = "image/png"
	}
	// A thumbnail bigger than the original helps nobody (a tiny palette PNG can encode
	// worse once it is RGBA).
	if int64(buf.Len()) >= size {
		return nil, "", false
	}
	writeThumbCache(key, contentType, buf.Bytes())
	return buf.Bytes(), contentType, true
}

func longEdge(w, h int) int {
	if w > h {
		return w
	}
	return h
}

// opaque reports whether every pixel is fully opaque. The standard library's image types
// all carry an Opaque() that answers it without a full scan where it can; anything exotic
// is treated as transparent, which only costs a larger PNG.
func opaque(img image.Image) bool {
	o, ok := img.(interface{ Opaque() bool })
	return ok && o.Opaque()
}

// boxDownscale averages each factor x factor block of source pixels into one output
// pixel. That is the cheap scaler that does not alias: dropping pixels (nearest
// neighbour) turns text and thin lines in a screenshot into noise, and a proper
// Catmull-Rom would mean a dependency (golang.org/x/image) for a difference nobody can
// see at 512 px. Rows are read through the concrete RGBA fast path where possible.
func boxDownscale(src image.Image, factor int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx()/factor, b.Dy()/factor
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	n := uint32(factor * factor)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sr, sg, sb, sa uint32
			for dy := 0; dy < factor; dy++ {
				for dx := 0; dx < factor; dx++ {
					r, g, bl, a := src.At(b.Min.X+x*factor+dx, b.Min.Y+y*factor+dy).RGBA()
					sr += r
					sg += g
					sb += bl
					sa += a
				}
			}
			// RGBA() returns 16-bit alpha-premultiplied values; >>8 narrows each
			// averaged channel back to the 8-bit premultiplied form image.RGBA holds.
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(sr / n >> 8),
				G: uint8(sg / n >> 8),
				B: uint8(sb / n >> 8),
				A: uint8(sa / n >> 8),
			})
		}
	}
	return dst
}

// ── disk cache ────────────────────────────────────────────────────────────────────────
//
// Keyed by the file's identity (path, size, mtime) and the requested edge, so a rewritten
// file simply misses. Entries are plain image files under ~/.cache/agent-fleet/thumbs,
// next to the memo images; losing the directory costs one re-decode.

func thumbCacheDir() string {
	return filepath.Join(homeDir(), ".cache", "agent-fleet", "thumbs")
}

func thumbCacheKey(display string, size int64, modTime time.Time, edge int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%d", display, size, modTime.UnixNano(), edge)))
	return hex.EncodeToString(sum[:])
}

// Extension carries the content type, so a cache hit does not have to sniff.
func thumbCacheFile(key, contentType string) string {
	ext := ".jpg"
	if contentType == "image/png" {
		ext = ".png"
	}
	return filepath.Join(thumbCacheDir(), key+ext)
}

func readThumbCache(key string) (data []byte, contentType string, ok bool) {
	for _, ct := range []string{"image/jpeg", "image/png"} {
		b, err := os.ReadFile(thumbCacheFile(key, ct))
		if err == nil && len(b) > 0 {
			return b, ct, true
		}
	}
	return nil, "", false
}

func writeThumbCache(key, contentType string, data []byte) {
	dir := thumbCacheDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".thumb-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return
	}
	if err := os.Rename(name, thumbCacheFile(key, contentType)); err != nil {
		os.Remove(name)
		return
	}
	sweepThumbCache(dir)
}

const (
	thumbCacheTTL   = 14 * 24 * time.Hour
	thumbSweepEvery = time.Hour
)

var thumbSweep struct {
	mu   sync.Mutex
	last time.Time
}

// sweepThumbCache drops entries nothing has asked for in a fortnight. Cheap hygiene, not
// a quota: the cache holds derived data only, so a miss is a re-decode and never a loss.
func sweepThumbCache(dir string) {
	thumbSweep.mu.Lock()
	if time.Since(thumbSweep.last) < thumbSweepEvery {
		thumbSweep.mu.Unlock()
		return
	}
	thumbSweep.last = time.Now()
	thumbSweep.mu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-thumbCacheTTL)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		os.Remove(filepath.Join(dir, e.Name()))
	}
}
