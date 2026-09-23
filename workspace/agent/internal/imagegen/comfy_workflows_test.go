package imagegen

// comfy_workflows_test.go — the golden tests ADR 0072 decision 4 asks for: ComfyUI's node graph
// API has no version-to-version compatibility promise, so a template's exact shape is pinned
// here rather than left to "it still looks about right". A future edit to any comfyGraph*
// function changes these files, and that diff — not a passing test — is the thing a reviewer
// has to read before merging.
//
// sdxl / zimage / flux2_klein are pinned against the SAME inputs bench-image-engine.py measured
// working on a real GPU (ADR 0072 "実測で解けた点"); flux1 / sd35 are pinned against inputs that
// have since been run on this deployment's hardware too (ADR 0072 P2 残作業 5) — sd35's shape
// here is the CORRECTED one, after the first version was refused by ComfyUI at validation.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// comfyGoldenParams matches bench-image-engine.py's PROMPT/SEED exactly, so the sdxl/zimage/
// flux2_klein fixtures below are byte-for-byte what the GPU-verified harness actually sent.
var comfyGoldenParams = comfyParams{
	Prompt:    "a red fox sitting on a mossy rock in a misty forest at dawn, detailed fur, soft light",
	Seed:      1234,
	Width:     1024,
	Height:    1024,
	BatchSize: 1,
}

func TestComfyWorkflowsMatchGoldenFixtures(t *testing.T) {
	for _, c := range comfyFamilyFixtures {
		t.Run(c.name, func(t *testing.T) {
			g, err := comfyBuildGraph(c.family, c.files, comfyGoldenParams)
			if err != nil {
				t.Fatalf("comfyBuildGraph(%s) = %v", c.family, err)
			}
			got, err := json.MarshalIndent(g, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "comfy_"+c.name+".golden.json")
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			if string(got) != string(want) {
				t.Errorf("%s's graph no longer matches %s.\nGot:\n%s\nIf this change is intended, "+
					"overwrite the fixture and explain why in the commit.", c.name, path, got)
			}
		})
	}
}

// comfyFamilyFixture is one family's files plus the nodes that must end up reading the LoRA
// chain: everything downstream of the model loader, and everything downstream of the CLIP one.
// Naming them per family is the point — a chain that is BUILT but not consumed is exactly the
// failure a golden fixture cannot see, because the lora node is present either way.
type comfyFamilyFixture struct {
	name        string
	family      comfyFamily
	files       comfyFiles
	modelInputs []string // "<node>.<input>" that must carry the patched MODEL
	clipInputs  []string // "<node>.<input>" that must carry the patched CLIP
	// latentInput is the sampler input the request's latent arrives at, and denoiseAt is where
	// an edit's partial denoise is expressed. They differ by family because the sampler does:
	// KSampler has a denoise of its own, BasicScheduler cuts the schedule, and klein's
	// Flux2Scheduler has no denoise at all — which is the whole reason it needs an extra node.
	latentInput string
	denoiseAt   string // "" when the family expresses denoise with a node instead
}

var comfyFamilyFixtures = []comfyFamilyFixture{
	{"sdxl", ComfyFamilySDXL, comfyFiles{Checkpoint: "sd_xl_base_1.0.safetensors"},
		[]string{"ks.model"}, []string{"pos.clip", "neg.clip"}, "ks.latent_image", "ks.denoise"},
	// Pinned against the same 1024x1024 params as every other fixture, which is NOT this
	// family's own default size (comfyDefaultSizes puts sd15 at 512) — the golden's job is the
	// graph's shape, and holding the inputs identical is what makes it comparable to sdxl's.
	{"sd15", ComfyFamilySD15, comfyFiles{Checkpoint: "v1-5-pruned-emaonly.safetensors"},
		[]string{"ks.model"}, []string{"pos.clip", "neg.clip"}, "ks.latent_image", "ks.denoise"},
	{"zimage", ComfyFamilyZImage, comfyFiles{
		DiffusionModel: "z_image_turbo_bf16.safetensors", ClipL: "qwen_3_4b_fp8_mixed.safetensors", Vae: "ae.safetensors"},
		[]string{"ms.model"}, []string{"pos.clip", "neg.clip"}, "ks.latent_image", "ks.denoise"},
	{"flux2_klein", ComfyFamilyFlux2Klein, comfyFiles{
		DiffusionModel: "flux-2-klein-4b.safetensors", ClipL: "qwen_3_4b_fp8_mixed.safetensors", Vae: "flux2-vae.safetensors"},
		[]string{"guider.model"}, []string{"pos.clip"}, "sca.latent_image", ""},
	// BasicScheduler derives the sigmas from the model it is given, so it has to see the same
	// patched one the guider samples with.
	{"flux1", ComfyFamilyFlux1, comfyFiles{
		DiffusionModel: "flux1-dev.safetensors", ClipL: "clip_l.safetensors", T5xxl: "t5xxl_fp8.safetensors", Vae: "ae.safetensors"},
		[]string{"guider.model", "scheduler.model"}, []string{"pos.clip"}, "sca.latent_image", "scheduler.denoise"},
	// The same family with a VAE declared separately, which is the only shape a checkpoint
	// published without VAE tensors can be used in (comfyCheckpointVAE): one VAELoader that both
	// the encode and the decode read, and a graph otherwise identical to the fixture above.
	{"sdxl_external_vae", ComfyFamilySDXL, comfyFiles{
		Checkpoint: "illustrious_xl_v3.safetensors", Vae: "sdxl_vae.safetensors"},
		[]string{"ks.model"}, []string{"pos.clip", "neg.clip"}, "ks.latent_image", "ks.denoise"},
	{"sd35", ComfyFamilySD35, comfyFiles{Checkpoint: "sd3.5_large.safetensors",
		ClipL: "clip_l.safetensors", ClipG: "clip_g.safetensors", T5xxl: "t5xxl_fp16.safetensors"},
		[]string{"ks.model"}, []string{"pos.clip", "neg.clip"}, "ks.latent_image", "ks.denoise"},
	// The one family that is split like klein and sampled like SDXL. The file names are the ones
	// circlestone-labs/Anima and the ComfyUI template publish, so the fixture reads as the graph
	// an operator would build by hand from the model card.
	{"anima", ComfyFamilyAnima, comfyFiles{
		DiffusionModel: "anima-aesthetic-v1.1.safetensors", ClipL: "qwen_3_06b_base.safetensors",
		Vae: "qwen_image_vae.safetensors"},
		[]string{"ks.model"}, []string{"pos.clip", "neg.clip"}, "ks.latent_image", "ks.denoise"},
	// The names Comfy-Org/Krea-2 publishes. 🔴 The fixture's value is `clip.type`: "krea2" is
	// read by the loader, and the default would load the same file as a generic Qwen3-VL and
	// return a picture made against different conditioning, with no error anywhere.
	{"krea2", ComfyFamilyKrea2, comfyFiles{
		DiffusionModel: "krea2_turbo_fp8_scaled.safetensors", ClipL: "qwen3vl_4b_fp8_scaled.safetensors",
		Vae: "qwen_image_vae.safetensors"},
		[]string{"ks.model"}, []string{"pos.clip", "neg.clip"}, "ks.latent_image", "ks.denoise"},
}

// comfyLinkAt reads the graph edge at "<node>.<input>".
func comfyLinkAt(t *testing.T, g comfyGraph, ref string) []any {
	t.Helper()
	parts := strings.SplitN(ref, ".", 2)
	node, ok := g[parts[0]]
	if !ok {
		t.Fatalf("the graph has no node %q", parts[0])
	}
	link, ok := node.Inputs[parts[1]].([]any)
	if !ok {
		t.Fatalf("%s is not a link: %#v", ref, node.Inputs[parts[1]])
	}
	return link
}

// Every family's LoRA chain, checked where a golden fixture cannot look: that the nodes
// downstream actually READ the chain rather than the bare loader they used to (ADR 0072 decision
// 5, phase P3). The node class and its input names are ComfyUI v0.34.0's own (nodes.py:
// LoraLoader takes model/clip/lora_name/strength_model/strength_clip and returns MODEL, CLIP).
func TestComfyWorkflowsChainLoras(t *testing.T) {
	for _, c := range comfyFamilyFixtures {
		t.Run(c.name, func(t *testing.T) {
			p := comfyGoldenParams
			p.Loras = []comfyLora{{Name: "watercolor-v2.safetensors", Weight: 0.8}}
			g, err := comfyBuildGraph(c.family, c.files, p)
			if err != nil {
				t.Fatalf("comfyBuildGraph(%s) = %v", c.family, err)
			}
			lora, ok := g["lora1"]
			if !ok {
				t.Fatal("no lora1 node in the graph")
			}
			if lora.ClassType != "LoraLoader" {
				t.Errorf("class_type = %q, want LoraLoader", lora.ClassType)
			}
			if got := lora.Inputs["lora_name"]; got != "watercolor-v2.safetensors" {
				t.Errorf("lora_name = %v, want the on-disk basename", got)
			}
			for _, in := range []string{"strength_model", "strength_clip"} {
				if got := lora.Inputs[in]; got != 0.8 {
					t.Errorf("%s = %v, want the requested weight", in, got)
				}
			}
			// The chain's own inputs must be links, i.e. it sits BETWEEN the loaders and the
			// rest rather than floating unconnected.
			for _, in := range []string{"model", "clip"} {
				if _, ok := lora.Inputs[in].([]any); !ok {
					t.Errorf("lora1.%s = %#v, want a link to a loader", in, lora.Inputs[in])
				}
			}
			for _, ref := range c.modelInputs {
				if got := comfyLinkAt(t, g, ref); got[0] != "lora1" || got[1] != 0 {
					t.Errorf("%s reads %v, want the patched MODEL [lora1 0]", ref, got)
				}
			}
			for _, ref := range c.clipInputs {
				if got := comfyLinkAt(t, g, ref); got[0] != "lora1" || got[1] != 1 {
					t.Errorf("%s reads %v, want the patched CLIP [lora1 1]", ref, got)
				}
			}
		})
	}
}

