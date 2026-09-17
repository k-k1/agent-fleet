package main

// A quantisation repository is one model at a dozen sizes (ADR 0089).
//
// The operator's report was two things at once: adding a second size of a model already in the
// catalogue meant finding the repository again in the 探す tab, and the screen said
// "KV キャッシュ 66560 MiB" whatever file was picked — which reads as "this can never run" about a
// model that runs on the same card at a window nobody was being shown.

import (
	"testing"
)

// hfLadderStub points the ingest at the local Hugging Face stub. 🔴 Its own helper because
// `hfStub` only STARTS the server — the caller redirects `engineIngestBase`, and a test that
// forgets to reads the real huggingface.co (which is how this one first "failed": nine real
// quantisations where the stub publishes three).
func hfLadderStub(t *testing.T) {
	t.Helper()
	srv := hfStub(t)
	old := engineIngestBase
	engineIngestBase = srv.URL
	t.Cleanup(func() { engineIngestBase = old })
}

// 🔴 Measured live on unsloth/Qwen3.8-27B-GGUF, 2026-09-18: thirty `.gguf` files, of which one is
// an importance matrix and two are vision projectors. A plain list of "every .gguf" buries the
// fourteen quantisations that are the point of the repository — and offering an imatrix as
// something to take in is a multi-gigabyte download that builds a row nothing can load.
func TestCandidateRoleTellsAQuantisationFromTheThingsBesideIt(t *testing.T) {
	for _, c := range []struct{ name, want string }{
		{"Qwen3.8-27B-UD-IQ2_XXS.gguf", engineCandidateModel},
		{"Qwen3.8-27B-UD-Q4_K_XL.gguf", engineCandidateModel},
		{"MTP/mtp-Qwen3.8-27B-Q4_0.gguf", engineCandidateModel},
		{"imatrix_unsloth.gguf", engineCandidateImatrix},
		{"mmproj-F16.gguf", engineCandidateProjector},
		{"mmproj-BF16.gguf", engineCandidateProjector},
		// A path is not a signal on its own: the projector is one wherever it sits, and a
		// quantisation in a subdirectory is still a quantisation.
		{"some/dir/mmproj-F16.gguf", engineCandidateProjector},
		{"BF16/Qwen3.8-27B-BF16.gguf", engineCandidateModel},
	} {
		if got := engineCandidateRole(c.name); got != c.want {
			t.Errorf("role(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// The listing is the ladder's data: every quantisation with its size, priced against the card by
// the panel. It must carry the role so the ladder can fold the three files that are not choices,
// and it must NOT drop them — this same route is the manual file picker.
func TestIngestListCarriesTheRoleAndStillListsEverything(t *testing.T) {
	hfLadderStub(t)

	files, aerr := engineIngestList(t.Context(),
		engineIngestSource{HF: &engineIngestHF{Repo: "Qwen/Qwen2.5-Coder-0.5B-Instruct-GGUF"}}, "gguf")
	if aerr != nil {
		t.Fatalf("list: %v", aerr.message)
	}
	if len(files) != 3 {
		t.Fatalf("files = %+v, want the three quantisations with a sha256", files)
	}
	for _, f := range files {
		if f.Role != engineCandidateModel {
			t.Errorf("%s: role = %q, want every quantisation to be a model", f.Name, f.Role)
		}
	}
}

// 🔴 The number the operator saw, reproduced from the real geometry. 65 blocks x 4 KV heads x
// (256 + 256) x 2 bytes is 260 MiB per 1,024 tokens, and the model publishes a 262,144 ceiling:
// 66,560 MiB. The arithmetic was never wrong — the window was, because the panel pre-filled the
// field with a ceiling the CP sends labelled as a ceiling.
func TestKVCacheReproducesTheFigureTheOperatorReported(t *testing.T) {
	qwen := engineKVGeometry{Layers: 65, HeadsKV: 4, KeyLen: 256, ValLen: 256}
	if got := engineKVCacheMiB(qwen, 262144); got != 66560 {
		t.Errorf("KV at the published ceiling = %d MiB, want 66560", got)
	}
	// And at a window this deployment can actually run, on a card it actually has.
	if got := engineKVCacheMiB(qwen, 32768); got != 8320 {
		t.Errorf("KV at 32768 = %d MiB, want 8320", got)
	}
	// Per 1,024 tokens is what rides on the wire, because the cache is linear in the window and
	// one number then answers every value somebody can type into the field.
	if got := engineKVCacheMiB(qwen, 1024); got != 260 {
		t.Errorf("KV per 1k = %d MiB, want 260", got)
	}
	// The 64-block builds of the SAME repository answer 256, which is why the listing names the
	// file its figure came from instead of claiming to speak for every file.
	if got := engineKVCacheMiB(engineKVGeometry{Layers: 64, HeadsKV: 4, KeyLen: 256, ValLen: 256}, 1024); got != 256 {
		t.Errorf("KV per 1k for a 64-block build = %d MiB, want 256", got)
	}
}
