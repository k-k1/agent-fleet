package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- per-layer attention: GPT-OSS, gemma-4, LFM2 ---------------------------------
//
// The fixtures under testdata/gguf are the real headers of the three files the ADR 0093 trials
// ran, cut just past the first tokenizer key — the same place a production read runs out of
// window, so each one also proves the fold happens on a SHORT read. Real rather than built
// because the claim under test is "this is what llama.cpp allocated for that file", and a built
// header would only restate what the reader already believes.

func ggufFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "gguf", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ggufFixtureGeometry reads a fixture through the same two-window ladder the ingest and the
// bucket roads use.
func ggufFixtureGeometry(t *testing.T, name string) engineKVGeometry {
	t.Helper()
	b := ggufFixture(t, name)
	g, err := engineGGUFGeometryFrom(func(window int) ([]byte, error) {
		return b[:min(window, len(b))], nil
	})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return g
}

// 🔴 The positive control, against llama-server's own `llama_kv_cache: size = …` lines, measured
// 2026-09-28 on an RTX 5060 Ti at `-c 24576 -ngl 99 --jinja` with the server's default four
// slots over one unified cache:
//
//	gemma-4-12b-it Q4_K_M   non-SWA 24576 cells  8 layers  384.00 MiB   SWA 4608 cells 40 layers 1440.00 MiB
//	gpt-oss-20b MXFP4       non-SWA 24576 cells 12 layers  576.00 MiB   SWA 1024 cells 12 layers   24.00 MiB
//	LFM2.5-8B-A1B Q4_K_M    24576 cells 6 layers 288.00 MiB (18 convolution layers hold 1.12 MiB of state)
//
// Before this reader, gemma-4 and LFM2.5 had no geometry at all (enabling them answered 409
// engine_vram_confirm) and gpt-oss was sized at 1152 MiB, every layer counted as full.
func TestEngineKVCacheMiBMatchesLlamaCppPerLayer(t *testing.T) {
	cases := []struct {
		file            string
		full, swa       int // MiB at 24576, each half as llama.cpp printed it
		per1k, fixedMiB int // what the panel is sent
	}{
		{"gemma4-12b.gguf.head", 384, 1440, 16, 1440},
		{"gpt-oss-20b.gguf.head", 576, 24, 24, 24},
		{"lfm2moe-8b-a1b.gguf.head", 288, 0, 12, 0},
	}
	for _, c := range cases {
		g := ggufFixtureGeometry(t, c.file)
		if !g.layered() {
			t.Fatalf("%s: read without per-layer widths: %+v", c.file, g)
		}
		fullOnly, swaOnly := g, g
		fullOnly.SWAWidth, swaOnly.FullWidth = 0, 0
		if got := engineKVCacheMiB(fullOnly, 24576); got != c.full {
			t.Errorf("%s: full layers %d MiB, llama.cpp allocated %d", c.file, got, c.full)
		}
		if c.swa > 0 {
			if got := engineKVCacheMiB(swaOnly, 24576); got != c.swa {
				t.Errorf("%s: sliding layers %d MiB, llama.cpp allocated %d", c.file, got, c.swa)
			}
		}
		if got := engineKVCacheMiB(g, 24576); got != c.full+c.swa {
			t.Errorf("%s: KV %d MiB, llama.cpp allocated %d", c.file, got, c.full+c.swa)
		}
		per1k, fixed := engineKVPricing(g)
		if per1k != c.per1k || fixed != c.fixedMiB {
			t.Errorf("%s: pricing %d/1k + %d, want %d/1k + %d", c.file, per1k, fixed, c.per1k, c.fixedMiB)
		}
	}
}

// The layer counts behind those numbers, so a regression names the rule it broke rather than a
// MiB figure.
func TestParseGGUFGeometryFoldsTheRealHeaders(t *testing.T) {
	gemma := ggufFixtureGeometry(t, "gemma4-12b.gguf.head")
	// 8 full layers of 1 head x (512+512), 40 sliding of 8 x (256+256).
	if gemma.FullWidth != 8*1*1024 || gemma.SWAWidth != 40*8*512 || gemma.SlidingWindow != 1024 {
		t.Errorf("gemma-4: %+v", gemma)
	}
	// The per-layer head count stands in for the scalar as its largest entry.
	if gemma.HeadsKV != 8 || gemma.Ceiling != 262144 {
		t.Errorf("gemma-4 scalars: %+v", gemma)
	}
	// No pattern in the header: llama.cpp's default for the architecture, every other layer.
	oss := ggufFixtureGeometry(t, "gpt-oss-20b.gguf.head")
	if oss.FullWidth != 12*8*128 || oss.SWAWidth != 12*8*128 || oss.SlidingWindow != 128 {
		t.Errorf("gpt-oss: %+v", oss)
	}
	// Convolution layers declare 0 KV heads and hold no cache.
	lfm := ggufFixtureGeometry(t, "lfm2moe-8b-a1b.gguf.head")
	if lfm.FullWidth != 6*8*128 || lfm.SWAWidth != 0 {
		t.Errorf("lfm2: %+v", lfm)
	}
}