// Several LoRAs stack: each one patches the output of the one before it. Chained in the order
// asked for, because "sketch then watercolour" and the reverse are different pictures.
func TestComfyWorkflowsChainSeveralLorasInOrder(t *testing.T) {
	p := comfyGoldenParams
	p.Loras = []comfyLora{{Name: "a.safetensors", Weight: 1}, {Name: "b.safetensors", Weight: 0.5}}
	g, err := comfyBuildGraph(ComfyFamilySDXL, comfyFiles{Checkpoint: "x.safetensors"}, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := g["lora1"].Inputs["lora_name"]; got != "a.safetensors" {
		t.Errorf("lora1 = %v, want the first one asked for", got)
	}
	if got, _ := g["lora2"].Inputs["model"].([]any); len(got) != 2 || got[0] != "lora1" {
		t.Errorf("lora2.model reads %v, want lora1's output", g["lora2"].Inputs["model"])
	}
	if got, _ := g["lora2"].Inputs["clip"].([]any); len(got) != 2 || got[0] != "lora1" {
		t.Errorf("lora2.clip reads %v, want lora1's output", g["lora2"].Inputs["clip"])
	}
	if got := comfyLinkAt(t, g, "ks.model"); got[0] != "lora2" {
		t.Errorf("ks.model reads %v, want the LAST link of the chain", got)
	}
}

// The negative control for the golden fixtures above: a request with no LoRA has to produce the
// graph that was pinned before this feature existed, node for node.
func TestComfyWorkflowsAddNoLoraNodeWhenNoneAsked(t *testing.T) {
	for _, c := range comfyFamilyFixtures {
		t.Run(c.name, func(t *testing.T) {
			g, err := comfyBuildGraph(c.family, c.files, comfyGoldenParams)
			if err != nil {
				t.Fatal(err)
			}
			for id := range g {
				if strings.HasPrefix(id, "lora") {
					t.Errorf("node %q exists without a LoRA being asked for", id)
				}
			}
		})
	}
}

// --- image-to-image (ADR 0072 P2's remaining work) ------------------------------------------

// Every family's edit path, checked where a golden fixture cannot look: that the sampler starts
// from the caller's picture instead of an empty latent, that the VAE it is encoded with is the
// family's OWN (the checkpoint's for sdxl/sd35, the standalone loader's for the split ones — a
// crossed link here decodes to noise and fails nothing), and that the partial denoise lands
// wherever that family expresses it.
func TestComfyWorkflowsEditStartsFromTheInputPicture(t *testing.T) {
	for _, c := range comfyFamilyFixtures {
		t.Run(c.name, func(t *testing.T) {
			p := comfyGoldenParams
			p.Op, p.Images = OpEdit, []string{"af-photo.png"}
			g, err := comfyBuildGraph(c.family, c.files, p)
			if err != nil {
				t.Fatalf("comfyBuildGraph(%s) = %v", c.family, err)
			}
			if _, empty := g["lat"]; empty {
				t.Error("an empty latent was built for an edit")
			}
			img, ok := g["img"]
			if !ok || img.ClassType != "LoadImage" {
				t.Fatalf("no LoadImage node: %+v", g["img"])
			}
			if got := img.Inputs["image"]; got != "af-photo.png" {
				t.Errorf("LoadImage.image = %v, want the uploaded name", got)
			}
			enc, ok := g["enc"]
			if !ok || enc.ClassType != "VAEEncode" {
				t.Fatalf("no VAEEncode node: %+v", g["enc"])
			}
			if link, _ := enc.Inputs["pixels"].([]any); len(link) != 2 || link[0] != "img" {
				t.Errorf("VAEEncode.pixels = %v, want the loaded picture", enc.Inputs["pixels"])
			}
			// The VAE has to be the one this family decodes with, or the encode and the decode
			// are different models and the picture comes back as noise.
			wantVae := comfyLinkAt(t, g, "dec.vae")
			gotVae, _ := enc.Inputs["vae"].([]any)
			if len(gotVae) != 2 || gotVae[0] != wantVae[0] || gotVae[1] != wantVae[1] {
				t.Errorf("VAEEncode.vae = %v, want the same VAE the decode uses (%v)", gotVae, wantVae)
			}
			if link := comfyLinkAt(t, g, c.latentInput); link[0] != "enc" {
				t.Errorf("%s reads %v, want the encoded picture", c.latentInput, link)
			}
			if c.denoiseAt != "" {
				parts := strings.SplitN(c.denoiseAt, ".", 2)
				if got := g[parts[0]].Inputs[parts[1]]; got != comfyEditDenoise {
					t.Errorf("%s = %v, want the edit recipe's %v", c.denoiseAt, got, comfyEditDenoise)
				}
			}
		})
	}
}

// klein is the one family whose scheduler has no denoise at all (Flux2Scheduler takes steps and a
// size, v0.34.0), so an edit's partial denoise has to be a TAIL cut off the schedule — and the
// tail is SplitSigmasDenoise's SECOND output. Taking the first would sample the part an edit
// exists to skip, which produces a picture and no error.
func TestComfyWorkflowsKleinEditSplitsTheSigmas(t *testing.T) {
	files := comfyFiles{DiffusionModel: "k.safetensors", ClipL: "q.safetensors", Vae: "v.safetensors"}
	p := comfyGoldenParams
	p.Op, p.Images = OpEdit, []string{"af-photo.png"}
	g, err := comfyBuildGraph(ComfyFamilyFlux2Klein, files, p)
	if err != nil {
		t.Fatal(err)
	}
	split, ok := g["split"]
	if !ok || split.ClassType != "SplitSigmasDenoise" {
		t.Fatalf("no SplitSigmasDenoise node: %+v", g["split"])
	}
	if split.Inputs["denoise"] != comfyEditDenoise {
		t.Errorf("denoise = %v, want %v", split.Inputs["denoise"], comfyEditDenoise)
	}
	if link, _ := split.Inputs["sigmas"].([]any); len(link) != 2 || link[0] != "sigmas" {
		t.Errorf("split.sigmas = %v, want the Flux2Scheduler output", split.Inputs["sigmas"])
	}
	if link := comfyLinkAt(t, g, "sca.sigmas"); link[0] != "split" || link[1] != 1 {
		t.Errorf("sca.sigmas = %v, want low_sigmas (slot 1) of the split", link)
	}
	// generate and inpaint both run the full schedule, so neither pays for the extra node.
	for _, op := range []Op{OpGenerate, OpInpaint} {
		q := comfyGoldenParams
		q.Op, q.Images, q.Mask = op, []string{"af-photo.png"}, "af-mask.png"
		full, err := comfyBuildGraph(ComfyFamilyFlux2Klein, files, q)
		if err != nil {
			t.Fatal(err)
		}
		if _, has := full["split"]; has {
			t.Errorf("%s split the sigmas — it runs the whole schedule", op)
		}
		if link := comfyLinkAt(t, full, "sca.sigmas"); link[0] != "sigmas" {
			t.Errorf("%s: sca.sigmas = %v, want the scheduler directly", op, link)
		}
	}
}

// --- strength (ADR 0069 follow-up, 2026-09-13) -----------------------------------------------

// The caller's own strength has to reach the number each family expresses a partial denoise
// with, and nothing else may move: an argument that lands on four families and is quietly
// dropped by the fifth is the failure the vocabulary's per-family templates invite.
func TestComfyWorkflowsEditStrengthReachesTheSampler(t *testing.T) {
	const want = 0.25
	for _, c := range comfyFamilyFixtures {
		t.Run(c.name, func(t *testing.T) {
			s := want
			p := comfyGoldenParams
			p.Op, p.Images, p.Strength = OpEdit, []string{"af-photo.png"}, &s
			g, err := comfyBuildGraph(c.family, c.files, p)
			if err != nil {
				t.Fatalf("comfyBuildGraph(%s) = %v", c.family, err)
			}
			at := c.denoiseAt
			if at == "" {
				// klein expresses it on the extra node instead of on a sampler input.
				at = "split.denoise"
			}
			parts := strings.SplitN(at, ".", 2)
			if got := g[parts[0]].Inputs[parts[1]]; got != want {
				t.Errorf("%s = %v, want the caller's strength %v", at, got, want)
			}
		})
	}
}

// A strength outside (0,1] falls back to the recipe's own rather than reaching the graph. The
// edge refuses one by value (HandleGenerate), so this is the other half of that pair — and 0 in
// particular would divide by zero in klein's stretch.
func TestComfyWorkflowsEditStrengthOutOfRangeFallsBack(t *testing.T) {
	for _, bad := range []float64{0, -0.5, 1.5} {
		s := bad
		p := comfyGoldenParams
		p.Op, p.Images, p.Strength = OpEdit, []string{"af-photo.png"}, &s
		g, err := comfyBuildGraph(ComfyFamilySDXL, comfyFiles{Checkpoint: "sd_xl_base_1.0.safetensors"}, p)
		if err != nil {
			t.Fatalf("strength=%v: %v", bad, err)
		}
		if got := g["ks"].Inputs["denoise"]; got != comfyEditDenoise {
			t.Errorf("strength=%v: ks.denoise = %v, want the recipe's %v", bad, got, comfyEditDenoise)
		}
	}
}

// Inpaint repaints its masked area in full whatever the caller asked for: the noise mask is what
// preserves everything outside it, so a partial denoise there dulls the repaint instead of
// protecting anything. The caller hears about it in warnings (requestWarnings), not by having the
// number applied to something it does not mean.
func TestComfyWorkflowsInpaintIgnoresStrength(t *testing.T) {
	for _, c := range comfyFamilyFixtures {
		t.Run(c.name, func(t *testing.T) {
			s := 0.2
			p := comfyGoldenParams
			p.Op, p.Images, p.Mask, p.Strength = OpInpaint, []string{"af-photo.png"}, "af-mask.png", &s
			g, err := comfyBuildGraph(c.family, c.files, p)
			if err != nil {
				t.Fatalf("comfyBuildGraph(%s) = %v", c.family, err)
			}
			if _, has := g["split"]; has {
				t.Error("inpaint cut the schedule — it repaints the masked area in full")
			}
			if c.denoiseAt != "" {
				parts := strings.SplitN(c.denoiseAt, ".", 2)
				if got := g[parts[0]].Inputs[parts[1]]; got != 1.0 {
					t.Errorf("%s = %v, want a full denoise", c.denoiseAt, got)
				}
			}
		})
	}
}

// klein's schedule is STRETCHED before SplitSigmasDenoise cuts it, and this is the one place
// that can be checked without a GPU. Its tail is round(len(sigmas)*denoise) steps of whatever it
// was handed, so cutting the unstretched 4-step schedule would buy fewer sampling steps the
// gentler the edit — the other four families keep their step count at every denoise because
// their samplers stretch it themselves (comfy/samplers.py, KSampler.set_steps).
func TestComfyWorkflowsKleinStretchesTheScheduleBeforeCutting(t *testing.T) {
	files := comfyFiles{DiffusionModel: "k.safetensors", ClipL: "q.safetensors", Vae: "v.safetensors"}
	// steps asked of Flux2Scheduler, and the tail SplitSigmasDenoise then returns from it.
	for _, c := range []struct{ strength, schedule, tail float64 }{
		{0.6, 6, 4}, // int(4/0.6) = 6, round(6*0.6) = 4
		{0.25, 16, 4},
		{0.9, 4, 4},
	} {
		s := c.strength
		p := comfyGoldenParams
		p.Op, p.Images, p.Strength = OpEdit, []string{"af-photo.png"}, &s
		g, err := comfyBuildGraph(ComfyFamilyFlux2Klein, files, p)
		if err != nil {
			t.Fatalf("strength=%v: %v", c.strength, err)
		}
		if got := g["sigmas"].Inputs["steps"]; got != int(c.schedule) {
			t.Errorf("strength=%v: Flux2Scheduler.steps = %v, want %v", c.strength, got, int(c.schedule))
		}
		// What the engine will actually sample, by the node's own formula. The recipe asks for 4
		// and the caller's strength must not change it.
		if tail := math.Round(c.schedule * c.strength); tail != c.tail {
			t.Errorf("strength=%v: the tail is %v steps, want %v", c.strength, tail, c.tail)
		}
	}
	// generate runs the whole schedule and pays for no stretch.
	q := comfyGoldenParams
	g, err := comfyBuildGraph(ComfyFamilyFlux2Klein, files, q)
	if err != nil {
		t.Fatal(err)
	}
	if got := g["sigmas"].Inputs["steps"]; got != 4 {
		t.Errorf("generate: Flux2Scheduler.steps = %v, want the recipe's 4", got)
	}
}

// inpaint is an edit plus a noise mask, in every family. The mask reaches the sampler through the
// LATENT rather than through the conditioning, which is what makes one shape work for KSampler
// and SamplerCustomAdvanced alike.
func TestComfyWorkflowsInpaintMasksTheLatent(t *testing.T) {
	for _, c := range comfyFamilyFixtures {
		t.Run(c.name, func(t *testing.T) {
			p := comfyGoldenParams
			p.Op, p.Images, p.Mask = OpInpaint, []string{"af-photo.png"}, "af-mask.png"
			g, err := comfyBuildGraph(c.family, c.files, p)
			if err != nil {
				t.Fatalf("comfyBuildGraph(%s) = %v", c.family, err)
			}
			mask, ok := g["mask"]
			if !ok || mask.ClassType != "LoadImageMask" {
				t.Fatalf("no LoadImageMask node: %+v", g["mask"])
			}
			// Red, not alpha: LoadImage's own MASK output is 1-alpha, so an opaque black-and-
			// white PNG through that path repaints nothing and reports nothing.
			if mask.Inputs["channel"] != "red" {
				t.Errorf("channel = %v, want red", mask.Inputs["channel"])
			}
			if mask.Inputs["image"] != "af-mask.png" {
				t.Errorf("LoadImageMask.image = %v, want the uploaded mask", mask.Inputs["image"])
			}
			set, ok := g["noisemask"]
			if !ok || set.ClassType != "SetLatentNoiseMask" {
				t.Fatalf("no SetLatentNoiseMask node: %+v", g["noisemask"])
			}
			if link, _ := set.Inputs["samples"].([]any); len(link) != 2 || link[0] != "enc" {
				t.Errorf("noisemask.samples = %v, want the encoded picture", set.Inputs["samples"])
			}
			if link := comfyLinkAt(t, g, c.latentInput); link[0] != "noisemask" {
				t.Errorf("%s reads %v, want the masked latent", c.latentInput, link)
			}
			// Full denoise: the mask is what preserves everything outside it.
			if c.denoiseAt != "" {
				parts := strings.SplitN(c.denoiseAt, ".", 2)
				if got := g[parts[0]].Inputs[parts[1]]; got != float64(1) {
					t.Errorf("%s = %v, want 1 for inpaint", c.denoiseAt, got)
				}
			}
		})
	}
}

// The graph refuses to be built at all when the picture or the mask never arrived — a template
// that silently fell back to an empty latent would answer an edit with an unrelated picture.
func TestComfyWorkflowsRefuseImageToImageWithoutTheAttachments(t *testing.T) {
	for _, c := range comfyFamilyFixtures {
		t.Run(c.name, func(t *testing.T) {
			p := comfyGoldenParams
			p.Op = OpEdit
			if _, err := comfyBuildGraph(c.family, c.files, p); err == nil {
				t.Error("an edit with no input image built a graph")
			}
			p.Op, p.Images = OpInpaint, []string{"af-photo.png"}
			if _, err := comfyBuildGraph(c.family, c.files, p); err == nil {
				t.Error("an inpaint with no mask built a graph")
			}
		})
	}
}

// A model missing a required file is refused before any HTTP call is made (comfyBuildGraph
// checks per family), in the caller's own language rather than as a ComfyUI validation error a
// caller two layers up cannot act on.
func TestComfyWorkflowsRefuseMissingFiles(t *testing.T) {
	cases := []struct {
		name   string
		family comfyFamily
		files  comfyFiles
	}{
		{"sdxl needs a checkpoint", ComfyFamilySDXL, comfyFiles{}},
		{"zimage needs a diffusion model", ComfyFamilyZImage, comfyFiles{ClipL: "x", Vae: "y"}},
		{"zimage needs a text encoder", ComfyFamilyZImage, comfyFiles{DiffusionModel: "x", Vae: "y"}},
		{"zimage needs a vae", ComfyFamilyZImage, comfyFiles{DiffusionModel: "x", ClipL: "y"}},
		{"klein needs a diffusion model", ComfyFamilyFlux2Klein, comfyFiles{ClipL: "x", Vae: "y"}},
		{"flux1 needs both text encoders", ComfyFamilyFlux1, comfyFiles{DiffusionModel: "x", ClipL: "y", Vae: "z"}},
		{"sd35 needs a checkpoint", ComfyFamilySD35, comfyFiles{ClipL: "x", ClipG: "y", T5xxl: "z"}},
		{"sd35 needs a standalone t5xxl", ComfyFamilySD35, comfyFiles{Checkpoint: "x", ClipL: "y", ClipG: "z"}},
		// The one that was missing. Without --clip_g in the vocabulary this case could not even
		// be expressed, and the template pointed TripleCLIPLoader at the checkpoint instead.
		{"sd35 needs clip_g", ComfyFamilySD35, comfyFiles{Checkpoint: "x", ClipL: "y", T5xxl: "z"}},
		{"sd35 needs clip_l", ComfyFamilySD35, comfyFiles{Checkpoint: "x", ClipG: "y", T5xxl: "z"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := comfyBuildGraph(c.family, c.files, comfyGoldenParams); err == nil {
				t.Error("expected an error, got none")
			}
		})
	}
}

// The checkpoint families read the catalogue's `--vae` file when the row declares one, and the
// checkpoint's own VAE when it does not. That is the whole difference between a row that
// generates and one that dies in ComfyUI with `ERROR: VAE is invalid: None`, which is what a
// checkpoint published with no VAE tensors does on every op (measured 2026-09-11 on this
// deployment, an Illustrious/SDXL row: generate in VAEDecode, edit in VAEEncode). A caller
// cannot work around it — `generate_image` has no VAE argument — so the declaration is the fix,
// and the encode and the decode have to take the SAME one or the picture returns as noise.
func TestComfyCheckpointFamiliesTakeADeclaredVae(t *testing.T) {
	for _, fam := range []comfyFamily{ComfyFamilySDXL, ComfyFamilySD35} {
		// clip_l / clip_g / t5xxl are what sd35 additionally requires; sdxl ignores them.
		base := comfyFiles{Checkpoint: "c.safetensors",
			ClipL: "l.safetensors", ClipG: "g.safetensors", T5xxl: "t.safetensors"}
		p := comfyGoldenParams
		p.Op, p.Images = OpEdit, []string{"af-photo.png"}

		t.Run(string(fam)+" without one", func(t *testing.T) {
			g, err := comfyBuildGraph(fam, base, p)
			if err != nil {
				t.Fatal(err)
			}
			if _, has := g["vae"]; has {
				t.Error("a VAELoader was added for a row that declares no --vae file")
			}
			for _, ref := range []string{"dec.vae", "enc.vae"} {
				if got := comfyLinkAt(t, g, ref); got[0] != "ckpt" || got[1] != 2 {
					t.Errorf("%s reads %v, want the checkpoint's own VAE [ckpt 2]", ref, got)
				}
			}
		})

		t.Run(string(fam)+" with one", func(t *testing.T) {
			files := base
			files.Vae = "sdxl_vae.safetensors"
			g, err := comfyBuildGraph(fam, files, p)
			if err != nil {
				t.Fatal(err)
			}
			loader, ok := g["vae"]
			if !ok || loader.ClassType != "VAELoader" {
				t.Fatalf("no VAELoader node: %+v", g["vae"])
			}
			if got := loader.Inputs["vae_name"]; got != "sdxl_vae.safetensors" {
				t.Errorf("vae_name = %v, want the declared file's basename", got)
			}
			for _, ref := range []string{"dec.vae", "enc.vae"} {
				if got := comfyLinkAt(t, g, ref); got[0] != "vae" || got[1] != 0 {
					t.Errorf("%s reads %v, want the declared VAE [vae 0]", ref, got)
				}
			}
		})
	}
}

func TestComfyBuildGraphRefusesAnUnknownFamily(t *testing.T) {
	if _, err := comfyBuildGraph("made-up-family", comfyFiles{Checkpoint: "x"}, comfyGoldenParams); err == nil {
		t.Error("expected an error for an unrecognised family, got none")
	}
}

// The catalogue's Flag vocabulary is sd.cpp's own spelling (EngineFile's contract) — this pins
// that resolveComfyFiles reads exactly those flags and drops the rest silently rather than
// erroring on an unknown one, forward-compatible with a catalogue newer than this Agent.
func TestResolveComfyFiles(t *testing.T) {
	files := []EngineFile{
		{Flag: "", Name: "ckpt.safetensors"},
		{Flag: "--diffusion-model", Name: "unet.safetensors"},
		{Flag: "--clip_l", Name: "clip.safetensors"},
		{Flag: "--clip_g", Name: "clipg.safetensors"},
		{Flag: "--t5xxl", Name: "t5.safetensors"},
		{Flag: "--vae", Name: "vae.safetensors"},
		{Flag: "--lora-model-dir", Name: "ignored.safetensors"}, // unrecognised flag: dropped
		{Flag: "--vae", Name: ""},                               // empty name: dropped
	}
	got := resolveComfyFiles(files)
	want := comfyFiles{
		Checkpoint: "ckpt.safetensors", DiffusionModel: "unet.safetensors",
		ClipL: "clip.safetensors", ClipG: "clipg.safetensors",
		T5xxl: "t5.safetensors", Vae: "vae.safetensors",
	}
	if got != want {
		t.Errorf("resolveComfyFiles = %+v, want %+v", got, want)
	}
}

// comfyFileFlags is served to the Console (through the Control Plane) as the list of parts a
// split model may declare, so a flag on that list which resolveComfyFiles quietly drops would
// be an option an operator can pick and then watch fail at generation. Every declared flag has
// to land somewhere in comfyFiles, and nothing else may.
func TestComfyFileFlagsAllResolve(t *testing.T) {
	for _, flag := range comfyFileFlags {
		got := resolveComfyFiles([]EngineFile{{Flag: flag, Name: "x.safetensors"}})
		if got == (comfyFiles{}) {
			t.Errorf("flag %q resolves to nothing — it is offered in the panel and dropped here", flag)
		}
	}
	// The other direction: an unknown flag is DROPPED rather than refused (a catalogue newer
	// than this Agent must degrade, not fail every request), which is exactly why the list
	// above has to be complete.
	if got := resolveComfyFiles([]EngineFile{{Flag: "--controlnet", Name: "x.safetensors"}}); got != (comfyFiles{}) {
		t.Errorf("an unknown flag resolved to %+v, want it dropped", got)
	}
	// Positive control for the check above: the assertion must be able to fail.
	if got := resolveComfyFiles([]EngineFile{{Flag: "--vae", Name: "v.safetensors"}}); got.Vae != "v.safetensors" {
		t.Fatalf("--vae did not resolve (%+v) — the comparison above proves nothing", got)
	}
}

// The catalogue row's declared settings, reaching the graph (ADR 0072 decision 4, widened).
//
// Per FIELD, and that is the whole test: a row that declares only `steps` has to keep its
// family's sampler, scheduler and cfg. Folding the two into "the declaration or the recipe"
// would mean naming one number silently reset the other three.
func TestComfyRecipeTakesTheRowsDeclarationFieldByField(t *testing.T) {
	base, err := comfyBuildGraph(ComfyFamilySDXL, comfyFiles{Checkpoint: "x.safetensors"}, comfyGoldenParams)
	if err != nil {
		t.Fatal(err)
	}
	// The family's own recipe, read off the template rather than restated here.
	wantSampler, wantScheduler, wantCFG := base["ks"].Inputs["sampler_name"], base["ks"].Inputs["scheduler"], base["ks"].Inputs["cfg"]

	p := comfyGoldenParams
	p.Params = EngineParams{Steps: 30}
	g, err := comfyBuildGraph(ComfyFamilySDXL, comfyFiles{Checkpoint: "x.safetensors"}, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := g["ks"].Inputs["steps"]; got != 30 {
		t.Errorf("steps = %v, want the row's 30", got)
	}
	if got := g["ks"].Inputs["sampler_name"]; got != wantSampler {
		t.Errorf("sampler_name = %v, want the family's %v — declaring steps must not clear it", got, wantSampler)
	}
	if got := g["ks"].Inputs["scheduler"]; got != wantScheduler {
		t.Errorf("scheduler = %v, want the family's %v", got, wantScheduler)
	}
	if got := g["ks"].Inputs["cfg"]; got != wantCFG {
		t.Errorf("cfg = %v, want the family's %v", got, wantCFG)
	}

	// And all four together, which is the ordinary case: an author's published settings.
	p.Params = EngineParams{Steps: 24, CFG: 3.5, Sampler: "euler_ancestral", Scheduler: "sgm_uniform"}
	g, err = comfyBuildGraph(ComfyFamilySDXL, comfyFiles{Checkpoint: "x.safetensors"}, p)
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]any{
		"steps": 24, "cfg": 3.5, "sampler_name": "euler_ancestral", "scheduler": "sgm_uniform",
	} {
		if got := g["ks"].Inputs[in]; got != want {
			t.Errorf("%s = %v, want %v", in, got, want)
		}
	}
}

// 🔴 A name this Agent does not recognise is IGNORED and the family's own is kept. ComfyUI's
// sampler input is an enumeration and an unknown value fails the whole prompt with `Value not
// in list` — after the cold start somebody waited through — so the two outcomes are "a picture
// made with the family's sampler" and "no picture at all".
func TestComfyRecipeRefusesToForwardANameItDoesNotKnow(t *testing.T) {
	p := comfyGoldenParams
	p.Params = EngineParams{Sampler: "not_a_sampler", Scheduler: "not_a_scheduler", Steps: 12}
	g, err := comfyBuildGraph(ComfyFamilySDXL, comfyFiles{Checkpoint: "x.safetensors"}, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := g["ks"].Inputs["sampler_name"]; got == "not_a_sampler" {
		t.Error("an unknown sampler name reached the graph — ComfyUI answers Value not in list")
	}
	if got := g["ks"].Inputs["scheduler"]; got == "not_a_scheduler" {
		t.Error("an unknown scheduler name reached the graph")
	}
	// The positive control: the rest of the same declaration DID apply, so this is not passing
	// because the whole merge was skipped.
	if got := g["ks"].Inputs["steps"]; got != 12 {
		t.Errorf("steps = %v, want 12 — the valid half of the declaration must still apply", got)
	}
}

// The two families whose "cfg" is a different knob. FLUX.1 folds guidance into the conditioning
// (FluxGuidance + BasicGuider) and klein's CFGGuider runs the distilled path at a fixed 1, so
// the number a model card calls "CFG" is not this one — applying it would be a silently wrong
// picture rather than a refusal.
func TestComfyRecipeLeavesGuidanceAloneWhereCfgMeansSomethingElse(t *testing.T) {
	for _, c := range comfyFamilyFixtures {
		if c.family != ComfyFamilyFlux1 && c.family != ComfyFamilyFlux2Klein {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			before, err := comfyBuildGraph(c.family, c.files, comfyGoldenParams)
			if err != nil {
				t.Fatal(err)
			}
			p := comfyGoldenParams
			p.Params = EngineParams{CFG: 9, Steps: 6}
			after, err := comfyBuildGraph(c.family, c.files, p)
			if err != nil {
				t.Fatal(err)
			}
			for _, node := range []string{"guider", "guidance"} {
				b, ok := before[node]
				if !ok {
					continue
				}
				for in, want := range b.Inputs {
					if in != "cfg" && in != "guidance" {
						continue
					}
					if got := after[node].Inputs[in]; got != want {
						t.Errorf("%s.%s = %v, want the family's %v", node, in, got, want)
					}
				}
			}
			// The positive control again: steps, which these families DO take, moved.
			if c.family == ComfyFamilyFlux1 {
				if got := after["scheduler"].Inputs["steps"]; got != 6 {
					t.Errorf("flux1 steps = %v, want 6", got)
				}
			} else if got := after["sigmas"].Inputs["steps"]; got != 6 {
				t.Errorf("klein steps = %v, want 6", got)
			}
		})
	}
}

// --- Qwen-Image-Edit, both topologies (ADR 0094) ---------------------------------------------
//
// NOT members of comfyFamilyFixtures: every generic test above assumes a family that offers
// generate AND inpaint, expresses an edit as a PARTIAL denoise, and lets strength move it — none
// of which holds here (ADR 0094 background, decisions 2/3). These families get their own golden
// fixtures and their own tests for exactly the ways they differ.
//
// Every test below runs against BOTH, because what decisions 2-5 say is said about the instruction
// -edit shape and not about one version (comfyFamilyInstructionEdit). The one place the two must
// differ — the reference-latent method and the shift, which is the whole reason 2511 is a family
// rather than a row — has a test of its own.

var comfyQwenEditFamilies = []struct {
	family comfyFamily
	files  comfyFiles
}{
	{ComfyFamilyQwenImageEdit2509, comfyFiles{
		DiffusionModel: "qwen_image_edit_2509_fp8_e4m3fn.safetensors",
		ClipL:          "qwen_2.5_vl_7b_fp8_scaled.safetensors",
		Vae:            "qwen_image_vae.safetensors",
	}},
	{ComfyFamilyQwenImageEdit2511, comfyFiles{
		DiffusionModel: "qwen_image_edit_2511_fp8mixed.safetensors",
		ClipL:          "qwen_2.5_vl_7b_fp8_scaled.safetensors",
		Vae:            "qwen_image_vae.safetensors",
	}},
}

// comfyQwenEditFiles is the 2509 fixture, kept as a name for the tests that only need one row's
// worth of declared files.
var comfyQwenEditFiles = comfyQwenEditFamilies[0].files

func TestComfyWorkflowQwenImageEditMatchesGoldenFixture(t *testing.T) {
	for _, c := range comfyQwenEditFamilies {
		t.Run(string(c.family), func(t *testing.T) {
			p := comfyGoldenParams
			p.Op, p.Images = OpEdit, []string{"af-photo.png"}
			g, err := comfyBuildGraph(c.family, c.files, p)
			if err != nil {
				t.Fatalf("comfyBuildGraph(%s) = %v", c.family, err)
			}
			got, err := json.MarshalIndent(g, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "comfy_"+string(c.family)+".golden.json")
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			if string(got) != string(want) {
				t.Errorf("%s's graph no longer matches %s.\nGot:\n%s\nIf this change is intended, "+
					"overwrite the fixture and explain why in the commit.", c.family, path, got)
			}
		})
	}
}

// Inpaint on an instruction-edit family (ADR 0094 decision 3, claimed after 実測 G / 実測 I). Every
// assertion here is a way the picture comes back WRONG with no error anywhere, which is why they
// are pinned one by one rather than left to the golden:
//
//   - the mask must go through a FluxKontextImageScale of its OWN. The picture's own scale node
//     centre-crops to the nearest trained ratio; SetLatentNoiseMask only stretches. Measured on a
//     1820x1024 input: the repainted band's edge sat 8 px from where the picture's map puts it,
//     and routing the mask through this node moved it back (実測 I).
//   - it must be ImageToMask on RED, not LoadImageMask. LoadImage's MASK output is `1.0 - alpha`,
//     so an opaque black-and-white PNG arrives as an all-zero mask and repaints nothing.
//   - the noise mask must reach the SAMPLER. A SetLatentNoiseMask that is built but not consumed
//     is a full repaint of the whole picture, and the graph validates either way.
func TestComfyWorkflowQwenImageEditInpaintScalesTheMaskWithThePicture(t *testing.T) {
	for _, c := range comfyQwenEditFamilies {
		t.Run(string(c.family), func(t *testing.T) {
			p := comfyGoldenParams
			p.Op, p.Images, p.Mask = OpInpaint, []string{"af-photo.png"}, "af-mask.png"
			g, err := comfyBuildGraph(c.family, c.files, p)
			if err != nil {
				t.Fatalf("comfyBuildGraph(%s) = %v", c.family, err)
			}
			if got := g["maskimg"].Inputs["image"]; got != "af-mask.png" {
				t.Errorf("maskimg.image = %v, want the mask file", got)
			}
			if cls := g["maskscale"].ClassType; cls != "FluxKontextImageScale" {
				t.Errorf("maskscale = %q, want the same node the picture goes through — otherwise the"+
					" mask is stretched over a frame the picture was cropped out of", cls)
			}
			if got := g["maskscale"].Inputs["image"]; !reflect.DeepEqual(got, comfyLink("maskimg", 0)) {
				t.Errorf("maskscale.image = %v, want the mask's own LoadImage", got)
			}
			// The picture's scale node is a DIFFERENT one fed by the picture: one node for both
			// would put the mask's pixels into the encode.
			if got := g["scale"].Inputs["image"]; !reflect.DeepEqual(got, comfyLink("img", 0)) {
				t.Errorf("scale.image = %v, want the picture's own LoadImage", got)
			}
			if cls, ch := g["mask"].ClassType, g["mask"].Inputs["channel"]; cls != "ImageToMask" || ch != "red" {
				t.Errorf("mask = %s(channel=%v), want ImageToMask(channel=red) — alpha reads an opaque"+
					" black-and-white PNG as an all-zero mask", cls, ch)
			}
			if got := g["mask"].Inputs["image"]; !reflect.DeepEqual(got, comfyLink("maskscale", 0)) {
				t.Errorf("mask.image = %v, want the SCALED mask", got)
			}
			nm := g["noisemask"]
			if nm.ClassType != "SetLatentNoiseMask" ||
				!reflect.DeepEqual(nm.Inputs["samples"], comfyLink("enc", 0)) ||
				!reflect.DeepEqual(nm.Inputs["mask"], comfyLink("mask", 0)) {
				t.Errorf("noisemask = %+v, want SetLatentNoiseMask(enc, mask)", nm)
			}
			if got := g["ks"].Inputs["latent_image"]; !reflect.DeepEqual(got, comfyLink("noisemask", 0)) {
				t.Errorf("ks.latent_image = %v, want the noise mask — built and not consumed repaints"+
					" the whole picture with nothing to say so", got)
			}
			// Still 1: outside the mask is held by the mask, not by a partial denoise (decision 2).
			if got := g["ks"].Inputs["denoise"]; got != 1 {
				t.Errorf("denoise = %v, want 1", got)
			}
		})
	}
}

// And an edit builds NONE of it: the four nodes appear only when the op asks for them, so the
// golden for `op=edit` stays the graph this family has been running since P0.
func TestComfyWorkflowQwenImageEditAddsNoMaskNodesForAPlainEdit(t *testing.T) {
	p := comfyGoldenParams
	p.Op, p.Images, p.Mask = OpEdit, []string{"af-photo.png"}, "af-mask.png"
	g, err := comfyBuildGraph(ComfyFamilyQwenImageEdit2509, comfyQwenEditFiles, p)
	if err != nil {
		t.Fatalf("comfyBuildGraph() = %v", err)
	}
	for _, id := range []string{"maskimg", "maskscale", "mask", "noisemask"} {
		if _, ok := g[id]; ok {
			t.Errorf("an edit built %q — the mask only belongs to inpaint", id)
		}
	}
	if got := g["ks"].Inputs["latent_image"]; !reflect.DeepEqual(got, comfyLink("enc", 0)) {
		t.Errorf("ks.latent_image = %v, want the plain encode", got)
	}
}

// An inpaint with no mask is refused in the caller's language rather than sampled as a full
// repaint of the picture — the same refusal comfyRequestLatent gives the other families.
func TestComfyWorkflowQwenImageEditInpaintNeedsAMask(t *testing.T) {
	p := comfyGoldenParams
	p.Op, p.Images = OpInpaint, []string{"af-photo.png"}
	_, err := comfyBuildGraph(ComfyFamilyQwenImageEdit2509, comfyQwenEditFiles, p)
	if err == nil || !strings.Contains(err.Error(), "mask") {
		t.Fatalf("comfyBuildGraph() = %v, want a refusal naming the mask", err)
	}
}

// The family is READ back off a finished picture through SaveImage's filename_prefix
// (comfyFamilyFromPrefix), which is what fills the reproduction record's `family` — so every
// template's prefix has to be one comfyFamilyFromPrefix recognises, for EVERY family.
//
// Nothing pinned this before, and the hole is the shape a golden fixture cannot see: the goldens
// compare a prefix to a string that was written down at the same time, so a prefix the reader
// does not recognise matches its own fixture happily. Measured while consolidating the family
// declarations (ADR 0094 未解決 4): dropping flux2-klein's short name left `af-klein` on every
// picture it makes and unreadable by the reader, with the whole suite green.
func TestEveryFamilysPrefixIsReadableBack(t *testing.T) {
	files := map[comfyFamily]comfyFiles{}
	for _, c := range comfyFamilyFixtures {
		files[c.family] = c.files
	}
	for _, c := range comfyQwenEditFamilies {
		files[c.family] = c.files
	}
	// In neither fixture list because its graph is not either shape (ADR 0098), and this check
	// reaches every family in the vocabulary by design.
	files[ComfyFamilyQwenImage21] = comfyQwen21Files
	for _, family := range comfyFamilies {
		t.Run(string(family), func(t *testing.T) {
			f, ok := files[family]
			if !ok {
				t.Fatalf("%s is in no fixture list, so this check silently stops measuring it", family)
			}
			p := comfyGoldenParams
			p.Op, p.Images = OpEdit, []string{"af-photo.png"}
			g, err := comfyBuildGraph(family, f, p)
			if err != nil {
				t.Fatalf("comfyBuildGraph(%s) = %v", family, err)
			}
			prefix := stringOf(g["save"].Inputs["filename_prefix"])
			if prefix == "" {
				t.Fatalf("%s's SaveImage names no filename_prefix", family)
			}
			if got := comfyFamilyFromPrefix(prefix); got != string(family) {
				t.Errorf("comfyFamilyFromPrefix(%q) = %q, want %q — a picture this template makes"+
					" records a family nobody can read back", prefix, got, family)
			}
		})
	}
}

// The second reference (ADR 0094 decision 5, P3), wired the way 実測 D measured it and not the
// way it reads at first glance. Three things have to hold together and each fails silently on its
// own — an extra reference that is dropped, scaled, or attached to one side only all produce a
// picture with no error anywhere:
//
//   - image2 is a LoadImage of its own, NOT routed through FluxKontextImageScale. That node fixes
//     the FRAME, and the frame is image1's; scaling a borrowed object to the first picture's
//     aspect ratio crops away the thing the caller asked for.
//   - it reaches BOTH encodes. CFG subtracts the two conditionings, so a reference on one side
//     only leaves its own encoding in the difference.
//   - the latent still comes from scaled image1, so the output keeps the edited picture's size.
func TestComfyWorkflowQwenImageEditWiresTheSecondReferenceToBothEncodes(t *testing.T) {
	for _, c := range comfyQwenEditFamilies {
		t.Run(string(c.family), func(t *testing.T) {
			p := comfyGoldenParams
			// All three the node takes (実測 F measured three arriving at once), so the loop is
			// driven past its first turn — an `image2`-shaped special case would pass at two.
			p.Op, p.Images = OpEdit, []string{"af-scene.png", "af-plant.png", "af-duck.png"}
			g, err := comfyBuildGraph(c.family, c.files, p)
			if err != nil {
				t.Fatal(err)
			}
			for node, want := range map[string]string{"img2": "af-plant.png", "img3": "af-duck.png"} {
				n, ok := g[node]
				if !ok || n.ClassType != "LoadImage" || n.Inputs["image"] != want {
					t.Fatalf("%s = %+v, want a LoadImage of %s", node, g[node], want)
				}
			}
			for _, encode := range []string{"pos", "neg"} {
				if got := comfyLinkAt(t, g, encode+".image1"); got[0] != "scale" {
					t.Errorf("%s.image1 reads %v, want the scaled first picture", encode, got)
				}
				for input, want := range map[string]string{"image2": "img2", "image3": "img3"} {
					got := comfyLinkAt(t, g, encode+"."+input)
					if got[0] != want {
						t.Errorf("%s.%s reads %v, want the raw %s — scaling it would crop the borrowed object",
							encode, input, got, want)
					}
				}
			}
			if got := comfyLinkAt(t, g, "enc.pixels"); got[0] != "scale" {
				t.Errorf("enc.pixels reads %v, want scaled image1: the output keeps the EDITED picture's size", got)
			}
		})
	}
}

// The negative control for the test above, and the reason the extra wiring is a loop over what the
// caller actually sent rather than a fixed image2: one reference must produce the graph 実測 A and
// E ran, with no img2 node and no image2 input anywhere. Every other family is in this state
// permanently (Caps.MaxInputs is 1 for them), so an unconditional image2 would be a dangling link
// in seven templates at once.
func TestComfyWorkflowQwenImageEditOmitsImage2WhenOnlyOneReferenceWasSent(t *testing.T) {
	for _, c := range comfyQwenEditFamilies {
		t.Run(string(c.family), func(t *testing.T) {
			p := comfyGoldenParams
			p.Op, p.Images = OpEdit, []string{"af-scene.png"}
			g, err := comfyBuildGraph(c.family, c.files, p)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := g["img2"]; ok {
				t.Errorf("img2 = %+v, want no such node", g["img2"])
			}
			for _, encode := range []string{"pos", "neg"} {
				if v, ok := g[encode].Inputs["image2"]; ok {
					t.Errorf("%s.image2 = %v, want the input absent entirely", encode, v)
				}
			}
		})
	}
}

// The difference that makes 2511 a family of its own (ADR 0094 decision 6): both conditionings
// reach the sampler through FluxKontextMultiReferenceLatentMethod, and the shift is 3.1 rather
// than 3. Ported from the graph 実測 E ran, with 2509 as the control in the same test — "the node
// is there" and "the node is there in BOTH" look identical from one family alone.
func TestComfyWorkflowQwenImageEdit2511RoutesBothConditioningsThroughTheReferenceMethod(t *testing.T) {
	p := comfyGoldenParams
	p.Op, p.Images = OpEdit, []string{"af-photo.png"}
	g, err := comfyBuildGraph(ComfyFamilyQwenImageEdit2511, comfyQwenEditFamilies[1].files, p)
	if err != nil {
		t.Fatal(err)
	}
	for node, encode := range map[string]string{"posref": "pos", "negref": "neg"} {
		ref, ok := g[node]
		if !ok || ref.ClassType != "FluxKontextMultiReferenceLatentMethod" {
			t.Fatalf("%s = %+v, want a FluxKontextMultiReferenceLatentMethod", node, g[node])
		}
		if got := ref.Inputs["reference_latents_method"]; got != "index_timestep_zero" {
			t.Errorf("%s.reference_latents_method = %v, want index_timestep_zero", node, got)
		}
		if got := comfyLinkAt(t, g, node+".conditioning"); got[0] != encode || got[1] != 0 {
			t.Errorf("%s.conditioning reads %v, want the %s encode", node, got, encode)
		}
	}
	for input, want := range map[string]string{"positive": "posref", "negative": "negref"} {
		if got := comfyLinkAt(t, g, "ks."+input); got[0] != want {
			t.Errorf("ks.%s reads %v, want it through %s", input, got, want)
		}
	}
	if got := g["ms"].Inputs["shift"]; got != 3.1 {
		t.Errorf("ms.shift = %v, want 3.1", got)
	}
	// The control: 2509's template has no such node, and sampling at 3.1 there would be a number
	// nobody upstream ships or measured.
	c, err := comfyBuildGraph(ComfyFamilyQwenImageEdit2509, comfyQwenEditFiles, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c["posref"]; ok {
		t.Error("2509 grew a FluxKontextMultiReferenceLatentMethod, which its template does not have")
	}
	if got := comfyLinkAt(t, c, "ks.positive"); got[0] != "pos" {
		t.Errorf("2509 ks.positive reads %v, want the encode directly", got)
	}
	if got := c["ms"].Inputs["shift"]; got != float64(3) {
		t.Errorf("2509 ms.shift = %v, want 3", got)
	}
}

// The recipes are the two templates' own KSampler widgets (LoRA-switch false branch), and they
// differ in steps alone — 実測 A ran 20 on 2509, 実測 E ran 40 on 2511.
func TestComfyWorkflowQwenImageEditRecipesAreTheTemplates(t *testing.T) {
	want := map[comfyFamily]int{ComfyFamilyQwenImageEdit2509: 20, ComfyFamilyQwenImageEdit2511: 40}
	for _, c := range comfyQwenEditFamilies {
		p := comfyGoldenParams
		p.Op, p.Images = OpEdit, []string{"af-photo.png"}
		g, err := comfyBuildGraph(c.family, c.files, p)
		if err != nil {
			t.Fatal(err)
		}
		if got := g["ks"].Inputs["steps"]; got != want[c.family] {
			t.Errorf("%s ks.steps = %v, want %d", c.family, got, want[c.family])
		}
		if got := g["ks"].Inputs["cfg"]; got != float64(4) {
			t.Errorf("%s ks.cfg = %v, want 4 — a guided recipe (実測 A, 実測 E)", c.family, got)
		}
	}
}

// The op that reaches these families is always edit (Caps.Ops), but the builder itself is asked
// directly here — the same defence the missing-file tests below exercise.
func TestComfyWorkflowQwenImageEditRefusesWithoutAnImage(t *testing.T) {
	for _, c := range comfyQwenEditFamilies {
		p := comfyGoldenParams
		p.Op = OpEdit
		_, err := comfyBuildGraph(c.family, c.files, p)
		if err == nil {
			t.Fatalf("%s: an edit with no input image built a graph", c.family)
		}
		if !strings.Contains(err.Error(), "needs an input image") {
			t.Errorf("%s: err = %v, want it to say what is missing", c.family, err)
		}
	}
}

// Studio()'s sanity probe (comfyParams{Prompt: "x"}, Op == "") must keep succeeding on a row
// whose three files are all declared — the same check every other family passes without an
// image, because none of them requires one outside op=edit/inpaint either. Without this the
// family would silently never appear in the member-facing catalogue.
func TestComfyWorkflowQwenImageEditBuildsForTheStudioProbe(t *testing.T) {
	for _, c := range comfyQwenEditFamilies {
		if _, err := comfyBuildGraph(c.family, c.files, comfyParams{Prompt: "x"}); err != nil {
			t.Errorf("comfyBuildGraph(%s) with the Studio probe's params = %v, want success", c.family, err)
		}
	}
}

// Each family's own entry point names ITSELF in these refusals, which is what
// engine_catalog_test.go reads to learn the three parts a row of that family owes (and what a
// shared body could not say for either of them).
func TestComfyWorkflowQwenImageEditRefusesMissingFiles(t *testing.T) {
	cases := []struct {
		name  string
		files comfyFiles
	}{
		{"needs a diffusion model", comfyFiles{ClipL: "x", Vae: "y"}},
		{"needs a text encoder", comfyFiles{DiffusionModel: "x", Vae: "y"}},
		{"needs a vae", comfyFiles{DiffusionModel: "x", ClipL: "y"}},
	}
	for _, f := range comfyQwenEditFamilies {
		for _, c := range cases {
			t.Run(string(f.family)+"/"+c.name, func(t *testing.T) {
				_, err := comfyBuildGraph(f.family, c.files, comfyGoldenParams)
				if err == nil {
					t.Fatal("expected an error, got none")
				}
				if !strings.Contains(err.Error(), string(f.family)) {
					t.Errorf("err = %v, want it to name %s", err, f.family)
				}
			})
		}
	}
}

// Decision 2's whole reason: at denoise 1 the family follows the instruction (実測 A), and at
// 0.6 — the CURRENT op=edit default every other family uses — it comes back unedited (実測 C).
// So unlike every family in comfyFamilyFixtures, a caller's strength must never move this
// family's denoise off 1 at all.
func TestComfyWorkflowQwenImageEditDenoiseIsAlwaysOne(t *testing.T) {
	for _, c := range comfyQwenEditFamilies {
		for _, s := range []float64{0.1, 0.6, 1} {
			strength := s
			p := comfyGoldenParams
			p.Op, p.Images, p.Strength = OpEdit, []string{"af-photo.png"}, &strength
			g, err := comfyBuildGraph(c.family, c.files, p)
			if err != nil {
				t.Fatalf("%s strength=%v: %v", c.family, s, err)
			}
			if got := g["ks"].Inputs["denoise"]; got != 1 {
				t.Errorf("%s strength=%v: ks.denoise = %v, want 1 regardless", c.family, s, got)
			}
		}
	}
}

// The negative branch is a real encode (not ConditioningZeroOut), because the family samples at
// cfg 4 — a guided recipe (実測 A) — where a zeroed conditioning would not cancel the way it does
// on the distilled families' cfg 1.
func TestComfyWorkflowQwenImageEditEncodesARealNegative(t *testing.T) {
	for _, c := range comfyQwenEditFamilies {
		p := comfyGoldenParams
		p.Op, p.Images, p.Negative = OpEdit, []string{"af-photo.png"}, "blurry"
		g, err := comfyBuildGraph(c.family, c.files, p)
		if err != nil {
			t.Fatal(err)
		}
		neg, ok := g["neg"]
		if !ok || neg.ClassType != "TextEncodeQwenImageEditPlus" {
			t.Fatalf("%s: no TextEncodeQwenImageEditPlus negative node: %+v", c.family, g["neg"])
		}
		if neg.Inputs["prompt"] != "blurry" {
			t.Errorf("%s: neg.prompt = %v, want the composed negative", c.family, neg.Inputs["prompt"])
		}
	}
}

// 🔴 Unlike every other family, this one does NOT fall back to comfyNegativePrompt ("blurry,
// lowres, deformed, watermark, text") when nobody declares anything — that default was measured
// for the megapixel/SDXL-era families, the official 2509 template's own negative widget ships
// empty, and 実測 A's own edit replaced a sign's TEXT, which the default would fight.
func TestComfyWorkflowQwenImageEditNegativeIsEmptyWhenNobodyDeclaresOne(t *testing.T) {
	for _, c := range comfyQwenEditFamilies {
		p := comfyGoldenParams
		p.Op, p.Images = OpEdit, []string{"af-photo.png"}
		g, err := comfyBuildGraph(c.family, c.files, p)
		if err != nil {
			t.Fatal(err)
		}
		if got := g["neg"].Inputs["prompt"]; got != "" {
			t.Errorf("%s: neg.prompt = %q, want empty — the fixed SDXL-era default must not apply here", c.family, got)
		}
	}
}

// LoRAs chain the same way every other split family's do: patched before ModelSamplingAuraFlow,
// and read by CFGNorm and both TextEncodeQwenImageEditPlus encodes downstream of it.
func TestComfyWorkflowQwenImageEditChainsLoras(t *testing.T) {
	for _, c := range comfyQwenEditFamilies {
		p := comfyGoldenParams
		p.Op, p.Images = OpEdit, []string{"af-photo.png"}
		p.Loras = []comfyLora{{Name: "watercolor-v2.safetensors", Weight: 0.8}}
		g, err := comfyBuildGraph(c.family, c.files, p)
		if err != nil {
			t.Fatal(err)
		}
		if got := comfyLinkAt(t, g, "ms.model"); got[0] != "lora1" || got[1] != 0 {
			t.Errorf("%s: ms.model reads %v, want the patched MODEL [lora1 0]", c.family, got)
		}
		for _, ref := range []string{"pos.clip", "neg.clip"} {
			if got := comfyLinkAt(t, g, ref); got[0] != "lora1" || got[1] != 1 {
				t.Errorf("%s: %s reads %v, want the patched CLIP [lora1 1]", c.family, ref, got)
			}
		}
	}
}

// A family wired to the shared instruction-edit builder without a comfyQwenEditWirings row is
// refused, not silently sampled at shift 0 with no reference-method node — the zero value is a
// topology nobody has run, and it would come back as a worse picture rather than as an error.
func TestComfyWorkflowQwenImageEditRefusesAnUnwiredFamily(t *testing.T) {
	p := comfyGoldenParams
	p.Op, p.Images = OpEdit, []string{"af-photo.png"}
	_, err := comfyGraphQwenImageEdit(comfyQwenEditFiles, p, comfyFamily("qwen-image-edit-2599"))
	if err == nil {
		t.Fatal("a family with no wiring built a graph")
	}
	// The control: the two that ARE wired still build.
	for _, c := range comfyQwenEditFamilies {
		if _, err := comfyGraphQwenImageEdit(c.files, p, c.family); err != nil {
			t.Errorf("%s: %v", c.family, err)
		}
	}
}

// --- Qwen-Image 2.1 (ADR 0098) -----------------------------------------------------------------
//
// One template, two ops, and the tests below are written around the single thing that differs
// between the two published graphs — where KSampler's latent comes from. Everything else this
// family can get wrong (the VAE, the encoder's type, the reference inputs' names) fails without an
// error on real hardware, so each of those is pinned by name rather than by the goldens alone.

var comfyQwen21Files = comfyFiles{
	DiffusionModel: "qwen_image_2.1_int8_convrot.safetensors",
	ClipL:          "qwen3vl_8b_int8_convrot.safetensors",
	Vae:            "qwen_image_2.1_vae_bf16.safetensors",
}

// Two fixtures rather than one, because this is the first family whose graph is not the same
// document for `op=generate` and `op=edit`.
func TestComfyWorkflowQwenImage21MatchesGoldenFixtures(t *testing.T) {
	cases := []struct {
		name string
		mut  func(p *comfyParams)
	}{
		{"t2i", func(p *comfyParams) {}},
		{"edit", func(p *comfyParams) { p.Op, p.Images = OpEdit, []string{"af-photo.png"} }},
		// The one request that keeps the decode's alpha: no SplitImageWithAlpha before the save.
		{"t2i-transparent", func(p *comfyParams) { p.Transparent = true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := comfyGoldenParams
			c.mut(&p)
			g, err := comfyBuildGraph(ComfyFamilyQwenImage21, comfyQwen21Files, p)
			if err != nil {
				t.Fatalf("comfyBuildGraph(qwen-image-2.1, %s) = %v", c.name, err)
			}
			got, err := json.MarshalIndent(g, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "comfy_qwen-image-2.1-"+c.name+".golden.json")
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			if string(got) != string(want) {
				t.Errorf("qwen-image-2.1 %s's graph no longer matches %s.\nGot:\n%s\nIf this change is"+
					" intended, overwrite the fixture and explain why in the commit.", c.name, path, got)
			}
		})
	}
}

// The one difference between the two published templates, and the reason it is not cosmetic.
//
// Generating has to start from EmptyLatentImage at the caller's size: the encode node's own latent
// output is a bare `resolution` square (nodes_qwen.py builds `[1, 64, h // 16, w // 16]` from the
// first reference, falling back to resolution when there is none), so wiring it for text-to-image
// would answer every request at 1024² whatever size was asked for — a picture, not an error.
//
// Editing has to start from the encode node's latent: that is how "the output follows image_1"
// is expressed, and an EmptyLatentImage there would put the edit on a canvas the conditioning was
// not built for (the template's own note: "Keep it close to the resized image_1 size, or the edit
// can shift").
func TestComfyWorkflowQwenImage21TakesItsLatentFromTheOpsOwnSource(t *testing.T) {
	gen := comfyGoldenParams
	gen.Width, gen.Height = 1216, 832
	g, err := comfyBuildGraph(ComfyFamilyQwenImage21, comfyQwen21Files, gen)
	if err != nil {
		t.Fatal(err)
	}
	if got := comfyLinkAt(t, g, "ks.latent_image"); got[0] != "lat" {
		t.Errorf("generate reads %v, want the EmptyLatentImage: the encode's latent ignores the size", got)
	}
	lat, ok := g["lat"]
	if !ok || lat.ClassType != "EmptyLatentImage" {
		t.Fatalf("lat = %+v, want an EmptyLatentImage", g["lat"])
	}
	if lat.Inputs["width"] != 1216 || lat.Inputs["height"] != 832 {
		t.Errorf("lat = %v x %v, want the request's own 1216x832", lat.Inputs["width"], lat.Inputs["height"])
	}

	edit := comfyGoldenParams
	edit.Op, edit.Images = OpEdit, []string{"af-photo.png"}
	g, err = comfyBuildGraph(ComfyFamilyQwenImage21, comfyQwen21Files, edit)
	if err != nil {
		t.Fatal(err)
	}
	if got := comfyLinkAt(t, g, "ks.latent_image"); got[0] != "enc" || got[1] != 2 {
		t.Errorf("edit reads %v, want the encode node's own latent output [enc 2]", got)
	}
	if _, ok := g["lat"]; ok {
		t.Error("an edit still built an EmptyLatentImage: the canvas would stop following image_1")
	}
}

// Ten references, named the way the API names them: `images.image_1`, the Autogrow group and its
// member joined by a dot. 🔴 This test used to assert the bare `image_1` — the label the editor
// shows — and so pinned the bug instead of catching it: /prompt accepts the unknown key, and the
// node dies at run time with "unexpected keyword argument 'image_1'" (measured on the dev
// deployment, 2026-09-23). The edit families next door take `image1` with no underscore and no
// group; the two nodes are different nodes.
func TestComfyWorkflowQwenImage21WiresEveryReferenceOntoTheOneEncode(t *testing.T) {
	p := comfyGoldenParams
	p.Op = OpEdit
	for i := 0; i < comfyFamilyMaxInputs(ComfyFamilyQwenImage21); i++ {
		p.Images = append(p.Images, fmt.Sprintf("af-ref%d.png", i+1))
	}
	if len(p.Images) != 10 {
		t.Fatalf("the family declares %d references, and this test was written for 10", len(p.Images))
	}
	g, err := comfyBuildGraph(ComfyFamilyQwenImage21, comfyQwen21Files, p)
	if err != nil {
		t.Fatal(err)
	}
	enc, ok := g["enc"]
	if !ok || enc.ClassType != "TextEncodeQwenImage21" {
		t.Fatalf("enc = %+v, want a TextEncodeQwenImage21", g["enc"])
	}
	for i := range p.Images {
		node := fmt.Sprintf("img%d", i+1)
		if n, ok := g[node]; !ok || n.ClassType != "LoadImage" || n.Inputs["image"] != p.Images[i] {
			t.Fatalf("%s = %+v, want a LoadImage of %s", node, g[node], p.Images[i])
		}
		if got := comfyLinkAt(t, g, fmt.Sprintf("enc.images.image_%d", i+1)); got[0] != node {
			t.Errorf("enc.images.image_%d reads %v, want %s", i+1, got, node)
		}
	}
	// The spelling that failed on real hardware must not come back under any number.
	for key := range enc.Inputs {
		if strings.HasPrefix(key, "image") && !strings.HasPrefix(key, "images.") {
			t.Errorf("enc has input %q: the node takes its references only as `images.image_N`", key)
		}
	}
	// No FluxKontextImageScale anywhere: this node does its own resizing from `resolution`, and
	// an extra scale would fix every reference into the first one's frame.
	for id, n := range g {
		if n.ClassType == "FluxKontextImageScale" {
			t.Errorf("%s is a FluxKontextImageScale: this family resizes inside the encode", id)
		}
	}
	if enc.Inputs["resolution"] != comfyQwen21Resolution {
		t.Errorf("resolution = %v, want %d", enc.Inputs["resolution"], comfyQwen21Resolution)
	}
}

// Both conditionings come out of ONE node, which is what makes a negative reach the sampler at all
// here — and the wrong slot is the failure that produces a picture rather than an error: positive
// and negative swapped samples away from the prompt.
func TestComfyWorkflowQwenImage21ReadsBothConditioningsFromTheOneEncode(t *testing.T) {
	p := comfyGoldenParams
	p.Negative = "watermark"
	g, err := comfyBuildGraph(ComfyFamilyQwenImage21, comfyQwen21Files, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := comfyLinkAt(t, g, "ks.positive"); got[0] != "enc" || got[1] != 0 {
		t.Errorf("ks.positive reads %v, want the encode's first output", got)
	}
	if got := comfyLinkAt(t, g, "ks.negative"); got[0] != "enc" || got[1] != 1 {
		t.Errorf("ks.negative reads %v, want the encode's second output", got)
	}
	enc := g["enc"]
	if enc.Inputs["prompt"] != p.Prompt || enc.Inputs["negative_prompt"] != "watermark" {
		t.Errorf("enc prompts = %+v, want the request's own two", enc.Inputs)
	}
	// 🔴 The caller's negative verbatim, never comfyNegativeText's SDXL-era fallback: both
	// published templates ship the negative widget empty, and nobody has measured this family
	// against "blurry, lowres, deformed, watermark, text".
	p.Negative = ""
	g, err = comfyBuildGraph(ComfyFamilyQwenImage21, comfyQwen21Files, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := g["enc"].Inputs["negative_prompt"]; got != "" {
		t.Errorf("negative_prompt = %q, want empty — the fallback belongs to the SDXL-era families", got)
	}
}

// The loaders, pinned by the two values that fail silently. `type: "qwen_image"` is READ
// (comfy/sd.py reaches this family's encoder only through CLIPType.QWEN_IMAGE together with a
// state dict detected as Qwen3-VL-8B), and the denoise is 1 for both ops by construction.
func TestComfyWorkflowQwenImage21LoadersAndDenoise(t *testing.T) {
	for _, op := range []Op{OpGenerate, OpEdit} {
		p := comfyGoldenParams
		if op == OpEdit {
			p.Op, p.Images = OpEdit, []string{"af-photo.png"}
		}
		g, err := comfyBuildGraph(ComfyFamilyQwenImage21, comfyQwen21Files, p)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		if got := g["clip"].Inputs["type"]; got != "qwen_image" {
			t.Errorf("%s: clip.type = %v, want qwen_image — the default loads a different encoder"+
				" and returns a picture anyway", op, got)
		}
		if got := g["vae"].Inputs["vae_name"]; got != comfyQwen21Files.Vae {
			t.Errorf("%s: vae = %v, want the row's own 64-channel file", op, got)
		}
		if got := g["ks"].Inputs["denoise"]; got != 1 {
			t.Errorf("%s: denoise = %v, want 1 — both published templates sample at 1", op, got)
		}
	}
}

// The recipe is the published KSampler's, and the two templates agree on it.
func TestComfyWorkflowQwenImage21RecipeIsTheTemplates(t *testing.T) {
	r := comfyFamilyRecipeFor(ComfyFamilyQwenImage21)
	if r.Steps != 25 || r.CFG != 1 || r.Sampler != "euler" || r.Scheduler != "simple" {
		t.Errorf("recipe = %+v, want the shipped templates' 25 / cfg 1 / euler / simple", r)
	}
	// cfg 1, so a row that does not raise it advertises no negative prompt — the family's
	// template still encodes one, which is the pair comfyModelTakesNegative puts together.
	if !comfyFamilyTakesNegative(ComfyFamilyQwenImage21) {
		t.Error("the template encodes a real negative branch, so the FAMILY takes one")
	}
}

// This family generates AND edits, and it is the first one that does both while fixing its denoise
// at 1 — so the three answers that used to move together no longer do.
func TestComfyWorkflowQwenImage21CapabilitiesSplitFromTheEditOnlyFamilies(t *testing.T) {
	if got := fmt.Sprint(comfyFamilyOps(ComfyFamilyQwenImage21)); got != fmt.Sprint([]Op{OpGenerate, OpEdit}) {
		t.Errorf("ops = %s, want generate and edit (inpaint is unclaimed: nobody has run it)", got)
	}
	if comfyFamilyStrength(ComfyFamilyQwenImage21) {
		t.Error("strength has nowhere to go: the denoise is 1 for both ops")
	}
	if comfyFamilyHasNoSizes(ComfyFamilyQwenImage21) {
		t.Error("the generate path fills an EmptyLatentImage from the caller's size, so sizes are real")
	}
	if n := comfyFamilyMaxInputs(ComfyFamilyQwenImage21); n != 10 {
		t.Errorf("max inputs = %d, want the 10 the official edit template wires", n)
	}
	// The control: the 2509/2511 pair is unmoved by all of it — they still cannot generate, and
	// they claim the inpaint 実測 G and I measured on their own builder (ADR 0094 decision 3).
	for _, f := range []comfyFamily{ComfyFamilyQwenImageEdit2509, ComfyFamilyQwenImageEdit2511} {
		if got := fmt.Sprint(comfyFamilyOps(f)); got != fmt.Sprint([]Op{OpEdit, OpInpaint}) {
			t.Errorf("%s ops = %s, want edit and inpaint, and never generate", f, got)
		}
		if !comfyFamilyHasNoSizes(f) {
			t.Errorf("%s: sizes are still decided by the input picture", f)
		}
	}
}

// The probe Studio() runs (comfyParams{Prompt: "x"}, Op == "") must keep building on a row whose
// files are declared, the same way it does for the edit families — and an `op=edit` with nothing
// attached must still be refused.
func TestComfyWorkflowQwenImage21BuildsForTheProbeAndRefusesAnEmptyEdit(t *testing.T) {
	if _, err := comfyBuildGraph(ComfyFamilyQwenImage21, comfyQwen21Files, comfyParams{Prompt: "x"}); err != nil {
		t.Errorf("the studio probe no longer builds: %v", err)
	}
	if _, err := comfyBuildGraph(ComfyFamilyQwenImage21, comfyQwen21Files, comfyParams{Prompt: "x", Op: OpEdit}); err == nil {
		t.Error("an edit with no picture built a graph")
	}
}

// Each declared file named in its own refusal, which is also what engine_catalog_test.go reads
// this template's body for.
func TestComfyWorkflowQwenImage21RefusesMissingFiles(t *testing.T) {
	for _, c := range []struct {
		name  string
		files comfyFiles
	}{
		{"diffusion model", comfyFiles{ClipL: "e.safetensors", Vae: "v.safetensors"}},
		{"text encoder", comfyFiles{DiffusionModel: "m.safetensors", Vae: "v.safetensors"}},
		{"vae", comfyFiles{DiffusionModel: "m.safetensors", ClipL: "e.safetensors"}},
	} {
		if _, err := comfyBuildGraph(ComfyFamilyQwenImage21, c.files, comfyGoldenParams); err == nil {
			t.Errorf("a row with no %s built a graph", c.name)
		}
	}
}

// The implication the two fields owe each other (ADR 0098): editing through
// comfyGraphQwenImageEdit is editing at a fixed denoise 1, so a row carrying the wiring must also
// carry FixedDenoiseEdit. The reverse is deliberately NOT true — qwen-image-2.1 edits that way
// through a builder of its own — which is exactly why they are two fields, and why the one-way
// direction is worth pinning: a third instruction-edit topology added to the wiring and forgotten
// here would be handed a `strength` its sampler cannot spend, and 実測 C is what that returns (the
// same graph at denoise 0.6, unedited, with no error anywhere).
func TestInstructionEditWiringImpliesAFixedDenoise(t *testing.T) {
	var wired int
	for _, r := range comfyFamilyRows {
		if r.InstructionEdit == nil {
			continue
		}
		wired++
		if !r.FixedDenoiseEdit {
			t.Errorf("%s carries the edit wiring but not FixedDenoiseEdit: strength would reach a"+
				" sampler that samples at 1 regardless", r.Family)
		}
	}
	if wired == 0 {
		t.Fatal("no row carries the edit wiring, so this check measures nothing")
	}
	// The other direction, as the control: at least one family is a fixed-denoise edit WITHOUT the
	// wiring, or the two fields could be collapsed again and this test would not notice.
	var unwired int
	for _, r := range comfyFamilyRows {
		if r.FixedDenoiseEdit && r.InstructionEdit == nil {
			unwired++
		}
	}
	if unwired == 0 {
		t.Error("every fixed-denoise family also carries the wiring — the two fields are the same" +
			" question again, and comfyFamilyInstructionEdit could go back to reading the pointer")
	}
}

// Ops and "can a size reach this family" are one declaration, not two (ADR 0098). The derivation
// is the statement: a size only ever reaches an EmptyLatentImage, which only a generate path
// builds.
func TestFamilySizesFollowTheOpList(t *testing.T) {
	for _, family := range comfyFamilies {
		generates := false
		for _, op := range comfyFamilyOps(family) {
			if op == OpGenerate {
				generates = true
			}
		}
		if got := comfyFamilyHasNoSizes(family); got == generates {
			t.Errorf("%s: ops=%v but hasNoSizes=%v", family, comfyFamilyOps(family), got)
		}
	}
	// Named, so the table above cannot quietly become all-true or all-false.
	if !comfyFamilyHasNoSizes(ComfyFamilyQwenImageEdit2511) {
		t.Error("an edit-only family must still refuse sizes (ADR 0094 decision 4)")
	}
	if comfyFamilyHasNoSizes(ComfyFamilyQwenImage21) || comfyFamilyHasNoSizes(ComfyFamilySDXL) {
		t.Error("a family with a generate path reads sizes")
	}
}
