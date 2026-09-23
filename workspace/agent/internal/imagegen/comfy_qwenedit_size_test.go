package imagegen

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// The expected sizes were computed by upstream's own arithmetic in Python — `round()` included,
// which rounds half to even — so a mis-copied formula (axis order, rounding mode, the budget)
// fails here rather than as a soft picture on the engine.
func TestComfyQwenEditSizeMatchesUpstreamArithmetic(t *testing.T) {
	for _, c := range []struct{ w, h, wantW, wantH int }{
		{1024, 1024, 1024, 1024},
		{1248, 832, 1256, 840}, // FluxKontextImageScale's 3:2 entry is not a fixed point
		{1820, 1024, 1368, 768},
		{1496, 800, 1400, 752},
		{1920, 640, 1776, 592},
		{2048, 512, 2048, 512},
		{3819, 1000, 2016, 520}, // three steps: the slowest to settle between 1:4 and 4:1
		{1080, 1920, 768, 1368},
		{6000, 4000, 1256, 840},
		{4032, 3024, 1184, 888},
		{1392, 752, 1392, 752},
		{1400, 752, 1400, 752},
		{1264, 832, 1264, 832},
		{512, 512, 1024, 1024},
		{64, 4096, 128, 8192},
		{8192, 8192, 1024, 1024},
	} {
		w, h, ok := comfyQwenEditSize(c.w, c.h)
		if !ok || w != c.wantW || h != c.wantH {
			t.Errorf("comfyQwenEditSize(%d, %d) = %d, %d, %v; want %d, %d", c.w, c.h, w, h, ok, c.wantW, c.wantH)
		}
	}
}

// Over every ratio from 1:4 to 4:1 the answer is a real fixed point (the encoder would not resize
// it again) and the picture's aspect ratio moves by at most 1.6% — measured worst 1.52%, at 3.819.
func TestComfyQwenEditSizeIsAFixedPointThatKeepsTheRatio(t *testing.T) {
	worst := 0.0
	for _, h := range []int{480, 640, 768, 1000, 1024, 1080, 1200, 2048} {
		for w := h / 4; w <= h*4; w++ {
			fw, fh, ok := comfyQwenEditSize(w, h)
			if !ok {
				t.Fatalf("comfyQwenEditSize(%d, %d) did not settle", w, h)
			}
			if rw, rh := comfyQwenEditRefSize(fw, fh); rw != fw || rh != fh {
				t.Fatalf("comfyQwenEditSize(%d, %d) = %dx%d, which the encoder resizes again to %dx%d", w, h, fw, fh, rw, rh)
			}
			worst = math.Max(worst, math.Abs((float64(fw)/float64(fh))/(float64(w)/float64(h))-1))
		}
	}
	if worst > 0.016 {
		t.Errorf("worst aspect change = %.2f%%, want at most 1.6%%", worst*100)
	}
}

func TestComfyQwenEditSizeRefusesAMissingSide(t *testing.T) {
	for _, c := range [][2]int{{0, 0}, {0, 768}, {1024, 0}, {-1, 5}} {
		if _, _, ok := comfyQwenEditSize(c[0], c[1]); ok {
			t.Errorf("comfyQwenEditSize(%d, %d) answered ok for a picture with no size", c[0], c[1])
		}
	}
}

// The builder shrinks the whole picture to that size, and nothing in the graph crops. 1248x832 is
// the case that went wrong in production: FluxKontextImageScale handed the encoder exactly that.
func TestComfyWorkflowQwenImageEditShrinksToTheFixedPoint(t *testing.T) {
	for _, c := range comfyQwenEditFamilies {
		t.Run(string(c.family), func(t *testing.T) {
			p := comfyGoldenParams
			p.Op, p.Images, p.Width, p.Height = OpEdit, []string{"af-photo.png"}, 1248, 832
			g, err := comfyBuildGraph(c.family, c.files, p)
			if err != nil {
				t.Fatalf("comfyBuildGraph = %v", err)
			}
			in := g["scale"].Inputs
			if g["scale"].ClassType != "ImageScale" || in["width"] != 1256 || in["height"] != 840 ||
				in["crop"] != "disabled" || in["upscale_method"] != "lanczos" {
				t.Errorf("scale = %s%v, want ImageScale 1256x840, crop disabled, lanczos", g["scale"].ClassType, in)
			}
			for id, n := range g {
				if n.ClassType == "FluxKontextImageScale" {
					t.Errorf("%s is a FluxKontextImageScale: its table sizes are not the encoder's fixed points", id)
				}
			}
			for _, seam := range []struct{ node, input string }{{"enc", "pixels"}, {"pos", "image1"}, {"neg", "image1"}} {
				if got := g[seam.node].Inputs[seam.input]; got == nil || got.([]any)[0] != "scale" {
					t.Errorf("%s.%s = %v, want the scaled picture", seam.node, seam.input, got)
				}
			}
		})
	}
}

