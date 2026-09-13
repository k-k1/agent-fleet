package main

// engine_safetensors.go — "does this checkpoint carry a VAE", read from the file's own tensor
// list (ADR 0072 follow-up, VAE detection).
//
// Why a header read rather than a rule: an SDXL checkpoint published without VAE tensors passes
// every check this deployment has — the family is declared, the file exists, the row looks
// complete — and then fails EVERY request inside ComfyUI with `ERROR: VAE is invalid: None`,
// after the box has paid a 1-2.5 minute checkpoint switch (measured 2026-09-11 and again
// 2026-09-13 on this deployment: `waimatureillustrious_v30` failed while another SDXL row on the
// same box generated normally minutes later). Nothing a caller can do reaches it: `generate_image`
// has no VAE argument.
//
// The declaration cannot be made mandatory — most checkpoints bundle their VAE and would all
// become "file not set" — and a fallback to a known VAE name cannot exist either, because
// ComfyUI's `VAELoader.vae_name` is an enumeration of the box's own `models/vae` and naming a
// file the box does not hold breaks the families that work today. That leaves the file itself as
// the only source of the fact, which is what this reads.
//
// Where the bytes are read FROM is the source, over the same HTTP the resolve already uses —
// never S3. The CP has no S3 permission at all, does not know the bucket's name, and gaining
// either is a reversal of review R3 rather than an implementation detail (ADR 0072).
//
// 🔴 The verdict is three-valued and the third value is load-bearing. "Not read" and "no VAE in
// it" are different facts: a `.ckpt`, a source that refuses an anonymous Range, a header longer
// than the window — all of them mean nobody looked, and a row must never be marked broken on
// that. Only a header that was parsed and does not list the tensors says "no".

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// The three answers. Empty is "nobody read it", which is what every row taken in before this
// existed carries and what a failed read leaves behind.
const (
	engineVaeUnknown = ""
	engineVaeYes     = "yes"
	engineVaeNo      = "no"
)

// safetensorsHeadWindow is the first read. A single-file SDXL checkpoint's header is a few
// hundred kilobytes (one JSON object per tensor, ~2,500 of them), so this covers the case in one
// request against a file whose body is gigabytes.
const safetensorsHeadWindow = 1 << 20

// safetensorsHeadMax is the second and last. A header longer than this is not a checkpoint this
// deployment can run, and reading further would be downloading the weights one window at a time.
const safetensorsHeadMax = 16 << 20

// errSafetensorsShort is the one error worth retrying with a bigger window: the file declared a
// header longer than what was fetched, so the answer is not "no" but "not yet read".
var errSafetensorsShort = errors.New("safetensors: header window is short")

// safetensorsVaePrefixes are the tensor-name prefixes a bundled VAE goes by.
//
// `first_stage_model.` is the single-file (LDM) layout every SD/SDXL/SD3.5 checkpoint on this
// deployment uses — the autoencoder is literally the first stage of that pipeline. `vae.` is the
// diffusers spelling, which turns up in checkpoints converted from a diffusers tree.
//
// Matched as a PREFIX on the tensor name rather than as a substring: `model.diffusion_model.…`
// holds the word nowhere, but a substring match would be one publisher's layer name away from
// declaring every checkpoint fine, which is the direction that fails silently.
var safetensorsVaePrefixes = []string{"first_stage_model.", "vae."}

// engineSafetensorsHasVae reads a prefix of a safetensors file and answers whether its tensor
// list includes a VAE.
//
// It parses the header by STREAMING the keys rather than unmarshalling it whole: the values are
// per-tensor objects nothing here reads, and a checkpoint's header is large enough that decoding
// it into a map is a megabyte of garbage per read for one boolean.
func engineSafetensorsHasVae(buf []byte) (bool, error) {
	if len(buf) < 8 {
		return false, errSafetensorsShort
	}
	n := binary.LittleEndian.Uint64(buf[:8])
	// A pickle `.ckpt` and a truncated download both land here, and both read as an absurd
	// length. Refusing them by shape is what keeps "unknown" out of the "no" bucket.
	if n < 2 || n > safetensorsHeadMax {
		return false, fmt.Errorf("safetensors: header length %d is not a header", n)
	}
	if uint64(len(buf)-8) < n {
		return false, errSafetensorsShort
	}
	dec := json.NewDecoder(strings.NewReader(string(buf[8 : 8+n])))
	tok, err := dec.Token()
	if err != nil {
		return false, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return false, errors.New("safetensors: the header is not a JSON object")
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return false, err
		}
		name, _ := key.(string)
		// The value is skipped, not read. json.RawMessage consumes exactly one value whatever
		// its shape, which is what makes this loop independent of what a publisher puts in it.
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return false, err
		}
		for _, p := range safetensorsVaePrefixes {
			if strings.HasPrefix(name, p) {
				return true, nil
			}
		}
	}
	return false, nil
}

// engineSafetensorsVae fetches enough of a file to answer the question, over HTTP Range.
//
// `token` is the operator's Hugging Face token when one is registered, for the same reason the
// GGUF geometry read needs it: a gated repository answers metadata anonymously and refuses the
// file. Without one, a gated model's verdict simply stays unknown.
func engineSafetensorsVae(ctx context.Context, url, token string) (string, error) {
	has, err := engineSafetensorsTry(ctx, url, token, safetensorsHeadWindow)
	if errors.Is(err, errSafetensorsShort) {
		has, err = engineSafetensorsTry(ctx, url, token, safetensorsHeadMax)
	}
	if err != nil {
		return engineVaeUnknown, err
	}
	if has {
		return engineVaeYes, nil
	}
	return engineVaeNo, nil
}

func engineSafetensorsTry(ctx context.Context, url, token string, window int) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", window-1))
	if t := strings.TrimSpace(token); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := engineIngestHTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	// 206 is the answer that was asked for. A 200 means the server ignored the Range and is
	// about to send the whole file — the LimitReader caps what is actually pulled either way,
	// and a truncated body either parses or reports itself short.
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("safetensors: %s answered %d", url, resp.StatusCode)
	}
	buf, err := io.ReadAll(io.LimitReader(resp.Body, int64(window)))
	if err != nil {
		return false, err
	}
	return engineSafetensorsHasVae(buf)
}

// engineSafetensorsName says whether a file is one this can be asked about at all. A `.ckpt` is
// a pickle with no header to read, and a row holding one keeps the unknown verdict rather than
// being reported as a checkpoint with no VAE.
func engineSafetensorsName(name string) bool {
	l := strings.ToLower(strings.TrimSpace(name))
	return strings.HasSuffix(l, ".safetensors") || strings.HasSuffix(l, ".sft")
}
