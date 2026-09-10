package imagegen

// comfy_workflows.go — the five checkpoint-family workflow templates ADR 0072 decision 4
// requires (ComfyUI has no OpenAI-compatible surface, so "what generate_image sends" has to be
// a graph this package owns and version-pins, not something an engine version negotiates).
//
// Node ids are words rather than numbers so a validation error names something readable, the
// same choice `bench-image-engine.py` made. sdxl / zimage / klein are PORTED, byte-for-byte in
// shape, from that harness — the same graphs ADR 0072's "実測で解けた点" measured working on a
// real GPU (26/26 images, both checkpoints and both LoRA states). flux1 / sd35 were written from
// the checkpoint families' standard published ComfyUI recipes and have since been run on this
// deployment's hardware too (ADR 0072 P2 残作業 5, af-sandbox): flux1 generated on the first
// attempt, sd35 did NOT — see its own note below for what the golden test could not see.
// comfy_workflows_test.go pins each template's exact JSON shape so a future change is visible in
// the diff, which remains a different claim from "this graph is correct".
//
// Every template takes comfyFiles (the on-disk basenames ADR 0072 decision 2 declares, resolved
// from the catalogue's Flag vocabulary — see EngineFile) and comfyParams (the request-shaped
// knobs: prompt, seed, size, batch count, and the LoRAs to chain in). Nothing else varies:
// sampler, steps, cfg and scheduler are the family's own fixed recipe, not a caller's choice
// (P2 scope decision — a future phase may widen this, ADR 0072 phase P2 note).
//
// 🔴 None of the LoRA chains (P3) has been run on a GPU. The golden test pins their shape, which
// is the same claim it made about SD3.5 before that family turned out not to generate at all —
// so the loader node and its input names were read off ComfyUI v0.34.0's own source rather than
// assumed, and the completion definition stays "the same prompt and seed produce a different
// picture with the LoRA than without" (ADR 0072 phase P3), on real hardware.

