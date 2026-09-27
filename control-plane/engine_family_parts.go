package main

// engine_family_parts.go — the files a SPLIT family needs beside its diffusion model (ADR 0072
// decision 2, follow-up to the VAE remedy next door). The press that takes them in is
// engine_complete.go's: this is the table it and the plan (engine_plan.go) both read.
//
// engine_vae.go answers "this checkpoint carries no VAE" for the two single-file families. This
// is the other half of the same fault, and it is the bigger one: klein, Z-Image, Anima and Krea 2
// are not published as a checkpoint at all. They are a diffusion model, a text encoder and a VAE,
// and taking the diffusion model in leaves a row that is marked `files_missing`, cannot be
// enabled, and gives an operator no way forward from the screen they are on — the parts live in
// another repository, sometimes on another SOURCE (a Civitai merge whose encoder is on Hugging
// Face), so finding them meant knowing a repository name and searching for it by hand.
//
// 🔴 Every entry here was measured against the live APIs before it was written — anima and krea2
// on 2026-09-15, the two qwen-image-edit families on 2026-09-20, qwen-image-2.1 on 2026-09-21,
// flux1 on 2026-09-27:
// the repository, the path inside it, and that it is ungated. A wrong path here is a second
// download that 404s minutes after somebody pressed a button, which is exactly the shape of
// failure the ingest form exists to move earlier.
//
// ⚠️ The table is deliberately NOT complete. flux2-klein, sd35 and zimage are split too and
// have no entry yet, because nobody has measured their parts the way these were — and an entry
// written from memory would be that 404. A family with no entry behaves exactly as before: the
// row is marked and the parts are attached by hand.

import (
	"context"
	"log"
	"strings"
)

// engineFamilyPart is one file a family's template reads, as the ingest would declare it.
type engineFamilyPart struct {
	// Flag is the role this file plays — the same vocabulary engineComfyRequiredFlags is written
	// in, which is what makes "the parts this row is missing" a set subtraction.
	Flag string
	Repo string
	File string
	// S3Key is where it lands, and it is SHARED on purpose: two families whose part is the same
	// file point at the same key, so the second one finds it staged and downloads nothing.
	S3Key string
}

