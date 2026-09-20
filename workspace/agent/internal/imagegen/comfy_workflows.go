package imagegen

// comfy_workflows.go — the six checkpoint-family workflow templates ADR 0072 decision 4
// requires (ComfyUI has no OpenAI-compatible surface, so "what generate_image sends" has to be
// a graph this package owns and version-pins, not something an engine version negotiates).
//
// Node ids are words rather than numbers so a validation error names something readable, the
// same choice `bench-image-engine.py` made. sdxl / zimage / klein are PORTED, byte-for-byte in
// shape, from that harness — the same graphs ADR 0072's "実測で解けた点" measured working on a
// real GPU (26/26 images, both checkpoints and both LoRA states). flux1 / sd35 were written from
// the checkpoint families' standard published ComfyUI recipes and have since been run on this
// deployment's hardware too (ADR 0072 P2 残作業 5, af-sandbox): flux1 generated on the first
// attempt, sd35 did NOT — see its own note below for what the golden test could not see. sd15
// has not been run here at all: its graph is SDXL's shape and its recipe is ComfyUI's own
// shipped default for the family, which is a citation and not a measurement.
// comfy_workflows_test.go pins each template's exact JSON shape so a future change is visible in
// the diff, which remains a different claim from "this graph is correct".
//
// Every template takes comfyFiles (the on-disk basenames ADR 0072 decision 2 declares, resolved
// from the catalogue's Flag vocabulary — see EngineFile) and comfyParams (the request-shaped
// knobs: prompt, negative prompt, seed, size, batch count, and the LoRAs to chain in). Sampler,
// steps, cfg and scheduler are still not a CALLER's choice — ADR 0069's vocabulary has no such
// fields — but they are no longer fixed either: each template states its family's recipe and a
// catalogue row may replace it field by field (comfyRecipe.with). Which fields a family accepts
// differs, and each says why where it differs: flux1 and klein refuse a declared cfg because the
// number a model card calls "CFG" is a different knob in those graphs.
//
// 🔴 None of the LoRA chains (P3) has been run on a GPU. The golden test pins their shape, which
// is the same claim it made about SD3.5 before that family turned out not to generate at all —
// so the loader node and its input names were read off ComfyUI v0.34.0's own source rather than
// assumed, and the completion definition stays "the same prompt and seed produce a different
// picture with the LoRA than without" (ADR 0072 phase P3), on real hardware.

import (
	"fmt"
	"strconv"
	"strings"
)

// comfyNegativePrompt is the same negative prompt bench-image-engine.py measured with, for the
// families that use one (SDXL, SD3.5). It is the DEFAULT, used when neither the catalogue row,
// the caller nor the engine's administrator named one — an empty string there would let every
// artifact through, which is not what a caller who said nothing meant.
const comfyNegativePrompt = "blurry, lowres, deformed, watermark, text"

// comfyNegativeText is what a guided family's negative CLIPTextEncode is given: whatever the
// three declaring places composed (comfyNegativeFor), and this fixed default when they composed
// nothing. The fallback is what keeps a request that says nothing identical to the graph this
// package has always sent — and it is a fallback rather than a floor, so a catalogue row that
// declares its own negative replaces it instead of being appended to boilerplate the publisher
// did not ask for.
func comfyNegativeText(p comfyParams) string {
	if n := strings.TrimSpace(p.Negative); n != "" {
		return n
	}
	return comfyNegativePrompt
}

// comfyFiles is the resolved, per-role file set for one model — see EngineFile for how the
// catalogue's Flag maps onto these fields. A family's template reads only the fields it needs;
// an empty field on a required role is refused by comfyBuildGraph before any HTTP call is made.
type comfyFiles struct {
	Checkpoint     string // Flag == ""
	DiffusionModel string // --diffusion-model
	// ClipL is the flag `--clip_l` names. Multi-encoder families (flux1, sd35) read it as one of
	// their named text encoders; single-encoder families (zimage, flux2-klein) read it as
	// THE text encoder — there is no second flag for "the only one", so the catalogue entry for
	// those families' text encoder file is declared with `--clip_l` by convention (documented
	// at the ingest UI, not a new flag this system did not already have).
	ClipL string
	// ClipG is `--clip_g`, and only SD3.5 reads it. It was missing from the first cut of this
	// vocabulary, which cost that family its whole template: SD3.5's three encoders are three
	// separate files and TripleCLIPLoader enumerates models/text_encoders alone, so with no way
	// to name clip_g the graph had to hand it the CHECKPOINT's filename — and ComfyUI answered
	// `Value not in list: clip_name1` (measured on af-sandbox, ADR 0072 P2 残作業 5).
	ClipG string
	T5xxl string // --t5xxl
	// Vae is `--vae`. The split families require it — they have no other source of one — and for
	// the single-checkpoint families (sdxl, sd35) it is OPTIONAL and overrides what the checkpoint
	// carries, which is the only way to use a checkpoint published without VAE tensors at all.
	// See comfyCheckpointVAE.
	Vae string
}

// resolveComfyFiles turns the catalogue's flat, sd.cpp-flavoured file list into the named roles
// the templates read. Files with an unrecognised or empty Name are silently dropped — decision 2
// only ever declares the flags in EngineFile's own comment, and one outside that set would mean a
// catalogue newer than this Agent, which should degrade by ignoring the extra field rather than
// by refusing every request from this engine.
func resolveComfyFiles(files []EngineFile) comfyFiles {
	var f comfyFiles
	for _, e := range files {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			continue
		}
		switch strings.TrimSpace(e.Flag) {
		case "":
			f.Checkpoint = name
		case "--diffusion-model":
			f.DiffusionModel = name
		case "--clip_l":
			f.ClipL = name
		case "--clip_g":
			f.ClipG = name
		case "--t5xxl":
			f.T5xxl = name
		case "--vae":
			f.Vae = name
		}
	}
	return f
}

// comfyParams is the request-shaped input every template renders against.
type comfyParams struct {
	// Op decides what the sampler starts from: an empty latent (generate), or the caller's own
	// picture encoded back into one (edit / inpaint). Empty means generate — the zero value has
	// to be the operation every template was written for first.
	Op     Op
	Prompt string
	// Negative is the composed negative prompt (comfyNegativeFor). Empty means "nobody said",
	// which is NOT the same as "exclude nothing" — see comfyNegativeText. Only the two guided
	// families read it; the other three sample where it could not matter (Caps.Negative).
	Negative  string
	Seed      int64
	Width     int
	Height    int
	BatchSize int
	// Image and Mask are names ComfyUI's own input directory holds, NOT paths in this container:
	// LoadImage's `image` input is an enumeration over that directory (nodes.py), so the bytes
	// have to be uploaded before a graph can name them. comfyProvider.uploadImage does that and
	// fills these in with whatever name the engine answered with.
	Image string
	Mask  string
	// Loras are already resolved against the catalogue and checked against this model's family
	// (comfyResolveLoras) — a template applies them, it does not decide whether they fit.
	Loras []comfyLora
	// Strength is the caller's own answer to "how much of my picture should change" on an edit,
	// nil for "use the recipe's". Request-shaped, unlike Params below: ADR 0069's vocabulary
	// gained this word on 2026-09-13 precisely because it is the caller's to turn.
	Strength *float64
	// Params is what the CATALOGUE row for the chosen model declares, zero-valued when it
	// declares nothing. Not request-shaped like everything else here: ADR 0069's vocabulary has
	// no steps or cfg field, so this is the administrator's declaration reaching the graph, not
	// a caller's.
	Params EngineParams
}

// isImageToImage is "the sampler starts from the caller's picture rather than from noise".
func (p comfyParams) isImageToImage() bool { return p.Op == OpEdit || p.Op == OpInpaint }