import (
	"strconv"
	"strings"
)

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
	Vae   string // --vae
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
	Prompt    string
	Seed      int64
	Width     int
	Height    int
	BatchSize int
	// Loras are already resolved against the catalogue and checked against this model's family
	// (comfyResolveLoras) — a template applies them, it does not decide whether they fit.
	Loras []comfyLora
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
// returns (MODEL, CLIP). All five families here have a CLIP to hand it — the split ones from
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
	}
	// CheckpointLoaderSimple returns (MODEL, CLIP, VAE), so the LoRA chain hangs off slots 0
	// and 1 and the VAE keeps coming straight from the checkpoint — a LoRA never touches it.
	model, clip := comfyApplyLoras(g, p.Loras, comfyLink("ckpt", 0), comfyLink("ckpt", 1))
	g["pos"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": p.Prompt, "clip": clip}}
	g["neg"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": comfyNegativePrompt, "clip": clip}}
	g["lat"] = comfyNode{ClassType: "EmptyLatentImage", Inputs: map[string]any{
		"width": p.Width, "height": p.Height, "batch_size": p.BatchSize}}
	g["ks"] = comfyNode{ClassType: "KSampler", Inputs: map[string]any{
		"seed": p.Seed, "steps": 20, "cfg": 7, "sampler_name": "dpmpp_2m", "scheduler": "karras", "denoise": 1,
		"model": model, "positive": comfyLink("pos", 0), "negative": comfyLink("neg", 0),
		"latent_image": comfyLink("lat", 0)}}
	g["dec"] = comfyNode{ClassType: "VAEDecode", Inputs: map[string]any{"samples": comfyLink("ks", 0), "vae": comfyLink("ckpt", 2)}}
	g["save"] = comfyNode{ClassType: "SaveImage", Inputs: map[string]any{
		"filename_prefix": "af-sdxl", "images": comfyLink("dec", 0)}}
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
	g["lat"] = comfyNode{ClassType: "EmptySD3LatentImage", Inputs: map[string]any{
		"width": p.Width, "height": p.Height, "batch_size": p.BatchSize}}
	g["ks"] = comfyNode{ClassType: "KSampler", Inputs: map[string]any{
		"seed": p.Seed, "steps": 8, "cfg": 1, "sampler_name": "res_multistep", "scheduler": "simple", "denoise": 1,
		"model": comfyLink("ms", 0), "positive": comfyLink("pos", 0), "negative": comfyLink("neg", 0),
		"latent_image": comfyLink("lat", 0)}}
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
	g["sampler"] = comfyNode{ClassType: "KSamplerSelect", Inputs: map[string]any{"sampler_name": "euler"}}
	g["sigmas"] = comfyNode{ClassType: "Flux2Scheduler", Inputs: map[string]any{
		"steps": 4, "width": p.Width, "height": p.Height}}
	g["lat"] = comfyNode{ClassType: "EmptyFlux2LatentImage", Inputs: map[string]any{
		"width": p.Width, "height": p.Height, "batch_size": p.BatchSize}}
	g["noise"] = comfyNode{ClassType: "RandomNoise", Inputs: map[string]any{"noise_seed": p.Seed}}
	g["sca"] = comfyNode{ClassType: "SamplerCustomAdvanced", Inputs: map[string]any{
		"noise": comfyLink("noise", 0), "guider": comfyLink("guider", 0), "sampler": comfyLink("sampler", 0),
		"sigmas": comfyLink("sigmas", 0), "latent_image": comfyLink("lat", 0)}}
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
	g["lat"] = comfyNode{ClassType: "EmptySD3LatentImage", Inputs: map[string]any{
		"width": p.Width, "height": p.Height, "batch_size": p.BatchSize}}
	g["sampler"] = comfyNode{ClassType: "KSamplerSelect", Inputs: map[string]any{"sampler_name": "euler"}}
	g["scheduler"] = comfyNode{ClassType: "BasicScheduler", Inputs: map[string]any{
		"model": model, "scheduler": "simple", "steps": 20, "denoise": 1}}
	g["noise"] = comfyNode{ClassType: "RandomNoise", Inputs: map[string]any{"noise_seed": p.Seed}}
	g["guider"] = comfyNode{ClassType: "BasicGuider", Inputs: map[string]any{
		"model": model, "conditioning": comfyLink("guidance", 0)}}
	g["sca"] = comfyNode{ClassType: "SamplerCustomAdvanced", Inputs: map[string]any{
		"noise": comfyLink("noise", 0), "guider": comfyLink("guider", 0), "sampler": comfyLink("sampler", 0),
		"sigmas": comfyLink("scheduler", 0), "latent_image": comfyLink("lat", 0)}}
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
	// The MODEL comes from the checkpoint and the CLIP from TripleCLIPLoader — this is the one
	// family where the two halves LoraLoader wants come from different nodes.
	model, clip := comfyApplyLoras(g, p.Loras, comfyLink("ckpt", 0), comfyLink("clip", 0))
	g["pos"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": p.Prompt, "clip": clip}}
	g["neg"] = comfyNode{ClassType: "CLIPTextEncode", Inputs: map[string]any{
		"text": comfyNegativePrompt, "clip": clip}}
	g["lat"] = comfyNode{ClassType: "EmptySD3LatentImage", Inputs: map[string]any{
		"width": p.Width, "height": p.Height, "batch_size": p.BatchSize}}
	g["ks"] = comfyNode{ClassType: "KSampler", Inputs: map[string]any{
		"seed": p.Seed, "steps": 28, "cfg": 4.5, "sampler_name": "dpmpp_2m", "scheduler": "sgm_uniform", "denoise": 1,
		"model": model, "positive": comfyLink("pos", 0), "negative": comfyLink("neg", 0),
		"latent_image": comfyLink("lat", 0)}}
	g["dec"] = comfyNode{ClassType: "VAEDecode", Inputs: map[string]any{"samples": comfyLink("ks", 0), "vae": comfyLink("ckpt", 2)}}
	g["save"] = comfyNode{ClassType: "SaveImage", Inputs: map[string]any{
		"filename_prefix": "af-sd35", "images": comfyLink("dec", 0)}}
	return g, nil
}
