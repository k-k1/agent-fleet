package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// --- a GGUF header, built byte by byte ----------------------------------------
//
// Built rather than checked in as a blob: the point of these tests is the LAYOUT (where the
// next key starts after a value of each type), and a blob proves that one file parses while
// hiding which rule made it parse.

type ggufKV struct {
	key   string
	typ   uint32
	value any // int for scalars, string for strings, ggufArr for arrays
}

type ggufArr struct {
	elemType uint32
	values   []any
}

func ggufStr(b *bytes.Buffer, s string) {
	_ = binary.Write(b, binary.LittleEndian, uint64(len(s)))
	b.WriteString(s)
}

func ggufBuild(t *testing.T, version uint32, kvs []ggufKV) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("GGUF")
	_ = binary.Write(&b, binary.LittleEndian, version)
	_ = binary.Write(&b, binary.LittleEndian, uint64(7)) // tensor count: skipped, any value
	_ = binary.Write(&b, binary.LittleEndian, uint64(len(kvs)))
	for _, kv := range kvs {
		ggufStr(&b, kv.key)
		_ = binary.Write(&b, binary.LittleEndian, kv.typ)
		ggufValue(t, &b, kv.typ, kv.value)
	}
	return b.Bytes()
}

func ggufValue(t *testing.T, b *bytes.Buffer, typ uint32, v any) {
	t.Helper()
	switch typ {
	case ggufTypeString:
		ggufStr(b, v.(string))
	case ggufTypeUint32:
		_ = binary.Write(b, binary.LittleEndian, uint32(v.(int)))
	case ggufTypeUint64:
		_ = binary.Write(b, binary.LittleEndian, uint64(v.(int)))
	case ggufTypeFloat32:
		_ = binary.Write(b, binary.LittleEndian, float32(1.5))
	case ggufTypeArray:
		a := v.(ggufArr)
		_ = binary.Write(b, binary.LittleEndian, a.elemType)
		_ = binary.Write(b, binary.LittleEndian, uint64(len(a.values)))
		for _, e := range a.values {
			ggufValue(t, b, a.elemType, e)
		}
	default:
		t.Fatalf("fixture does not know how to write type %d", typ)
	}
}

// qwen2Header is the shape measured on qwen2.5-coder-1.5b-instruct-q4_k_m.gguf: no declared
// key/value length, so the geometry has to be derived.
func qwen2Header(t *testing.T) []byte {
	return ggufBuild(t, 3, []ggufKV{
		{"general.architecture", ggufTypeString, "qwen2"},
		{"general.name", ggufTypeString, "Qwen2.5 Coder 1.5B Instruct"},
		{"qwen2.context_length", ggufTypeUint32, 32768},
		{"qwen2.embedding_length", ggufTypeUint32, 1536},
		{"qwen2.block_count", ggufTypeUint32, 28},
		{"qwen2.attention.head_count", ggufTypeUint32, 12},
		{"qwen2.attention.head_count_kv", ggufTypeUint32, 2},
		{"tokenizer.ggml.tokens", ggufTypeArray, ggufArr{ggufTypeString, []any{"a", "bb", "ccc"}}},
	})
}

// qwen3moeHeader is the shape measured on Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf. 🔴 It
// declares key/value length 128 while embedding_length / head_count is 2048 / 32 = 64 — the
// case that makes the derivation a FALLBACK rather than the rule.
func qwen3moeHeader(t *testing.T) []byte {
	return ggufBuild(t, 3, []ggufKV{
		{"general.architecture", ggufTypeString, "qwen3moe"},
		{"qwen3moe.context_length", ggufTypeUint32, 262144},
		{"qwen3moe.embedding_length", ggufTypeUint32, 2048},
		{"qwen3moe.block_count", ggufTypeUint32, 48},
		{"qwen3moe.attention.head_count", ggufTypeUint32, 32},
		{"qwen3moe.attention.head_count_kv", ggufTypeUint32, 4},
		{"qwen3moe.attention.key_length", ggufTypeUint32, 128},
		{"qwen3moe.attention.value_length", ggufTypeUint32, 128},
	})
}

