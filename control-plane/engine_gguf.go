package main

// engine_gguf.go — the attention geometry a KV-cache estimate needs, read from a model's own
// GGUF header (ADR 0074 open question 7).
//
// Why this file exists at all: the VRAM answer used to be the weights and nothing else
// (`engineVramFloor`), and for the llm role the weights are not the big half — a 30B at 32k
// context wants 3 GiB of KV cache on top. The numbers that decide it are not derivable from
// anything the Control Plane already holds:
//
//   - Hugging Face's API does NOT publish them. `gguf` carries `total`, `architecture`,
//     `context_length` and the chat template, and a GGUF-only repository's `config` is `{}`
//     (measured 2026-09-11 on Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF). So the file itself is the
//     only source.
//   - There are two copies to read and this file reads both. The one at the SOURCE, over the
//     same HTTP the resolve already uses (engineGGUFGeometry), and the one already in this
//     deployment's BUCKET, through engineStorageMetadataPort.Prefix
//     (engineGGUFGeometryOfObject). The second road was missing until 2026-09-19, and its
//     absence is why every llm row on both deployments answered `vram_need_source: floor` — the
//     weights and nothing else — which fits almost any card and so let a row declaring 262,144
//     tokens be switched on without a word.
//
// It is stored on the row, and read at most once per loading write — at registration, or by
// engineAdminAPI.healGeometry for a row that has none. A header read per panel refresh would put
// a network call on a screen that lists every model, and the geometry of a file identified by
// sha256 cannot change under us.
//
// ⚠️ "at most once" and not "exactly once", and the difference is a supported-input assumption
// worth naming: healGeometry decides a row has been read by looking at `context_ceiling` (and at
// the per-layer widths, so a row read before them is read once more), so a
// header that declares no `<arch>.context_length` is re-read on every loading write. llama.cpp's
// converter always writes one, which is why this is accepted rather than paid for with another
// column — but a file from some other writer would be re-read, at up to two prefix reads a time,
// on every enable. It is bounded by an operator's own action and never by a poll.
//
// 🔴 What cannot be read here is the KV cache's element type: `-ctk`/`-ctv` live in
// `LlmExtraArgs`, a CloudFormation parameter that reaches the task definition and never the
// engine table. f16 is assumed. A deployment running a quantised KV cache is therefore
// OVER-estimated, which is the safe direction for "will this fit on that card" and the wrong
// one for "how much am I wasting" — say so rather than inventing a field the table has no way
// to fill.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The GGUF v3 metadata value types, in the spec's order.
const (
	ggufTypeUint8 uint32 = iota
	ggufTypeInt8
	ggufTypeUint16
	ggufTypeInt16
	ggufTypeUint32
	ggufTypeInt32
	ggufTypeFloat32
	ggufTypeBool
	ggufTypeString
	ggufTypeArray
	ggufTypeUint64
	ggufTypeInt64
	ggufTypeFloat64
)

// ggufHeadWindow is how much of the file is fetched on the first try, and ggufHeadMax the most
// that is ever fetched.
//
// Measured 2026-09-11 on the two models this deployment serves: every field below sits in the
// first **545 bytes** of qwen2.5-coder-1.5b-instruct-q4_k_m.gguf and the first **1,426** of
// Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf. 64 KiB is ~45x the worse of the two, and the second
// window exists only so that a file which happens to put a long `general.*` string first is not
// abandoned. Neither is "read until it parses": the tokenizer's token array runs to megabytes
// and is never needed, so an unbounded read would download it for nothing.
const (
	ggufHeadWindow = 64 << 10
	ggufHeadMax    = 1 << 20
)

// errGGUFShort is "the window ended mid-structure" — the one error worth retrying with a bigger
// window, and the one that must never be confused with "this is not a GGUF file".
var errGGUFShort = errors.New("gguf: header window is short")

