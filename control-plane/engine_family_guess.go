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
//   - when nothing matches confidently it returns "", and the picker stays empty. A wrong family
//     is worse than no family, because it silences `base_model_missing`, which is the row's only
//     mark that it cannot generate.
//
// Three of the four names that measurement found unrecognised (`Anima`, `Krea 2`, `LTXV 2.5`,
// `SD 1.5 Hyper`) have since been answered by giving the provider a template, not by loosening a
// needle. `LTXV 2.5` is still "" and stays that way until one exists.

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
	// equal is for a name that must match WHOLE. A substring needle is the right tool while the
	// family's name is a rare token ("sdxl", "zimage"); it is the wrong one when the name is a
	// common word other architectures build on — see the anima rule.
	equal []string
	// not is checked BEFORE any/equal and rejects the whole rule. It exists for a product whose
	// name outlived its architecture: the needle is right for every version but one.
	not []string
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
	// 🔴 `not` is Pony V7, and this was WRONG until 2026-09-15. Pony V6 is an SDXL fine-tune and
	// the needle is right for it; **V7 was rebuilt on AuraFlow** — a flow-matching DiT with a
	// UMT5 encoder, which the SDXL graph cannot load at all. Civitai publishes it as its own
	// baseModel string, `Pony V7` (measured), so without this the suggestion silenced
	// `base_model_missing` on a row that can only fail at generation.
	{family: "sdxl", not: []string{"ponyv7", "pony7"},
		any: []string{"sdxl", "pony", "illustrious", "noobai", "animagine"}},
	// After sdxl, because "sd15" is a substring of nothing above but the reverse order would
	// invite someone to shorten this needle to "sd1" and quietly swallow "sd1.x" spellings the
	// SDXL rule should have taken.
	{family: "sd15", any: []string{"sd15", "sd14", "stablediffusion15"}},
	// 🔴 anima matches WHOLE, and this is the rule that would be wrong as a substring. "anima"
	// is a prefix of three unrelated architectures that all appear as upstream base models:
	// Animagine (an SDXL fine-tune, and the rule above claims it), AnimateDiff and Wan-Animate
	// (video). As a needle it would take all three, each time silencing `base_model_missing` on
	// a row that cannot generate — the exact failure this whole file is written around. Every
	// Anima checkpoint and merge on Civitai publishes the bare string "Anima".
	{family: "anima", equal: []string{"anima"}},
	// 🔴 "krea2" and not "krea": FLUX.1 Krea dev is a FLUX.1 fine-tune and belongs to the rule
	// above, which takes it on "flux" before this one is reached — but a needle of "krea" here
	// would be a trap waiting for the day that order changes. The digit is what separates the
	// two products, so it is part of the needle.
	{family: "krea2", any: []string{"krea2"}},
	// 🔴 qwen-image-2.1 matches WHOLE, for anima's reason one step further. The string is Civitai's
	// own `Qwen 2`, which normalises to `qwen2`; as a SUBSTRING that needle would take every
	// `Qwen 2.5 …` and `Qwen2-VL …` spelling an upstream might publish, each time silencing
	// `base_model_missing` on a row this template cannot load.
	//
	// 🟢 This is the one Qwen string a rule is safe on, and the measurement is the whole argument
	// (2026-09-21, `types=Checkpoint` against the live API, both lists COMPLETE rather than sampled):
	//
	//	baseModels=Qwen 2  ->  5 checkpoints, every one of them Qwen-Image 2.1
	//	baseModels=Qwen    ->  20+ checkpoints spanning FIVE architectures — Qwen-Image,
	//	                       Qwen-Image-2512, Qwen-Image-Edit / 2509 / 2511, and a
	//	                       `Qwen-3-0.6B base/anima` that is not an image model at all
	//
	// So `qwen` and `qwen2` are not a family and its version: they are a junk drawer and a name.
	// The paragraph below is what the first line of that table buys, and it still stands.
	//
	// ⚠️ The residual risk is a FUTURE Qwen-Image release published under this same `Qwen 2` — the
	// suggestion would then offer 2.1's template for a topology it does not load. That is the
	// `Flux.2 Klein 9B-base` shape of trap, it cannot be pre-empted by a needle, and what answers
	// it is re-measuring this table rather than trusting it.
	{family: "qwen-image-2.1", equal: []string{"qwen2"}},
	// 🔴 No rule for either qwen-image-edit family here, on purpose. Measured 2026-09-20, Civitai
	// publishes the bare `Qwen` as baseModel for every Qwen-Image-Edit row found — the SAME
	// string Qwen-Image itself (text-to-image, ADR 0094's rejected "却下した案") would publish,
	// and this deployment has no template for that one. A rule here would suggest an EDIT-ONLY
	// family for a plain text-to-image row, which is worse than suggesting nothing (this file's
	// own opening rule: a wrong family silences `base_model_missing` on a row that cannot
	// generate). It would also have to pick BETWEEN 2509 and 2511 from a string that names
	// neither. engineFamilyUpstreams below carries hf only for the same reason.
	//
	// 🔴 Re-measured 2026-09-21 while the rule above was written, and the string is looser than
	// 2026-09-20 recorded: `baseModels=Qwen&types=Checkpoint` answers Qwen-Image, Qwen-Image-2512,
	// all three Qwen-Image-Edit versions AND a `Qwen-3-0.6B base/anima` row, which is a text
	// encoder. One string, five architectures. Whatever a rule here guessed, it would be wrong
	// about most of what carries the string.
}

