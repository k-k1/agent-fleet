package main

// Where the ~95 ms of a cold thumbnail actually goes (fs_thumb.go's warming notes give the
// total; these split it). The gallery warms a folder in the background, so this cost is only
// ever paid where warming did not get there first — which is exactly the first look at a folder
// nobody generated.
//
//	cd workspace/agent && go test -run '^$' -bench 'Thumb|Downscale' -benchmem ./...
//
// AF_BENCH_IMAGE=<file> swaps in a real picture (a generated PNG, a screenshot). The synthetic
// default is random noise: it cannot be compressed away, so it is a fair stand-in for the
// decode, and its pixel count is what the scaler's cost is made of.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"os"
	"testing"
	"time"
)

// benchSource returns the bytes to scale, and a name for the record.
func benchSource(b *testing.B) ([]byte, string) {
	b.Helper()
	if p := os.Getenv("AF_BENCH_IMAGE"); p != "" {
		raw, err := os.ReadFile(p)
		if err != nil {
			b.Fatalf("AF_BENCH_IMAGE: %v", err)
		}
		return raw, p
	}
	// 1024x1536 is the shape generate_image writes (ADR 0069), and the size fs_thumb.go's
	// own measurements were taken on.
	const w, h = 1024, 1536
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewPCG(1, 2))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(rng.UintN(256)), uint8(rng.UintN(256)), uint8(rng.UintN(256)), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		b.Fatal(err)
	}
	return buf.Bytes(), "synthetic 1024x1536 noise PNG"
}

// BenchmarkThumbnailCold is the whole job: decode, downscale, re-encode. The cache is bypassed
// by giving every iteration a different modTime, since the key is (path, size, mtime, edge).
func BenchmarkThumbnailCold(b *testing.B) {
	raw, name := benchSource(b)
	b.Setenv("HOME", b.TempDir())
	b.Logf("source: %s (%d bytes)", name, len(raw))
	for _, edge := range []int{256, 512} {
		b.Run("edge"+itoa(edge), func(b *testing.B) {
			var out int
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				mod := time.Unix(int64(1_700_000_000+i), 0)
				data, _, ok := thumbnail(bytes.NewReader(raw), "bench.png", int64(len(raw)), mod, edge, modeDownscale)
				if !ok {
					b.Fatal("thumbnail declined the source")
				}
				out = len(data)
			}
			b.ReportMetric(float64(out), "thumb_bytes")
		})
	}
}

// BenchmarkBoxDownscale is the scaler alone — the part that reads every source pixel.
func BenchmarkBoxDownscale(b *testing.B) {
	raw, _ := benchSource(b)
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		boxDownscale(img, 3)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d [8]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d[i:])
}
