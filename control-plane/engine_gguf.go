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
//   - The CP cannot read the bucket. It has no S3 permission at all and gains none (ADR 0072
//     review R3), so the copy that is read is the one still at the SOURCE, over the same HTTP
//     the resolve already uses.
//
// It is read ONCE, at registration, and stored on the row. A header read per panel refresh
// would put a network call on a screen that lists every model, and the geometry of a file
// identified by sha256 cannot change under us.
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
	"strings"
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

// engineKVGeometry is what a KV-cache estimate is computed from. Every field is required: a
// partially read header answers nothing, because the product of four numbers with one missing
// is not a smaller estimate, it is a wrong one.
type engineKVGeometry struct {
	Layers  int // <arch>.block_count
	HeadsKV int // <arch>.attention.head_count_kv
	KeyLen  int // <arch>.attention.key_length, else embedding_length / head_count
	ValLen  int // <arch>.attention.value_length, else the same fallback
}

func (g engineKVGeometry) complete() bool {
	return g.Layers > 0 && g.HeadsKV > 0 && g.KeyLen > 0 && g.ValLen > 0
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
			return geom, err
		}
		t, err := r.u32()
		if err != nil {
			return geom, err
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
			return geom, err
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
		}
		if geom.complete() {
			return geom, nil
		}
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
	geom, err := engineGGUFTry(ctx, url, token, ggufHeadWindow)
	if !errors.Is(err, errGGUFShort) {
		return geom, err
	}
	return engineGGUFTry(ctx, url, token, ggufHeadMax)
}

func engineGGUFTry(ctx context.Context, url, token string, window int) (engineKVGeometry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return engineKVGeometry{}, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", window-1))
	if t := strings.TrimSpace(token); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := engineIngestHTTP.Do(req)
	if err != nil {
		return engineKVGeometry{}, err
	}
	defer resp.Body.Close()
	// 206 is the answer that was asked for. A 200 means the server ignored the Range and is
	// about to send the whole file, which for an 18.5 GB checkpoint is not a fallback — the
	// read is capped either way by LimitReader, and a truncated body parses or does not.
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return engineKVGeometry{}, fmt.Errorf("gguf: %s answered %d", url, resp.StatusCode)
	}
	buf, err := io.ReadAll(io.LimitReader(resp.Body, int64(window)))
	if err != nil {
		return engineKVGeometry{}, err
	}
	return parseGGUFGeometry(buf)
}

// engineKVCacheMiB is the KV cache one model wants, in MiB.
//
// `n_layer × n_head_kv × (key_length + value_length) × ctx × bytes(cache element)`, which is
// llama.cpp's own `KV self size`. The two halves are kept apart rather than written as
// `2 × head_dim` because a model may declare different key and value widths, and one that does
// would be silently mis-sized by the doubled form.
func engineKVCacheMiB(g engineKVGeometry, contextTokens int) int {
	if !g.complete() || contextTokens <= 0 {
		return 0
	}
	const bytesPerElement = 2 // f16; see the note at the top of this file
	total := int64(g.Layers) * int64(g.HeadsKV) * int64(g.KeyLen+g.ValLen) *
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