// comfyRecipe is one family's sampler settings: the four numbers and names that used to be
// literals inside each template.
//
// They are still the DEFAULT — every template states its own, and every one of those has been
// run on a GPU. What changed is that a catalogue row may now replace them field by field
// (ADR 0072 decision 4, widened): a checkpoint's author publishes "Steps 30, CFG 4" and the
// deployment had no way to honour it short of editing this file.
type comfyRecipe struct {
	Steps     int
	CFG       float64
	Sampler   string
	Scheduler string
}

// with is the merge, and it is per FIELD. A row that declares only `steps` keeps this family's
// sampler, scheduler and cfg — anything else would mean declaring one number silently reset the
// other three to whatever a zero value happens to be.
//
// 🔴 A sampler or scheduler name this Agent does not recognise is IGNORED, and the family's own
// is kept. The input is an enumeration in ComfyUI and an unknown value fails the whole prompt
// with `Value not in list` — after the cold start somebody waited through — so the two outcomes
// are "a picture made with the family's sampler" and "no picture at all". The first is a far
// better answer to a name that may simply be newer than this binary.
func (r comfyRecipe) with(p EngineParams) comfyRecipe {
	if p.Steps > 0 {
		r.Steps = p.Steps
	}
	if p.CFG > 0 {
		r.CFG = p.CFG
	}
	if comfyKnownSampler(p.Sampler) {
		r.Sampler = strings.TrimSpace(p.Sampler)
	}
	if comfyKnownScheduler(p.Scheduler) {
		r.Scheduler = strings.TrimSpace(p.Scheduler)
	}
	return r
}

// comfySamplerNames and comfySchedulerNames are the names this Agent is willing to send.
//
// NOT a copy of ComfyUI's whole list, and deliberately not presented as one: it is the set that
// has a known meaning here, and everything outside it falls back rather than being forwarded on
// the chance that the engine knows it. The five templates' own choices are all in it by
// construction — they are the first five entries a reader should be able to find.
var comfySamplerNames = map[string]bool{
	"euler": true, "euler_ancestral": true, "heun": true, "lms": true,
	"dpmpp_2m": true, "dpmpp_2m_sde": true, "dpmpp_3m_sde": true,
	"dpmpp_sde": true, "dpmpp_2s_ancestral": true,
	"res_multistep": true, "ddim": true, "uni_pc": true, "lcm": true,
}

var comfySchedulerNames = map[string]bool{
	"normal": true, "karras": true, "exponential": true, "sgm_uniform": true,
	"simple": true, "ddim_uniform": true, "beta": true,
}

func comfyKnownSampler(s string) bool   { return comfySamplerNames[strings.TrimSpace(s)] }
func comfyKnownScheduler(s string) bool { return comfySchedulerNames[strings.TrimSpace(s)] }

// comfyFamilyRecipes is every family's own sampler settings, in ONE place.
//
// They used to be literals inside each template, which was fine while the only reader was the
// template itself. It stopped being fine when the member-facing catalogue had to report the
// EFFECTIVE defaults (ADR 0081 decision 5) — the form's placeholders are what will run, and a
// second copy of these five numbers for the status route to read is exactly the kind of pair
// that drifts silently and shows a member a step count no picture was ever made at.
//
// Each entry has been run on a GPU; a catalogue row and then the request may replace any field
// of it (comfyRecipe.with, comfyEffectiveParams). A family that leaves a field zero does not
// read it at all — flux1 and klein have no cfg, klein no scheduler — which is the same fact
// comfyFamilyKnobs states for the form.
var comfyFamilyRecipes = map[comfyFamily]comfyRecipe{
	// 🔴 sd15 is the one entry that has NOT been run on a GPU here. It is ComfyUI's own shipped
	// default graph for this family verbatim (web/scripts/defaultGraph.js, the workflow that
	// loads with v1-5-pruned-emaonly) rather than a number picked to look like SDXL's, so that
	// what backs it is a citation a reviewer can check instead of this author's taste.
	ComfyFamilySD15:       {Steps: 20, CFG: 8, Sampler: "euler", Scheduler: "normal"},
	ComfyFamilySDXL:       {Steps: 20, CFG: 7, Sampler: "dpmpp_2m", Scheduler: "karras"},
	ComfyFamilySD35:       {Steps: 28, CFG: 4.5, Sampler: "dpmpp_2m", Scheduler: "sgm_uniform"},
	ComfyFamilyFlux1:      {Steps: 20, Sampler: "euler", Scheduler: "simple"},
	ComfyFamilyFlux2Klein: {Steps: 4, Sampler: "euler"},
	ComfyFamilyZImage:     {Steps: 8, CFG: 1, Sampler: "res_multistep", Scheduler: "simple"},
	// 🔴 anima has not been run on a GPU here either. These are ComfyUI's own shipped template
	// for the family (workflow_templates/templates/image_anima_base_v1.json: 30 steps, cfg 4,
	// euler, simple), which is also inside the range the model card prints (30-50 steps, CFG
	// 4-5) — a citation a reviewer can check rather than a number picked to look like SDXL's.
	//
	// ⚠️ These are the BASE/Aesthetic numbers. Anima-Turbo is a separate checkpoint distilled to
	// cfg 1 and 8-12 steps, and sampled at 30/4 it burns out — that is the row's `params` to
	// declare, exactly as for the distilled SD1.5 variants.
	ComfyFamilyAnima: {Steps: 30, CFG: 4, Sampler: "euler", Scheduler: "simple"},
	// 🔴 krea2 has not been run on a GPU here, and its entry points the OTHER way from anima's:
	// these are the DISTILLED numbers. ComfyUI ships templates for Krea 2 Turbo only
	// (image_krea2_turbo_t2i.json: 8 steps, cfg 1, euler, simple) and none for Raw, so the
	// citable recipe is the distilled one — and Raw, at its published 52 steps with a real cfg,
	// is the row's `params` to declare. Guessing Raw's cfg to make the default "the base model"
	// would be taste dressed up as a default.
	ComfyFamilyKrea2: {Steps: 8, CFG: 1, Sampler: "euler", Scheduler: "simple"},
	// The official 2509 template's KSampler, LoRA-switch false branch (ADR 0094 実測): steps 20,
	// cfg 4, euler, simple. 実測 A ran these exact numbers and the edit was followed; 実測 B is
	// the same graph at 8 steps (comfyTrialSteps), which is still recognisably edited.
	ComfyFamilyQwenImageEdit2509: {Steps: 20, CFG: 4, Sampler: "euler", Scheduler: "simple"},
}

// recipe is the family default with this request's model declaration merged over it.
func (p comfyParams) recipe(base comfyRecipe) comfyRecipe { return base.with(p.Params) }

// comfyDefaultSizes is the size list a family falls back to when the catalogue row declares
// none, and the first element is what a request that names no size at all gets.
//
// 🔴 This is per-FAMILY because the resolution a diffusion model was trained at is not a
// preference. Five of the six were trained around a megapixel and share the list this package
// has always sent; SD1.5's UNet was trained at 512, and asking it for 1024 does not fail — it
// returns a picture with the subject duplicated, two heads or a second torso, because the
// composition the model knows tiles at that size. That is the failure mode this repository
// keeps paying for: no error, no warning, a plausible-looking wrong answer. A catalogue row's
// own Sizes still override this (comfySizesFor), but a row that declares nothing has to land
// somewhere its family can actually generate.
//
// The SD1.5 entry stops at 768 on the long side for the same reason: 512x768 is the portrait
// every model card for this family prints, and past it the duplication starts.
var comfyDefaultSizes = map[comfyFamily][]string{
	ComfyFamilySD15: {"512x512", "512x768", "768x512", "640x512", "512x640"},
}