func TestParseGGUFGeometryReadsBothShapes(t *testing.T) {
	got, err := parseGGUFGeometry(qwen2Header(t))
	if err != nil {
		t.Fatalf("qwen2: %v", err)
	}
	// 1536 / 12 = 128, derived because the file declares neither length. The ceiling rides along
	// on the same pass — it is not part of the cache arithmetic, but the bucket road has no other
	// way to learn it (engineGGUFGeometryOfObject).
	// PastArch: the fixture writes a tokenizer key, so the scan knows no architecture key is
	// still ahead — which is what makes a short read of this header trustworthy.
	want := engineKVGeometry{Layers: 28, HeadsKV: 2, KeyLen: 128, ValLen: 128, Ceiling: 32768, PastArch: true}
	if got != want {
		t.Errorf("qwen2 geometry = %+v, want %+v", got, want)
	}

	got, err = parseGGUFGeometry(qwen3moeHeader(t))
	if err != nil {
		t.Fatalf("qwen3moe: %v", err)
	}
	// 🔴 128, NOT 2048/32 = 64. Getting this wrong halves a 30B's KV estimate.
	// No PastArch: this fixture writes no tokenizer key, so the scan cannot know whether the
	// header had more to say. The ladder treats that as unsettled and reads further.
	want = engineKVGeometry{Layers: 48, HeadsKV: 4, KeyLen: 128, ValLen: 128, Ceiling: 262144}
	if got != want {
		t.Errorf("qwen3moe geometry = %+v, want %+v", got, want)
	}
}

// The declared length must beat the derivable one even when both are present, which is the
// regression the measurement above exists to prevent. Deriving first would read 64 here.
func TestParseGGUFGeometryPrefersTheDeclaredLength(t *testing.T) {
	g, err := parseGGUFGeometry(qwen3moeHeader(t))
	if err != nil {
		t.Fatal(err)
	}
	if g.KeyLen == 2048/32 {
		t.Fatal("key_length was derived from embedding_length/head_count although the file declares it")
	}
}

// A window that ends mid-structure is SHORT, never "no geometry": the caller retries a short
// read with a bigger window and gives up on anything else. Reporting the two alike would make a
// truncated read look like a model that does not declare its geometry.
func TestParseGGUFGeometryShortWindowIsRetryable(t *testing.T) {
	full := qwen3moeHeader(t)
	for _, n := range []int{4, 12, 24, 40, len(full) - 3} {
		_, err := parseGGUFGeometry(full[:n])
		if !errors.Is(err, errGGUFShort) {
			t.Errorf("prefix of %d bytes: err = %v, want errGGUFShort", n, err)
		}
	}
}

func TestParseGGUFGeometryRefusesWhatIsNotOne(t *testing.T) {
	if _, err := parseGGUFGeometry([]byte("NOPE\x03\x00\x00\x00")); err == nil {
		t.Error("a file that does not start with GGUF was accepted")
	}
	// v1 counted with uint32 and would mis-parse under this reader rather than fail.
	v1 := qwen2Header(t)
	v1[4] = 1
	if _, err := parseGGUFGeometry(v1); err == nil || errors.Is(err, errGGUFShort) {
		t.Errorf("version 1 err = %v, want a plain refusal", err)
	}
}

// A header that names no architecture, or names one and then declares nothing, answers
// "unknown" rather than a partial product. ADR 0074 decision 6: an unknown demand is never
// drawn as a fit.
func TestParseGGUFGeometryIncompleteIsAnError(t *testing.T) {
	buf := ggufBuild(t, 3, []ggufKV{
		{"general.architecture", ggufTypeString, "llama"},
		{"llama.block_count", ggufTypeUint32, 32},
		// no head_count_kv, no lengths, nothing to derive from
	})
	if _, err := parseGGUFGeometry(buf); err == nil {
		t.Error("a header with only block_count was accepted")
	}
}

// The token array is stepped over, not read — the reason the window can stay at 64 KiB while a
// real tokenizer section runs to megabytes. If skipArray got the width wrong, the keys after it
// would be garbage and this would fail.
func TestParseGGUFGeometrySkipsPastArraysAndFloats(t *testing.T) {
	buf := ggufBuild(t, 3, []ggufKV{
		{"general.architecture", ggufTypeString, "qwen2"},
		{"tokenizer.ggml.tokens", ggufTypeArray, ggufArr{ggufTypeString, []any{"x", "yy"}}},
		{"tokenizer.ggml.scores", ggufTypeArray, ggufArr{ggufTypeFloat32, []any{0, 0, 0}}},
		{"qwen2.rope.freq_base", ggufTypeFloat32, 0},
		{"qwen2.block_count", ggufTypeUint32, 28},
		{"qwen2.embedding_length", ggufTypeUint32, 1536},
		{"qwen2.attention.head_count", ggufTypeUint32, 12},
		{"qwen2.attention.head_count_kv", ggufTypeUint32, 2},
	})
	got, err := parseGGUFGeometry(buf)
	if err != nil {
		t.Fatal(err)
	}
	if want := (engineKVGeometry{Layers: 28, HeadsKV: 2, KeyLen: 128, ValLen: 128, PastArch: true}); got != want {
		t.Errorf("geometry = %+v, want %+v", got, want)
	}
}

