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
	"errors"
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
	// Images and Mask are names ComfyUI's own input directory holds, NOT paths in this container:
	// LoadImage's `image` input is an enumeration over that directory (nodes.py), so the bytes
	// have to be uploaded before a graph can name them. comfyProvider.uploadImage does that and
	// fills these in with whatever name the engine answered with.
	//
	// Images is in the caller's own order and the FIRST one is the picture being edited — every
	// family reads that one, and only the instruction-edit families read any of the rest (ADR
	// 0094 decision 5). A template must take what it can use and say nothing about the others:
	// the count was already refused against Caps.MaxInputs (comfyCheckInputs) before any upload.
	Images []string
	Mask   string
	// Transparent is the caller's `background=transparent`. Only a family that decodes an alpha
	// channel reads it (comfyFamilyRow.Alpha); for it, anything else — auto, opaque, unset —
	// means the alpha is stripped before the save.
	Transparent bool
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

// image is the n-th reference (0-based) or "" when the caller sent fewer than that. Templates read
// the ones they support through this rather than indexing, so a family that wires image2 does not
// panic on the request that sent one picture — which is every request the other six families make.
func (p comfyParams) image(n int) string {
	if n < 0 || n >= len(p.Images) {
		return ""
	}
	return p.Images[n]
}

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

// recipe is the family default with this request's model declaration merged over it.
func (p comfyParams) recipe(base comfyRecipe) comfyRecipe { return base.with(p.Params) }

// comfyMegapixelSizes is what the megapixel-era families share, and it is the exact list this
// package sent before sizes became a per-family answer (comfyFamilyRow.Sizes).
var comfyMegapixelSizes = []string{"1024x1024", "1152x896", "896x1152", "1216x832", "832x1216"}

