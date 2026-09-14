package main

import (
	"strings"
	"testing"
)

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
		{"Pony Diffusion V6 XL", "sdxl"},
		// 🔴 The one version the needle must NOT take. Pony V7 was rebuilt on AuraFlow — a
		// flow-matching DiT with a UMT5 encoder — and the SDXL graph cannot load it. Civitai
		// publishes it under exactly this string (measured 2026-09-15), so before the deny
		// needle this answered "sdxl" and silenced the row's only mark that it cannot generate.
		{"Pony V7", ""},
		{"pony-v7-base", ""},
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
		{"Krea 2", "krea2"},
		{"Krea2", "krea2"},
		// 🔴 FLUX.1 Krea dev is a FLUX.1 fine-tune and must stay there. It is why this family's
		// needle carries the digit.
		{"FLUX.1 Krea dev", "flux1"},
		// 🔴 No template, so no suggestion. Measured on the same 20 rows: these are what
		// Civitai ranks highest today, and a wrong family would silence `base_model_missing` —
		// the row's only mark that it cannot generate.
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

// The table read backwards, which is what a family FILTER is made of. Two properties matter and
// neither is about any single name:
//
//	every family the provider dispatches on must be a key here, or the panel offers a filter
//	that answers 400;
//	nothing here may name a family the provider does not have — the filter would narrow a list
//	to models this deployment cannot load.
//
// 🔴 A missing per-source entry is LEGITIMATE and is not a gap: measured 2026-09-15, Civitai
// publishes no `baseModel` string for sd35 or zimage at all. It is the empty VALUE that the
// search route turns into a refusal, rather than into an empty list that reads as an answer.
func TestFamilyUpstreamsCoverTheVocabulary(t *testing.T) {
	for _, family := range engineComfyFamilies {
		up, ok := engineFamilyUpstreams[family]
		if !ok {
			t.Errorf("family %q has no upstream names, so browsing cannot offer it", family)
			continue
		}
		if len(up.civitai) == 0 && up.hf == "" {
			t.Errorf("family %q has neither source, so the filter can only ever refuse", family)
		}
	}
	vocab := map[string]bool{}
	for _, f := range engineComfyFamilies {
		vocab[f] = true
	}
	for family := range engineFamilyUpstreams {
		if !vocab[family] {
			t.Errorf("upstream names for %q, which the provider has no template for", family)
		}
	}
}

// 🔴 The names are the UPSTREAM's, not this deployment's. A family whose Civitai entry held the
// needle (`pony`) rather than the published string (`Pony`) would answer an empty list, because
// both upstreams answer an unknown value with no results and no error — indistinguishable, on
// screen, from "there are no models of this family".
func TestFamilyUpstreamNamesAreUpstreamSpellings(t *testing.T) {
	for family, up := range engineFamilyUpstreams {
		for _, name := range up.civitai {
			if name == strings.ToLower(name) && name != strings.ToUpper(name) {
				t.Errorf("%s declares Civitai name %q in lower case; the published strings are"+
					" capitalised (\"SD 1.5\", \"Pony\", \"Krea 2\")", family, name)
			}
			if engineFamilyFromUpstream(name) != family && family != "sdxl" {
				// sdxl is the exception on purpose: its names are other products' (Illustrious,
				// NoobAI) and the guess maps them to sdxl, which is the same answer read the
				// other way. Every other family must round-trip.
				t.Errorf("%s declares %q, which the guess reads as %q", family, name,
					engineFamilyFromUpstream(name))
			}
		}
		if up.hf != "" && !strings.Contains(up.hf, "/") {
			t.Errorf("%s declares Hugging Face base %q, which is not an owner/repository", family, up.hf)
		}
	}
}
