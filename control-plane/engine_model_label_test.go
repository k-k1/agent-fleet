package main

// The name a member is shown instead of a row id (ADR 0090).

import (
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestEngineModelLabelTellsTwoSizesOfOneModelApart(t *testing.T) {
	repo := "unsloth/Qwen3.8-27B-GGUF"
	for _, c := range []struct{ file, want string }{
		{"Qwen3.8-27B-UD-IQ2_XXS.gguf", "unsloth/Qwen3.8-27B-GGUF UD-IQ2_XXS"},
		{"Qwen3.8-27B-UD-IQ2_S.gguf", "unsloth/Qwen3.8-27B-GGUF UD-IQ2_S"},
	} {
		m := store.EngineModel{ID: "x", DisplayName: repo, Source: "hf:" + repo + "/" + c.file}
		if got := engineModelLabel(m); got != c.want {
			t.Errorf("label(%s) = %q, want %q", c.file, got, c.want)
		}
	}
}

func TestEngineModelLabelUsesTheVersionWhereThereIsOne(t *testing.T) {
	m := store.EngineModel{ID: "meinamix_5038", DisplayName: "MeinaMix", VersionName: "Meina V11",
		Source: "civitai:5038"}
	if got := engineModelLabel(m); got != "MeinaMix Meina V11" {
		t.Errorf("label = %q", got)
	}
	// Civitai's default version name is the model's own, and "MeinaMix MeinaMix" is worse than
	// the name alone.
	m.VersionName = "MeinaMix"
	if got := engineModelLabel(m); got != "MeinaMix" {
		t.Errorf("label = %q, want the repeated version dropped", got)
	}
}

// 🔴 Absent, not a guess. Every reader falls back to the id, which is what they all did before
// this existed — so a row with no model page behind it is unchanged rather than half-named.
func TestEngineModelLabelIsEmptyWithoutAName(t *testing.T) {
	for _, m := range []store.EngineModel{
		{ID: "seeded"},
		{ID: "by-hand", Source: "url:https://example.invalid/x.gguf"},
	} {
		if got := engineModelLabel(m); got != "" {
			t.Errorf("label(%s) = %q, want none", m.ID, got)
		}
	}
	// A name with nothing to distinguish it is just the name.
	if got := engineModelLabel(store.EngineModel{ID: "a", DisplayName: "some/Repo"}); got != "some/Repo" {
		t.Errorf("label = %q", got)
	}
}

// A file named some other way keeps its whole stem: longer than ideal, never wrong.
func TestEngineModelVariantKeepsAnUnfamiliarName(t *testing.T) {
	m := store.EngineModel{ID: "a", DisplayName: "someone/Whatever-GGUF",
		Source: "hf:someone/Whatever-GGUF/ggml-model-q4.gguf"}
	if got := engineModelVariant(m); got != "ggml-model-q4" {
		t.Errorf("variant = %q", got)
	}
	// And a source naming a DIFFERENT repository is not mined for a suffix.
	m.Source = "hf:other/Repo/x.gguf"
	if got := engineModelVariant(m); got != "" {
		t.Errorf("variant = %q, want none for another repository's file", got)
	}
}