// engineKVGeometry is what a KV-cache estimate is computed from. The first four are required:
// a partially read header answers nothing, because the product of four numbers with one
// missing is not a smaller estimate, it is a wrong one.
//
// The last two are OPTIONAL modifiers, and 0 means "this architecture does not have one" —
// which is the same answer as a row read before they were parsed, and reduces to the plain
// `every layer caches every token` formula either way.
type engineKVGeometry struct {
	Layers  int // <arch>.block_count
	HeadsKV int // <arch>.attention.head_count_kv
	KeyLen  int // <arch>.attention.key_length, else embedding_length / head_count
	ValLen  int // <arch>.attention.value_length, else the same fallback

	// NextN is <arch>.nextn_predict_layers: multi-token-prediction heads. They are counted in
	// block_count and carry a full set of attention tensors, but llama.cpp does not run them —
	// it says so, once per tensor, as `model has unused tensor blk.<n>.nextn.* -- ignoring`.
	// Measured on Qwen3.8-27B: block_count 65, nextn_predict_layers 1, and the ignored block is
	// blk.64. Counting it overstates the cache by one layer in sixty-five.
	NextN int
	// FullAttnInterval is <arch>.full_attention_interval: in a hybrid model only every Nth
	// layer is full attention and holds a cache that grows with the context. The rest are
	// recurrent (the same header declares <arch>.ssm.*), and their state is a fixed size per
	// sequence rather than per token — so they do not belong in a number that is multiplied by
	// the window.
	//
	// 🔴 This is the difference between an estimate that is right and one that is four times
	// too big, which is not a rounding error when it decides whether a window fits on a card.
	FullAttnInterval int

	// FullWidth and SWAWidth are the model's cache per token, in elements, summed layer by
	// layer: Σ n_head_kv(il) × (key_length(il) + value_length(il)) over the layers that cache
	// every token of the window, and over the sliding-window layers whose cache is capped by
	// SlidingWindow (<arch>.attention.sliding_window). Layers with no KV heads — the short
	// convolutions of LFM2, the recurrent layers of a hybrid — and layers that borrow another
	// layer's cache contribute to neither.
	//
	// They exist because GPT-OSS, gemma-4 and LFM2 mix layer kinds with different head counts
	// and head widths, which the four numbers above cannot describe: gemma-4-12b's 40
	// sliding layers carry 8 heads of 256 and its 8 full layers 1 head of 512. See
	// ggufLayerWidths for the rules, all taken from llama.cpp's own loader.
	//
	// Non-zero on every header this reader completes. Both zero means a row read before they
	// existed, and engineKVCacheMiB falls back to the four-number formula for it.
	FullWidth, SWAWidth, SlidingWindow int

	// Ceiling is `<arch>.context_length`: the largest window the model was TRAINED for. Not part
	// of the cache arithmetic at all — it is read here because it is in the same header, on the
	// same pass, and because the bucket road (engineGGUFGeometryOfObject) has no other way to
	// learn it. 🔴 A ceiling, not a setting.
	Ceiling int

	// PastArch is "a tokenizer key went by", i.e. every `<arch>.*` key this header holds has
	// already been seen. It is what makes a short read trustworthy: complete() says the four
	// required numbers are in hand, and only this says nothing OPTIONAL was left behind the end
	// of the window. Not stored on the row — it describes the read, not the model.
	PastArch bool
}

func (g engineKVGeometry) complete() bool {
	return g.Layers > 0 && g.HeadsKV > 0 && g.KeyLen > 0 && g.ValLen > 0
}

// settled is complete AND known to have nothing optional still ahead of it — the only state in
// which a short read may be stored. See PastArch.
func (g engineKVGeometry) settled() bool { return g.complete() && g.PastArch }

// cacheLayers is how many of block_count actually hold a per-token KV cache.
//
// Verified against llama.cpp itself (af-sandbox, 2026-09-18): Qwen3.8-27B started with
// --ctx-size 262144 asked the CUDA backend for `allocating 16384.00 MiB` and named the failure
// `failed to allocate buffer for kv cache`. (65-1)/4 = 16 layers x 4 kv heads x (256+256) x
// 262144 x 2 B is 16384.00 MiB exactly — the plain block_count form says 66560.
func (g engineKVGeometry) cacheLayers() int {
	n := g.Layers
	// Guarded rather than trusted: a header claiming more prediction heads than it has blocks
	// is nonsense, and subtracting it would turn an over-estimate into a zero.
	if g.NextN > 0 && g.NextN < g.Layers {
		n -= g.NextN
	}
	if g.FullAttnInterval > 1 {
		// Integer division on purpose: llama.cpp makes layer i full when (i+1) % interval == 0,
		// so out of n layers exactly floor(n/interval) of them cache.
		n /= g.FullAttnInterval
	}
	return n
}

// ggufReader walks a byte window, refusing to run off the end rather than panicking.
type ggufReader struct {
	b []byte
	o int
}

func (r *ggufReader) take(n int) ([]byte, error) {
	if n < 0 || r.o+n > len(r.b) {
		return nil, errGGUFShort
	}
	v := r.b[r.o : r.o+n]
	r.o += n
	return v, nil
}

