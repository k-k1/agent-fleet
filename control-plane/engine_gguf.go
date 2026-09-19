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
// It is read ONCE and stored on the row — at registration, or on the first loading write to a
// row that has none (engineAdminAPI.healGeometry). A header read per panel refresh would put a
// network call on a screen that lists every model, and the geometry of a file identified by
// sha256 cannot change under us.
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

// ggufStop hands a short read back to the LADDER rather than deciding for it.
//
// The scan no longer stops at the four required fields (the hybrid modifiers are written after
// them), so it walks on into `tokenizer.ggml.tokens` — an array of the whole vocabulary,
// megabytes long, which no window this file fetches will ever contain. Ending there with the
// geometry in hand is a perfectly usable read.
//
// 🔴 But it is not the same as a COMPLETE one, and this function used to say it was. Between
// `attention.value_length` and `full_attention_interval` there are several more keys, and a
// header whose `general.*` strings are long enough pushes the modifiers past a 64 KiB window.
// Swallowing the short error there returned a geometry that looks whole and prices its cache
// four times too high — the very bug this pass exists to fix, reached through the error path.
// So the error travels with the partial answer and engineGGUFGeometryFrom tries the bigger
// window before settling for it.
func ggufStop(geom engineKVGeometry, err error) error {
	_ = geom
	return err
}

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

	// Ceiling is `<arch>.context_length`: the largest window the model was TRAINED for. Not part
	// of the cache arithmetic at all — it is read here because it is in the same header, on the
	// same pass, and because the bucket road (engineGGUFGeometryOfObject) has no other way to
	// learn it. 🔴 A ceiling, not a setting.
	Ceiling int
}

func (g engineKVGeometry) complete() bool {
	return g.Layers > 0 && g.HeadsKV > 0 && g.KeyLen > 0 && g.ValLen > 0
}

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

// parseGGUFGeometry reads the header out of a prefix of a GGUF file.
//
// It stops as soon as it has all four numbers — the fields sit near the front, ahead of the
// tokenizer, so the common case never touches most of the window.
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
	var embedding, heads int
	for i := uint64(0); i < kvCount; i++ {
		key, err := r.str()
		if err != nil {
			return geom, ggufStop(geom, err)
		}
		t, err := r.u32()
		if err != nil {
			return geom, ggufStop(geom, err)
		}
		// The architecture names every other key, so it has to be read before they can be
		// recognised. llama.cpp writes it first; if some writer does not, the keys simply are
		// not matched and the row stays unknown.
		if key == "general.architecture" {
			if t != ggufTypeString {
				return geom, errors.New("gguf: general.architecture is not a string")
			}
			if arch, err = r.str(); err != nil {
				return geom, err
			}
			continue
		}
		n, isInt, err := r.intValue(t)
		if err != nil {
			return geom, ggufStop(geom, err)
		}
		if !isInt || arch == "" || !strings.HasPrefix(key, arch+".") {
			continue
		}
		switch strings.TrimPrefix(key, arch+".") {
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
		}
		// 🔴 No early return on complete(). The two modifiers are written AFTER
		// attention.value_length (measured on Qwen3.8-27B: value_length is key 28 of 50 and
		// full_attention_interval is key 36), so stopping at the four required fields is
		// exactly how a hybrid model came out looking like a dense one.
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
	if !geom.complete() {
		return geom, errors.New("gguf: the header does not declare the attention geometry")
	}
	return geom, nil
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
			if best.complete() {
				return best, nil
			}
			return geom, err // not a GGUF, or not one this reader parses: reading more cannot help
		}
		if geom.complete() {
			best = geom
		}
	}
	if best.complete() {
		return best, nil
	}
	return best, errGGUFShort
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

// engineKVCacheMiB is the KV cache one model wants, in MiB.
//
// `cacheLayers × n_head_kv × (key_length + value_length) × ctx × bytes(cache element)`, which
// is llama.cpp's own `KV self size`. The two halves are kept apart rather than written as
// `2 × head_dim` because a model may declare different key and value widths, and one that does
// would be silently mis-sized by the doubled form.
//
// The layer count is cacheLayers() and not block_count: see there for the two reasons they
// differ and for the measurement that settled it.
//
// What this still does NOT count is the recurrent half of a hybrid model — the fixed-size SSM
// state of the layers cacheLayers() drops. It is per SEQUENCE rather than per token, so it does
// not belong in a figure the window multiplies, and llama.cpp allocates it outside the buffer
// this function's measurement was taken from.
func engineKVCacheMiB(g engineKVGeometry, contextTokens int) int {
	if !g.complete() || contextTokens <= 0 {
		return 0
	}
	layers := g.cacheLayers()
	if layers <= 0 {
		return 0
	}
	const bytesPerElement = 2 // f16; see the note at the top of this file
	total := int64(layers) * int64(g.HeadsKV) * int64(g.KeyLen+g.ValLen) *
		int64(contextTokens) * bytesPerElement
	return int(total / (1024 * 1024))
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

// engineApplyGeometry writes a read header onto a row. One function because the six fields are
// one fact: a row carrying four of them and not the other two is a row that estimates four times
// too high, which is the bug this whole pass exists to close.
func engineApplyGeometry(m *store.EngineModel, geom engineKVGeometry) {
	m.KVLayers, m.KVHeadsKV = geom.Layers, geom.HeadsKV
	m.KVKeyLen, m.KVValueLen = geom.KeyLen, geom.ValLen
	m.KVNextN, m.KVFullAttnInterval = geom.NextN, geom.FullAttnInterval
	// Only when the header said so: the ingest road may already have it from the upstream API,
	// and a 0 read off a header that does not declare one must not erase that.
	if geom.Ceiling > 0 {
		m.ContextCeiling = geom.Ceiling
	}
}
