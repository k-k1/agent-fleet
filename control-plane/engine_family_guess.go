package main

// Guessing which workflow family an upstream model belongs to (ADR 0072 decision 2).
//
// Decision 2 says the OPERATOR declares the family and that an upstream display name must never
// be stored as one — "SDXL 1.0" and "Flux.1 D" are not what the comfy provider dispatches on,
// and rows carrying them looked complete and refused to generate. That decision stands. What it
// left is a picker that starts empty on every ingest while the answer is usually sitting in the
// same document: measured 2026-09-12 on Civitai's top 20, every row publishes a `baseModel`.
//
// So this translates, and the answer is a SUGGESTION the panel pre-selects and a person can
// change. Two rules keep it from becoming the thing decision 2 forbade:
//
//   - it only ever returns a member of the vocabulary the provider dispatches on, never the
//     upstream string;
//   - when nothing matches confidently it returns "", and the picker stays empty. Most of what
//     Civitai ranks highly has no family here at all — measured on the same 20: `Anima`,
//     `Krea 2`, `LTXV 2.5`, `SD 1.5 Hyper`. A wrong family is worse than no family, because it
//     silences `base_model_missing`, which is the row's only mark that it cannot generate.

import "strings"

// engineFamilyGuess maps an upstream base-model string onto one of the families `provider`
// dispatches on. "" when the provider has no family vocabulary, when the string is empty, or
// when nothing here recognises it.
func engineFamilyGuess(provider, upstream string) string {
	vocab := engineBaseModelsFor(provider)
	if len(vocab) == 0 {
		return ""
	}
	fam := engineFamilyFromUpstream(upstream)
	if fam == "" {
		return ""
	}
	// Never suggest a family this provider does not have a template for: the vocabulary is the
	// authority, and a deployment whose provider grows a sixth family gets it here for free.
	for _, v := range vocab {
		if v == fam {
			return fam
		}
	}
	return ""
}

// engineFamilyRule is one recognised architecture: the substrings an upstream name is matched
// against, and the family they mean.
//
// Matched on a normalised name (lower case, no spaces, dots or dashes) because the same
// architecture is written four ways across the two sources — "SDXL 1.0", "sdxl-turbo",
// "Flux.1 D", "FLUX.1-dev" all appear. The needles are written normalised too, so the table
// reads as what it is compared against.
type engineFamilyRule struct {
	family string
	any    []string
}

// SD 1.5 was deliberately absent here while the comfy provider had no template for it. It has
// one now, and the rule matches SD 1.4 too — the two share a UNet, a CLIP and a VAE, so they
// load through the identical graph.
//
// 🔴 SD 2.0 / 2.1 must NOT reach that rule. They are a different architecture (OpenCLIP-H, and
// v-prediction at 768), and no needle below matches them, which is the intended outcome rather
// than a gap: an unrecognised name leaves the picker empty and an operator declares it.
//
// ⚠️ The distilled 1.5 variants ("SD 1.5 LCM", "SD 1.5 Hyper") DO match, and correctly — they
// load through the same graph. What they do not share is the recipe: sampled at the family's 20
// steps and cfg 8 an LCM checkpoint burns out. That is the row's `params` to declare, and the
// family suggestion is right either way.
//
// The three SDXL derivatives are the point of this table. Pony, Illustrious and NoobAI are
// SDXL-architecture fine-tunes: they load through the SDXL graph, and they are what Civitai's
// image rankings are mostly made of, so without them the suggestion would be empty exactly
// where it is most wanted.
var engineFamilyRules = []engineFamilyRule{
	// Order matters where one name contains another: "flux2" and "flux.2" must be read before
	// the bare "flux1"/"flux" prefixes below them.
	{family: "flux2-klein", any: []string{"flux2", "klein"}},
	{family: "flux1", any: []string{"flux"}},
	{family: "sd35", any: []string{"sd35", "stablediffusion35"}},
	{family: "zimage", any: []string{"zimage"}},
	{family: "sdxl", any: []string{"sdxl", "pony", "illustrious", "noobai", "animagine"}},
	// After sdxl, because "sd15" is a substring of nothing above but the reverse order would
	// invite someone to shorten this needle to "sd1" and quietly swallow "sd1.x" spellings the
	// SDXL rule should have taken.
	{family: "sd15", any: []string{"sd15", "sd14", "stablediffusion15"}},
}

// engineFamilyFromUpstream is the table above applied to one string, before the vocabulary is
// consulted.
func engineFamilyFromUpstream(upstream string) string {
	n := engineFamilyNormalise(upstream)
	if n == "" {
		return ""
	}
	for _, r := range engineFamilyRules {
		for _, needle := range r.any {
			if strings.Contains(n, engineFamilyNormalise(needle)) {
				return r.family
			}
		}
	}
	return ""
}

// engineFamilyNormalise strips what the two sources vary freely: case, spaces, dots, dashes and
// underscores. 🔴 Not digits — "sd35" and "sd15" differ by nothing else.
func engineFamilyNormalise(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch r {
		case ' ', '.', '-', '_', '/':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
