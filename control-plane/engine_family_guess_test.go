package main

import "testing"

// The strings are the ones the two APIs actually answered on 2026-09-12 (Civitai's top 20
// monthly checkpoints, plus the Hugging Face card fields), not invented shapes. Half of them
// have no family here at all, and that half is the point: this suggestion exists to save a
// choice, never to make one up.
func TestFamilyGuessOnlyAnswersWhatTheProviderCanRun(t *testing.T) {
	for _, tc := range []struct{ upstream, want string }{
		// Written four ways across the two sources, all the same architecture.
		{"SDXL 1.0", "sdxl"},
		{"sdxl-turbo", "sdxl"},
		{"SDXL", "sdxl"},
		// The SDXL fine-tunes Civitai's image rankings are mostly made of. They load through
		// the SDXL graph, and without them the suggestion would be empty exactly where it is
		// most wanted.
		{"Pony", "sdxl"},
		{"Illustrious", "sdxl"},
		{"NoobAI", "sdxl"},
		{"Flux.1 D", "flux1"},
		{"FLUX.1-schnell", "flux1"},
		{"SD 3.5", "sd35"},
		{"Z-Image Turbo", "zimage"},
		{"ZImageTurbo", "zimage"},
		{"FLUX.2 klein", "flux2-klein"},
		// 🔴 No template, so no suggestion. Measured on the same 20 rows: these are what
		// Civitai ranks highest today, and a wrong family would silence `base_model_missing` —
		// the row's only mark that it cannot generate.
		{"SD 1.5", ""},
		{"SD 1.5 Hyper", ""},
		{"Anima", ""},
		{"Krea 2", ""},
		{"LTXV 2.5", ""},
		{"", ""},
	} {
		if got := engineFamilyGuess("comfy", tc.upstream); got != tc.want {
			t.Errorf("guess(%q) = %q, want %q", tc.upstream, got, tc.want)
		}
	}
}

// A provider with no family vocabulary has nothing to dispatch on, so there is no such choice
// to suggest — the same rule that makes the panel not draw the picker at all.
func TestFamilyGuessIsSilentWithoutAVocabulary(t *testing.T) {
	if got := engineFamilyGuess("sdcpp", "SDXL 1.0"); got != "" {
		t.Errorf("guess for a provider with no families = %q, want empty", got)
	}
	if got := engineFamilyGuess("", "SDXL 1.0"); got != "" {
		t.Errorf("guess with no provider = %q, want empty", got)
	}
}

// 🔴 The vocabulary is the authority, not this table. A family recognised here that the
// provider does not have a template for would be suggested into a picker that does not offer
// it, and the ingest would refuse the row a moment later.
func TestFamilyGuessNeverLeavesTheProvidersVocabulary(t *testing.T) {
	vocab := map[string]bool{}
	for _, f := range engineBaseModelsFor("comfy") {
		vocab[f] = true
	}
	for _, r := range engineFamilyRules {
		for _, needle := range r.any {
			got := engineFamilyGuess("comfy", needle)
			if got != "" && !vocab[got] {
				t.Errorf("rule %q suggested %q, which is not in %v", needle, got, engineBaseModelsFor("comfy"))
			}
		}
	}
}