// 🔴 Anima and Krea 2 both read the Qwen-Image VAE, and it is the same file — sha256
// a70580f0… in circlestone-labs/Anima, Comfy-Org/Krea-2 and Comfy-Org/Qwen-Image_ComfyUI
// (measured). They are declared from ONE repository rather than each from its own, because the
// reuse check compares the artifact identity (`hf:<repo>@<rev>/<path>#sha256:…`) and not the
// hash: the same bytes from two repositories are two identities, two downloads and two keys.
var engineFamilyParts = map[string][]engineFamilyPart{
	"anima": {
		{Flag: "--clip_l", Repo: "circlestone-labs/Anima",
			File:  "split_files/text_encoders/qwen_3_06b_base.safetensors",
			S3Key: "image/text_encoders/qwen_3_06b_base.safetensors"},
		{Flag: "--vae", Repo: "circlestone-labs/Anima",
			File:  "split_files/vae/qwen_image_vae.safetensors",
			S3Key: "image/vae/qwen_image_vae.safetensors"},
	},
	"krea2": {
		// The fp8 encoder rather than the bf16 one: 5.24 GB against 8.88 GB, and this family's
		// weights are already 13.1 GB on a card the deployment has to fit both on. 🔴 The bf16
		// build is the one a reference-image path needs (the fp8 conversion breaks the vision
		// tower), which is a declaration to change by hand the day an edit op reads a reference.
		{Flag: "--clip_l", Repo: "Comfy-Org/Krea-2",
			File:  "text_encoders/qwen3vl_4b_fp8_scaled.safetensors",
			S3Key: "image/text_encoders/qwen3vl_4b_fp8_scaled.safetensors"},
		{Flag: "--vae", Repo: "circlestone-labs/Anima",
			File:  "split_files/vae/qwen_image_vae.safetensors",
			S3Key: "image/vae/qwen_image_vae.safetensors"},
	},
	// The two instruction-edit families (ADR 0094 decision 7). They declare the SAME two parts as
	// each other — the topology that makes them separate families is in the graph, not in the
	// files — and the VAE is the same file anima and krea2 already point at, from the same
	// repository as those two for the identity reason above.
	//
	// 🔴 The fp8 SCALED text encoder, and krea2's note next door does not apply: "the fp8
	// conversion breaks the vision tower" was written about a family whose reference-image path
	// nobody had run. Here the official template names this exact file, and 実測 A and D (ADR
	// 0094) both read a reference picture through it.
	"qwen-image-edit-2509": {
		{Flag: "--clip_l", Repo: "Comfy-Org/Qwen-Image_ComfyUI",
			File:  "split_files/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors",
			S3Key: "image/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors"},
		{Flag: "--vae", Repo: "circlestone-labs/Anima",
			File:  "split_files/vae/qwen_image_vae.safetensors",
			S3Key: "image/vae/qwen_image_vae.safetensors"},
	},
	"qwen-image-edit-2511": {
		{Flag: "--clip_l", Repo: "Comfy-Org/Qwen-Image_ComfyUI",
			File:  "split_files/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors",
			S3Key: "image/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors"},
		{Flag: "--vae", Repo: "circlestone-labs/Anima",
			File:  "split_files/vae/qwen_image_vae.safetensors",
			S3Key: "image/vae/qwen_image_vae.safetensors"},
	},
	// qwen-image-2.1 (ADR 0098) shares NOTHING with the four entries above, and the keys say so.
	//
	// 🔴 Not the Qwen-Image VAE. This family's autoencoder is 64-channel at a spatial downscale of
	// 16 (comfy/latent_formats.py's QwenImage21, measured on ComfyUI v0.37.0); the file every
	// family above points at is the 16-channel one at a downscale of 8. They load through the same
	// VAELoader and the same `--vae` flag, so nothing refuses the swap — it decodes to noise.
	// Sharing the key would also be worse than a wrong path: the reuse check would find the wrong
	// bytes already staged and download nothing.
	//
	// 🔴 The repository is Comfy-Org's mirror and not `Qwen/Qwen-Image-2.1`. Both are ungated
	// (measured 2026-09-21, anonymous: `gated: false`), but the publisher's own repo ships the
	// diffusers layout, and what a ComfyUI loader reads is the single-file split the mirror
	// publishes — the same division of labour as Comfy-Org/Qwen-Image_ComfyUI above.
	//
	// The int8 builds rather than the bf16 ones, because that is what the official template names
	// in both of its versions (image_qwen_image_2_1_t2i.json and …_image_edit.json: the CLIPLoader
	// widget reads `qwen3vl_8b_int8_convrot.safetensors`). The bf16 encoder is 16.33 GiB against
	// 8.71 — on a card that also has to hold 6.76 GiB of weights, the citable choice and the one
	// that fits are the same choice.
	//
	// ⚠️ `text_encoders/qwen3.5_9b_qwen_image_2.1_pe_{t2i,i2i}.int8_convrot.safetensors` also live
	// in that repository and are NOT this. They are the prompt-enhancer models, absent from the
	// template's own model list; declared as `--clip_l` they would load, encode and sample, which
	// is this file's usual failure mode.
	"qwen-image-2.1": {
		{Flag: "--clip_l", Repo: "Comfy-Org/Qwen-Image-2.1",
			File:  "text_encoders/qwen3vl_8b_int8_convrot.safetensors",
			S3Key: "image/text_encoders/qwen3vl_8b_int8_convrot.safetensors"},
		{Flag: "--vae", Repo: "Comfy-Org/Qwen-Image-2.1",
			File:  "vae/qwen_image_2.1_vae_bf16.safetensors",
			S3Key: "image/vae/qwen_image_2.1_vae_bf16.safetensors"},
	},
	// flux1: two text encoders and a VAE, read by DualCLIPLoader (type flux) and VAELoader.
	// Measured 2026-09-27 through /api/models/<repo>?blobs=true, anonymous, both repositories
	// `gated: false`:
	//
	//	clip_l.safetensors            246,144,152 B  sha256 660c6f5b…
	//	t5xxl_fp8_e4m3fn.safetensors  4,893,934,904 B sha256 7d330da4…
	//	flux-vae-bf16.safetensors     167,664,710 B  sha256 0c0c8ac4…
	//
	// 🔴 `t5xxl_fp8_e4m3fn_scaled.safetensors`, in the same repository, is NOT a substitute: it
	// carries per-tensor scales, a different format under the same flag.
	//
	// The VAE is Kijai's bf16 copy and not black-forest-labs/FLUX.1-schnell's `ae.safetensors`.
	// Both hold the same weights (schnell's in fp32, apache-2.0), but schnell is `gated: auto`:
	// fetching it needs a Hugging Face token whose account accepted the terms, and a part this
	// table names has to be fetchable by every deployment, tokenless ones included. The copy's
	// header was read by range request: the original `ae` layout (`decoder.conv_in.*`, 244 BF16
	// tensors) that VAELoader reads, not the diffusers one. Its repository states licence `other`
	// because it also hosts the non-commercial FLUX.1-dev weights, so that is what the ingest
	// records as accepted; the VAE itself is the apache-2.0 schnell release.
	//
	// The two encoders are the same bytes SD3.5 reads (sha256 identical in
	// stabilityai/stable-diffusion-3.5-large and Comfy-Org/stable-diffusion-3.5-fp8, measured the
	// same day). An sd35 entry should declare them from this repository and at these keys: the
	// plan's reuse compares the artifact identity, so the same bytes from stabilityai would be a
	// second identity at a key this one already holds, and the plan would skip the part.
	//
	// With an entry, completing a row no longer offers what sits in the role's directory: a
	// hand-staged `ae.safetensors` or fp16 T5 is passed over and these files are downloaded, on
	// purpose, because the directory is where the Qwen encoders of other families live too.
	//
	// ⚠️ Its key is NOT `image/vae/ae.safetensors`, the name hand-staged FLUX VAEs usually carry:
	// the planner declares whatever already sits at a part's key without comparing the bytes, so a
	// key named after this file keeps an unrelated `ae.safetensors` from being taken for it.
	"flux1": {
		{Flag: "--clip_l", Repo: "comfyanonymous/flux_text_encoders",
			File:  "clip_l.safetensors",
			S3Key: "image/text_encoders/clip_l.safetensors"},
		{Flag: "--t5xxl", Repo: "comfyanonymous/flux_text_encoders",
			File:  "t5xxl_fp8_e4m3fn.safetensors",
			S3Key: "image/text_encoders/t5xxl_fp8_e4m3fn.safetensors"},
		{Flag: "--vae", Repo: "Kijai/flux-fp8",
			File:  "flux-vae-bf16.safetensors",
			S3Key: "image/vae/flux-vae-bf16.safetensors"},
	},
}