// comfyMegapixelSizes is what the five megapixel-era families share, and it is the exact list
// this package sent before sizes became a per-family answer.
var comfyMegapixelSizes = []string{"1024x1024", "1152x896", "896x1152", "1216x832", "832x1216"}

// comfySizesForFamily is the fallback list, never an override — see comfySizesFor.
func comfySizesForFamily(f comfyFamily) []string {
	if s, ok := comfyDefaultSizes[f]; ok {
		return s
	}
	return comfyMegapixelSizes
}

// comfyDefaultSize is what a request that named no size is generated at: the first preset of
// the family's own list, which is every family's native square.
func comfyDefaultSize(f comfyFamily) (int, int) {
	for _, s := range comfySizesForFamily(f) {
		if w, h, ok := parseSize(s); ok {
			return w, h
		}
	}
	return 1024, 1024
}

// comfyEditDenoise is how much of the caller's picture an edit changes when the caller says
// nothing: enough to follow a new prompt, little enough to leave the composition recognisable.
const comfyEditDenoise = 0.6

// denoise is what every family's sampler runs at.
//
// Inpaint stays at 1 even when a strength was asked for: the area OUTSIDE the mask is preserved
// by the noise mask, not by a partial denoise, so lowering it would only make the repainted area
// a weak echo of what was there. The caller is told so in the result's warnings (requestWarnings)
// rather than having the number quietly applied to something it does not mean.
//
// A strength outside (0,1] falls back to the recipe's own. The edge rejects one by value with a
// reason (HandleGenerate), so this is the unreachable half of the pair rather than the check —
// but it keeps this function total, and 0 in particular would divide by zero in the klein
// schedule below.
func (p comfyParams) denoise() float64 {
	if p.Op != OpEdit {
		return 1
	}
	if s := p.Strength; s != nil && *s > 0 && *s <= 1 {
		return *s
	}
	return comfyEditDenoise
}

// comfyRequestLatent builds what the sampler starts from, and is the whole of the image-to-image
// difference (ADR 0072 P2's remaining work): an empty latent for generate, and for edit / inpaint
// the caller's own picture run back through the model's VAE.
//
// emptyClass is the family's own empty-latent node — the three spellings (EmptyLatentImage,
// EmptySD3LatentImage, EmptyFlux2LatentImage) take the same three inputs, so they differ by name
// alone. VAEEncode does not: it is one node for every family, and the vae link is what makes it
// the right one.
//
// Node definitions checked against ComfyUI v0.34.0 rather than assumed (nodes.py):
// VAEEncode(pixels: IMAGE, vae: VAE) -> LATENT; SetLatentNoiseMask(samples: LATENT, mask: MASK)
// -> LATENT; LoadImage(image) -> (IMAGE, MASK); LoadImageMask(image, channel) -> MASK.
//
// 🔴 The mask is read off the RED channel, not alpha. LoadImage's own MASK output is `1.0 -
// alpha`, so an ordinary opaque black-and-white PNG — which is what a caller draws and what
// sd-server's route takes — would arrive as an all-zero mask and repaint nothing at all, with no
// error anywhere. Red gives the channel verbatim: white is the area to repaint, which is what
// this tool's own description promises.
//
// VAEEncodeForInpaint is deliberately NOT used. It blanks the masked pixels before encoding,
// which is what an inpainting-specific checkpoint expects; none of the families here is one,
// and handing an ordinary checkpoint a blanked hole is how inpainting produces grey mush.
func comfyRequestLatent(g comfyGraph, p comfyParams, vae []any, emptyClass string) ([]any, error) {
	if !p.isImageToImage() {
		g["lat"] = comfyNode{ClassType: emptyClass, Inputs: map[string]any{
			"width": p.Width, "height": p.Height, "batch_size": p.BatchSize}}
		return comfyLink("lat", 0), nil
	}
	if p.Image == "" {
		return nil, fmt.Errorf("%s needs an input image, and none reached the graph", p.Op)
	}
	g["img"] = comfyNode{ClassType: "LoadImage", Inputs: map[string]any{"image": p.Image}}
	g["enc"] = comfyNode{ClassType: "VAEEncode", Inputs: map[string]any{
		"pixels": comfyLink("img", 0), "vae": vae}}
	if p.Op != OpInpaint {
		return comfyLink("enc", 0), nil
	}
	if p.Mask == "" {
		return nil, fmt.Errorf("inpaint needs a mask image, and none reached the graph")
	}
	g["mask"] = comfyNode{ClassType: "LoadImageMask", Inputs: map[string]any{
		"image": p.Mask, "channel": "red"}}
	g["noisemask"] = comfyNode{ClassType: "SetLatentNoiseMask", Inputs: map[string]any{
		"samples": comfyLink("enc", 0), "mask": comfyLink("mask", 0)}}
	return comfyLink("noisemask", 0), nil
}

// comfyLora is one LoRA to chain into a template: the name ComfyUI knows it by on disk, and the
// strength both halves of LoraLoader get.
type comfyLora struct {
	Name   string
	Weight float64
}

// comfyApplyLoras chains one LoraLoader per requested LoRA between the loaders and everything
// downstream, and answers with the links that now carry MODEL and CLIP. Nothing is added when
// nothing was asked for, so the graph of a request without LoRAs is byte-for-byte the one the
// golden test already pins.
//
// The node is `LoraLoader` for every family, which is checked against ComfyUI v0.34.0's own
// definition rather than assumed (nodes.py, the pinned ref in deploy/aws/ecs/comfyui/Dockerfile):
// required inputs `model` (MODEL), `clip` (CLIP), `lora_name`, `strength_model`, `strength_clip`;
// returns (MODEL, CLIP). All seven families here have a CLIP to hand it — the split ones from
// their own text-encoder loader — so none of them needs LoraLoaderModelOnly.
//
// 🔴 `lora_name` is an ENUMERATION over folder_paths' "loras" list, i.e. `<models>/loras`
// searched RECURSIVELY, each entry a path relative to that directory (folder_paths.py's
// recursive_search). The box mounts the bucket's `image/` prefix as ComfyUI's models directory
// (60-engines.yaml links /ComfyUI/models -> /models/image), so a key `image/loras/x.safetensors`
// is listed as `x.safetensors` and the basename EngineLora carries is exactly right — while a key
// nested any deeper would be listed as `sub/x.safetensors` and rejected as `Value not in list`,
// the same way SD3.5's clip_name1 was (ADR 0072 P2 残作業 5). LoRAs land flat under
// `image/loras/`; that is the premise this depends on.
//
// One strength for both halves: that is what the published per-family LoRA workflows do, and
// load_lora_for_models only patches the keys a LoRA actually carries, so a text encoder the LoRA
// never touched is unaffected by naming a strength for it.
func comfyApplyLoras(g comfyGraph, loras []comfyLora, model, clip []any) (modelOut, clipOut []any) {
	for i, l := range loras {
		id := "lora" + strconv.Itoa(i+1)
		g[id] = comfyNode{ClassType: "LoraLoader", Inputs: map[string]any{
			"lora_name": l.Name, "strength_model": l.Weight, "strength_clip": l.Weight,
			"model": model, "clip": clip}}
		model, clip = comfyLink(id, 0), comfyLink(id, 1)
	}
	return model, clip
}

// comfyGraph is the API-format document /prompt takes: node id -> {class_type, inputs}.
type comfyGraph map[string]comfyNode

type comfyNode struct {
	ClassType string         `json:"class_type"`
	Inputs    map[string]any `json:"inputs"`
}