func (r *ggufReader) u32() (uint32, error) {
	b, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func (r *ggufReader) u64() (uint64, error) {
	b, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}

// str reads a GGUF string. The length is bounded before it is used as one: a corrupt or
// misaligned read otherwise asks for a multi-gigabyte slice, and `take` would answer "short"
// for a file that is not short at all.
func (r *ggufReader) str() (string, error) {
	n, err := r.u64()
	if err != nil {
		return "", err
	}
	if n > uint64(len(r.b)) {
		return "", errGGUFShort
	}
	b, err := r.take(int(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ggufFixedWidth is the byte width of the scalar types; 0 means "not a scalar".
func ggufFixedWidth(t uint32) int {
	switch t {
	case ggufTypeUint8, ggufTypeInt8, ggufTypeBool:
		return 1
	case ggufTypeUint16, ggufTypeInt16:
		return 2
	case ggufTypeUint32, ggufTypeInt32, ggufTypeFloat32:
		return 4
	case ggufTypeUint64, ggufTypeInt64, ggufTypeFloat64:
		return 8
	}
	return 0
}

// intValue reads one metadata value and returns it as an int when it is an integer type. The
// bool is "this was a number"; strings, arrays and floats are skipped over correctly and
// reported as not-a-number rather than guessed at.
func (r *ggufReader) intValue(t uint32) (int, bool, error) {
	if w := ggufFixedWidth(t); w > 0 {
		b, err := r.take(w)
		if err != nil {
			return 0, false, err
		}
		switch t {
		case ggufTypeUint8, ggufTypeBool:
			return int(b[0]), t == ggufTypeUint8, nil
		case ggufTypeInt8:
			return int(int8(b[0])), true, nil
		case ggufTypeUint16:
			return int(binary.LittleEndian.Uint16(b)), true, nil
		case ggufTypeInt16:
			return int(int16(binary.LittleEndian.Uint16(b))), true, nil
		case ggufTypeUint32:
			return int(binary.LittleEndian.Uint32(b)), true, nil
		case ggufTypeInt32:
			return int(int32(binary.LittleEndian.Uint32(b))), true, nil
		case ggufTypeUint64, ggufTypeInt64:
			return int(binary.LittleEndian.Uint64(b)), true, nil
		}
		return 0, false, nil // float32/float64: skipped, not a count
	}
	switch t {
	case ggufTypeString:
		_, err := r.str()
		return 0, false, err
	case ggufTypeArray:
		return 0, false, r.skipArray()
	}
	// An unknown type cannot be skipped, because its width is what tells us where the next key
	// starts. Stopping is the only honest answer.
	return 0, false, fmt.Errorf("gguf: unknown value type %d", t)
}

// skipArray steps over an array without materialising it. The token array is the reason: it
// holds every vocabulary entry, runs to megabytes, and is never read here.
func (r *ggufReader) skipArray() error {
	et, err := r.u32()
	if err != nil {
		return err
	}
	n, err := r.u64()
	if err != nil {
		return err
	}
	if w := ggufFixedWidth(et); w > 0 {
		if n > uint64(len(r.b)) {
			return errGGUFShort
		}
		_, err := r.take(w * int(n))
		return err
	}
	if et == ggufTypeString {
		for i := uint64(0); i < n; i++ {
			if _, err := r.str(); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("gguf: unsupported array element type %d", et)
}

// ggufMaxLayerArray bounds a per-layer array read into memory. The largest block_count in
// circulation is in the low hundreds, so anything past this is not a per-layer array.
const ggufMaxLayerArray = 4096

// intArray reads an array of integers or booleans — the per-layer keys. Any other element type
// is not a per-layer count and is refused rather than skipped, because the caller asked for it
// by name.
func (r *ggufReader) intArray() ([]int, error) {
	et, err := r.u32()
	if err != nil {
		return nil, err
	}
	n, err := r.u64()
	if err != nil {
		return nil, err
	}
	if n > ggufMaxLayerArray {
		return nil, fmt.Errorf("gguf: a per-layer array of %d entries", n)
	}
	if et == ggufTypeFloat32 || et == ggufTypeFloat64 || ggufFixedWidth(et) == 0 {
		return nil, fmt.Errorf("gguf: a per-layer array of element type %d", et)
	}
	out := make([]int, n)
	for i := range out {
		v, _, err := r.intValue(et)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// parseGGUFGeometry reads the header out of a prefix of a GGUF file: every `<arch>.*` key, which
// llama.cpp's converter writes ahead of the tokenizer, so the common case never touches most of
// the window.
func parseGGUFGeometry(buf []byte) (engineKVGeometry, error) {
	r := &ggufReader{b: buf}
	magic, err := r.take(4)
	if err != nil {
		return engineKVGeometry{}, err
	}
	if string(magic) != "GGUF" {
		return engineKVGeometry{}, errors.New("gguf: not a GGUF file")
	}
	version, err := r.u32()
	if err != nil {
		return engineKVGeometry{}, err
	}
	// v2 and v3 share this layout (v3 only added new value types). v1 counted with uint32 and
	// is refused rather than mis-parsed — llama.cpp has not written one since 2023.
	if version < 2 || version > 3 {
		return engineKVGeometry{}, fmt.Errorf("gguf: unsupported version %d", version)
	}
	if _, err := r.u64(); err != nil { // tensor count, not needed
		return engineKVGeometry{}, err
	}
	kvCount, err := r.u64()
	if err != nil {
		return engineKVGeometry{}, err
	}

	var arch string
	var geom engineKVGeometry
	var layers ggufLayerKeys
	var embedding, heads int
	// 🔴 The marker that says a short read is nevertheless a COMPLETE one. llama.cpp's converter
	// writes `general.*`, then every `<arch>.*` key, then `tokenizer.*` — so once a tokenizer key
	// has gone past, no architecture key is still coming and running out of window afterwards
	// costs nothing. Without it there is no way to tell "the header had no modifiers" from "the
	// window ended before them", and the two have to be told apart: the second, taken for the
	// first, stores a four-times-too-high cache estimate that reads as authoritative.
	//
	// 🔴 A short window ends the scan and does NOT end the parse. The scan walks on into
	// `tokenizer.ggml.tokens`, megabytes long, which no window this file fetches contains — so
	// ending there is the normal way a good read finishes, and what was read still has to be
	// folded. The error travels with the answer, so engineGGUFGeometryFrom can tell a read that
	// ended past the architecture keys (PastArch) from one that ended inside them.
	scan := func() error {
		for i := uint64(0); i < kvCount; i++ {
			key, err := r.str()
			if err != nil {
				return err
			}
			// 🔴 Set from the KEY, before the value is parsed. The value that follows the first
			// tokenizer key is the vocabulary array, which is exactly the thing no window contains —
			// so a marker set after parsing it would never be set at all.
			if strings.HasPrefix(key, "tokenizer.") {
				geom.PastArch = true
			}
			t, err := r.u32()
			if err != nil {
				return err
			}
			// The architecture names every other key, so it has to be read before they can be
			// recognised. llama.cpp writes it first; if some writer does not, the keys simply are
			// not matched and the row stays unknown.
			if key == "general.architecture" {
				if t != ggufTypeString {
					return errors.New("gguf: general.architecture is not a string")
				}
				if arch, err = r.str(); err != nil {
					return err
				}
				continue
			}
			field := ""
			if arch != "" && strings.HasPrefix(key, arch+".") {
				field = strings.TrimPrefix(key, arch+".")
			}
			// The two keys a model may write once per layer. Read into memory only for these: every
			// other array is skipped, and the one that matters for that is the vocabulary.
			if t == ggufTypeArray && (field == "attention.head_count_kv" || field == "attention.sliding_window_pattern") {
				vals, err := r.intArray()
				if err != nil {
					return err
				}
				if field == "attention.head_count_kv" {
					layers.headsKV = vals
				} else {
					layers.swaPattern = vals
				}
				continue
			}
			n, isInt, err := r.intValue(t)
			if err != nil {
				return err
			}
			if !isInt || field == "" {
				continue
			}
			switch field {
			case "block_count":
				geom.Layers = n
			case "attention.head_count_kv":
				geom.HeadsKV = n
			case "attention.key_length":
				geom.KeyLen = n
			case "attention.value_length":
				geom.ValLen = n
			case "embedding_length":
				embedding = n
			case "attention.head_count":
				heads = n
			case "nextn_predict_layers":
				geom.NextN = n
			case "full_attention_interval":
				geom.FullAttnInterval = n
			case "context_length":
				geom.Ceiling = n
			case "attention.sliding_window":
				geom.SlidingWindow = n
			case "attention.sliding_window_pattern":
				layers.swaEvery = n
			case "attention.key_length_swa":
				layers.keyLenSWA = n
			case "attention.value_length_swa":
				layers.valLenSWA = n
			case "attention.shared_kv_layers":
				layers.sharedKV = n
			}
			// 🔴 No early return on complete(). The two modifiers are written AFTER
			// attention.value_length (measured on Qwen3.8-27B: value_length is key 28 of 50 and
			// full_attention_interval is key 36), so stopping at the four required fields is
			// exactly how a hybrid model came out looking like a dense one.
		}
		return nil
	}
	scanErr := scan()
	if scanErr != nil && !errors.Is(scanErr, errGGUFShort) {
		return geom, scanErr
	}
	// 🔴 The fallback is `embedding_length / head_count`, and it is ONLY a fallback. Measured
	// 2026-09-11: qwen3moe declares key_length = value_length = 128 while embedding_length /
	// head_count is 2048 / 32 = 64, so deriving it would have halved a 30B's KV estimate. The
	// declared value wins wherever a model bothers to state one.
	if heads > 0 && embedding > 0 {
		if geom.KeyLen == 0 {
			geom.KeyLen = embedding / heads
		}
		if geom.ValLen == 0 {
			geom.ValLen = embedding / heads
		}
	}
	// A per-layer head count stands in for the scalar the four-number formula needs, so that
	// complete() keeps meaning "the attention shape was declared". Its largest entry, because
	// that formula is only the fallback for a row stored before the widths were — it must not
	// come out SMALLER than the truth.
	for _, h := range layers.headsKV {
		geom.HeadsKV = max(geom.HeadsKV, h)
	}
	if !geom.complete() {
		if scanErr != nil {
			return geom, scanErr
		}
		return geom, errors.New("gguf: the header does not declare the attention geometry")
	}
	// 🔴 A window that ended INSIDE the architecture keys may still be missing a per-layer key,
	// so it goes back as short and the ladder tries the bigger window. One that ended past them
	// (PastArch) has seen everything the fold uses: a fold error there is the header's own, and
	// masking it as short would let the ladder store the geometry without its widths.
	if scanErr != nil && !geom.PastArch {
		return geom, scanErr
	}
	full, swa, err := ggufLayerWidths(arch, geom, layers)
	if err != nil {
		return geom, err
	}
	geom.FullWidth, geom.SWAWidth = full, swa
	return geom, scanErr
}

// ggufLayerKeys is what a header says about individual layers, collected during the scan and
// only interpreted at its end: gemma-4 writes head_count_kv as key 23 and the sliding pattern
// that gives it meaning as key 33.
type ggufLayerKeys struct {
	headsKV    []int // <arch>.attention.head_count_kv when written per layer; 0 = no KV cache
	swaPattern []int // <arch>.attention.sliding_window_pattern as an array; non-zero = sliding
	swaEvery   int   // the same key as a scalar period
	keyLenSWA  int   // <arch>.attention.key_length_swa
	valLenSWA  int   // <arch>.attention.value_length_swa
	sharedKV   int   // <arch>.attention.shared_kv_layers: trailing layers that reuse a cache
}

// ggufSWAPeriod is llama.cpp's default sliding-window layout for architectures whose header
// declares a window but no pattern — GPT-OSS is one: `sliding_window = 128` and nothing about
// which layers use it. Each entry mirrors the `load_swa_pattern(ml, n[, dense_first])` call in
// llama.cpp's src/models/<arch>.cpp, and the layout is llama_hparams::set_swa_pattern: layer il
// slides when il % n < n-1, or, dense first, when il % n != 0.
//
// 🔴 Only architectures whose cache is the plain sliding kind belong here. One missing is safe
// — its layers are all counted as full, an over-estimate — but a wrong entry under-states a
// cache, which is how a window that does not fit gets switched on. Chunked attention (llama4)
// and phi3's declared-but-unused window are deliberately absent.
var ggufSWAPeriod = map[string]struct {
	every      int
	denseFirst bool
}{
	"gpt-oss": {2, false}, // measured 2026-09-28: 12 of 24 layers over 1024 cells, 24.00 MiB
	"gemma2":  {2, false},
	"gemma3":  {6, false},
	"cohere2": {4, false},
	"exaone4": {4, false},
}

// ggufLayerWidths folds a header into FullWidth and SWAWidth, following llama.cpp's loader:
//
//   - the layers that run: block_count less nextn_predict_layers
//   - a layer with no KV heads caches nothing (LFM2's convolutions), and neither does a layer
//     that full_attention_interval makes recurrent (Qwen3.5's hybrids)
//   - the last shared_kv_layers reuse an earlier layer's cache (gemma-3n and the small gemma-4s)
//   - a layer slides when the pattern says so, and uses key_length_swa / value_length_swa
//     where the header declares them
//
// Measured against llama-server on 2026-09-28 (-c 24576, four slots): gemma-4-12b 8 full
// layers at 384.00 MiB and 40 sliding at 1440.00, gpt-oss-20b 12 and 12 at 576.00 and 24.00,
// LFM2.5-8B-A1B 6 attention layers at 288.00 — each what engineKVCacheMiB computes from these.
func ggufLayerWidths(arch string, g engineKVGeometry, k ggufLayerKeys) (full, swa int, err error) {
	// A per-layer array that does not cover every block is a header this reader does not
	// understand, and guessing the missing layers would be guessing the cache.
	if k.headsKV != nil && len(k.headsKV) != g.Layers {
		return 0, 0, fmt.Errorf("gguf: head_count_kv has %d entries for %d blocks", len(k.headsKV), g.Layers)
	}
	if k.swaPattern != nil && len(k.swaPattern) != g.Layers {
		return 0, 0, fmt.Errorf("gguf: sliding_window_pattern has %d entries for %d blocks", len(k.swaPattern), g.Layers)
	}
	n := g.Layers
	if g.NextN > 0 && g.NextN < n {
		n -= g.NextN
	}
	cached := n
	if k.sharedKV > 0 && k.sharedKV < n {
		cached = n - k.sharedKV
	}
	period, known := ggufSWAPeriod[arch]
	if k.swaEvery > 0 {
		period.every, known = k.swaEvery, true
	}
	slides := func(il int) bool {
		switch {
		case g.SlidingWindow <= 0:
			return false
		case k.swaPattern != nil:
			return k.swaPattern[il] != 0
		case !known:
			return false
		case period.denseFirst:
			return il%period.every != 0
		}
		return il%period.every < period.every-1
	}
	for il := 0; il < cached; il++ {
		if g.FullAttnInterval > 1 && (il+1)%g.FullAttnInterval != 0 {
			continue
		}
		heads := g.HeadsKV
		if k.headsKV != nil {
			heads = k.headsKV[il]
		}
		if heads <= 0 {
			continue
		}
		if slides(il) {
			kl, vl := g.KeyLen, g.ValLen
			if k.keyLenSWA > 0 {
				kl = k.keyLenSWA
			}
			if k.valLenSWA > 0 {
				vl = k.valLenSWA
			}
			swa += heads * (kl + vl)
		} else {
			full += heads * (g.KeyLen + g.ValLen)
		}
	}
	if full+swa == 0 {
		return 0, 0, errors.New("gguf: no layer of this header holds a KV cache")
	}
	return full, swa, nil
}

// engineGGUFGeometry fetches enough of a GGUF file to read its geometry, over HTTP Range.
//
// `token` is the operator's Hugging Face token when one is registered, for exactly the reason
// the ingest needs it: a gated repository answers metadata anonymously but refuses the FILE.
// Without it a gated model's row simply stays unknown.
func engineGGUFGeometry(ctx context.Context, url, token string) (engineKVGeometry, error) {
	return engineGGUFGeometryFrom(func(window int) ([]byte, error) {
		return engineGGUFBytes(ctx, url, token, window)
	})
}

// engineGGUFGeometryFrom walks the two-window ladder over whatever supplies the bytes — the
// upstream URL or this deployment's own bucket — so both roads answer the same way.
//
// 🔴 The bigger window is tried even when the smaller one already produced a COMPLETE geometry.
// Complete means the four required fields, and the optional modifiers are written after them:
// settling for the small read is how a hybrid model comes back looking dense, which is a cache
// estimate four times too high. A partial answer is kept only as the fallback for when the
// second read fails or is short too — better than nothing, and strictly what the previous
// behaviour gave.
func engineGGUFGeometryFrom(read func(window int) ([]byte, error)) (engineKVGeometry, error) {
	var best engineKVGeometry
	for _, window := range []int{ggufHeadWindow, ggufHeadMax} {
		buf, err := read(window)
		if err != nil {
			break
		}
		geom, err := parseGGUFGeometry(buf)
		if err == nil {
			return geom, nil // the whole header, modifiers and all
		}
		if !errors.Is(err, errGGUFShort) {
			// 🔴 Not a GGUF, or not one this reader parses — and NOT rescued by whatever the
			// smaller window happened to yield. A header this reader cannot walk to the end of
			// is one whose optional modifiers may be sitting past the point it gave up, so a
			// geometry salvaged from the first read would be stored as `weights_kv` — which
			// reads as authoritative — while possibly being the four-times-too-high form. `floor`
			// says "we do not know" out loud, and that is the honest answer here.
			//
			// Nothing usable comes back with it either: "err != nil means do not use this" is a
			// property here rather than a convention every caller has to remember.
			return engineKVGeometry{}, err
		}
		// 🔴 `settled`, not `complete`. Four numbers in hand is not the same as "the header had
		// nothing else to say": the optional modifiers come after them, and a window that ended
		// in between yields a geometry that looks whole and prices its cache four times too
		// high. PastArch is the only thing that can tell the two apart — a tokenizer key went
		// by, so every architecture key this file holds is already behind us.
		//
		// A settled read needs no second window either, which is what makes the common case one
		// round trip rather than two.
		if geom.settled() {
			return geom, nil
		}
	}
	if best.settled() {
		return best, nil
	}
	// Complete but NOT settled: the window ended somewhere inside the architecture block, so
	// there may be a modifier past it and there is no way to know. Refused rather than stored,
	// for the same reason an unfinishable header is: `floor` says "we do not know" out loud,
	// and a wrong `weights_kv` does not.
	return engineKVGeometry{}, errGGUFShort
}

// engineGGUFBytes is the first `window` bytes of the file at url, over HTTP Range.
func engineGGUFBytes(ctx context.Context, url, token string, window int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", window-1))
	if t := strings.TrimSpace(token); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := engineIngestHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// 206 is the answer that was asked for. A 200 means the server ignored the Range and is
	// about to send the whole file, which for an 18.5 GB checkpoint is not a fallback — the
	// read is capped either way by LimitReader, and a truncated body parses or does not.
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gguf: %s answered %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, int64(window)))
}

// The llama-server settings the sliding-window cache is sized by. The Control Plane passes none of
// them (`LlmExtraArgs` defaults to `-ngl,99,--jinja,--no-mmap`), so these are the server's own
// defaults: `--parallel` auto is 4 slots over one unified cache, and the micro-batch is 512.
// Measured 2026-09-28, the log line `n_parallel is set to auto, using n_parallel = 4 and
// kv_unified = true`. An operator who adds `-np` or `--swa-full` there moves the real figure.
const (
	engineLlmSlots  = 4
	engineLlmUbatch = 512
)

// engineSWACells is how many tokens a sliding-window layer's cache holds: llama.cpp's
// llama_kv_cache_iswa sizes it `GGML_PAD(min(n_ctx, n_swa × n_seq + n_ubatch), 256)` with a unified
// cache. Every slot keeps its own window, and the micro-batch in flight is on top — so gpt-oss's
// 128-token window is 1024 cells, not 128. contextTokens <= 0 asks for the cap alone.
func engineSWACells(slidingWindow, contextTokens int) int {
	cells := slidingWindow*engineLlmSlots + engineLlmUbatch
	if contextTokens > 0 && contextTokens < cells {
		cells = contextTokens
	}
	return (cells + 255) / 256 * 256
}

// engineKVCacheMiB is the KV cache one model wants at a window, in MiB — llama.cpp's own
// `llama_kv_cache: size = …`, f16.
//
// `FullWidth × ctx + SWAWidth × engineSWACells(ctx)`, elements of two bytes. The full layers
// grow with the window and the sliding ones stop at their cap. Measured 2026-09-28 against
// llama-server at -c 24576 (see ggufLayerWidths): 1824, 600 and 288 MiB, exact to the MiB.
//
// A row stored before the widths were falls back to `cacheLayers × n_head_kv × (key_length +
// value_length) × ctx`, which is right for a model whose layers all look alike and is what such
// a row was sized by until its header is read again. The two halves are kept apart rather than
// written as `2 × head_dim` because a model may declare different key and value widths.
//
// What this does NOT count is the fixed state of the layers that hold no KV cache — LFM2's
// convolutions, a hybrid's recurrent layers. It is per sequence rather than per token, and small
// where measured: LFM2.5-8B-A1B's is 1.12 MiB over 18 layers and four slots.
func engineKVCacheMiB(g engineKVGeometry, contextTokens int) int {
	if !g.complete() || contextTokens <= 0 {
		return 0
	}
	const bytesPerElement = 2 // f16; see the note at the top of this file
	var total int64
	if g.layered() {
		total = int64(g.FullWidth) * int64(contextTokens) * bytesPerElement
		if g.SWAWidth > 0 {
			total += int64(g.SWAWidth) * int64(engineSWACells(g.SlidingWindow, contextTokens)) * bytesPerElement
		}
	} else {
		layers := g.cacheLayers()
		if layers <= 0 {
			return 0
		}
		total = int64(layers) * int64(g.HeadsKV) * int64(g.KeyLen+g.ValLen) *
			int64(contextTokens) * bytesPerElement
	}
	return int(total / (1024 * 1024))
}

// engineKVPricing is the cache as the panel prices it: MiB per 1024 tokens of window for the
// layers that grow with it, plus the MiB the sliding-window layers hold at their cap whatever the
// window. The panel multiplies the first and adds the second.
//
// 🔴 Splitting it is what keeps a sliding model's window from being priced linearly. gemma-4-12b's
// sliding layers are 1440 of its 1824 MiB at 24,576 tokens and exactly the same 1440 at 131,072 —
// one per-1k figure taken at 1024 tokens would put them in the rate and multiply them by 128.
//
// The fixed part is taken at the cap, so a window smaller than engineSWACells' cap (4608 tokens
// for gemma-4) is over-stated by the difference: the safe direction, at windows nobody runs.
func engineKVPricing(g engineKVGeometry) (per1k, fixed int) {
	if !g.complete() {
		return 0, 0
	}
	if !g.layered() {
		layers := g.cacheLayers()
		if layers <= 0 {
			return 0, 0
		}
		return engineCeilMiB(int64(layers) * int64(g.HeadsKV) * int64(g.KeyLen+g.ValLen) * 1024 * 2), 0
	}
	const bytesPerElement = 2
	per1k = engineCeilMiB(int64(g.FullWidth) * 1024 * bytesPerElement)
	fixed = engineCeilMiB(int64(g.SWAWidth) * int64(engineSWACells(g.SlidingWindow, 0)) * bytesPerElement)
	return per1k, fixed
}

// engineCeilMiB rounds bytes UP to MiB. 🔴 The panel multiplies per1k by the window, so a fraction
// dropped here is dropped 256 times at 262,144 tokens: a width of 1,000 elements is 1.95 MiB per
// 1k, and truncated to 1 it prices a 512 MiB cache at 256.
func engineCeilMiB(bytes int64) int {
	return int((bytes + 1<<20 - 1) >> 20)
}

// engineIngestGeometry is the ingest path's one attempt at a model's geometry.
//
// Deliberately best-effort and silent about most failures: this runs while an administrator is
// waiting for a job to start, the answer is an IMPROVEMENT to an estimate rather than anything
// the ingest depends on, and every way it can fail already has a defined outcome — the row
// keeps its floor.
//
// Only GGUF is attempted. A safetensors checkpoint carries no such header, and ADR 0074's first
// measurement showed the image role's memory is dominated by compute buffers rather than by
// anything a header could declare.
func engineIngestGeometry(ctx context.Context, kind string, res engineResolved,
	tokens *engineHfTokens) engineKVGeometry {
	if !strings.EqualFold(strings.TrimSpace(kind), "gguf") {
		return engineKVGeometry{}
	}
	url := strings.TrimSpace(res.DownloadURL)
	if url == "" {
		return engineKVGeometry{}
	}
	// A gated repository refuses the FILE to an anonymous caller even though it answered the
	// metadata (ADR 0072, "P5 を実機で押した"), so the registered token is used here for the
	// same reason the ingest task needs it. Without one, the read simply does not happen.
	var token string
	if res.Gated && tokens != nil {
		if t, aerr := tokens.plaintext(ctx); aerr == nil {
			token = t
		}
	}
	geom, err := engineGGUFGeometry(ctx, url, token)
	if err != nil {
		log.Printf("engines: %s: no KV geometry from the GGUF header (%v) - the row keeps its floor",
			res.Source, err)
		return engineKVGeometry{}
	}
	return geom
}

// engineGGUFName is whether a key names a file this reader can parse at all. The bucket holds
// safetensors beside GGUFs and a prefix read of the wrong format is a wasted round trip that
// ends in "not a GGUF file" — the same guard engineSafetensorsName is for the other question.
func engineGGUFName(key string) bool {
	return strings.EqualFold(path.Ext(strings.TrimSpace(key)), ".gguf")
}

// engineGGUFGeometryOfObject is the geometry of a file whose bytes are already in this
// deployment's bucket — the road `POST …/models` and `…/objects/register` take, where there is
// no upstream URL to read and there may never have been one (the bytes can outlive the job that
// fetched them).
//
// 🔴 This is the half the file's own header comment said did not exist: "a row registered from
// the bucket still reaches this reader through no road, so its geometry stays unknown". That was
// true, and it is why every llm row on both deployments answers `vram_need_source: floor` — the
// weights and nothing else, which fits almost any card and so let a row declaring 262,144 tokens
// be switched on without a word (ADR 0074's 2026-09-19 follow-up).
//
// Best-effort and deliberately quiet, exactly like engineVaeOfObject next door: a refused read,
// an unconfigured bucket, a file that is not a GGUF, a header past the ceiling — all of them
// leave the geometry unknown, which is where the row already was. The one thing it must not do
// is fail the press.
func engineGGUFGeometryOfObject(ctx context.Context, key string, storage *engineStorage) engineKVGeometry {
	if !engineGGUFName(key) || storage == nil || !storage.configured() {
		return engineKVGeometry{}
	}
	geom, err := engineGGUFGeometryFrom(func(window int) ([]byte, error) {
		return storage.prefix(ctx, key, window)
	})
	if err != nil {
		log.Printf("engines: %s: the attention geometry went unread (%v)", key, err)
		return engineKVGeometry{}
	}
	return geom
}

// engineGeometryFile names the file whose header answers for the row: its own weights, under the
// unflagged slot. Every other flag names a part, and a text encoder's header says nothing about
// the model that loads it — the same rule engineVaeMainFile states for the other question.
func engineGeometryFile(m store.EngineModel) (string, bool) {
	for _, f := range m.Files {
		if strings.TrimSpace(f.Flag) == "" && engineGGUFName(f.S3Key) {
			return f.S3Key, true
		}
	}
	return "", false
}

// engineApplyGeometry writes a read header onto a row. One function, with engineRowGeometry and
// engineStoreKV beside it, because the fields are one fact: a row carrying some of them and not
// the rest estimates its cache off a shape no file has, which is the bug this whole pass exists
// to close.
func engineApplyGeometry(m *store.EngineModel, geom engineKVGeometry) {
	m.KVLayers, m.KVHeadsKV = geom.Layers, geom.HeadsKV
	m.KVKeyLen, m.KVValueLen = geom.KeyLen, geom.ValLen
	m.KVNextN, m.KVFullAttnInterval = geom.NextN, geom.FullAttnInterval
	m.KVFullWidth, m.KVSWAWidth, m.KVSlidingWindow = geom.FullWidth, geom.SWAWidth, geom.SlidingWindow
	// Only when the header said so: the ingest road may already have it from the upstream API,
	// and a 0 read off a header that does not declare one must not erase that.
	if geom.Ceiling > 0 {
		m.ContextCeiling = geom.Ceiling
	}
}

// engineRowGeometry is the geometry a row stored — what every estimate of a registered model is
// computed from.
func engineRowGeometry(m store.EngineModel) engineKVGeometry {
	return engineKVGeometry{
		Layers: m.KVLayers, HeadsKV: m.KVHeadsKV, KeyLen: m.KVKeyLen, ValLen: m.KVValueLen,
		NextN: m.KVNextN, FullAttnInterval: m.KVFullAttnInterval,
		FullWidth: m.KVFullWidth, SWAWidth: m.KVSWAWidth, SlidingWindow: m.KVSlidingWindow,
		Ceiling: m.ContextCeiling,
	}
}

// engineStoreKV is a read header as the targeted writes take it.
func engineStoreKV(g engineKVGeometry) store.EngineModelKV {
	return store.EngineModelKV{
		Layers: g.Layers, HeadsKV: g.HeadsKV, KeyLen: g.KeyLen, ValueLen: g.ValLen,
		NextN: g.NextN, FullAttnInterval: g.FullAttnInterval,
		FullWidth: g.FullWidth, SWAWidth: g.SWAWidth, SlidingWindow: g.SlidingWindow,
		Ceiling: g.Ceiling,
	}
}

// layered is whether this geometry was read layer by layer. A row stored before that carries
// only the per-model numbers, which mis-size any model whose layers differ.
func (g engineKVGeometry) layered() bool { return g.FullWidth > 0 || g.SWAWidth > 0 }
