package imagegen

import "math"

// comfyQwenEditRefPixels is the pixel budget TextEncodeQwenImageEditPlus re-scales image1 to
// before it encodes the reference latent (ComfyUI v0.37.0 comfy_extras/nodes_qwen.py:
// `total = int(1024 * 1024)`).
const comfyQwenEditRefPixels = comfyQwenEditRefPixelsSide * comfyQwenEditRefPixelsSide

const comfyQwenEditRefPixelsSide = 1024

// comfyQwenEditMaxSteps bounds the fixed-point search. Every ratio from 1:64 to 64:1 settles in
// at most six steps; a search still moving after this many is refused rather than guessed.
const comfyQwenEditMaxSteps = 16

// comfyQwenEditRefSize is TextEncodeQwenImageEditPlus's own reference rescale, copied from
// comfy_extras/nodes_qwen.py (v0.37.0): scale to ~1 MP, each side rounded to a multiple of 8.
// Python's round() rounds half to even, hence math.RoundToEven, and the operations run in the
// same order — so that a pin bump changing upstream's arithmetic is a diff of this one function.
//
// 🔴 A ComfyUI bump has to re-read that function at both tags: nothing here fails when upstream
// changes it, and the pictures just come back soft again (see comfyQwenEditSize).
func comfyQwenEditRefSize(w, h int) (int, int) {
	s := math.Sqrt(float64(comfyQwenEditRefPixels) / float64(w*h))
	return int(math.RoundToEven(float64(w)*s/8.0)) * 8, int(math.RoundToEven(float64(h)*s/8.0)) * 8
}

// comfyQwenEditSize is the size an instruction-edit family's picture is shrunk to before it
// reaches the graph (ADR 0094, revision of 2026-09-23): the input's own size with the encoder's
// rounding applied until it stops moving. The picture is never cropped; its aspect ratio moves by
// at most ~1.5% for inputs from 1:4 to 4:1.
//
// Why a fixed point: the encoder re-scales image1 itself before making the reference latent, and
// when the size it is handed is not already its own answer, that second resize is an `area`
// re-sample at a scale near 1 — nearly every pixel averaged with its neighbour. Measured on one
// photo and seed (docs/log/112 §13): 1248x832, the size FluxKontextImageScale's table picks for
// 3:2, scored 0.41 on the gradient-energy ratio and 1264x832, a fixed point, 0.78; on another,
// 1400x752, a fixed point outside that table, scored 1.03 and 1424x752, not one, 0.89.
//
// ok is false when no size can be computed: a zero side, or a search that does not settle.
func comfyQwenEditSize(w, h int) (int, int, bool) {
	if w <= 0 || h <= 0 {
		return 0, 0, false
	}
	for range comfyQwenEditMaxSteps {
		nw, nh := comfyQwenEditRefSize(w, h)
		if nw <= 0 || nh <= 0 {
			return 0, 0, false
		}
		if nw == w && nh == h {
			return w, h, true
		}
		w, h = nw, nh
	}
	return 0, 0, false
}
