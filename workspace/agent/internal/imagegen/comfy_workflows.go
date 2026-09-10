package imagegen

// comfy_workflows.go — the five checkpoint-family workflow templates ADR 0072 decision 4
// requires (ComfyUI has no OpenAI-compatible surface, so "what generate_image sends" has to be
// a graph this package owns and version-pins, not something an engine version negotiates).
//
// Node ids are words rather than numbers so a validation error names something readable, the
// same choice `bench-image-engine.py` made. sdxl / zimage / klein are PORTED, byte-for-byte in
// shape, from that harness — the same graphs ADR 0072's "実測で解けた点" measured working on a
// real GPU (26/26 images, both checkpoints and both LoRA states). flux1 / sd35 are new: built
// from the checkpoint families' standard published ComfyUI recipes, but NOT yet run on this
// deployment's hardware — comfy_workflows_test.go pins their exact JSON shape so a future
// change is visible in the diff, which is a different claim from "this graph is correct".
//
// Every template takes comfyFiles (the on-disk basenames ADR 0072 decision 2 declares, resolved
// from the catalogue's Flag vocabulary — see EngineFile) and comfyParams (the request-shaped
// knobs: prompt, seed, size, batch count). Nothing else varies: sampler, steps, cfg and scheduler
// are the family's own fixed recipe, not a caller's choice (P2 scope decision — a future phase
// may widen this, ADR 0072 phase P2 note).

import "strings"

// comfyNegativePrompt is the same negative prompt bench-image-engine.py measured with, for the
// families that use one (SDXL, SD3.5). Never exposed to the caller: `Request` has no negative-
// prompt field (ADR 0069's vocabulary is provider-neutral), so a fixed, reasonable default is
// the honest answer rather than an empty string that would let every artifact through.
const comfyNegativePrompt = "blurry, lowres, deformed, watermark, text"

// comfyFiles is the resolved, per-role file set for one model — see EngineFile for how the
// catalogue's Flag maps onto these fields. A family's template reads only the fields it needs;
// an empty field on a required role is refused by comfyBuildGraph before any HTTP call is made.
type comfyFiles struct {
	Checkpoint     string // Flag == ""
	DiffusionModel string // --diffusion-model
	// ClipL is the flag `--clip_l` names. Dual/triple-encoder families (flux1, sd35) read it as
	// the first of two text encoders; single-encoder families (zimage, flux2-klein) read it as
	// THE text encoder — there is no second flag for "the only one", so the catalogue entry for
	// those families' text encoder file is declared with `--clip_l` by convention (documented
	// at the ingest UI, not a new flag this system did not already have).
	ClipL string
	T5xxl string // --t5xxl
	Vae   string // --vae
}

// resolveComfyFiles turns the catalogue's flat, sd.cpp-flavoured file list into the named roles
// the templates read. Files with an unrecognised or empty Name are silently dropped — decision 2
// only ever declares the four flags in EngineFile's own comment, and a fifth would mean a
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
	Prompt    string
	Seed      int64
	Width     int
	Height    int
	BatchSize int
}

// comfyGraph is the API-format document /prompt takes: node id -> {class_type, inputs}.
type comfyGraph map[string]comfyNode

type comfyNode struct {
	ClassType string         `json:"class_type"`
	Inputs    map[string]any `json:"inputs"`
}

// comfyLink is a graph edge: [node id, output slot].
func comfyLink(node string, slot int) []any { return []any{node, slot} }

// comfyFamily is one of ADR 0072's five checkpoint families (decision 2's `baseModel` values),
// each naming its own template builder.
type comfyFamily string

const (
	ComfyFamilySDXL       comfyFamily = "sdxl"
	ComfyFamilySD35       comfyFamily = "sd35"
	ComfyFamilyFlux1      comfyFamily = "flux1"
	ComfyFamilyFlux2Klein comfyFamily = "flux2-klein"
	ComfyFamilyZImage     comfyFamily = "zimage"
)

// comfyFamilies is every family comfyBuildGraph dispatches on. One list, so the acceptance
// check and the error message that tells an operator what to declare cannot disagree with the
// switch below. The Control Plane validates catalogue rows against the same five spellings and
// keeps its copy honest by reading THIS file (engine_catalog_test.go).
var comfyFamilies = []comfyFamily{
	ComfyFamilySDXL, ComfyFamilySD35, ComfyFamilyFlux1, ComfyFamilyFlux2Klein, ComfyFamilyZImage,
}

// comfyFileFlags is the Flag vocabulary resolveComfyFiles understands, in the order a panel
// should offer them. "" is a single-file checkpoint (SDXL, SD3.5); the rest each name one part
// of a split model, which is the ONLY way FLUX.2 klein and Z-Image can be declared at all.
// comfy_test.go pins that every one of these actually resolves, and the Control Plane serves
// the list to the Console so the two cannot disagree about what a flag is called.
var comfyFileFlags = []string{"", "--diffusion-model", "--clip_l", "--t5xxl", "--vae"}

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
	default:
		return nil, errUnknownComfyFamily(family)
	}
}