// 🔴 The negative control for the pattern table: the same gpt-oss header under an architecture
// the table does not know is counted as all-full — the old, safe over-estimate — which is also
// what shows the table entry is the thing that halves it.
func TestGGUFLayerWidthsUnknownPatternCountsEveryLayerFull(t *testing.T) {
	oss := ggufFixtureGeometry(t, "gpt-oss-20b.gguf.head")
	full, swa, err := ggufLayerWidths("some-future-arch", oss, ggufLayerKeys{})
	if err != nil {
		t.Fatal(err)
	}
	if full != 24*8*128 || swa != 0 {
		t.Errorf("unknown arch: full %d swa %d, want every layer full", full, swa)
	}
	oss.FullWidth, oss.SWAWidth = full, swa
	if got := engineKVCacheMiB(oss, 24576); got != 1152 {
		t.Errorf("unknown arch: %d MiB, want the all-full 1152", got)
	}
	// And a declared scalar period stands in for the table.
	full, swa, _ = ggufLayerWidths("some-future-arch", oss, ggufLayerKeys{swaEvery: 2})
	if full != 12*8*128 || swa != 12*8*128 {
		t.Errorf("declared period 2: full %d swa %d", full, swa)
	}
}

// A row stored before the widths existed keeps the formula it was sized by.
func TestEngineKVCacheMiBRowWithoutWidthsKeepsTheOldFormula(t *testing.T) {
	oss := ggufFixtureGeometry(t, "gpt-oss-20b.gguf.head")
	oss.FullWidth, oss.SWAWidth, oss.SlidingWindow = 0, 0, 0
	if got := engineKVCacheMiB(oss, 24576); got != 1152 {
		t.Errorf("legacy row: %d MiB, want 1152", got)
	}
	if per1k, fixed := engineKVPricing(oss); per1k != 48 || fixed != 0 {
		t.Errorf("legacy row pricing: %d/1k + %d, want 48 + 0", per1k, fixed)
	}
}

// The other rules llama.cpp's loader applies, on built headers since no fixture exercises them.
func TestGGUFLayerWidthsRules(t *testing.T) {
	base := engineKVGeometry{Layers: 6, HeadsKV: 2, KeyLen: 64, ValLen: 64, SlidingWindow: 512}
	per := 2 * 128
	cases := []struct {
		name      string
		g         engineKVGeometry
		arch      string
		k         ggufLayerKeys
		full, swa int
	}{
		// gemma-3n / small gemma-4: the last shared_kv_layers reuse an earlier cache.
		{"shared kv", base, "", ggufLayerKeys{swaPattern: []int{1, 1, 0, 1, 1, 0}, sharedKV: 2}, 1 * per, 3 * per},
		// Multi-token-prediction heads are never run.
		{"nextn", engineKVGeometry{Layers: 6, HeadsKV: 2, KeyLen: 64, ValLen: 64, NextN: 2}, "", ggufLayerKeys{}, 4 * per, 0},
		// dense_first: layer 0 of every period is full (llama_hparams::set_swa_pattern).
		{"dense first", base, "", ggufLayerKeys{swaEvery: 3}, 2 * per, 4 * per},
		// A pattern without a window is not a sliding cache.
		{"no window", engineKVGeometry{Layers: 6, HeadsKV: 2, KeyLen: 64, ValLen: 64}, "gemma3", ggufLayerKeys{}, 6 * per, 0},
		// Sliding layers take the _swa head widths where declared.
		{"swa widths", base, "", ggufLayerKeys{swaPattern: []int{1, 0, 1, 0, 1, 0}, keyLenSWA: 32, valLenSWA: 32}, 3 * per, 3 * 2 * 64},
		// Qwen3.5's hybrids through the same fold: every 3rd layer caches.
		{"interval", engineKVGeometry{Layers: 6, HeadsKV: 2, KeyLen: 64, ValLen: 64, FullAttnInterval: 3}, "", ggufLayerKeys{}, 2 * per, 0},
	}
	for _, c := range cases {
		if c.name == "dense first" {
			// Through the table, since a scalar period carries no dense_first flag.
			ggufSWAPeriodForTest(t, "dense-first-arch", 3, true)
			c.arch, c.k.swaEvery = "dense-first-arch", 0
		}
		full, swa, err := ggufLayerWidths(c.arch, c.g, c.k)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if full != c.full || swa != c.swa {
			t.Errorf("%s: full %d swa %d, want %d and %d", c.name, full, swa, c.full, c.swa)
		}
	}
}