// comfyLink is a graph edge: [node id, output slot].
func comfyLink(node string, slot int) []any { return []any{node, slot} }

// comfyFamily is one of ADR 0072's checkpoint families (decision 2's `baseModel` values), each
// naming its own template builder.
type comfyFamily string

const (
	ComfyFamilySD15       comfyFamily = "sd15"
	ComfyFamilySDXL       comfyFamily = "sdxl"
	ComfyFamilySD35       comfyFamily = "sd35"
	ComfyFamilyFlux1      comfyFamily = "flux1"
	ComfyFamilyFlux2Klein comfyFamily = "flux2-klein"
	ComfyFamilyZImage     comfyFamily = "zimage"
	ComfyFamilyAnima      comfyFamily = "anima"
	ComfyFamilyKrea2      comfyFamily = "krea2"
	// ComfyFamilyQwenImageEdit2509 is ADR 0094's instruction-edit family: unlike every family
	// above, `op=edit` here is not the shared LoadImage+VAEEncode img2img path at a partial
	// denoise (comfyRequestLatent) — the input picture conditions the sampler through
	// TextEncodeQwenImageEditPlus at a FULL denoise, so it gets its own template rather than a
	// recipe entry in the shared one (comfyGraphQwenImageEdit2509).
	ComfyFamilyQwenImageEdit2509 comfyFamily = "qwen-image-edit-2509"
)

// comfyFamilies is every family comfyBuildGraph dispatches on. One list, so the acceptance
// check and the error message that tells an operator what to declare cannot disagree with the
// switch below. The Control Plane validates catalogue rows against the same spellings and
// keeps its copy honest by reading THIS file (engine_catalog_test.go).
//
// Ordered oldest architecture first, which is the order an operator's selector offers them in.
var comfyFamilies = []comfyFamily{
	ComfyFamilySD15, ComfyFamilySDXL, ComfyFamilySD35,
	ComfyFamilyFlux1, ComfyFamilyFlux2Klein, ComfyFamilyZImage,
	ComfyFamilyAnima, ComfyFamilyKrea2, ComfyFamilyQwenImageEdit2509,
}

// comfyFileFlags is the Flag vocabulary resolveComfyFiles understands, in the order a panel
// should offer them. "" is a single-file checkpoint (SDXL, SD3.5); the rest each name one part
// of a split model, which is the ONLY way FLUX.2 klein and Z-Image can be declared at all.
// comfy_test.go pins that every one of these actually resolves, and the Control Plane serves
// the list to the Console so the two cannot disagree about what a flag is called.
var comfyFileFlags = []string{"", "--diffusion-model", "--clip_l", "--clip_g", "--t5xxl", "--vae"}

// comfyFamilyList spells the vocabulary for a human: what to put in the catalogue's base_model.
func comfyFamilyList() string {
	out := make([]string, len(comfyFamilies))
	for i, f := range comfyFamilies {
		out[i] = string(f)
	}
	return strings.Join(out, ", ")
}

// comfyBuildGraph dispatches to the family's template, after checking every file the family
// needs is actually declared. A missing file is refused HERE, in the caller's language, rather
// than surfacing 100 requests later as a ComfyUI "node has no ckpt_name" validation error.
func comfyBuildGraph(family comfyFamily, files comfyFiles, p comfyParams) (comfyGraph, error) {
	switch family {
	case ComfyFamilySD15:
		return comfyGraphSD15(files, p)
	case ComfyFamilySDXL:
		return comfyGraphSDXL(files, p)
	case ComfyFamilySD35:
		return comfyGraphSD35(files, p)
	case ComfyFamilyFlux1:
		return comfyGraphFlux1(files, p)
	case ComfyFamilyFlux2Klein:
		return comfyGraphFlux2Klein(files, p)
	case ComfyFamilyZImage:
		return comfyGraphZImage(files, p)
	case ComfyFamilyAnima:
		return comfyGraphAnima(files, p)
	case ComfyFamilyKrea2:
		return comfyGraphKrea2(files, p)
	case ComfyFamilyQwenImageEdit2509:
		return comfyGraphQwenImageEdit2509(files, p)
	default:
		return nil, errUnknownComfyFamily(family)
	}
}

// comfyCheckpointVAE answers with the VAE a single-checkpoint family (sdxl, sd35) encodes and
// decodes with: the catalogue's own `--vae` file when the row declares one, and the checkpoint's
// third output otherwise. Adding the loader node here rather than in each template keeps "which
// VAE" one answer for the encode and the decode, which is what keeps them the same model.
//
// 🔴 CheckpointLoaderSimple's VAE output is None when the checkpoint carries no VAE tensors, and
// nothing on the way there refuses it: the graph validates, the box pays the 1-2.5 minute
// checkpoint switch, and then every op dies inside ComfyUI with `ERROR: VAE is invalid: None` —
// generate in VAEDecode, edit already in VAEEncode. Measured 2026-09-11 on this deployment with
// an Illustrious/SDXL checkpoint published without one, against another SDXL row that generated
// fine minutes later. A caller cannot act on that: `generate_image` has no VAE argument, so the
// declaration is the only place the fact can live. The default stays the checkpoint's own VAE,
// which is what every bundled checkpoint has and what the golden fixtures pin.
func comfyCheckpointVAE(g comfyGraph, f comfyFiles) []any {
	if f.Vae == "" {
		return comfyLink("ckpt", 2)
	}
	g["vae"] = comfyNode{ClassType: "VAELoader", Inputs: map[string]any{"vae_name": f.Vae}}
	return comfyLink("vae", 0)
}

// --- SDXL — ported from bench-image-engine.py's g_sdxl (GPU-verified, ADR 0072) -------------
//
// SD1.5 shares this graph. Both are one checkpoint carrying MODEL, CLIP and VAE, sampled with a
// KSampler and a real negative branch, and they differ only in the recipe and in what the saved
// file is named after. The two entry points below stay separate rather than collapsing into one
// `case`: engine_catalog_test.go reads THIS file to learn which files each family needs, by
// pairing a `comfyGraph*` body's `errComfyMissingFile` with the fields it refuses on — a family
// that never names itself in a refusal is a family that check silently stops measuring.

func comfyGraphSDXL(f comfyFiles, p comfyParams) (comfyGraph, error) {
	if f.Checkpoint == "" {
		return nil, errComfyMissingFile("sdxl", "checkpoint")
	}
	return comfyGraphSingleCheckpoint(f, p, ComfyFamilySDXL)
}

func comfyGraphSD15(f comfyFiles, p comfyParams) (comfyGraph, error) {
	if f.Checkpoint == "" {
		return nil, errComfyMissingFile("sd15", "checkpoint")
	}
	return comfyGraphSingleCheckpoint(f, p, ComfyFamilySD15)
}