// --- SDXL — ported from bench-image-engine.py's g_sdxl (GPU-verified, ADR 0072) -------------

func comfyGraphSDXL(f comfyFiles, p comfyParams) (comfyGraph, error) {
	if f.Checkpoint == "" {
		return nil, errComfyMissingFile("sdxl", "checkpoint")
	}
	g := comfyGraph{
		"ckpt": {ClassType: "CheckpointLoaderSimple", Inputs: map[string]any{"ckpt_name": f.Checkpoint}},
		"pos": {ClassType: "CLIPTextEncode", Inputs: map[string]any{
			"text": p.Prompt, "clip": comfyLink("ckpt", 1)}},
		"neg": {ClassType: "CLIPTextEncode", Inputs: map[string]any{
			"text": comfyNegativePrompt, "clip": comfyLink("ckpt", 1)}},
		"lat": {ClassType: "EmptyLatentImage", Inputs: map[string]any{
			"width": p.Width, "height": p.Height, "batch_size": p.BatchSize}},
		"ks": {ClassType: "KSampler", Inputs: map[string]any{
			"seed": p.Seed, "steps": 20, "cfg": 7, "sampler_name": "dpmpp_2m", "scheduler": "karras", "denoise": 1,
			"model": comfyLink("ckpt", 0), "positive": comfyLink("pos", 0), "negative": comfyLink("neg", 0),
			"latent_image": comfyLink("lat", 0)}},
		"dec": {ClassType: "VAEDecode", Inputs: map[string]any{"samples": comfyLink("ks", 0), "vae": comfyLink("ckpt", 2)}},
		"save": {ClassType: "SaveImage", Inputs: map[string]any{
			"filename_prefix": "af-sdxl", "images": comfyLink("dec", 0)}},
	}
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
		"ms":   {ClassType: "ModelSamplingAuraFlow", Inputs: map[string]any{"shift": 3, "model": comfyLink("unet", 0)}},
		"pos": {ClassType: "CLIPTextEncode", Inputs: map[string]any{
			"text": p.Prompt, "clip": comfyLink("clip", 0)}},
		"neg": {ClassType: "CLIPTextEncode", Inputs: map[string]any{
			"text": "", "clip": comfyLink("clip", 0)}},
		"lat": {ClassType: "EmptySD3LatentImage", Inputs: map[string]any{
			"width": p.Width, "height": p.Height, "batch_size": p.BatchSize}},
		"ks": {ClassType: "KSampler", Inputs: map[string]any{
			"seed": p.Seed, "steps": 8, "cfg": 1, "sampler_name": "res_multistep", "scheduler": "simple", "denoise": 1,
			"model": comfyLink("ms", 0), "positive": comfyLink("pos", 0), "negative": comfyLink("neg", 0),
			"latent_image": comfyLink("lat", 0)}},
		"dec": {ClassType: "VAEDecode", Inputs: map[string]any{"samples": comfyLink("ks", 0), "vae": comfyLink("vae", 0)}},
		"save": {ClassType: "SaveImage", Inputs: map[string]any{
			"filename_prefix": "af-zimage", "images": comfyLink("dec", 0)}},
	}
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
		"pos": {ClassType: "CLIPTextEncode", Inputs: map[string]any{
			"text": p.Prompt, "clip": comfyLink("clip", 0)}},
		"zero": {ClassType: "ConditioningZeroOut", Inputs: map[string]any{"conditioning": comfyLink("pos", 0)}},
		"guider": {ClassType: "CFGGuider", Inputs: map[string]any{
			"model": comfyLink("unet", 0), "positive": comfyLink("pos", 0), "negative": comfyLink("zero", 0), "cfg": 1}},
		"sampler": {ClassType: "KSamplerSelect", Inputs: map[string]any{"sampler_name": "euler"}},
		"sigmas": {ClassType: "Flux2Scheduler", Inputs: map[string]any{
			"steps": 4, "width": p.Width, "height": p.Height}},
		"lat": {ClassType: "EmptyFlux2LatentImage", Inputs: map[string]any{
			"width": p.Width, "height": p.Height, "batch_size": p.BatchSize}},
		"noise": {ClassType: "RandomNoise", Inputs: map[string]any{"noise_seed": p.Seed}},
		"sca": {ClassType: "SamplerCustomAdvanced", Inputs: map[string]any{
			"noise": comfyLink("noise", 0), "guider": comfyLink("guider", 0), "sampler": comfyLink("sampler", 0),
			"sigmas": comfyLink("sigmas", 0), "latent_image": comfyLink("lat", 0)}},
		"dec": {ClassType: "VAEDecode", Inputs: map[string]any{"samples": comfyLink("sca", 0), "vae": comfyLink("vae", 0)}},
		"save": {ClassType: "SaveImage", Inputs: map[string]any{
			"filename_prefix": "af-klein", "images": comfyLink("dec", 0)}},
	}
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
		"pos": {ClassType: "CLIPTextEncode", Inputs: map[string]any{
			"text": p.Prompt, "clip": comfyLink("clip", 0)}},
		"guidance": {ClassType: "FluxGuidance", Inputs: map[string]any{
			"conditioning": comfyLink("pos", 0), "guidance": 3.5}},
		"lat": {ClassType: "EmptySD3LatentImage", Inputs: map[string]any{
			"width": p.Width, "height": p.Height, "batch_size": p.BatchSize}},
		"sampler": {ClassType: "KSamplerSelect", Inputs: map[string]any{"sampler_name": "euler"}},
		"scheduler": {ClassType: "BasicScheduler", Inputs: map[string]any{
			"model": comfyLink("unet", 0), "scheduler": "simple", "steps": 20, "denoise": 1}},
		"noise": {ClassType: "RandomNoise", Inputs: map[string]any{"noise_seed": p.Seed}},
		"guider": {ClassType: "BasicGuider", Inputs: map[string]any{
			"model": comfyLink("unet", 0), "conditioning": comfyLink("guidance", 0)}},
		"sca": {ClassType: "SamplerCustomAdvanced", Inputs: map[string]any{
			"noise": comfyLink("noise", 0), "guider": comfyLink("guider", 0), "sampler": comfyLink("sampler", 0),
			"sigmas": comfyLink("scheduler", 0), "latent_image": comfyLink("lat", 0)}},
		"dec": {ClassType: "VAEDecode", Inputs: map[string]any{"samples": comfyLink("sca", 0), "vae": comfyLink("vae", 0)}},
		"save": {ClassType: "SaveImage", Inputs: map[string]any{
			"filename_prefix": "af-flux1", "images": comfyLink("dec", 0)}},
	}
	return g, nil
}