func ggufSWAPeriodForTest(t *testing.T, arch string, every int, denseFirst bool) {
	t.Helper()
	ggufSWAPeriod[arch] = struct {
		every      int
		denseFirst bool
	}{every, denseFirst}
	t.Cleanup(func() { delete(ggufSWAPeriod, arch) })
}

// A per-layer array that does not cover every block is refused, not padded: the missing layers'
// cache would be a guess. Refused as a NON-short error, so the ladder does not store a salvage.
func TestParseGGUFGeometryRefusesAMisSizedLayerArray(t *testing.T) {
	heads := make([]any, 5)
	for i := range heads {
		heads[i] = 8
	}
	buf := ggufBuild(t, 3, []ggufKV{
		{"general.architecture", ggufTypeString, "gemma4"},
		{"gemma4.block_count", ggufTypeUint32, 6},
		{"gemma4.attention.head_count_kv", ggufTypeArray, ggufArr{ggufTypeUint32, heads}},
		{"gemma4.attention.key_length", ggufTypeUint32, 64},
		{"gemma4.attention.value_length", ggufTypeUint32, 64},
		{"tokenizer.ggml.model", ggufTypeString, "x"},
	})
	_, err := engineGGUFGeometryFrom(func(int) ([]byte, error) { return buf, nil })
	if err == nil || errors.Is(err, errGGUFShort) || !strings.Contains(err.Error(), "5 entries for 6 blocks") {
		t.Errorf("mis-sized array: %v", err)
	}
}

// The sliding cache is sized per slot plus the micro-batch, then padded — which is why
// gpt-oss's 128-token window is 1024 cells and not 128.
func TestEngineSWACells(t *testing.T) {
	for _, c := range []struct{ window, ctx, want int }{
		{128, 24576, 1024},  // measured
		{1024, 24576, 4608}, // measured
		{1024, 2000, 2048},  // a window smaller than the cap, padded
		{1024, 0, 4608},     // the cap alone
		{4096, 8192, 8192},  // the window is the ceiling
	} {
		if got := engineSWACells(c.window, c.ctx); got != c.want {
			t.Errorf("window %d ctx %d: %d cells, want %d", c.window, c.ctx, got, c.want)
		}
	}
}

// The same refusal when the window ends inside the vocabulary — the normal way a production read
// finishes. Masked as short, the ladder would have stored the geometry without its widths.
func TestParseGGUFGeometryFoldErrorPastTheArchKeysIsNotShort(t *testing.T) {
	heads := make([]any, 5)
	for i := range heads {
		heads[i] = 8
	}
	buf := ggufBuild(t, 3, []ggufKV{
		{"general.architecture", ggufTypeString, "gemma4"},
		{"gemma4.block_count", ggufTypeUint32, 6},
		{"gemma4.attention.head_count_kv", ggufTypeArray, ggufArr{ggufTypeUint32, heads}},
		{"gemma4.attention.key_length", ggufTypeUint32, 64},
		{"gemma4.attention.value_length", ggufTypeUint32, 64},
		{"tokenizer.ggml.tokens", ggufTypeArray, ggufArr{ggufTypeString, []any{"a", "bb", "ccc", "dddd"}}},
	})
	cut := buf[:len(buf)-6] // inside the token array
	g, err := engineGGUFGeometryFrom(func(int) ([]byte, error) { return cut, nil })
	if err == nil || errors.Is(err, errGGUFShort) || g.complete() {
		t.Errorf("fold error past the arch keys: %+v %v", g, err)
	}
}

// The panel multiplies per1k by the window, so it is rounded UP: a width of 1,000 elements is
// 1.95 MiB per 1k, and truncated to 1 it priced 262,144 tokens at half their cost.
func TestEngineKVPricingRoundsUp(t *testing.T) {
	g := engineKVGeometry{Layers: 1, HeadsKV: 1, KeyLen: 500, ValLen: 500, FullWidth: 1000}
	per1k, _ := engineKVPricing(g)
	if per1k != 2 {
		t.Fatalf("per1k = %d, want 2 (1.95 rounded up)", per1k)
	}
	if exact := engineKVCacheMiB(g, 262144); per1k*256 < exact {
		t.Errorf("per1k x 256 = %d under-states the %d MiB cache", per1k*256, exact)
	}
	// The row stored before the widths, through the old formula: rounded up the same way.
	legacy := g
	legacy.FullWidth = 0
	if per1k, _ := engineKVPricing(legacy); per1k != 2 {
		t.Errorf("legacy per1k = %d, want 2", per1k)
	}
}