// engineFamilyFromUpstream is the table above applied to one string, before the vocabulary is
// consulted.
func engineFamilyFromUpstream(upstream string) string {
	n := engineFamilyNormalise(upstream)
	if n == "" {
		return ""
	}
	for _, r := range engineFamilyRules {
		if engineFamilyExcluded(n, r.not) {
			continue
		}
		for _, needle := range r.any {
			if strings.Contains(n, engineFamilyNormalise(needle)) {
				return r.family
			}
		}
		for _, whole := range r.equal {
			if n == engineFamilyNormalise(whole) {
				return r.family
			}
		}
	}
	return ""
}

// engineFamilyExcluded answers whether a normalised name carries one of a rule's deny needles.
func engineFamilyExcluded(normalised string, not []string) bool {
	for _, needle := range not {
		if strings.Contains(normalised, engineFamilyNormalise(needle)) {
			return true
		}
	}
	return false
}

// --- the same map read backwards: which upstream names ARE this family -----------------------
//
// The guess above translates one upstream string into a family. Browsing needs the reverse — "show
// me what this deployment could actually load, of this family" — and the two sources answer it
// with completely different keys, so this table holds both.
//
// 🔴 Every value here was MEASURED against the live API on 2026-09-15, because both upstreams
// answer a name they do not know with an empty list rather than an error. A typo in this table
// is indistinguishable, on screen, from "there are no Anima models" — which is why the spellings
// are not derived from the needles above (`pony` is a needle; `Pony` is the string) and why a
// family with nothing measured is left EMPTY rather than guessed at.
type engineFamilyUpstream struct {
	// civitai is the exact `baseModel` strings, for `baseModels=` (repeatable, measured: two
	// values answer the union). A family is usually several — SDXL's fine-tunes each publish
	// their own name, and Klein publishes one per size.
	civitai []string
	// hf is the repository OTHERS tag as their base (`filter=base_model:<repo>`). One rather
	// than many because Hugging Face ANDs its filters: two would answer models derived from
	// both at once, which is nothing.
	//
	// ⚠️ It finds DERIVATIVES. The canonical repository does not tag itself, so
	// `base_model:circlestone-labs/Anima` does not return `circlestone-labs/Anima` — the browse
	// ranking and a search by name are what reach that, which is what they are for.
	hf string
}

