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
// 🔴 Every entry here was measured against the live APIs on 2026-09-15: the repository, the path
// inside it, and that it is ungated. A wrong path here is a second download that 404s minutes
// after somebody pressed a button, which is exactly the shape of failure the ingest form exists
// to move earlier.
//
// ⚠️ The table is deliberately NOT complete. flux1, flux2-klein, sd35 and zimage are split too and
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