// --- SD3.5 — NOT YET RUN ON THIS DEPLOYMENT'S HARDWARE ---------------------------------------
//
// The single-checkpoint form (Stability's official release bundles UNet+VAE+CLIP-L+CLIP-G in
// one file), which is the only shape this system's four-flag vocabulary (EngineFile) can
// express without a fifth flag for clip_g. A catalogue entry that also declares `--t5xxl` is
// read for a HIGHER-quality standalone T5 (Stability documents the bundled one as a smaller,
// lower-quality default) via TripleCLIPLoader; without one this template does not build at all,
// since CheckpointLoaderSimple's own CLIP output cannot be partially overridden.

func comfyGraphSD35(f comfyFiles, p comfyParams) (comfyGraph, error) {
	if f.Checkpoint == "" {
		return nil, errComfyMissingFile("sd35", "checkpoint")
	}
	if f.T5xxl == "" {
		return nil, errComfyMissingFile("sd35", "t5xxl (this template needs a standalone T5, ADR 0072 P2 note)")
	}
	g := comfyGraph{
		"ckpt": {ClassType: "CheckpointLoaderSimple", Inputs: map[string]any{"ckpt_name": f.Checkpoint}},
		"clip": {ClassType: "TripleCLIPLoader", Inputs: map[string]any{
			// clip_l and clip_g ride on the checkpoint's own bundle; only t5xxl is swapped in,
			// which is why clip_name1/clip_name2 name the SAME checkpoint file twice — the
			// loader reads the specific tensors it needs out of whichever file it is given.
			"clip_name1": f.Checkpoint, "clip_name2": f.Checkpoint, "clip_name3": f.T5xxl}},
		"pos": {ClassType: "CLIPTextEncode", Inputs: map[string]any{
			"text": p.Prompt, "clip": comfyLink("clip", 0)}},
		"neg": {ClassType: "CLIPTextEncode", Inputs: map[string]any{
			"text": comfyNegativePrompt, "clip": comfyLink("clip", 0)}},
		"lat": {ClassType: "EmptySD3LatentImage", Inputs: map[string]any{
			"width": p.Width, "height": p.Height, "batch_size": p.BatchSize}},
		"ks": {ClassType: "KSampler", Inputs: map[string]any{
			"seed": p.Seed, "steps": 28, "cfg": 4.5, "sampler_name": "dpmpp_2m", "scheduler": "sgm_uniform", "denoise": 1,
			"model": comfyLink("ckpt", 0), "positive": comfyLink("pos", 0), "negative": comfyLink("neg", 0),
			"latent_image": comfyLink("lat", 0)}},
		"dec": {ClassType: "VAEDecode", Inputs: map[string]any{"samples": comfyLink("ks", 0), "vae": comfyLink("ckpt", 2)}},
		"save": {ClassType: "SaveImage", Inputs: map[string]any{
			"filename_prefix": "af-sd35", "images": comfyLink("dec", 0)}},
	}
	return g, nil
}