var engineFamilyUpstreams = map[string]engineFamilyUpstream{
	// The distilled 1.5 variants are their own strings upstream and load through the same graph.
	"sd15": {civitai: []string{"SD 1.5", "SD 1.4", "SD 1.5 LCM", "SD 1.5 Hyper"},
		hf: "stable-diffusion-v1-5/stable-diffusion-v1-5"},
	// 🔴 `Pony V7` is deliberately absent — AuraFlow, not SDXL (see the deny needle above).
	// `SDXL Turbo` and `Animagine` are absent for the opposite reason: measured, Civitai answers
	// nothing for either, so they are not strings it publishes and listing them would be a guess.
	"sdxl": {civitai: []string{"SDXL 1.0", "Pony", "Illustrious", "NoobAI", "SDXL Lightning", "SDXL Hyper"},
		hf: "stabilityai/stable-diffusion-xl-base-1.0"},
	// 🔴 No Civitai entry: every spelling tried (`SD 3.5`, `SD 3.5 Large`, `SD 3.5 Medium`,
	// `SD3.5`, `Stable Diffusion 3.5`) answered empty on 2026-09-15. Filtering by this family
	// there is refused rather than answered with a list that means nothing.
	"sd35":        {hf: "stabilityai/stable-diffusion-3.5-large"},
	"flux1":       {civitai: []string{"Flux.1 D", "Flux.1 S"}, hf: "black-forest-labs/FLUX.1-dev"},
	"flux2-klein": {civitai: []string{"Flux.2 Klein 4B", "Flux.2 Klein 9B", "Flux.2 Klein 9B-base"}, hf: "black-forest-labs/FLUX.2-klein-4B"},
	// 🔴 Same as sd35: no Civitai spelling answered anything (`Z-Image`, `Z-Image Turbo`,
	// `ZImage`, `Z Image`).
	"zimage": {hf: "Tongyi-MAI/Z-Image-Turbo"},
	"anima":  {civitai: []string{"Anima"}, hf: "circlestone-labs/Anima"},
	"krea2":  {civitai: []string{"Krea 2"}, hf: "krea/Krea-2-Raw"},
	// 🔴 No Civitai entry, same reason as sd35/zimage above: the only baseModel Civitai
	// publishes for this model is the bare `Qwen` (measured 2026-09-20), which a future
	// Qwen-Image (text-to-image, this deployment has no template for it) row would publish
	// identically — filtering by family on that string would list models that cannot generate
	// at all. hf is the canonical publisher repo.
	"qwen-image-edit-2509": {hf: "Qwen/Qwen-Image-Edit-2509"},
	// 🔴 Same reasoning for the missing Civitai entry, and the hf repository is this version's own
	// (measured 2026-09-20: `base_model:Qwen/Qwen-Image-Edit-2511` answers derivatives — LoRAs and
	// GGUF conversions). The two versions are NOT interchangeable here even though the guess above
	// suggests neither: a row browsed as 2511 is loaded by the 2511 template, and a 2509 LoRA
	// listed under it would be offered for a graph it was not trained against.
	"qwen-image-edit-2511": {hf: "Qwen/Qwen-Image-Edit-2511"},
	// 🔴 The one Qwen family that DOES have a Civitai entry, and the measurement is why: this
	// version publishes `Qwen 2` rather than the bare `Qwen` its siblings share, and
	// `baseModels=Qwen 2&types=Checkpoint` answers FIVE rows and all five are Qwen-Image 2.1
	// (2026-09-21, the complete list and not a page of one). A string that names exactly one
	// architecture is a string this list can carry.
	//
	// ⚠️ Some of those rows are the CLOSED API version and carry nothing to download — the same
	// shape Krea 2's Civitai page has. Browsing finds them; the ingest has no source for them, and
	// that is the upstream's answer rather than a spelling to fix here.
	//
	// hf is the publisher's own repository: measured, `base_model:Qwen/Qwen-Image-2.1` answers
	// derivatives (the Comfy-Org mirror and several GGUF conversions).
	"qwen-image-2.1": {civitai: []string{"Qwen 2"}, hf: "Qwen/Qwen-Image-2.1"},
}

// engineFamilyValidFor answers whether `family` is a member of the vocabulary this request could
// filter by at all: the provider's when there is one, and the comfy vocabulary for the
// engine-less browse, which is the only kind of engine that has families.
func engineFamilyValidFor(provider, family string) bool {
	if strings.TrimSpace(family) == "" {
		return true
	}
	vocab := engineBaseModelsFor(provider)
	if len(vocab) == 0 {
		vocab = engineComfyFamilies
	}
	for _, f := range vocab {
		if f == strings.TrimSpace(family) {
			return true
		}
	}
	return false
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
