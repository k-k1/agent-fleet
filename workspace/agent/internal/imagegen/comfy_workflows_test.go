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
	"os"
	"path/filepath"
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
			p.Op, p.Image = OpEdit, "af-photo.png"
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
	p.Op, p.Image = OpEdit, "af-photo.png"
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
		q.Op, q.Image, q.Mask = op, "af-photo.png", "af-mask.png"
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

// inpaint is an edit plus a noise mask, in every family. The mask reaches the sampler through the
// LATENT rather than through the conditioning, which is what makes one shape work for KSampler
// and SamplerCustomAdvanced alike.
func TestComfyWorkflowsInpaintMasksTheLatent(t *testing.T) {
	for _, c := range comfyFamilyFixtures {
		t.Run(c.name, func(t *testing.T) {
			p := comfyGoldenParams
			p.Op, p.Image, p.Mask = OpInpaint, "af-photo.png", "af-mask.png"
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
			p.Op, p.Image = OpInpaint, "af-photo.png"
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
		p.Op, p.Image = OpEdit, "af-photo.png"

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