// --- the estimate --------------------------------------------------------------

// 🔴 The two expectations below are what llama.cpp actually printed on the deployment's
// g6.xlarge (ADR 0074, "未解決 7 の llm 側を実機で押した"). They are the positive control for
// the formula: a change that keeps the unit tests above green and breaks the arithmetic shows
// up here as a disagreement with a real `KV self size` line.
func TestEngineKVCacheMiBMatchesTheMeasuredKVSelfSize(t *testing.T) {
	cases := []struct {
		name string
		g    engineKVGeometry
		ctx  int
		want int // MiB, as llama.cpp reported it
	}{
		{"qwen2.5-coder-1.5b at 16384", engineKVGeometry{Layers: 28, HeadsKV: 2, KeyLen: 128, ValLen: 128}, 16384, 448},
		{"qwen3-coder-30b-a3b at 32768", engineKVGeometry{Layers: 48, HeadsKV: 4, KeyLen: 128, ValLen: 128}, 32768, 3072},
	}
	for _, c := range cases {
		if got := engineKVCacheMiB(c.g, c.ctx); got != c.want {
			t.Errorf("%s: KV = %d MiB, want %d", c.name, got, c.want)
		}
	}
}

// Undeclared context is not "zero KV", it is "no answer" — and the caller must keep the row at
// its floor rather than adding 0 and calling the result complete.
func TestEngineKVCacheMiBNeedsBothHalves(t *testing.T) {
	full := engineKVGeometry{Layers: 28, HeadsKV: 2, KeyLen: 128, ValLen: 128}
	if got := engineKVCacheMiB(full, 0); got != 0 {
		t.Errorf("no context: %d, want 0", got)
	}
	if got := engineKVCacheMiB(engineKVGeometry{Layers: 28}, 16384); got != 0 {
		t.Errorf("partial geometry: %d, want 0", got)
	}
}

// --- the hybrid modifiers ------------------------------------------------------

// 🔴 The positive control for the correction, and the reason it exists: these are the numbers
// af-sandbox's llama.cpp printed on 2026-09-18 for Qwen3.8-27B (block_count 65,
// nextn_predict_layers 1, full_attention_interval 4). Started with --ctx-size 262144 it asked
// the CUDA backend for `allocating 16384.00 MiB` and named the failure `failed to allocate
// buffer for kv cache`. Multiplying by all 65 blocks says 66560 — four times the truth, which
// is what had the panel warn about windows that fit and the class ladder reach for a bigger
// card than the model needs.
func TestEngineKVCacheMiBCountsOnlyTheCachingLayers(t *testing.T) {
	qwen35 := engineKVGeometry{
		Layers: 65, HeadsKV: 4, KeyLen: 256, ValLen: 256,
		NextN: 1, FullAttnInterval: 4,
	}
	if got, want := qwen35.cacheLayers(), 16; got != want {
		t.Fatalf("cacheLayers = %d, want %d ((65-1)/4)", got, want)
	}
	for _, c := range []struct{ ctx, want int }{
		{262144, 16384}, // the measured allocation, to the MiB
		{131072, 8192},
		{65536, 4096},
		{32768, 2048},
	} {
		if got := engineKVCacheMiB(qwen35, c.ctx); got != c.want {
			t.Errorf("KV at %d = %d MiB, want %d", c.ctx, got, c.want)
		}
	}
	// And the shape of the bug, stated as a number so a regression cannot pass quietly.
	dense := engineKVGeometry{Layers: 65, HeadsKV: 4, KeyLen: 256, ValLen: 256}
	if got := engineKVCacheMiB(dense, 262144); got != 66560 {
		t.Errorf("without the modifiers KV = %d MiB, want the old 66560", got)
	}
}