// An edit that reaches the builder without a size is refused, not shrunk to a guess.
func TestComfyWorkflowQwenImageEditRefusesAnEditWithNoSize(t *testing.T) {
	for _, c := range comfyQwenEditFamilies {
		p := comfyGoldenParams
		p.Op, p.Images, p.Width, p.Height = OpEdit, []string{"af-photo.png"}, 0, 0
		if _, err := comfyBuildGraph(c.family, c.files, p); err == nil {
			t.Errorf("%s built a graph for an edit with no picture size", c.family)
		}
	}
}

// jpegWithOrientation encodes a w x h JPEG and puts an EXIF APP1 segment carrying Orientation o
// straight after SOI, the way a phone camera writes one.
func jpegWithOrientation(t *testing.T, w, h, o int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var enc bytes.Buffer
	if err := jpeg.Encode(&enc, img, nil); err != nil {
		t.Fatal(err)
	}
	// Big-endian TIFF: header, IFD0 at offset 8 with one entry (0x0112, SHORT, 1, o), next IFD 0.
	var tiff bytes.Buffer
	tiff.WriteString("MM\x00\x2a")
	_ = binary.Write(&tiff, binary.BigEndian, uint32(8))
	_ = binary.Write(&tiff, binary.BigEndian, uint16(1))
	_ = binary.Write(&tiff, binary.BigEndian, []uint16{0x0112, 3})
	_ = binary.Write(&tiff, binary.BigEndian, uint32(1))
	_ = binary.Write(&tiff, binary.BigEndian, []uint16{uint16(o), 0})
	_ = binary.Write(&tiff, binary.BigEndian, uint32(0))
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	raw := enc.Bytes()
	out := append([]byte{}, raw[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, raw[2:]...)
}

// The size is the one ComfyUI's LoadImage sees, which applies the EXIF orientation. The PNG and
// WebP fixtures were written by PIL, and PIL's own ImageOps.exif_transpose turns each 40x24 into
// 24x40 — the same library LoadImage calls, so it is the reference for what "upright" means.
func TestComfyPictureSizeAppliesTheOrientationLoadImageApplies(t *testing.T) {
	for _, c := range []struct {
		name         string
		raw          []byte
		wantW, wantH int
	}{
		{"jpeg orientation 1", jpegWithOrientation(t, 64, 32, 1), 64, 32},
		{"jpeg orientation 3 (half turn, same shape)", jpegWithOrientation(t, 64, 32, 3), 64, 32},
		{"jpeg orientation 6 (phone portrait)", jpegWithOrientation(t, 64, 32, 6), 32, 64},
		{"jpeg orientation 8", jpegWithOrientation(t, 64, 32, 8), 32, 64},
		{"png no exif", tinyPNG(t, 40, 24), 40, 24},
		{"png eXIf orientation 6", readFixture(t, "orient6_40x24.png"), 24, 40},
		{"webp orientation 6", readFixture(t, "orient6_40x24.webp"), 24, 40},
		{"webp no exif", readFixture(t, "plain_40x24.webp"), 40, 24},
	} {
		w, h, ok := comfyPictureSize(c.raw)
		if !ok || w != c.wantW || h != c.wantH {
			t.Errorf("%s: comfyPictureSize = %d, %d, %v; want %d, %d", c.name, w, h, ok, c.wantW, c.wantH)
		}
	}
	if _, _, ok := comfyPictureSize([]byte("BM not a picture this package can decode")); ok {
		t.Error("comfyPictureSize answered ok for bytes no registered decoder reads")
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