// comfySizesForFamily is the fallback list, never an override — see comfySizesFor.
func comfySizesForFamily(f comfyFamily) []string {
	if r, ok := comfyFamilyRowFor(f); ok && len(r.Sizes) > 0 {
		return r.Sizes
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
	// Only the first: these six families have nowhere to put a second reference, and the count was
	// already held to Caps.MaxInputs — which is 1 for all of them — before anything was uploaded.
	if p.image(0) == "" {
		return nil, fmt.Errorf("%s needs an input image, and none reached the graph", p.Op)
	}
	g["img"] = comfyNode{ClassType: "LoadImage", Inputs: map[string]any{"image": p.image(0)}}
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

// comfyWith lays a node's own inputs over a shared set, answering a fresh map. `over` wins, so a
// template can never have an optional extra silently replace an input it states itself — and the
// shared set is not mutated, which matters because the same `refs` map is laid over both the
// positive and the negative encode.
func comfyWith(base, over map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

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
	// ComfyFamilyQwenImageEdit2511 is the same instruction-edit shape one upstream release later,
	// and it is a SECOND family rather than a row's `params` because the two templates differ in
	// WIRING: 2511 puts a FluxKontextMultiReferenceLatentMethod between each conditioning and the
	// sampler (comfyGraphQwenImageEdit). ADR 0094 decision 6's rule is "one family per TOPOLOGY,
	// not per version" — a 2512 that ships 2511's wiring would be a row here, not a tenth family —
	// and the version is in both names because a name that meant "the topology 2509 introduced"
	// while spelling itself `qwen-image-edit` stops making sense the day that happens.
	ComfyFamilyQwenImageEdit2511 comfyFamily = "qwen-image-edit-2511"
	// ComfyFamilyQwenImage21 is ADR 0098's family, and it is the first one here that BOTH generates
	// from a prompt alone and edits by instruction — the two published templates are the same graph
	// with a different latent into the sampler (comfyGraphQwenImage21). It is not a version of the
	// two above and shares no file with them: a 7B single-stream DiT against their 20B MMDiT, a
	// Qwen3-VL-8B text encoder against Qwen2.5-VL-7B, and a 64-channel RGBA autoencoder at a
	// spatial downscale of 16 against the 16-channel Qwen-Image VAE at 8.
	//
	// 🔴 Spelled with the dot the product carries. The Control Plane validates rows against this
	// exact string (engine_catalog.go's engineComfyFamilies) and engine_catalog_test.go reads it
	// out of this file, so the two spellings are one spelling.
	ComfyFamilyQwenImage21 comfyFamily = "qwen-image-2.1"
)

// comfyFamilyRow is everything a family declares that is a VALUE rather than a graph: the
// sampler recipe, which knobs its template reads, what a trial samples at, the sizes it falls
// back to, whether a negative prompt moves it, and — for the instruction-edit families — the
// wiring that separates their topologies.
//
// One row per family, because ADR 0094 未解決 4 measured what the alternative costs: adding a
// family meant writing its name in seventeen places, nine of them in this package, each of them
// a separate table or predicate. Forgetting one is not a build error — it is a silent wrong
// answer. A family missing from the old comfyFamilyInstructionEdit advertises `generate` and
// `inpaint` its template cannot build; missing from the old comfyTrialSteps, a trial run
// quietly samples the full recipe and costs what the batch costs.
//
// 🔴 What is NOT here is the template. comfyBuildGraph keeps its switch, and not out of
// conservatism: a `Build func(...)` field here does not compile. The templates READ this table
// (comfyRecipe, and the wiring below), so a table that also held them is an initialisation
// cycle — measured, `initialization cycle for comfyFamilyRows ... refers to comfyGraphSD15 ...
// refers to comfyFamilyRows`. Whoever takes 未解決 4's remaining step across the module boundary
// inherits that constraint: the data and the graphs cannot be one declaration in Go unless the
// graphs take their row as an argument.
//
// The per-family template functions stay one per family for a second reason too:
// engine_catalog_test.go (the other module) learns which files a family needs by pairing a
// `comfyGraph*` body's errComfyMissingFile with the fields it refuses on, so a family that never
// names itself in a refusal is a family that check silently stops measuring.
type comfyFamilyRow struct {
	Family comfyFamily
	// Recipe is this family's own sampler settings, and the reason they are declared once rather
	// than as literals inside the template: the member-facing catalogue reports the EFFECTIVE
	// defaults (ADR 0081 decision 5), so the form's placeholders are what will run. A second copy
	// for the status route to read is exactly the pair that drifts silently and shows a member a
	// step count no picture was ever made at. A catalogue row and then the request may replace
	// any field (comfyRecipe.with, comfyEffectiveParams); a family that leaves a field zero does
	// not read it at all — flux1 and klein have no cfg, klein no scheduler — which is the same
	// fact SamplerKnobs states for the form.
	Recipe comfyRecipe
	// SamplerKnobs is the subset of `steps cfg sampler scheduler` this family's template actually
	// reads. `negative` and `strength` are NOT listed here: comfyFamilyKnobs appends them from
	// Guided and InstructionEdit, so the two answers cannot disagree with the ones Caps gives.
	//
	// Derived from the templates and to be read next to them: flux1 and klein fold guidance into
	// the conditioning, so the number a model card calls "CFG" is a different knob there, and
	// klein's Flux2Scheduler takes a size and not a schedule name.
	SamplerKnobs []string
	// TrialSteps is what a trial run samples at (ADR 0081 decision 11). The number wanted is the
	// smallest one at which the composition is still recognisable, which only a picture can
	// answer — every entry below says whether a picture answered it, and most say no.
	//
	// Two families declare their recipe's own number (klein 4, krea2 8) because a distilled model
	// has no cheaper setting: a trial there differs from the batch in nothing but where it sits
	// in the queue. Zero is what an undeclared family answers, and comfyTrialStepsFor turns it
	// into "leave the caller's steps alone".
	TrialSteps int
	// Guided is whether a negative prompt can move this family's picture at all. 🔴 Only the
	// GUIDED families: the distilled ones sample at cfg 1 (zimage's KSampler, klein's CFGGuider)
	// or fold FLUX.1's guidance into the conditioning (BasicGuider, no negative input at all),
	// and at cfg 1 classifier-free guidance is `uncond + 1*(cond - uncond)`, which is cond
	// exactly. The negative words would ride in the graph, cost a text encode, and change no
	// pixel. Wiring them anyway and reporting the capability as true is worse than refusing: the
	// caller gets no warning, the picture looks right, and the thing they asked to keep out is
	// in it.
	//
	// ⚠️ This is the family's TEMPLATE, not the answer a member gets: anima and krea2 are true
	// here because their graphs encode a real negative, while their distilled variants
	// (Anima-Turbo, Krea 2 Turbo) declare cfg 1 and cancel it anyway. comfyModelTakesNegative is
	// what puts the two facts together, and it is the one every caller asks.
	Guided bool
	// Sizes is the list a catalogue row falls back to when it declares none, and the first
	// element is what a request that names no size at all gets. Empty means comfyMegapixelSizes.
	//
	// 🔴 Per-FAMILY because the resolution a diffusion model was trained at is not a preference.
	// SD1.5's UNet was trained at 512, and asking it for 1024 does not fail — it returns a picture
	// with the subject duplicated, two heads or a second torso, because the composition the model
	// knows tiles at that size. That is the failure mode this repository keeps paying for: no
	// error, no warning, a plausible-looking wrong answer. A catalogue row's own Sizes still
	// override this (comfySizesFor), but a row that declares nothing has to land somewhere its
	// family can actually generate.
	Sizes []string
	// Prefix is the short word this family's template puts in SaveImage's filename_prefix, when
	// that is not the family's own spelling. It is how the reproduction record reads the family
	// back off a picture (comfyFamilyFromPrefix).
	Prefix string
	// InstructionEdit is non-nil for exactly the families whose `op=edit` is instruction editing
	// rather than the shared partial-denoise img2img — and it carries the wiring that separates
	// their topologies.
	//
	// 🔴 One field and not two lists. The old pair (a predicate naming two families, a wiring map
	// keyed by the same two) could disagree: a third topology added to the wiring map and
	// forgotten in the predicate would advertise `generate`, `inpaint` and `strength` it cannot
	// build, and 実測 C is what that costs — the same graph at denoise 0.6 came back unedited,
	// with no error and no warning.
	//
	// ⚠️ It stops being the single fact decisions 2-5 and 12 turn on the moment a family edits by
	// instruction through a DIFFERENT builder, which qwen-image-2.1 does (ADR 0098). Decision 2's
	// half moved to FixedDenoiseEdit below; this field stays narrow, and a family it is non-nil
	// for is exactly a family comfyGraphQwenImageEdit can build.
	InstructionEdit *comfyQwenEditWiring
	// FixedDenoiseEdit is ADR 0094 decision 2's fact on its own: `op=edit` here conditions the
	// sampler through the picture at a FULL denoise, so a caller's Strength has nowhere to go.
	// Every InstructionEdit family is one of these; qwen-image-2.1 is one WITHOUT being one of
	// those, which is why the two are separate fields (a test pins the implication).
	FixedDenoiseEdit bool
	// Ops is which ops this family's template can build, and empty means the permissive default
	// (ADR 0094 decision 3 — generate, edit and inpaint). It is a declaration rather than a
	// derivation of InstructionEdit because the two stopped agreeing: qwen-image-2.1 edits by
	// instruction AND generates from a prompt alone.
	//
	// 🔴 A family that cannot generate can also never be handed a size — the only place a size
	// reaches is an EmptyLatentImage, which only a generate path builds — so decision 4's
	// "this family has no sizes" is READ OFF this field (comfyFamilyHasNoSizes) rather than
	// declared twice.
	Ops []Op
	// RefInputs is how many reference pictures the template actually READS, and 0 means one
	// (ADR 0094 decision 5). 🔴 The number and the code path move together: raising it without
	// widening the upload and the template is not a smaller version of the feature — the extra
	// pictures pass comfyCheckInputs, are never wired, and the caller gets a picture that ignored
	// them with no warning anywhere (実測 C's shape of lie).
	RefInputs int
	// Alpha is whether this family's VAE decodes to FOUR channels — a picture with an alpha
	// channel, which SaveImage keeps. Only qwen-image-2.1 (ADR 0098): every other family's
	// decode is RGB, so a transparent background is something they cannot produce at all.
	//
	// 🔴 It decides two things, and both would lie without it: the template strips the alpha
	// unless the caller asked for `background=transparent` (measured: an ordinary photograph
	// comes back with alpha 252-255 on 47 % of its pixels — up to 1.2 % see-through, and enough
	// to keep every thumbnail a PNG, fs_thumb.go's opaque()), and comfyWarnings stops saying
	// "opaque produced" to a caller who got exactly the transparency they asked for.
	Alpha bool
	// Dialect, QualityPrefixes, StepsRange and CFGRange are how a person writes for this family
	// (ADR 0100 decision 7): the prompt's dialect, the quality prefixes its model cards print, and
	// the published ranges the steps and cfg sit in. They are advice and nothing reads them to
	// build a graph; they live here so the pane's family card and get_image_studio's model facts
	// are drawn from the same row as the knobs, rather than from a second table in the Console
	// that a new family has to remember.
	//
	// CFGRange is zero for a family whose template does not read cfg (flux1, klein), which is the
	// same fact SamplerKnobs states.
	Dialect         comfyDialect
	QualityPrefixes []string
	StepsRange      [2]int
	CFGRange        [2]float64
}

// comfyDialect is how a family's prompt is written: a comma-separated tag list, or sentences.
type comfyDialect string

const (
	comfyDialectTags      comfyDialect = "tags"
	comfyDialectSentences comfyDialect = "sentences"
)

// comfyFamilyRows is the vocabulary itself, ordered oldest architecture first — which is the
// order an operator's selector offers them in. The Control Plane validates catalogue rows
// against the same spellings and keeps its copy honest by reading the constants above out of
// THIS file (engine_catalog_test.go).
var comfyFamilyRows = []comfyFamilyRow{{
	Family: ComfyFamilySD15,
	// 🔴 The one recipe that has NOT been run on a GPU here. It is ComfyUI's own shipped default
	// graph for this family verbatim (web/scripts/defaultGraph.js, the workflow that loads with
	// v1-5-pruned-emaonly) rather than a number picked to look like SDXL's, so that what backs it
	// is a citation a reviewer can check instead of this author's taste.
	Recipe:       comfyRecipe{Steps: 20, CFG: 8, Sampler: "euler", Scheduler: "normal"},
	SamplerKnobs: comfyKnobsSampled, TrialSteps: 10, Guided: true,
	// Stops at 768 on the long side for the reason the Sizes field states: 512x768 is the
	// portrait every model card for this family prints, and past it the duplication starts.
	Sizes: []string{"512x512", "512x768", "768x512", "640x512", "512x640"},
	// Same tag dialect as SDXL: SD1.5 fine-tunes are overwhelmingly booru-tagged, and this is the
	// prefix their cards print.
	Dialect: comfyDialectTags, QualityPrefixes: []string{"masterpiece, best quality"},
	StepsRange: [2]int{20, 30}, CFGRange: [2]float64{6, 9},
}, {
	Family:       ComfyFamilySDXL,
	Recipe:       comfyRecipe{Steps: 20, CFG: 7, Sampler: "dpmpp_2m", Scheduler: "karras"},
	SamplerKnobs: comfyKnobsSampled, TrialSteps: 10, Guided: true,
	// Two dialects share the family: base/Illustrious/NoobAI take `masterpiece, best quality`,
	// Pony takes the score tags. Both are offered and the card says which is which.
	Dialect:         comfyDialectTags,
	QualityPrefixes: []string{"masterpiece, best quality", "score_9, score_8_up, score_7_up"},
	StepsRange:      [2]int{20, 40}, CFGRange: [2]float64{5, 9},
}, {
	Family:       ComfyFamilySD35,
	Recipe:       comfyRecipe{Steps: 28, CFG: 4.5, Sampler: "dpmpp_2m", Scheduler: "sgm_uniform"},
	SamplerKnobs: comfyKnobsSampled, TrialSteps: 12, Guided: true,
	Dialect: comfyDialectSentences, StepsRange: [2]int{24, 40}, CFGRange: [2]float64{3.5, 6},
}, {
	Family:       ComfyFamilyFlux1,
	Recipe:       comfyRecipe{Steps: 20, Sampler: "euler", Scheduler: "simple"},
	SamplerKnobs: []string{"steps", "sampler", "scheduler"}, TrialSteps: 8,
	Dialect: comfyDialectSentences, StepsRange: [2]int{16, 32},
}, {
	Family:       ComfyFamilyFlux2Klein,
	Recipe:       comfyRecipe{Steps: 4, Sampler: "euler"},
	SamplerKnobs: []string{"steps", "sampler"},
	// The recipe's own 4: a distilled 4-step model has no cheaper trial. See the field.
	TrialSteps: 4,
	Prefix:     "klein",
	Dialect:    comfyDialectSentences,
	StepsRange: [2]int{4, 8},
}, {
	Family:       ComfyFamilyZImage,
	Recipe:       comfyRecipe{Steps: 8, CFG: 1, Sampler: "res_multistep", Scheduler: "simple"},
	SamplerKnobs: comfyKnobsSampled, TrialSteps: 4,
	Dialect: comfyDialectSentences, StepsRange: [2]int{6, 12}, CFGRange: [2]float64{1, 2},
}, {
	Family: ComfyFamilyAnima,
	// 🔴 Not run on a GPU here either. ComfyUI's own shipped template for the family
	// (workflow_templates/templates/image_anima_base_v1.json: 30 steps, cfg 4, euler, simple),
	// which is also inside the range the model card prints (30-50 steps, CFG 4-5) — a citation a
	// reviewer can check rather than a number picked to look like SDXL's.
	//
	// ⚠️ These are the BASE/Aesthetic numbers. Anima-Turbo is a separate checkpoint distilled to
	// cfg 1 and 8-12 steps, and sampled at 30/4 it burns out — that is the row's `params` to
	// declare, exactly as for the distilled SD1.5 variants.
	Recipe:       comfyRecipe{Steps: 30, CFG: 4, Sampler: "euler", Scheduler: "simple"},
	SamplerKnobs: comfyKnobsSampled,
	// A third of the family's 30, rather than SDXL's 10 out of 20: the model card's floor for the
	// undistilled versions is 30 steps, so a trial at 10 would be judging a composition this
	// family does not produce at 10.
	TrialSteps: 12, Guided: true,
	// Danbooru tags, captions, or both — the model card documents all three; tags are what a
	// prompt is most often wrong in. The card's own prefix, and the shorter one it tells
	// Anima-Aesthetic users to prefer (without score_* tags), so the advice holds for both.
	Dialect:         comfyDialectTags,
	QualityPrefixes: []string{"masterpiece, best quality, score_7, safe", "masterpiece, best quality"},
	StepsRange:      [2]int{30, 50}, CFGRange: [2]float64{4, 5},
}, {
	Family: ComfyFamilyKrea2,
	// 🔴 Not run on a GPU here, and this one points the OTHER way from anima's: these are the
	// DISTILLED numbers. ComfyUI ships templates for Krea 2 Turbo only (image_krea2_turbo_t2i.json:
	// 8 steps, cfg 1, euler, simple) and none for Raw, so the citable recipe is the distilled one
	// — and Raw, at its published 52 steps with a real cfg, is the row's `params` to declare.
	// Guessing Raw's cfg to make the default "the base model" would be taste dressed up as a
	// default.
	Recipe:       comfyRecipe{Steps: 8, CFG: 1, Sampler: "euler", Scheduler: "simple"},
	SamplerKnobs: comfyKnobsSampled,
	// The recipe is already 8, so for a Turbo row a trial IS the batch (klein's case). It is kept
	// for the Raw rows, where it is the family's published 52 cut to a sixth.
	TrialSteps: 8, Guided: true,
	// No quality-tag convention: trained for aesthetics, and its own enhancer rewrites a short
	// prompt into a paragraph. The ranges span both modes — Turbo (8, cfg 1) and Raw (52, real
	// guidance) — because a family cannot say which mode a row is.
	Dialect: comfyDialectSentences, StepsRange: [2]int{8, 52}, CFGRange: [2]float64{1, 4.5},
}, {
	Family: ComfyFamilyQwenImageEdit2509,
	// The official 2509 template's KSampler, LoRA-switch false branch (ADR 0094 実測): steps 20,
	// cfg 4, euler, simple. 実測 A ran these exact numbers and the edit was followed.
	Recipe:       comfyRecipe{Steps: 20, CFG: 4, Sampler: "euler", Scheduler: "simple"},
	SamplerKnobs: comfyKnobsSampled,
	// ADR 0094 実測 B: the same graph at 8 steps still followed the instruction (63.2 s vs 実測 A's
	// 226.2 s at the family's own 20) — measured, unlike every entry above.
	TrialSteps: 8,
	// Guided, and the evidence is 実測 A and 実測 E: both ran cfg 4 and the edit was followed, and
	// the template gives the negative branch its own TextEncodeQwenImageEditPlus encode rather
	// than ConditioningZeroOut, so the negative genuinely moves the picture (decision 12).
	Guided:          true,
	InstructionEdit: &comfyQwenEditWiring{Shift: 3},
	// inpaint since 実測 G / 実測 I (ADR 0094 decision 3, unresolved 2): a mask that does NOT cover
	// the sign left the sign unchanged where the same request without one changes it, so the mask
	// is a real gate even at a full denoise. The wiring is comfyQwenEditNoiseMask: the mask onto
	// the latent the scaled picture was encoded to, not onto an empty one.
	FixedDenoiseEdit: true, Ops: []Op{OpEdit, OpInpaint}, RefInputs: 3,
	// Instruction editing: the prompt is a sentence describing the change. The recipe is the only
	// published setting, so the "range" is the recipe itself.
	Dialect: comfyDialectSentences, StepsRange: [2]int{20, 20}, CFGRange: [2]float64{4, 4},
}, {
	Family: ComfyFamilyQwenImageEdit2511,
	// The official 2511 template's KSampler, same LoRA-switch false branch: steps 40, cfg 4,
	// euler, simple. Twice 2509's steps, which is upstream's number and not a typo — 実測 E ran
	// these and took 393.8 s for one 1024² edit, the cost ADR 0094's 未解決 1 is about.
	Recipe:       comfyRecipe{Steps: 40, CFG: 4, Sampler: "euler", Scheduler: "simple"},
	SamplerKnobs: comfyKnobsSampled,
	// Carried over from 2509's measurement rather than scaled with the recipe (2511 samples at 40
	// where 2509 samples at 20). The two graphs differ in where the reference latents enter, not
	// in how the sampler descends, and a fifth of 40 is the same kind of guess as every entry
	// above that no picture answered.
	TrialSteps: 8, Guided: true,
	InstructionEdit: &comfyQwenEditWiring{Shift: 3.1, RefMethod: "index_timestep_zero"},
	// inpaint for the same reason 2509 claims it: one builder, one wiring (comfyQwenEditNoiseMask).
	// 実測 G and I ran on 2509; this one rides the SAME code path rather than a parallel one, which
	// is the only kind of family ADR 0094 decision 5 allows to inherit a measurement.
	FixedDenoiseEdit: true, Ops: []Op{OpEdit, OpInpaint}, RefInputs: 3,
	Dialect: comfyDialectSentences, StepsRange: [2]int{40, 40}, CFGRange: [2]float64{4, 4},
}, {
	Family: ComfyFamilyQwenImage21,
	// 🔴 Not run on a GPU here. These are the KSampler widgets BOTH of ComfyUI's shipped templates
	// carry (image_qwen_image_2_1_t2i.json and image_qwen_image_2_1_image_edit.json, read
	// 2026-09-21): 25 steps, cfg 1, euler, simple. The two agreeing is why there is no range to
	// span the way krea2's two modes do.
	//
	// ⚠️ 25 is the templates' starting point and not the pipeline's: their own note says the
	// official pipeline uses "about 40-50 with euler". Taking 25 is taking the published graph; a
	// row that wants the slower, better setting declares it in `params`.
	Recipe:       comfyRecipe{Steps: 25, CFG: 1, Sampler: "euler", Scheduler: "simple"},
	SamplerKnobs: comfyKnobsSampled,
	// A guess of anima's kind and not of 2509's: nothing has been run. Roughly a third of the
	// recipe's 25, where the undistilled families above sit — and this one is NOT distilled, so
	// klein's and krea2's "a trial is the batch" does not apply. Its cfg 1 comes from the
	// architecture, not from a step-count schedule.
	TrialSteps: 8,
	// Guided in the sense this field means: the template gives the negative its own encode
	// (TextEncodeQwenImage21 emits both conditionings from one node), so a negative CAN move the
	// picture. The recipe's cfg 1 is what makes comfyModelTakesNegative answer false for every row
	// that does not raise it — the templates say exactly that: "negative_prompt: unused while
	// cfg is 1".
	Guided: true,
	// Not InstructionEdit: comfyGraphQwenImageEdit cannot build this one. It edits by instruction
	// through its own builder at a fixed denoise 1, which is the field below, and it also
	// generates — the first family here to do both.
	FixedDenoiseEdit: true, Ops: []Op{OpGenerate, OpEdit},
	// The model's own "native transparency": 64 latent channels decode to RGBA. Measured on the
	// dev deployment 2026-09-23 (ADR 0098 P2) — a transparent-background request came back with
	// 17.6 % of its pixels at alpha 0, and an ordinary photograph came back RGBA as well.
	Alpha: true,
	// The megapixel list, and then the same five shapes at twice the side: the templates' note
	// says the model renders 2048² directly. Measured through the Agent on the dev deployment
	// 2026-09-23 (ADR 0098 Open 3, an L40S): 2048x2048 came back whole in 116 s against 44 s for
	// 1024², at a peak of 19,592 MiB in use — one subject, no tiling. Only the square was run; the
	// other four are smaller in pixels (the ladder is 1216x832's own ratios, doubled, and the
	// square sits exactly on imagegenMaxPixels), and all are multiples of 32 for the 16x latent.
	// The first entry stays 1024² so a request naming no size costs what it did.
	Sizes: []string{
		"1024x1024", "1152x896", "896x1152", "1216x832", "832x1216",
		"2048x2048", "2304x1792", "1792x2304", "2432x1664", "1664x2432",
	},
	// 🔴 A CITATION, not a measurement, and the one entry in this column that is not backed by a
	// run. The node takes image_1..image_16; the official edit template wires exactly ten and its
	// note says "Up to 10 reference images". Taking the published wiring is this repository's rule
	// for a family nobody has run — the same rule the recipe above follows — but 2509's 3 was
	// deliberately NOT raised on "the wiring is a loop, so more must work" (ADR 0094 decision 5),
	// so the difference is named here rather than left to look alike. A run that finds the tenth
	// picture ignored makes this the wrong number, and this is where it is corrected.
	RefInputs: 10,
	// Sentences, and edit instructions name their references inline as `<image1>`. 25 is where
	// both templates start and 50 the top of their note's range; cfg is raised above 1 only
	// together with a negative prompt.
	Dialect: comfyDialectSentences, StepsRange: [2]int{25, 50}, CFGRange: [2]float64{1, 4},
}}

// comfyKnobsSampled is `steps cfg sampler scheduler` — every knob a plain KSampler reads, which
// is what the guided families and the two cfg-1 distilled ones (zimage, krea2) all take.
// A named value because six rows share it verbatim, and a seventh spelling it differently by
// accident is the drift comfyFamilyRows exists to stop.
var comfyKnobsSampled = []string{"steps", "cfg", "sampler", "scheduler"}

// comfyFamilyRowFor is the table lookup. False for a family nobody declares, which every caller
// turns into the same refusal the switch's default does.
func comfyFamilyRowFor(f comfyFamily) (comfyFamilyRow, bool) {
	for _, r := range comfyFamilyRows {
		if r.Family == f {
			return r, true
		}
	}
	return comfyFamilyRow{}, false
}

// comfyFamilyRecipeFor is the family's own sampler settings, and the zero recipe for a family
// nobody declares — which is what the templates already did when the map had no entry.
func comfyFamilyRecipeFor(f comfyFamily) comfyRecipe {
	r, _ := comfyFamilyRowFor(f)
	return r.Recipe
}

// comfyFamilies is every family comfyBuildGraph dispatches on, in the table's order, so the
// acceptance check and the error message that tells an operator what to declare cannot disagree
// with the table or with the switch.
var comfyFamilies = comfyFamilyNames()

func comfyFamilyNames() []comfyFamily {
	out := make([]comfyFamily, len(comfyFamilyRows))
	for i, r := range comfyFamilyRows {
		out[i] = r.Family
	}
	return out
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
	case ComfyFamilyQwenImageEdit2511:
		return comfyGraphQwenImageEdit2511(files, p)
	case ComfyFamilyQwenImage21:
		return comfyGraphQwenImage21(files, p)
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
	r := p.recipe(comfyFamilyRecipeFor(family))
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
	r := p.recipe(comfyFamilyRecipeFor(ComfyFamilyZImage))
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
	r := p.recipe(comfyFamilyRecipeFor(ComfyFamilyFlux2Klein))
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
	r := p.recipe(comfyFamilyRecipeFor(ComfyFamilyFlux1))
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
	r := p.recipe(comfyFamilyRecipeFor(ComfyFamilySD35))
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
// encoder by DETECTION, not by this field: comfy/sd.py (v0.37.0, the ref in
// deploy/aws/ecs/comfyui/Dockerfile) reads the state dict's hidden size, answers TEModel.QWEN3_06B
// at 1024, and takes the `comfy.text_encoders.anima` branch at line 1971 — which sits OUTSIDE
// every clip_type test. `anima` is not one of CLIPLoader's type values at all (nodes.py:1012), so
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
	r := p.recipe(comfyFamilyRecipeFor(ComfyFamilyAnima))
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
// harmless default is neither. comfy/sd.py (v0.37.0) reaches the Krea2 tokenizer only through
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
	r := p.recipe(comfyFamilyRecipeFor(ComfyFamilyKrea2))
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

// --- Qwen-Image-Edit — instruction editing, two topologies (ADR 0094 P0/P1) ------------------
//
// Neither family is a member of comfyFamilyFixtures, and neither goes through comfyRequestLatent /
// comfyParams.denoise the way the other eight do — they genuinely are not the same shape (ADR 0094
// background). `op=edit` on every other family is img2img: LoadImage + VAEEncode feeding a
// PARTIAL denoise, so the picture the sampler starts from still carries the composition. Here the
// input picture conditions the sampler through TextEncodeQwenImageEditPlus (vision tokens plus a
// ~1MP reference latent, ADR 0094's background) while the sampler itself runs a FULL denoise —
// the VAEEncode below only fixes the output canvas's size, the same "denoise 1 ignores the
// latent's content, only its shape" fact anima and krea2 already rely on for their own
// EmptyLatentImage. Generate and inpaint are refused before this function is ever called
// (Caps.Ops via comfyFamilyOps — decision 3), so denoise is a literal 1 rather than a call to
// comfyParams.denoise(): there is no op these families reach that ever wants anything else, and a
// caller's Strength is refused at the edge before a request reaches here (decision 2).
//
// Node graph (ported from the official templates and RUN — 実測 A on 2509, 実測 E on 2511, plus
// P0's live acceptance of 2509 through this Agent's own route): UNETLoader +
// CLIPLoader(type=qwen_image) + VAELoader load the three declared files; LoadImage's output is
// shrunk whole by ImageScale to comfyQwenEditSize (decision 4 as revised on 2026-09-23 — the size
// follows the picture, which is also why no `size` reaches these families) and that SAME picture
// feeds both TextEncodeQwenImageEditPlus encodes (positive and negative — the negative is a real
// encode, not ConditioningZeroOut, because 実測 A ran at cfg 4, a guided recipe) and a VAEEncode
// that gives KSampler its starting latent's shape. ModelSamplingAuraFlow + CFGNorm patch the model
// the way zimage's ModelSamplingAuraFlow does, one step further per the official template.
//
// The two entry points stay separate rather than collapsing into one `case`, for the reason the
// SDXL/SD1.5 pair above states: engine_catalog_test.go learns which files a family needs by
// pairing a comfyGraph* body's errComfyMissingFile with the fields it refuses on, and a family
// that never names itself in a refusal is a family that check silently stops measuring.

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
	return comfyGraphQwenImageEdit(f, p, ComfyFamilyQwenImageEdit2509)
}

// 2511 declares the same three parts as 2509 — the same text encoder and the same VAE file, which
// is why engine_family_parts.go points both families at one pair of S3 keys.
func comfyGraphQwenImageEdit2511(f comfyFiles, p comfyParams) (comfyGraph, error) {
	if f.DiffusionModel == "" {
		return nil, errComfyMissingFile("qwen-image-edit-2511", "diffusion model")
	}
	if f.ClipL == "" {
		return nil, errComfyMissingFile("qwen-image-edit-2511", "text encoder (Qwen2.5-VL-7B, declared as --clip_l)")
	}
	if f.Vae == "" {
		return nil, errComfyMissingFile("qwen-image-edit-2511", "vae")
	}
	return comfyGraphQwenImageEdit(f, p, ComfyFamilyQwenImageEdit2511)
}

// comfyQwenEditWiring is everything that separates the two instruction-edit topologies, and it
// hangs off comfyFamilyRow.InstructionEdit so that the answer to "what makes 2511 a family of
// its own" is one place a reader can look (ADR 0094 decision 6: a family per TOPOLOGY).
//
// Shift lives HERE and not as a fifth comfyRecipe field on purpose. comfyRecipe's four fields are
// exactly the ones a catalogue row's `params` may replace — ADR 0069's vocabulary has no fifth
// word — so a shift field would be a number no row could set and no status route could report,
// sitting in the struct whose whole job is the EFFECTIVE recipe (comfyEffectiveParams).
type comfyQwenEditWiring struct {
	// Shift is ModelSamplingAuraFlow's, from the official template of each version.
	Shift float64
	// RefMethod is FluxKontextMultiReferenceLatentMethod's `reference_latents_method`, inserted
	// between EACH conditioning and the sampler. Empty means the node is not in this topology at
	// all, which is 2509: adding it there would be a wiring nobody upstream ships or has run.
	RefMethod string
}

func comfyGraphQwenImageEdit(f comfyFiles, p comfyParams, family comfyFamily) (comfyGraph, error) {
	// The image is required only when Op says this IS an edit or an inpaint. Generate() never
	// reaches this builder with anything else (Caps.Ops refuses generate before comfyBuildGraph is
	// called at all), but Studio()'s own sanity probe (comfyParams{Prompt: "x"}, Op == "") has to
	// keep succeeding on a row whose files are all declared — the same reason none of the other
	// families' generate path demands one either.
	if p.isImageToImage() && p.image(0) == "" {
		return nil, fmt.Errorf("%s needs an input image, and none reached the graph", p.Op)
	}
	if p.Op == OpInpaint && p.Mask == "" {
		return nil, errors.New("inpaint needs a mask image, and none reached the graph")
	}
	// Refused rather than defaulted: the zero value is shift 0 and no reference-method node, which
	// is not a topology anybody has run — it would build, sample, and hand back a degraded picture
	// with nothing to say why. A third instruction-edit family arriving without a wiring on its
	// row is the case this exists for (decision 6 expects more of them), and every other unknown
	// family in this file fails the same way.
	row, ok := comfyFamilyRowFor(family)
	if !ok || row.InstructionEdit == nil {
		return nil, errUnknownComfyFamily(family)
	}
	w := *row.InstructionEdit
	g := comfyGraph{
		"unet": {ClassType: "UNETLoader", Inputs: map[string]any{"unet_name": f.DiffusionModel, "weight_dtype": "default"}},
		"clip": {ClassType: "CLIPLoader", Inputs: map[string]any{"clip_name": f.ClipL, "type": "qwen_image", "device": "default"}},
		"vae":  {ClassType: "VAELoader", Inputs: map[string]any{"vae_name": f.Vae}},
		"img":  {ClassType: "LoadImage", Inputs: map[string]any{"image": p.image(0)}},
	}
	model, clip := comfyApplyLoras(g, p.Loras, comfyLink("unet", 0), comfyLink("clip", 0))
	// The whole picture, shrunk — never cropped — to a fixed point of the encoder's own rescale
	// (comfyQwenEditSize has the measurements). Studio()'s probe carries no picture and no size,
	// and any square answer keeps it building; an edit that reached here without a size is a bug
	// upstream of this function, since Generate refuses such a picture before it wakes the engine.
	sw, sh := p.Width, p.Height
	if !p.isImageToImage() && (sw <= 0 || sh <= 0) {
		sw, sh = comfyQwenEditRefPixelsSide, comfyQwenEditRefPixelsSide
	}
	fw, fh, ok := comfyQwenEditSize(sw, sh)
	if !ok {
		return nil, fmt.Errorf("%s cannot size a %dx%d picture for its encoder", family, p.Width, p.Height)
	}
	g["scale"] = comfyNode{ClassType: "ImageScale", Inputs: map[string]any{
		"image": comfyLink("img", 0), "upscale_method": "lanczos", "width": fw, "height": fh, "crop": "disabled"}}
	// The extra references, exactly as 実測 D wired them (ADR 0094 decision 5): a bare LoadImage
	// each, straight into the encode. They are not scaled here: the frame is image1's alone, the
	// others are things to look at, and the encoder gives each reference its own index rather
	// than a place on image1's grid. The latent still comes from scaled image1 below.
	refs := map[string]any{}
	for n := 1; n < len(p.Images); n++ {
		id := fmt.Sprintf("img%d", n+1)
		g[id] = comfyNode{ClassType: "LoadImage", Inputs: map[string]any{"image": p.image(n)}}
		refs[fmt.Sprintf("image%d", n+1)] = comfyLink(id, 0)
	}
	g["pos"] = comfyNode{ClassType: "TextEncodeQwenImageEditPlus", Inputs: comfyWith(refs, map[string]any{
		"clip": clip, "vae": comfyLink("vae", 0), "prompt": p.Prompt, "image1": comfyLink("scale", 0)})}
	// p.Negative directly, NOT comfyNegativeText(p): that fallback ("blurry, lowres, deformed,
	// watermark, text") was measured for the megapixel families sharing SDXL's era (ADR 0072),
	// and the official templates' own negative widget ships empty. Falling back to it here would
	// fight the instruction itself on the one edit 実測 A made — replacing the sign's own TEXT —
	// and nobody has measured these families against that default at all.
	//
	// The same references go on the NEGATIVE side too — 実測 D's graph did, and the reason is
	// CFG: the two conditionings are subtracted from one another, so a reference present on only
	// one of them would leave its own encoding in the difference and push the picture towards or
	// away from the borrowed object regardless of what either prompt says.
	g["neg"] = comfyNode{ClassType: "TextEncodeQwenImageEditPlus", Inputs: comfyWith(refs, map[string]any{
		"clip": clip, "vae": comfyLink("vae", 0), "prompt": p.Negative, "image1": comfyLink("scale", 0)})}
	pos, neg := comfyLink("pos", 0), comfyLink("neg", 0)
	if w.RefMethod != "" {
		g["posref"] = comfyNode{ClassType: "FluxKontextMultiReferenceLatentMethod", Inputs: map[string]any{
			"conditioning": pos, "reference_latents_method": w.RefMethod}}
		g["negref"] = comfyNode{ClassType: "FluxKontextMultiReferenceLatentMethod", Inputs: map[string]any{
			"conditioning": neg, "reference_latents_method": w.RefMethod}}
		pos, neg = comfyLink("posref", 0), comfyLink("negref", 0)
	}
	g["ms"] = comfyNode{ClassType: "ModelSamplingAuraFlow", Inputs: map[string]any{"shift": w.Shift, "model": model}}
	g["norm"] = comfyNode{ClassType: "CFGNorm", Inputs: map[string]any{"model": comfyLink("ms", 0), "strength": 1}}
	g["enc"] = comfyNode{ClassType: "VAEEncode", Inputs: map[string]any{
		"pixels": comfyLink("scale", 0), "vae": comfyLink("vae", 0)}}
	latent := comfyLink("enc", 0)
	if p.Op == OpInpaint {
		latent = comfyQwenEditNoiseMask(g, p)
	}
	r := p.recipe(comfyFamilyRecipeFor(family))
	g["ks"] = comfyNode{ClassType: "KSampler", Inputs: map[string]any{
		"seed": p.Seed, "steps": r.Steps, "cfg": r.CFG, "sampler_name": r.Sampler, "scheduler": r.Scheduler,
		// Fixed at 1, not p.denoise(): 実測 C is decision 2's whole reason — the same graph at
		// denoise 0.6 comes back unedited, with no error and no warning. Inpaint keeps it too — the
		// area outside the mask is held by the noise mask, not by a partial denoise, which is the
		// same rule comfyParams.denoise states for every other family.
		"denoise": 1,
		"model":   comfyLink("norm", 0), "positive": pos, "negative": neg,
		"latent_image": latent}}
	g["dec"] = comfyNode{ClassType: "VAEDecode", Inputs: map[string]any{"samples": comfyLink("ks", 0), "vae": comfyLink("vae", 0)}}
	g["save"] = comfyNode{ClassType: "SaveImage", Inputs: map[string]any{
		"filename_prefix": "af-" + comfyFamilyPrefixName(family), "images": comfyLink("dec", 0)}}
	return g, nil
}

// comfyQwenEditNoiseMask is this family's inpaint (ADR 0094 decision 3, claimed after 実測 G and
// 実測 I): the caller's mask confines a FULL denoise to the area they drew, and the conditioning
// still sees the whole unmasked picture, which is what makes the repaint agree with the scene
// around it.
//
// The mask needs no scaling node of its own. SetLatentNoiseMask stretches it over the latent, and
// the picture is itself a plain stretch of the whole input (no crop), so both land on the same
// relative region — measured on a 1820x1024 input: the repainted band's edge within 3 px of where
// the picture's map puts it, inside the repaint's own transition band (docs/log/112 §11, T3). A
// mask of another size is still the same relative region, as it is for every other family.
//
// ImageToMask on the red channel, not LoadImageMask, for the reason comfyRequestLatent states:
// LoadImage's own MASK output is `1.0 - alpha`, so an ordinary opaque black-and-white PNG would
// arrive as an all-zero mask and repaint nothing at all, with no error anywhere. Red is the
// channel verbatim — white is the area to repaint.
func comfyQwenEditNoiseMask(g comfyGraph, p comfyParams) []any {
	g["maskimg"] = comfyNode{ClassType: "LoadImage", Inputs: map[string]any{"image": p.Mask}}
	g["mask"] = comfyNode{ClassType: "ImageToMask", Inputs: map[string]any{
		"image": comfyLink("maskimg", 0), "channel": "red"}}
	g["noisemask"] = comfyNode{ClassType: "SetLatentNoiseMask", Inputs: map[string]any{
		"samples": comfyLink("enc", 0), "mask": comfyLink("mask", 0)}}
	return comfyLink("noisemask", 0)
}

// --- Qwen-Image 2.1 — ComfyUI's own shipped templates, NOT YET RUN ON THIS DEPLOYMENT'S HARDWARE
//
// 🔴 THIS FAMILY NEEDS ComfyUI v0.37.0 OR LATER, which is where the pin now stands
// (deploy/aws/ecs/comfyui/Dockerfile). `TextEncodeQwenImage21` first appears in v0.37.0 — measured
// 2026-09-21 by reading comfy_extras/nodes_qwen.py at v0.35.2, v0.36.0 and v0.37.0 — so an engine
// built before that bump refuses a row of this family in /prompt's own validation, as an unknown
// node type. That is a loud failure rather than a silent one, and it is the FIRST thing to check
// when a run of this family fails: the box may still be running the older baked image.
//
// One graph, two published templates. Comfy Org ships image_qwen_image_2_1_t2i.json and
// image_qwen_image_2_1_image_edit.json, and read side by side (2026-09-21) they are the same nodes
// wired the same way; the edit one differs in exactly three things, of which two are inert:
//
//   - the LATENT into KSampler — an EmptyLatentImage at the requested size for text-to-image, the
//     ENCODE NODE's own third output for an edit. That output is an all-zero latent shaped to the
//     resized image_1 (nodes_qwen.py: `[1, 64, h // 16, w // 16]`), which is how "the output
//     follows image_1" is expressed. The template puts a ComfySwitchNode between the two and
//     defaults it to the encode's; this builder picks by whether a reference picture is present,
//     which is the same choice with no node to carry it.
//   - QwenImage21Cache set to `auto` / `default`, which is a no-op: the model reads
//     `transformer_options.get("qwen_image21_cache", {})` and defaults device to "auto" and dtype
//     to "default" when the node is absent (comfy/ldm/qwen_image21/model.py, select_prefix_cache).
//     Left out rather than emitted as a node that only exists in v0.37.0 and changes nothing.
//   - image_1..image_10 on the encode. The node itself takes image_1..image_16; TEN is what the
//     official template wires, and that is the number this family declares (comfyFamilyMaxInputs).
//
// 🔴 The 64-channel VAE is this family's own file and NOT the Qwen-Image VAE the four Qwen-adjacent
// families above share. comfy/latent_formats.py's QwenImage21 is `latent_channels = 64`,
// `spacial_downscale_ratio = 16`; the shared one is 16 channels at 8. Both load through VAELoader
// and both are declared `--vae`, so nothing on the way refuses the wrong one — it decodes to noise.
//
// 🔴 `type: "qwen_image"` on the CLIPLoader IS READ, and on its own it is not enough. comfy/sd.py
// at v0.37.0 reaches this family's encoder only through `clip_type == CLIPType.QWEN_IMAGE` AND a
// state dict detected as `TEModel.QWEN3VL_8B` (line 1955); the Qwen2.5-VL-7B file the edit families
// declare, at the same type, takes the generic qwen_image branch instead. So this is the krea2 case
// rather than the anima one — the field matters — but it is the FILE that separates this family
// from its siblings, and declaring theirs here loads, encodes, samples and returns a picture.
//
// EmptyLatentImage is the 4-channel node at a downscale of 8, and that is not a mismatch here
// either: it hands the sampler `downscale_ratio_spacial: 8` alongside the tensor, and
// fix_empty_latent_channels rescales an ALL-ZERO latent to the model's own 16 as well as repeating
// its channels out to 64 (comfy/sample.py). Anima's template already relies on the channel half of
// that; this family is the first to need the spatial half.
//
// SaveImage rather than the templates' SaveImageAdvanced: this model writes RGBA, and SaveImage
// keeps it — it hands the raw array to PIL, so a 4-channel picture is saved as a PNG with its
// alpha (nodes.py, save_images). What SaveImage also does, and this route depends on, is write the
// `prompt` PNG chunk that readImageProps reads (ADR 0094 P2).
//
// 🔴 The alpha reaches the save only when the caller asked for `background=transparent`.
// Otherwise SplitImageWithAlpha (comfy_extras/nodes_compositing.py: `image[..., :3]`, a no-op on
// a 3-channel picture) drops it first, because the model writes an alpha channel on EVERY
// picture: an ordinary photograph came back with alpha 252-255 on 47 % of its pixels (ADR 0098
// P2) — invisible on screen, up to 1.2 % see-through over a background, and one pixel below 255
// is enough to make every thumbnail of it a PNG five to eight times a JPEG's size. Stripping in
// the graph rather than re-encoding here keeps the `prompt` chunk SaveImage writes.
// ⚠️ What lies under an alpha of 0 is not white: a transparent-background picture measured a
// flat purple there (about 163,58,206). A caller who asks for transparency in the PROMPT but
// not in `background` gets that colour as the background.
func comfyGraphQwenImage21(f comfyFiles, p comfyParams) (comfyGraph, error) {
	if f.DiffusionModel == "" {
		return nil, errComfyMissingFile("qwen-image-2.1", "diffusion model")
	}
	if f.ClipL == "" {
		return nil, errComfyMissingFile("qwen-image-2.1", "text encoder (Qwen3-VL-8B, declared as --clip_l)")
	}
	if f.Vae == "" {
		return nil, errComfyMissingFile("qwen-image-2.1", "vae (this family's own 64-channel one, not the Qwen-Image VAE)")
	}
	if p.Op == OpEdit && p.image(0) == "" {
		return nil, fmt.Errorf("%s needs an input image, and none reached the graph", p.Op)
	}
	g := comfyGraph{
		"unet": {ClassType: "UNETLoader", Inputs: map[string]any{"unet_name": f.DiffusionModel, "weight_dtype": "default"}},
		"clip": {ClassType: "CLIPLoader", Inputs: map[string]any{"clip_name": f.ClipL, "type": "qwen_image", "device": "default"}},
		"vae":  {ClassType: "VAELoader", Inputs: map[string]any{"vae_name": f.Vae}},
	}
	model, clip := comfyApplyLoras(g, p.Loras, comfyLink("unet", 0), comfyLink("clip", 0))
	// Every reference goes in as a bare LoadImage, image_1 included — unlike the edit families next
	// door there is no ImageScale to fit image_1 into a frame, because this node does that resizing
	// itself (one `resolution` for all of them, aspect preserved, rounded to 32).
	//
	// 🔴 The key is `images.image_N`, not `image_N`. The references are a V3 Autogrow group named
	// `images`, and the API-format id of each member is the group and the member joined by a dot
	// (comfy_api/latest/_io.py, finalize_prefix). `image_N` is only the label the editor shows.
	// Measured on the dev deployment 2026-09-23: the bare spelling passes /prompt validation —
	// an unknown optional key is not refused there — and then dies inside the node with
	// `TextEncodeQwenImage21.execute() got an unexpected keyword argument 'image_1'`, so every
	// edit of this family failed while text-to-image worked.
	refs := map[string]any{}
	for n := range p.Images {
		id := fmt.Sprintf("img%d", n+1)
		g[id] = comfyNode{ClassType: "LoadImage", Inputs: map[string]any{"image": p.image(n)}}
		refs[fmt.Sprintf("images.image_%d", n+1)] = comfyLink(id, 0)
	}
	// One node encodes BOTH conditionings, so there is no positive/negative pair to keep in step —
	// and p.Negative directly rather than comfyNegativeText(p), for the reason the edit families
	// state: the fallback default was measured for the SDXL-era families, and both official
	// templates here ship the negative widget empty.
	//
	// `vae` is wired for both ops, where the t2i template leaves it unconnected. It is an optional
	// input the node reads only inside its reference loop (nodes_qwen.py), so with no reference it
	// is ignored — and holding the node's shape constant across the two ops is one fewer branch
	// than a wiring that differs in a way nothing can observe.
	g["enc"] = comfyNode{ClassType: "TextEncodeQwenImage21", Inputs: comfyWith(refs, map[string]any{
		"clip": clip, "vae": comfyLink("vae", 0),
		"prompt": p.Prompt, "negative_prompt": p.Negative,
		"resolution": comfyQwen21Resolution})}
	lat := comfyLink("enc", 2)
	if len(p.Images) == 0 {
		// Nothing to follow, so the caller's size decides — and the encode node's own latent could
		// not serve here anyway: with no reference it is a bare `resolution` square, which would
		// answer every text-to-image request at 1024² whatever was asked for.
		g["lat"] = comfyNode{ClassType: "EmptyLatentImage", Inputs: map[string]any{
			"width": p.Width, "height": p.Height, "batch_size": p.BatchSize}}
		lat = comfyLink("lat", 0)
	}
	r := p.recipe(comfyFamilyRecipeFor(ComfyFamilyQwenImage21))
	g["ks"] = comfyNode{ClassType: "KSampler", Inputs: map[string]any{
		"seed": p.Seed, "steps": r.Steps, "cfg": r.CFG, "sampler_name": r.Sampler, "scheduler": r.Scheduler,
		// 1, not p.denoise(): both published templates sample at 1 for both of their ops, because
		// an edit here conditions the sampler through the picture rather than starting from it.
		// 実測 C in ADR 0094 is what a partial denoise does to a family of this shape — the same
		// request came back unedited, with no error and no warning.
		"denoise": 1,
		"model":   model, "positive": comfyLink("enc", 0), "negative": comfyLink("enc", 1),
		"latent_image": lat}}
	g["dec"] = comfyNode{ClassType: "VAEDecode", Inputs: map[string]any{
		"samples": comfyLink("ks", 0), "vae": comfyLink("vae", 0)}}
	out := comfyLink("dec", 0)
	if !p.Transparent {
		g["rgb"] = comfyNode{ClassType: "SplitImageWithAlpha", Inputs: map[string]any{"image": out}}
		out = comfyLink("rgb", 0)
	}
	g["save"] = comfyNode{ClassType: "SaveImage", Inputs: map[string]any{
		"filename_prefix": "af-" + comfyFamilyPrefixName(ComfyFamilyQwenImage21), "images": out}}
	return g, nil
}

// comfyQwen21Resolution is the pixel budget TextEncodeQwenImage21 resizes every reference picture
// to — `resolution x resolution`, aspect preserved, rounded to a multiple of 32 — and, through the
// node's own latent output, the canvas an edit is produced on.
//
// 🔴 1024 and not the official edit template's 0. The two published numbers disagree, and this is
// the one place in this template where the published graph is not taken verbatim: the node's own
// default is 1024, the template's note calls 1024 "the official default", and 0 means "keep each
// reference at its own size". A template author picks the demo assets; this route takes whatever
// picture a member uploads, and 0 would encode an 8000px photograph at 8000px — a VAE encode and a
// vision-token count nothing here caps, on a card that also holds 15 GiB of weights.
const comfyQwen21Resolution = 1024