// Both modifiers are optional, and a row that has neither — every dense model, and every row
// written before these columns existed — must estimate exactly as it did before.
func TestEngineKVCacheMiBModifiersAreOptionalAndGuarded(t *testing.T) {
	base := engineKVGeometry{Layers: 32, HeadsKV: 8, KeyLen: 128, ValLen: 128}
	if got, want := base.cacheLayers(), 32; got != want {
		t.Errorf("no modifiers: cacheLayers = %d, want %d", got, want)
	}
	// An interval of 1 is "every layer", not a division by one that reads as special.
	one := base
	one.FullAttnInterval = 1
	if got := one.cacheLayers(); got != 32 {
		t.Errorf("interval 1: cacheLayers = %d, want 32", got)
	}
	// Nonsense must not turn an over-estimate into a zero: a header claiming more prediction
	// heads than it has blocks is ignored rather than subtracted.
	for _, n := range []int{32, 33, -1} {
		bad := base
		bad.NextN = n
		if got := bad.cacheLayers(); got != 32 {
			t.Errorf("nextn %d: cacheLayers = %d, want the unmodified 32", n, got)
		}
	}
}

// The two modifiers are written AFTER attention.value_length in a real header, so a reader that
// stopped as soon as the four required fields were in hand never saw them — which is exactly
// how a hybrid model came out looking dense. Key order here is Qwen3.8-27B's own.
func TestParseGGUFGeometryReadsPastTheRequiredFields(t *testing.T) {
	buf := ggufBuild(t, 3, []ggufKV{
		{"general.architecture", ggufTypeString, "qwen35"},
		{"qwen35.block_count", ggufTypeUint32, 65},
		{"qwen35.attention.head_count_kv", ggufTypeUint32, 4},
		{"qwen35.attention.key_length", ggufTypeUint32, 256},
		{"qwen35.attention.value_length", ggufTypeUint32, 256},
		{"qwen35.nextn_predict_layers", ggufTypeUint32, 1},
		{"qwen35.full_attention_interval", ggufTypeUint32, 4},
	})
	got, err := parseGGUFGeometry(buf)
	if err != nil {
		t.Fatal(err)
	}
	want := engineKVGeometry{Layers: 65, HeadsKV: 4, KeyLen: 256, ValLen: 256, NextN: 1, FullAttnInterval: 4}
	if got != want {
		t.Errorf("geometry = %+v, want %+v", got, want)
	}
}

// Reading on past the required fields means the scan walks into the tokenizer array, which no
// window ever contains. parseGGUFGeometry reports that as errGGUFShort WITH whatever it managed
// to read, and the LADDER decides — because "the four required fields" is not the same as "the
// whole header", and treating it as such is how a hybrid model comes back looking dense.
func TestParseGGUFGeometryShortWindowKeepsWhatItRead(t *testing.T) {
	buf := ggufBuild(t, 3, []ggufKV{
		{"general.architecture", ggufTypeString, "qwen2"},
		{"qwen2.block_count", ggufTypeUint32, 28},
		{"qwen2.attention.head_count_kv", ggufTypeUint32, 2},
		{"qwen2.attention.key_length", ggufTypeUint32, 128},
		{"qwen2.attention.value_length", ggufTypeUint32, 128},
		{"tokenizer.ggml.tokens", ggufTypeArray, ggufArr{ggufTypeString, []any{"x", "yy"}}},
	})
	got, err := parseGGUFGeometry(buf[:len(buf)-6])
	if !errors.Is(err, errGGUFShort) {
		t.Fatalf("err = %v, want errGGUFShort", err)
	}
	if want := (engineKVGeometry{Layers: 28, HeadsKV: 2, KeyLen: 128, ValLen: 128, PastArch: true}); got != want {
		t.Errorf("geometry = %+v, want %+v (what it read, alongside the error)", got, want)
	}
}

