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
	cases := []struct {
		name   string
		family comfyFamily
		files  comfyFiles
	}{
		{"sdxl", ComfyFamilySDXL, comfyFiles{Checkpoint: "sd_xl_base_1.0.safetensors"}},
		{"zimage", ComfyFamilyZImage, comfyFiles{
			DiffusionModel: "z_image_turbo_bf16.safetensors", ClipL: "qwen_3_4b_fp8_mixed.safetensors", Vae: "ae.safetensors"}},
		{"flux2_klein", ComfyFamilyFlux2Klein, comfyFiles{
			DiffusionModel: "flux-2-klein-4b.safetensors", ClipL: "qwen_3_4b_fp8_mixed.safetensors", Vae: "flux2-vae.safetensors"}},
		{"flux1", ComfyFamilyFlux1, comfyFiles{
			DiffusionModel: "flux1-dev.safetensors", ClipL: "clip_l.safetensors", T5xxl: "t5xxl_fp8.safetensors", Vae: "ae.safetensors"}},
		{"sd35", ComfyFamilySD35, comfyFiles{Checkpoint: "sd3.5_large.safetensors",
			ClipL: "clip_l.safetensors", ClipG: "clip_g.safetensors", T5xxl: "t5xxl_fp16.safetensors"}},
	}
	for _, c := range cases {
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
