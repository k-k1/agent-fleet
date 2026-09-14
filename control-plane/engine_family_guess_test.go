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
		// SD1.5 answered "" until the family got a template. The distilled variants come back
		// as sd15 too, and correctly — they load through the same graph; what they do not
		// share is the step count, which is the row's `params` to declare.
		{"SD 1.5", "sd15"},
		{"SD 1.5 Hyper", "sd15"},
		{"SD 1.5 LCM", "sd15"},
		{"SD 1.4", "sd15"},
		// 🔴 SD 2.x is NOT SD1.5: OpenCLIP-H instead of ViT-L, v-prediction at 768. It has no
		// template here, and the normalised "sd20"/"sd21" must never reach the sd15 rule —
		// which is why that rule's needles are spelled out rather than shortened to "sd1".
		{"SD 2.0", ""},
		{"SD 2.1", ""},
		// Anima answered "" until the family got a template, like SD1.5 before it.
		{"Anima", "anima"},
		// 🔴 The three reasons that rule matches WHOLE and not as a substring. Animagine is an
		// SDXL fine-tune and must keep answering sdxl; the other two are video architectures
		// with no template here, and taking them would silence the one mark that says so.
		{"Animagine XL 3.1", "sdxl"},
		{"AnimateDiff", ""},
		{"Wan Animate", ""},
		// 🔴 No template, so no suggestion. Measured on the same 20 rows: these are what
		// Civitai ranks highest today, and a wrong family would silence `base_model_missing` —
		// the row's only mark that it cannot generate.
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
