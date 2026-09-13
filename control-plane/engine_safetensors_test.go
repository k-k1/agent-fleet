package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// safetensorsFixture builds a file prefix the way the format does: an 8-byte little-endian
// header length, then that many bytes of JSON. The body is left out — the point of the format
// is that the header is readable without it.
func safetensorsFixture(t *testing.T, names []string) []byte {
	t.Helper()
	obj := map[string]any{"__metadata__": map[string]string{"format": "pt"}}
	for _, n := range names {
		obj[n] = map[string]any{"dtype": "F16", "shape": []int{2, 2}, "data_offsets": []int{0, 8}}
	}
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	out := make([]byte, 8, 8+len(b))
	binary.LittleEndian.PutUint64(out, uint64(len(b)))
	return append(out, b...)
}

// The pair this whole feature is decided by. A checkpoint that bundles its VAE and one that does
// not must come out DIFFERENT here, because on the engine they differ only in that every request
// against the second one dies in VAEDecode after the box has paid the checkpoint switch.
func TestSafetensorsHasVae(t *testing.T) {
	bundled := safetensorsFixture(t, []string{
		"model.diffusion_model.input_blocks.0.0.weight",
		"conditioner.embedders.0.transformer.text_model.encoder.layers.0.mlp.fc1.weight",
		"first_stage_model.decoder.conv_in.weight",
	})
	has, err := engineSafetensorsHasVae(bundled)
	if err != nil || !has {
		t.Fatalf("a checkpoint with first_stage_model tensors read as %v (%v)", has, err)
	}
	none := safetensorsFixture(t, []string{
		"model.diffusion_model.input_blocks.0.0.weight",
		"conditioner.embedders.0.transformer.text_model.encoder.layers.0.mlp.fc1.weight",
	})
	has, err = engineSafetensorsHasVae(none)
	if err != nil {
		t.Fatalf("a checkpoint without a VAE did not parse: %v", err)
	}
	if has {
		t.Fatal("a checkpoint with no VAE tensors was reported as bundling one")
	}
	// The diffusers spelling of the same thing.
	has, err = engineSafetensorsHasVae(safetensorsFixture(t, []string{"vae.decoder.conv_in.weight"}))
	if err != nil || !has {
		t.Fatalf("the diffusers spelling read as %v (%v)", has, err)
	}
}

// 🔴 The prefix is a prefix. A substring match would find "vae" inside somebody's layer name and
// declare a broken row fine, which is the direction that fails silently on the engine.
func TestSafetensorsVaeNameIsAPrefix(t *testing.T) {
	has, err := engineSafetensorsHasVae(safetensorsFixture(t, []string{
		// Both names carry a needle, neither STARTS with one: a UNet block that borrowed the
		// word, and a tensor nested under something else. Under a substring rule this file
		// reads as a checkpoint that bundles its VAE, and the row it belongs to would be
		// declared fine while every request against it dies in VAEDecode.
		"model.diffusion_model.vae.proj.weight",
		"cond_stage_model.first_stage_model.embed.weight",
	}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if has {
		t.Fatal("a tensor name that merely contains the word was taken for a bundled VAE")
	}
}

// Not read and not there are different answers, and everything that cannot be parsed has to land
// on the first one. A row is marked broken on "no" alone.
func TestSafetensorsUnreadableIsNotNo(t *testing.T) {
	if _, err := engineSafetensorsHasVae([]byte{1, 2, 3}); !errors.Is(err, errSafetensorsShort) {
		t.Fatalf("a file shorter than the length prefix answered %v", err)
	}
	// A pickle .ckpt: the first eight bytes are not a length and read as an absurd one.
	pickle := append([]byte{0x80, 0x02, 0x7d, 0x71, 0x00, 0x28, 0x58, 0xff}, []byte("garbage")...)
	if _, err := engineSafetensorsHasVae(pickle); err == nil {
		t.Fatal("a pickle checkpoint was parsed as a safetensors header")
	}
	// A header that is longer than what was fetched asks for a bigger window rather than
	// answering "no" out of the part that did arrive.
	full := safetensorsFixture(t, []string{"first_stage_model.decoder.conv_in.weight"})
	if _, err := engineSafetensorsHasVae(full[:len(full)-10]); !errors.Is(err, errSafetensorsShort) {
		t.Fatalf("a truncated header answered %v instead of asking for more", err)
	}
}

// The window widens once. A header bigger than the first read must not be reported as a
// checkpoint with no VAE, which is what the retry exists to prevent.
func TestSafetensorsVaeOverHTTP(t *testing.T) {
	var names []string
	for i := 0; i < 200; i++ {
		names = append(names, strings.Repeat("a", 200)+string(rune('0'+i%10)))
	}
	body := safetensorsFixture(t, append(names, "first_stage_model.decoder.conv_in.weight"))
	var windows []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		windows = append(windows, rng)
		// Answer the FIRST request with less than the header needs, the way a real Range does
		// when the window is smaller than the header.
		n := len(body)
		if len(windows) == 1 {
			n = 64
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[:n])
	}))
	defer srv.Close()
	got, err := engineSafetensorsVae(t.Context(), srv.URL, "")
	if err != nil {
		t.Fatalf("range read: %v", err)
	}
	if got != engineVaeYes {
		t.Fatalf("verdict %q, want %q", got, engineVaeYes)
	}
	if len(windows) != 2 {
		t.Fatalf("windows %v, want a short read followed by a wider one", windows)
	}
	if !strings.Contains(windows[1], "16777215") {
		t.Fatalf("the second window was %q, not the wide one", windows[1])
	}
}

// A source that refuses leaves the verdict unknown, never "no".
func TestSafetensorsVaeRefusedIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	got, err := engineSafetensorsVae(t.Context(), srv.URL, "")
	if err == nil {
		t.Fatal("a 401 was not reported as a failed read")
	}
	if got != engineVaeUnknown {
		t.Fatalf("verdict %q, want unknown", got)
	}
}

func TestSafetensorsName(t *testing.T) {
	for _, n := range []string{"a.safetensors", "A.SafeTensors", "b.sft"} {
		if !engineSafetensorsName(n) {
			t.Fatalf("%s is a safetensors file", n)
		}
	}
	for _, n := range []string{"a.ckpt", "b.gguf", "c.pt", ""} {
		if engineSafetensorsName(n) {
			t.Fatalf("%s has no safetensors header to read", n)
		}
	}
}