// 🔴 The regression the ladder exists to stop. Between attention.value_length and
// full_attention_interval there are more keys, and a header whose general.* strings are long
// enough pushes the modifiers past the first window. Settling for a COMPLETE-but-unmodified
// geometry there prices the cache four times too high — the exact bug this pass fixes, reached
// through the error path instead of the happy one.
func TestEngineGGUFGeometryLadderTriesTheBiggerWindowForTheModifiers(t *testing.T) {
	full := ggufBuild(t, 3, []ggufKV{
		{"general.architecture", ggufTypeString, "qwen35"},
		{"qwen35.block_count", ggufTypeUint32, 65},
		{"qwen35.attention.head_count_kv", ggufTypeUint32, 4},
		{"qwen35.attention.key_length", ggufTypeUint32, 256},
		{"qwen35.attention.value_length", ggufTypeUint32, 256},
		{"qwen35.nextn_predict_layers", ggufTypeUint32, 1},
		{"qwen35.full_attention_interval", ggufTypeUint32, 4},
		{"qwen35.context_length", ggufTypeUint32, 262144},
	})
	// Where the first window lands: after value_length, before the modifiers.
	cut := bytes.Index(full, []byte("qwen35.nextn_predict_layers"))
	if cut <= 0 {
		t.Fatal("could not place the cut")
	}
	windows := []int{}
	got, err := engineGGUFGeometryFrom(func(window int) ([]byte, error) {
		windows = append(windows, window)
		if window == ggufHeadWindow {
			return full[:cut], nil
		}
		return full, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 2 {
		t.Fatalf("windows tried = %v, want both — a complete-but-unmodified read is not the end", windows)
	}
	if got.NextN != 1 || got.FullAttnInterval != 4 || got.Ceiling != 262144 {
		t.Fatalf("geometry = %+v, want the modifiers the second window carries", got)
	}
	// And the number that rides on them: 16 caching layers, not 64.
	if kv := engineKVCacheMiB(got, 262144); kv != 16384 {
		t.Errorf("KV = %d MiB, want the measured 16384", kv)
	}
}

// 🔴 Rewritten after review round 3, which pointed out that the previous version of this test
// fixed the WRONG behaviour as correct: it asserted that a short read of an unsettled header was
// a success. Four numbers in hand is not "the header had nothing else to say" — the optional
// modifiers come after them — so the two cases have to be told apart, and PastArch is what tells
// them apart.
func TestEngineGGUFGeometryLadderTrustsOnlyASettledRead(t *testing.T) {
	// Settled: a tokenizer key went by, so no architecture key is still ahead. Short here costs
	// nothing, and it costs no SECOND read either.
	settled := ggufBuild(t, 3, []ggufKV{
		{"general.architecture", ggufTypeString, "qwen2"},
		{"qwen2.block_count", ggufTypeUint32, 28},
		{"qwen2.attention.head_count_kv", ggufTypeUint32, 2},
		{"qwen2.attention.key_length", ggufTypeUint32, 128},
		{"qwen2.attention.value_length", ggufTypeUint32, 128},
		{"tokenizer.ggml.tokens", ggufTypeArray, ggufArr{ggufTypeString, []any{"x", "yy"}}},
	})
	reads := 0
	got, err := engineGGUFGeometryFrom(func(int) ([]byte, error) {
		reads++
		return settled[:len(settled)-6], nil
	})
	if err != nil {
		t.Fatalf("settled but short: err = %v, want nil", err)
	}
	if got.Layers != 28 || !got.PastArch {
		t.Errorf("geometry = %+v", got)
	}
	if reads != 1 {
		t.Errorf("reads = %d, want 1 — a settled read needs no bigger window", reads)
	}

	// UNSETTLED: the four are in hand and the window ended inside the architecture block. There
	// may be a modifier past it and there is no way to know, so both windows short is a refusal
	// rather than a four-times-too-high answer stored as `weights_kv`.
	unsettled := ggufBuild(t, 3, []ggufKV{
		{"general.architecture", ggufTypeString, "qwen35"},
		{"qwen35.block_count", ggufTypeUint32, 65},
		{"qwen35.attention.head_count_kv", ggufTypeUint32, 4},
		{"qwen35.attention.key_length", ggufTypeUint32, 256},
		{"qwen35.attention.value_length", ggufTypeUint32, 256},
		{"qwen35.nextn_predict_layers", ggufTypeUint32, 1},
		{"qwen35.full_attention_interval", ggufTypeUint32, 4},
	})
	cut := bytes.Index(unsettled, []byte("qwen35.nextn_predict_layers"))
	if cut <= 0 {
		t.Fatal("could not place the cut")
	}
	if _, err := engineGGUFGeometryFrom(func(int) ([]byte, error) { return unsettled[:cut], nil }); !errors.Is(err, errGGUFShort) {
		t.Errorf("both windows unsettled: err = %v, want errGGUFShort", err)
	}

	// And the case review round 3 named: the first window is complete-but-unsettled and the
	// SECOND read fails outright — a timeout or an AccessDenied, which is an ordinary transient.
	// Salvaging the first read there stores the unmodified shape as authoritative.
	n := 0
	if _, err := engineGGUFGeometryFrom(func(int) ([]byte, error) {
		n++
		if n == 1 {
			return unsettled[:cut], nil
		}
		return nil, errors.New("AccessDenied")
	}); err == nil {
		t.Error("a failed second read let an unsettled first read through")
	}

	// Never complete at all stays a refusal, as before.
	early := ggufBuild(t, 3, []ggufKV{
		{"general.architecture", ggufTypeString, "qwen2"},
		{"qwen2.block_count", ggufTypeUint32, 28},
	})
	if _, err := engineGGUFGeometryFrom(func(int) ([]byte, error) { return early[:len(early)-4], nil }); !errors.Is(err, errGGUFShort) {
		t.Errorf("never complete: err = %v, want errGGUFShort", err)
	}

	// A file that is not a GGUF at all is refused on the FIRST window: reading more of it cannot
	// turn it into one, and a second range GET is a round trip spent on nothing.
	reads = 0
	if _, err := engineGGUFGeometryFrom(func(int) ([]byte, error) {
		reads++
		return []byte("not a gguf at all, really"), nil
	}); err == nil || errors.Is(err, errGGUFShort) {
		t.Errorf("not a GGUF: err = %v, want a plain refusal", err)
	}
	if reads != 1 {
		t.Errorf("reads = %d, want 1", reads)
	}
}

// 🔴 Found by re-review. A header this reader cannot walk to the end of is one whose optional
// modifiers may sit past the point it gave up — so a geometry salvaged from the SMALLER window
// would be stored as `weights_kv`, which reads as authoritative, while possibly being the
// four-times-too-high form. Refuse, and let the row say `floor` out loud instead.
func TestEngineGGUFGeometryLadderRefusesAHeaderItCannotFinish(t *testing.T) {
	// First window: the four are in hand and NOT settled — no tokenizer key went by, so the
	// ladder goes on to the second. (A settled first read would return there and this test would
	// never reach the corruption.)
	unsettled := ggufBuild(t, 3, []ggufKV{
		{"general.architecture", ggufTypeString, "qwen2"},
		{"qwen2.block_count", ggufTypeUint32, 28},
		{"qwen2.attention.head_count_kv", ggufTypeUint32, 2},
		{"qwen2.attention.key_length", ggufTypeUint32, 128},
		{"qwen2.attention.value_length", ggufTypeUint32, 128},
		{"qwen2.rope.freq_base", ggufTypeUint32, 7},
	})
	// Second: the same header with a value type this reader does not know sitting after the
	// four, which is a file it cannot walk.
	corrupt := ggufBuild(t, 3, []ggufKV{
		{"general.architecture", ggufTypeString, "qwen2"},
		{"qwen2.block_count", ggufTypeUint32, 28},
		{"qwen2.attention.head_count_kv", ggufTypeUint32, 2},
		{"qwen2.attention.key_length", ggufTypeUint32, 128},
		{"qwen2.attention.value_length", ggufTypeUint32, 128},
		{"qwen2.rope.freq_base", ggufTypeUint32, 0},
	})
	// Bend the last key's type number to one the spec does not define. The fixture will not
	// write such a thing, and that is the point: it is what a damaged file looks like, and the
	// reader's answer to an unknown width is to stop, because it no longer knows where the next
	// key begins.
	at := bytes.LastIndex(corrupt, []byte("qwen2.rope.freq_base"))
	if at < 0 {
		t.Fatal("could not place the corruption")
	}
	corrupt[at+len("qwen2.rope.freq_base")] = 99
	n := 0
	got, err := engineGGUFGeometryFrom(func(int) ([]byte, error) {
		n++
		if n == 1 {
			return unsettled[:len(unsettled)-4], nil
		}
		return corrupt, nil
	})
	if err == nil {
		t.Fatalf("a header that cannot be finished came back as success: %+v", got)
	}
	if errors.Is(err, errGGUFShort) {
		t.Errorf("err = %v, want a plain refusal rather than the retryable one", err)
	}
	if got.complete() {
		t.Errorf("geometry = %+v, want nothing stored from a file this reader gave up on", got)
	}
}