// comfyGraphSingleCheckpoint is the body the two guided single-checkpoint families share. The
// caller has already refused a missing checkpoint; what is left to fail is an edit with no image
// or an inpaint with no mask, which comfyRequestLatent reports in the caller's own language.
func comfyGraphSingleCheckpoint(f comfyFiles, p comfyParams, family comfyFamily) (comfyGraph, error) {
	g := comfyGraph{
		"ckpt": {ClassType: "CheckpointLoaderSimple", Inputs: map[string]any{"ckpt_name": f.Checkpoint}},
	}
	vae := comfyCheckpointVAE(g, f)
	// CheckpointLoaderSimple returns (MODEL, CLIP, VAE), so the LoRA chain hangs off slots 0
	// and 1 and the VAE is never part of it — a LoRA never touches it.
	model, clip := comfyApplyLoras(g, p.Loras, comfyLink("ckpt", 0), comfyLink("ckpt", 1))
	g["pos"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": p.Prompt, "clip": clip}}
	g["neg"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": comfyNegativeText(p), "clip": clip}}
	lat, err := comfyRequestLatent(g, p, vae, "EmptyLatentImage")
	if err != nil {
		return nil, err
	}
	r := p.recipe(comfyFamilyRecipes[family])
	g["ks"] = comfyNode{ClassType: "KSampler", Inputs: map[string]any{
		"seed": p.Seed, "steps": r.Steps, "cfg": r.CFG, "sampler_name": r.Sampler, "scheduler": r.Scheduler,
		"denoise": p.denoise(),
		"model":   model, "positive": comfyLink("pos", 0), "negative": comfyLink("neg", 0),
		"latent_image": lat}}
	g["dec"] = comfyNode{ClassType: "VAEDecode", Inputs: map[string]any{"samples": comfyLink("ks", 0), "vae": vae}}
	g["save"] = comfyNode{ClassType: "SaveImage", Inputs: map[string]any{
		"filename_prefix": "af-" + comfyFamilyPrefixName(family), "images": comfyLink("dec", 0)}}
	return g, nil
}

// --- Z-Image-Turbo — ported from bench-image-engine.py's g_zimage (GPU-verified) ------------

func comfyGraphZImage(f comfyFiles, p comfyParams) (comfyGraph, error) {
	if f.DiffusionModel == "" {
		return nil, errComfyMissingFile("zimage", "diffusion model")
	}
	if f.ClipL == "" {
		return nil, errComfyMissingFile("zimage", "text encoder")
	}
	if f.Vae == "" {
		return nil, errComfyMissingFile("zimage", "vae")
	}
	g := comfyGraph{
		"unet": {ClassType: "UNETLoader", Inputs: map[string]any{"unet_name": f.DiffusionModel, "weight_dtype": "default"}},
		"clip": {ClassType: "CLIPLoader", Inputs: map[string]any{"clip_name": f.ClipL, "type": "lumina2", "device": "default"}},
		"vae":  {ClassType: "VAELoader", Inputs: map[string]any{"vae_name": f.Vae}},
	}
	// Before ModelSamplingAuraFlow, not after: the LoRA patches the diffusion model's weights,
	// while that node rewrites the sampling schedule of whatever model it is handed.
	model, clip := comfyApplyLoras(g, p.Loras, comfyLink("unet", 0), comfyLink("clip", 0))
	g["ms"] = comfyNode{ClassType: "ModelSamplingAuraFlow", Inputs: map[string]any{"shift": 3, "model": model}}
	g["pos"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": p.Prompt, "clip": clip}}
	g["neg"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": "", "clip": clip}}
	lat, err := comfyRequestLatent(g, p, comfyLink("vae", 0), "EmptySD3LatentImage")
	if err != nil {
		return nil, err
	}
	r := p.recipe(comfyFamilyRecipes[ComfyFamilyZImage])
	g["ks"] = comfyNode{ClassType: "KSampler", Inputs: map[string]any{
		"seed": p.Seed, "steps": r.Steps, "cfg": r.CFG, "sampler_name": r.Sampler, "scheduler": r.Scheduler,
		"denoise": p.denoise(),
		"model":   comfyLink("ms", 0), "positive": comfyLink("pos", 0), "negative": comfyLink("neg", 0),
		"latent_image": lat}}
	g["dec"] = comfyNode{ClassType: "VAEDecode", Inputs: map[string]any{"samples": comfyLink("ks", 0), "vae": comfyLink("vae", 0)}}
	g["save"] = comfyNode{ClassType: "SaveImage", Inputs: map[string]any{
		"filename_prefix": "af-zimage", "images": comfyLink("dec", 0)}}
	return g, nil
}

// --- FLUX.2 [klein] 4B — ported from bench-image-engine.py's g_klein (GPU-verified) ----------

func comfyGraphFlux2Klein(f comfyFiles, p comfyParams) (comfyGraph, error) {
	if f.DiffusionModel == "" {
		return nil, errComfyMissingFile("flux2-klein", "diffusion model")
	}
	if f.ClipL == "" {
		return nil, errComfyMissingFile("flux2-klein", "text encoder")
	}
	if f.Vae == "" {
		return nil, errComfyMissingFile("flux2-klein", "vae")
	}
	g := comfyGraph{
		"unet": {ClassType: "UNETLoader", Inputs: map[string]any{"unet_name": f.DiffusionModel, "weight_dtype": "default"}},
		"clip": {ClassType: "CLIPLoader", Inputs: map[string]any{"clip_name": f.ClipL, "type": "flux2", "device": "default"}},
		"vae":  {ClassType: "VAELoader", Inputs: map[string]any{"vae_name": f.Vae}},
	}
	model, clip := comfyApplyLoras(g, p.Loras, comfyLink("unet", 0), comfyLink("clip", 0))
	g["pos"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": p.Prompt, "clip": clip}}
	g["zero"] = comfyNode{ClassType: "ConditioningZeroOut", Inputs: map[string]any{"conditioning": comfyLink("pos", 0)}}
	g["guider"] = comfyNode{ClassType: "CFGGuider", Inputs: map[string]any{
		"model": model, "positive": comfyLink("pos", 0), "negative": comfyLink("zero", 0), "cfg": 1}}
	// 🔴 Steps and the sampler only. The `cfg: 1` above is the DISTILLED path's fixed value, not
	// a guidance scale a model card is talking about when it prints "CFG 4" — and this family
	// has no scheduler name to set at all (Flux2Scheduler takes a size, not a schedule name).
	r := p.recipe(comfyFamilyRecipes[ComfyFamilyFlux2Klein])
	g["sampler"] = comfyNode{ClassType: "KSamplerSelect", Inputs: map[string]any{"sampler_name": r.Sampler}}
	// Flux2Scheduler has no denoise of its own — it takes steps and a size and nothing else
	// (comfy_extras/nodes_flux.py, v0.34.0) — so an edit's partial denoise is a TAIL of that
	// schedule, cut by SplitSigmasDenoise. Its second output (low_sigmas) is the tail; taking
	// the first would sample the part an edit is meant to skip.
	//
	// The schedule is STRETCHED before it is cut, and that is what keeps `strength` meaning the
	// same thing here as in the other four families. Their samplers stretch it themselves:
	// `new_steps = int(steps/denoise)`, then the last `steps+1` sigmas (comfy/samplers.py,
	// KSampler.set_steps — BasicScheduler does the same), so the number of steps ACTUALLY sampled
	// does not move with the denoise, only where on the schedule they start. SplitSigmasDenoise
	// does not: its tail is `round(len(sigmas)*denoise)` steps of whatever it was handed
	// (comfy_extras/nodes_custom_sampler.py), so asking it to cut an unstretched 4-step schedule
	// buys fewer sampling steps the gentler the edit — 2 at the default 0.6, and 1 at 0.25. Since
	// that still produces a picture it is the kind of wrongness nothing reports.
	steps, d := r.Steps, p.denoise()
	if d < 1 {
		steps = int(float64(r.Steps) / d)
	}
	g["sigmas"] = comfyNode{ClassType: "Flux2Scheduler", Inputs: map[string]any{
		"steps": steps, "width": p.Width, "height": p.Height}}
	sigmas := comfyLink("sigmas", 0)
	if d < 1 {
		g["split"] = comfyNode{ClassType: "SplitSigmasDenoise", Inputs: map[string]any{
			"sigmas": sigmas, "denoise": d}}
		sigmas = comfyLink("split", 1)
	}
	lat, err := comfyRequestLatent(g, p, comfyLink("vae", 0), "EmptyFlux2LatentImage")
	if err != nil {
		return nil, err
	}
	g["noise"] = comfyNode{ClassType: "RandomNoise", Inputs: map[string]any{"noise_seed": p.Seed}}
	g["sca"] = comfyNode{ClassType: "SamplerCustomAdvanced", Inputs: map[string]any{
		"noise": comfyLink("noise", 0), "guider": comfyLink("guider", 0), "sampler": comfyLink("sampler", 0),
		"sigmas": sigmas, "latent_image": lat}}
	g["dec"] = comfyNode{ClassType: "VAEDecode", Inputs: map[string]any{"samples": comfyLink("sca", 0), "vae": comfyLink("vae", 0)}}
	g["save"] = comfyNode{ClassType: "SaveImage", Inputs: map[string]any{
		"filename_prefix": "af-klein", "images": comfyLink("dec", 0)}}
	return g, nil
}

// --- FLUX.1 (dev/schnell) — NOT YET RUN ON THIS DEPLOYMENT'S HARDWARE ------------------------
//
// The standard published ComfyUI text-to-image graph for the FLUX.1 family: DualCLIPLoader +
// UNETLoader + FluxGuidance's distilled-CFG path (BasicGuider, not CFGGuider — FLUX.1 folds
// guidance into the conditioning rather than sampling twice). Pinned by
// comfy_workflows_test.go so a future edit shows in the diff; that pins the SHAPE, not
// correctness on real hardware, which nobody has measured for this family yet (ADR 0072 P2
// scope: the completion definition only requires SDXL and one of {klein, zimage}).

func comfyGraphFlux1(f comfyFiles, p comfyParams) (comfyGraph, error) {
	if f.DiffusionModel == "" {
		return nil, errComfyMissingFile("flux1", "diffusion model")
	}
	if f.ClipL == "" || f.T5xxl == "" {
		return nil, errComfyMissingFile("flux1", "clip_l and t5xxl text encoders")
	}
	if f.Vae == "" {
		return nil, errComfyMissingFile("flux1", "vae")
	}
	g := comfyGraph{
		"unet": {ClassType: "UNETLoader", Inputs: map[string]any{"unet_name": f.DiffusionModel, "weight_dtype": "default"}},
		"clip": {ClassType: "DualCLIPLoader", Inputs: map[string]any{
			"clip_name1": f.T5xxl, "clip_name2": f.ClipL, "type": "flux", "device": "default"}},
		"vae": {ClassType: "VAELoader", Inputs: map[string]any{"vae_name": f.Vae}},
	}
	// BasicScheduler gets the patched model too, not the bare one: it derives the sigmas FROM
	// the model it is handed, so feeding it a different model than the guider samples with would
	// schedule one network and denoise another.
	model, clip := comfyApplyLoras(g, p.Loras, comfyLink("unet", 0), comfyLink("clip", 0))
	g["pos"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": p.Prompt, "clip": clip}}
	g["guidance"] = comfyNode{ClassType: "FluxGuidance", Inputs: map[string]any{
		"conditioning": comfyLink("pos", 0), "guidance": 3.5}}
	lat, err := comfyRequestLatent(g, p, comfyLink("vae", 0), "EmptySD3LatentImage")
	if err != nil {
		return nil, err
	}
	// 🔴 No cfg. FLUX.1 folds guidance into the conditioning (FluxGuidance above, and the guider
	// is BasicGuider rather than CFGGuider), so the number a model card calls "CFG" for this
	// family is FluxGuidance's `guidance` and not a sampler cfg — two different knobs with one
	// name. Applying the declared cfg here would turn "CFG 4" into a silently wrong picture.
	r := p.recipe(comfyFamilyRecipes[ComfyFamilyFlux1])
	g["sampler"] = comfyNode{ClassType: "KSamplerSelect", Inputs: map[string]any{"sampler_name": r.Sampler}}
	// BasicScheduler DOES have a denoise (unlike klein's Flux2Scheduler), and it cuts the tail
	// itself: total_steps = steps/denoise, then the last steps+1 sigmas. So an edit needs no
	// extra node here.
	g["scheduler"] = comfyNode{ClassType: "BasicScheduler", Inputs: map[string]any{
		"model": model, "scheduler": r.Scheduler, "steps": r.Steps, "denoise": p.denoise()}}
	g["noise"] = comfyNode{ClassType: "RandomNoise", Inputs: map[string]any{"noise_seed": p.Seed}}
	g["guider"] = comfyNode{ClassType: "BasicGuider", Inputs: map[string]any{
		"model": model, "conditioning": comfyLink("guidance", 0)}}
	g["sca"] = comfyNode{ClassType: "SamplerCustomAdvanced", Inputs: map[string]any{
		"noise": comfyLink("noise", 0), "guider": comfyLink("guider", 0), "sampler": comfyLink("sampler", 0),
		"sigmas": comfyLink("scheduler", 0), "latent_image": lat}}
	g["dec"] = comfyNode{ClassType: "VAEDecode", Inputs: map[string]any{"samples": comfyLink("sca", 0), "vae": comfyLink("vae", 0)}}
	g["save"] = comfyNode{ClassType: "SaveImage", Inputs: map[string]any{
		"filename_prefix": "af-flux1", "images": comfyLink("dec", 0)}}
	return g, nil
}

// --- SD3.5 — run on this deployment's hardware (ADR 0072 P2 残作業 5) --------------------------
//
// The checkpoint carries the MMDiT and the VAE; all THREE text encoders are separate files.
//
// 🔴 The first cut of this template assumed Stability shipped one bundle (UNet+VAE+CLIP-L+CLIP-G)
// and handed TripleCLIPLoader the checkpoint's own filename for clip_name1/clip_name2, on the
// theory that the loader would read whichever tensors it needed out of it. It does not, and it
// never could: TripleCLIPLoader's three inputs are enumerations over models/text_encoders, so a
// name that lives in models/checkpoints is not a value they accept. Measured on af-sandbox with
// `sd3.5_medium.safetensors`:
//
//	Value not in list: clip_name1: 'sd3.5_medium.safetensors'
//	  not in ['clip_l.safetensors', 'qwen_3_4b_fp8_mixed.safetensors', 't5xxl_fp8_e4m3fn.safetensors']
//
// The golden test could not have caught it — it pinned the shape of a graph nobody had run. The
// fix declares clip_g as its own file, which is what ComfyUI's published SD3.5 recipe does and
// what stable-diffusion.cpp's flag vocabulary (the one EngineFile borrows from) already spelled;
// `--clip_g` was simply left out when that vocabulary was copied across.

func comfyGraphSD35(f comfyFiles, p comfyParams) (comfyGraph, error) {
	if f.Checkpoint == "" {
		return nil, errComfyMissingFile("sd35", "checkpoint")
	}
	if f.ClipL == "" || f.ClipG == "" || f.T5xxl == "" {
		return nil, errComfyMissingFile("sd35", "clip_l, clip_g and t5xxl text encoders (SD3.5 declares all three as separate files)")
	}
	g := comfyGraph{
		"ckpt": {ClassType: "CheckpointLoaderSimple", Inputs: map[string]any{"ckpt_name": f.Checkpoint}},
		"clip": {ClassType: "TripleCLIPLoader", Inputs: map[string]any{
			// The order is ComfyUI's own published SD3.5 template. It is not load-bearing —
			// sd3_clip identifies each encoder from its state dict — but matching the published
			// recipe is what makes this graph comparable to one a person would build by hand.
			"clip_name1": f.ClipG, "clip_name2": f.ClipL, "clip_name3": f.T5xxl}},
	}
	vae := comfyCheckpointVAE(g, f)
	// The MODEL comes from the checkpoint and the CLIP from TripleCLIPLoader — this is the one
	// family where the two halves LoraLoader wants come from different nodes.
	model, clip := comfyApplyLoras(g, p.Loras, comfyLink("ckpt", 0), comfyLink("clip", 0))
	g["pos"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": p.Prompt, "clip": clip}}
	g["neg"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": comfyNegativeText(p), "clip": clip}}
	lat, err := comfyRequestLatent(g, p, vae, "EmptySD3LatentImage")
	if err != nil {
		return nil, err
	}
	r := p.recipe(comfyFamilyRecipes[ComfyFamilySD35])
	g["ks"] = comfyNode{ClassType: "KSampler", Inputs: map[string]any{
		"seed": p.Seed, "steps": r.Steps, "cfg": r.CFG, "sampler_name": r.Sampler, "scheduler": r.Scheduler,
		"denoise": p.denoise(),
		"model":   model, "positive": comfyLink("pos", 0), "negative": comfyLink("neg", 0),
		"latent_image": lat}}
	g["dec"] = comfyNode{ClassType: "VAEDecode", Inputs: map[string]any{"samples": comfyLink("ks", 0), "vae": vae}}
	g["save"] = comfyNode{ClassType: "SaveImage", Inputs: map[string]any{
		"filename_prefix": "af-sd35", "images": comfyLink("dec", 0)}}
	return g, nil
}

// --- Anima — ComfyUI's own shipped template, NOT YET RUN ON THIS DEPLOYMENT'S HARDWARE ------
//
// Anima is a 2B anime/illustration model (CircleStone Labs with Comfy Org, built on NVIDIA's
// Cosmos-Predict2-2B) published as three files: the diffusion model, Qwen3-0.6B as the text
// encoder, and the Qwen-Image VAE. So it declares like klein and Z-Image and SAMPLES like SDXL —
// a plain KSampler with a real negative branch, because this family is guided (cfg 4) rather
// than distilled.
//
// 🔴 `type: "stable_diffusion"` on the CLIPLoader is INERT here, and is the value ComfyUI's own
// template ships rather than a claim about what the encoder is. The pinned engine picks Anima's
// encoder by DETECTION, not by this field: comfy/sd.py (v0.34.0, the ref in
// deploy/aws/ecs/comfyui/Dockerfile) reads the state dict's hidden size, answers TEModel.QWEN3_06B
// at 1024, and takes the `comfy.text_encoders.anima` branch at line 1927 — which sits OUTSIDE
// every clip_type test. `anima` is not one of CLIPLoader's type values at all (nodes.py:1011), so
// there is nothing truer to write. That also means this family cannot be broken the way Krea 2
// can, where the type IS read and a default silently selects a different encoder.
//
// 🔴 EmptyLatentImage is the 4-channel node and the Qwen-Image VAE is a 16-channel autoencoder.
// That is not a mismatch: KSampler calls comfy.sample.fix_empty_latent_channels, which repeats an
// ALL-ZERO latent out to the model's own channel count (comfy/sample.py:45). The edit path is
// unaffected for the same reason — a VAEEncode latent is not empty, and it already comes back in
// this VAE's format. The official template uses this node for exactly this reason.
func comfyGraphAnima(f comfyFiles, p comfyParams) (comfyGraph, error) {
	if f.DiffusionModel == "" {
		return nil, errComfyMissingFile("anima", "diffusion model")
	}
	if f.ClipL == "" {
		return nil, errComfyMissingFile("anima", "text encoder (Qwen3-0.6B, declared as --clip_l)")
	}
	if f.Vae == "" {
		return nil, errComfyMissingFile("anima", "vae")
	}
	g := comfyGraph{
		"unet": {ClassType: "UNETLoader", Inputs: map[string]any{"unet_name": f.DiffusionModel, "weight_dtype": "default"}},
		"clip": {ClassType: "CLIPLoader", Inputs: map[string]any{"clip_name": f.ClipL, "type": "stable_diffusion", "device": "default"}},
		"vae":  {ClassType: "VAELoader", Inputs: map[string]any{"vae_name": f.Vae}},
	}
	model, clip := comfyApplyLoras(g, p.Loras, comfyLink("unet", 0), comfyLink("clip", 0))
	g["pos"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": p.Prompt, "clip": clip}}
	g["neg"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": comfyNegativeText(p), "clip": clip}}
	lat, err := comfyRequestLatent(g, p, comfyLink("vae", 0), "EmptyLatentImage")
	if err != nil {
		return nil, err
	}
	r := p.recipe(comfyFamilyRecipes[ComfyFamilyAnima])
	g["ks"] = comfyNode{ClassType: "KSampler", Inputs: map[string]any{
		"seed": p.Seed, "steps": r.Steps, "cfg": r.CFG, "sampler_name": r.Sampler, "scheduler": r.Scheduler,
		"denoise": p.denoise(),
		"model":   model, "positive": comfyLink("pos", 0), "negative": comfyLink("neg", 0),
		"latent_image": lat}}
	g["dec"] = comfyNode{ClassType: "VAEDecode", Inputs: map[string]any{
		"samples": comfyLink("ks", 0), "vae": comfyLink("vae", 0)}}
	g["save"] = comfyNode{ClassType: "SaveImage", Inputs: map[string]any{
		"filename_prefix": "af-anima", "images": comfyLink("dec", 0)}}
	return g, nil
}

// --- Krea 2 — ComfyUI's own shipped Turbo template, NOT YET RUN ON THIS DEPLOYMENT'S HARDWARE
//
// Krea 2 is a 12.9B DiT (Krea's first foundation model) published as the same three parts as
// anima — diffusion model, a Qwen3-VL-4B text encoder, the Qwen-Image VAE — and it samples
// through the same plain KSampler. Two things make it its own template rather than anima's with
// a different recipe: the CLIPLoader type below, and the family's two modes.
//
// 🔴 `type: "krea2"` IS READ, and this is the one place in this file where a wrong-looking-but-
// harmless default is neither. comfy/sd.py (v0.34.0) reaches the Krea2 tokenizer only through
// `clip_type == CLIPType.KREA2`; a Qwen3-VL-4B file loaded at the node's default falls into the
// generic qwen3vl branch instead, which loads, encodes, samples and returns a picture — made
// against different conditioning than the model was trained on. No error anywhere. (anima is the
// opposite case and its template says so.)
//
// ⚠️ The shipped template zeroes the negative out (ConditioningZeroOut) because Turbo samples at
// cfg 1, where a negative cancels exactly. This graph encodes a real one instead, because the
// SAME template has to serve a Krea-2-Raw row: undistilled, 52 steps, a real cfg, and a negative
// prompt that does move the picture. At cfg 1 the two graphs produce identical pixels, and the
// extra encode is the same text on every request, which ComfyUI serves from its execution cache
// after the first. Declaring cfg 1 is also what makes the engine stop ADVERTISING the negative
// prompt for that row (comfyModelTakesNegative).
func comfyGraphKrea2(f comfyFiles, p comfyParams) (comfyGraph, error) {
	if f.DiffusionModel == "" {
		return nil, errComfyMissingFile("krea2", "diffusion model")
	}
	if f.ClipL == "" {
		return nil, errComfyMissingFile("krea2", "text encoder (Qwen3-VL-4B, declared as --clip_l)")
	}
	if f.Vae == "" {
		return nil, errComfyMissingFile("krea2", "vae")
	}
	g := comfyGraph{
		"unet": {ClassType: "UNETLoader", Inputs: map[string]any{"unet_name": f.DiffusionModel, "weight_dtype": "default"}},
		"clip": {ClassType: "CLIPLoader", Inputs: map[string]any{"clip_name": f.ClipL, "type": "krea2", "device": "default"}},
		"vae":  {ClassType: "VAELoader", Inputs: map[string]any{"vae_name": f.Vae}},
	}
	model, clip := comfyApplyLoras(g, p.Loras, comfyLink("unet", 0), comfyLink("clip", 0))
	g["pos"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": p.Prompt, "clip": clip}}
	g["neg"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": comfyNegativeText(p), "clip": clip}}
	// EmptyLatentImage for the reason anima's template spells out: KSampler repeats an all-zero
	// latent out to the model's own channel count, and this VAE's 16 are not the node's 4.
	lat, err := comfyRequestLatent(g, p, comfyLink("vae", 0), "EmptyLatentImage")
	if err != nil {
		return nil, err
	}
	r := p.recipe(comfyFamilyRecipes[ComfyFamilyKrea2])
	g["ks"] = comfyNode{ClassType: "KSampler", Inputs: map[string]any{
		"seed": p.Seed, "steps": r.Steps, "cfg": r.CFG, "sampler_name": r.Sampler, "scheduler": r.Scheduler,
		"denoise": p.denoise(),
		"model":   model, "positive": comfyLink("pos", 0), "negative": comfyLink("neg", 0),
		"latent_image": lat}}
	g["dec"] = comfyNode{ClassType: "VAEDecode", Inputs: map[string]any{
		"samples": comfyLink("ks", 0), "vae": comfyLink("vae", 0)}}
	g["save"] = comfyNode{ClassType: "SaveImage", Inputs: map[string]any{
		"filename_prefix": "af-krea2", "images": comfyLink("dec", 0)}}
	return g, nil
}

// --- Qwen-Image-Edit-2509 — instruction editing, NOT YET RUN BY THIS AGENT (ADR 0094 P0) ------
//
// This family is not a member of comfyFamilyFixtures and does not go through comfyRequestLatent /
// comfyParams.denoise the way the other eight do — it genuinely is not the same shape (ADR 0094
// background). `op=edit` on every other family is img2img: LoadImage + VAEEncode feeding a
// PARTIAL denoise, so the picture the sampler starts from still carries the composition. Here the
// input picture conditions the sampler through TextEncodeQwenImageEditPlus (vision tokens plus a
// ~1MP reference latent, ADR 0094's background) while the sampler itself runs a FULL denoise —
// the VAEEncode below only fixes the output canvas's size, the same "denoise 1 ignores the
// latent's content, only its shape" fact anima and krea2 already rely on for their own
// EmptyLatentImage. Generate and inpaint are refused before this function is ever called
// (Caps.Ops via comfyFamilyOps — decision 3), so denoise is a literal 1 rather than a call to
// comfyParams.denoise(): there is no op this family reaches that ever wants anything else, and a
// caller's Strength is refused at the edge before a request reaches here (decision 2).
//
// Node graph (background / 実測, matched against the running engine's /object_info on
// 2026-09-20 — CLIPLoader's `qwen_image` type and TextEncodeQwenImageEditPlus/
// FluxKontextImageScale/CFGNorm are confirmed to exist, but this exact wiring has not been run):
// UNETLoader + CLIPLoader(type=qwen_image) + VAELoader load the three declared files; LoadImage's
// output is rescaled by FluxKontextImageScale to the nearest of the model's trained aspect ratios
// (decision 4 — this is also why no `size` reaches this family) and that SAME scaled picture
// feeds both TextEncodeQwenImageEditPlus (positive and negative — the negative is a real encode,
// not ConditioningZeroOut, because 実測 A ran at cfg 4, a guided recipe) and a VAEEncode that
// gives KSampler its starting latent's shape. ModelSamplingAuraFlow(shift 3) + CFGNorm patch the
// model the way zimage's ModelSamplingAuraFlow does, one step further per the official template.
func comfyGraphQwenImageEdit2509(f comfyFiles, p comfyParams) (comfyGraph, error) {
	if f.DiffusionModel == "" {
		return nil, errComfyMissingFile("qwen-image-edit-2509", "diffusion model")
	}
	if f.ClipL == "" {
		return nil, errComfyMissingFile("qwen-image-edit-2509", "text encoder (Qwen2.5-VL-7B, declared as --clip_l)")
	}
	if f.Vae == "" {
		return nil, errComfyMissingFile("qwen-image-edit-2509", "vae")
	}
	// The image is required only when Op says this IS an edit. Generate() never reaches this
	// builder with anything else (Caps.Ops refuses generate/inpaint before comfyBuildGraph is
	// called at all), but Studio()'s own sanity probe (comfyParams{Prompt: "x"}, Op == "") has to
	// keep succeeding on a row whose files are all declared — the same reason none of the other
	// families' generate path demands one either.
	if p.Op == OpEdit && p.Image == "" {
		return nil, fmt.Errorf("%s needs an input image, and none reached the graph", p.Op)
	}
	g := comfyGraph{
		"unet": {ClassType: "UNETLoader", Inputs: map[string]any{"unet_name": f.DiffusionModel, "weight_dtype": "default"}},
		"clip": {ClassType: "CLIPLoader", Inputs: map[string]any{"clip_name": f.ClipL, "type": "qwen_image", "device": "default"}},
		"vae":  {ClassType: "VAELoader", Inputs: map[string]any{"vae_name": f.Vae}},
		"img":  {ClassType: "LoadImage", Inputs: map[string]any{"image": p.Image}},
	}
	model, clip := comfyApplyLoras(g, p.Loras, comfyLink("unet", 0), comfyLink("clip", 0))
	g["scale"] = comfyNode{ClassType: "FluxKontextImageScale", Inputs: map[string]any{"image": comfyLink("img", 0)}}
	g["pos"] = comfyNode{ClassType: "TextEncodeQwenImageEditPlus", Inputs: map[string]any{
		"clip": clip, "vae": comfyLink("vae", 0), "prompt": p.Prompt, "image1": comfyLink("scale", 0)}}
	g["neg"] = comfyNode{ClassType: "TextEncodeQwenImageEditPlus", Inputs: map[string]any{
		"clip": clip, "vae": comfyLink("vae", 0), "prompt": comfyNegativeText(p), "image1": comfyLink("scale", 0)}}
	g["ms"] = comfyNode{ClassType: "ModelSamplingAuraFlow", Inputs: map[string]any{"shift": 3, "model": model}}
	g["norm"] = comfyNode{ClassType: "CFGNorm", Inputs: map[string]any{"model": comfyLink("ms", 0), "strength": 1}}
	g["enc"] = comfyNode{ClassType: "VAEEncode", Inputs: map[string]any{
		"pixels": comfyLink("scale", 0), "vae": comfyLink("vae", 0)}}
	r := p.recipe(comfyFamilyRecipes[ComfyFamilyQwenImageEdit2509])
	g["ks"] = comfyNode{ClassType: "KSampler", Inputs: map[string]any{
		"seed": p.Seed, "steps": r.Steps, "cfg": r.CFG, "sampler_name": r.Sampler, "scheduler": r.Scheduler,
		// Fixed at 1, not p.denoise(): 実測 C is decision 2's whole reason — the same graph at
		// denoise 0.6 comes back unedited, with no error and no warning.
		"denoise": 1,
		"model":   comfyLink("norm", 0), "positive": comfyLink("pos", 0), "negative": comfyLink("neg", 0),
		"latent_image": comfyLink("enc", 0)}}
	g["dec"] = comfyNode{ClassType: "VAEDecode", Inputs: map[string]any{"samples": comfyLink("ks", 0), "vae": comfyLink("vae", 0)}}
	g["save"] = comfyNode{ClassType: "SaveImage", Inputs: map[string]any{
		"filename_prefix": "af-qwen-image-edit-2509", "images": comfyLink("dec", 0)}}
	return g, nil
}