// engineFamilyPartsFor is the parts a family declares, or nothing.
func engineFamilyPartsFor(family string) []engineFamilyPart {
	return engineFamilyParts[strings.TrimSpace(family)]
}

// enginePartsHeld is what the deployment ALREADY has, for a plan that would rather declare than
// download. Two independent things, because they answer different halves of "is it here":
//
//	known    — every S3 key this deployment has a record of, from the catalogue AND from finished
//	           ingest jobs (engineKnownArtifacts), with the upstream identity it was verified
//	           against before upload;
//	storage  — HeadObject, which is the only thing that proves bytes occupy that key NOW.
//
// 🔴 Neither substitutes for the other: a record without an object is a key whose bytes were
// purged, and an object without a record is bytes nobody can say the provenance of.
type enginePartsHeld struct {
	known   map[string]*engineKnownArtifact
	storage *engineStorage
}

// enginePartsHeld gathers the two records a plan reuses bytes from. It is on the admin API
// because both of its sources are: the catalogue and the job history are read through the same
// grant the caller already holds, and the storage checker lives on the ingester.
func (a engineAdminAPI) enginePartsHeld(ctx context.Context, g engineIngestGrant, role string) enginePartsHeld {
	held := enginePartsHeld{}
	models, jobs, err := a.engineStorageRows(ctx, g, role)
	if err != nil {
		// Not fatal: without the job history a part is simply downloaded again, which is the
		// answer this deployment gave before it could look. Nothing is attached on a guess.
		log.Printf("engines: parts plan could not read %s's storage rows (%v): reuse is skipped", role, err)
		return held
	}
	held.known = engineKnownArtifacts(models, jobs)
	if ing := a.reg.ingester(); ing != nil {
		held.storage = ing.storageChecker()
	}
	return held
}
