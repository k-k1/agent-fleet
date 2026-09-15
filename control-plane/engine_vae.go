package main

// engine_vae.go — the family VAE a checkpoint that carries none has to borrow (ADR 0072
// follow-up, VAE detection).
//
// engine_safetensors.go answers "does this file bundle a VAE". This file is what the deployment
// then DOES about a "no": mark the row and refuse to switch it on. The remedy is 揃える
// (engine_complete.go), which folds the family's own VAE into the gap it closes and attaches it
// under `--vae` — the declaration comfyCheckpointVAE reads
// (workspace/agent/internal/imagegen/comfy_workflows.go).
//
// 🔴 The verdict is read ONCE, at the press that takes the checkpoint in (engineVaeOfIngest,
// through the plan). There is no second read: the header scan and the per-row re-read left with
// their routes (ADR 0085 P3), so a row whose bytes were registered from the bucket rather than
// taken in has no verdict at all and is marked `vae_unread` until it does.
//
// 🔴 Only the single-checkpoint families are asked about. flux1, flux2-klein and zimage
// already REQUIRE a `--vae` file to be declared (engineComfyRequiredFlags), so a row of theirs
// that has none is already marked and refused by the files guard — a second mark saying the same
// thing is one too many. sd15, sdxl and sd35 are the ones whose template falls back to the
// checkpoint's own third output, and that is exactly the fallback that yields None.

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineFamilyVae is the file a family decodes with when its checkpoint brought none.
//
// One entry per family rather than a per-row field: which autoencoder an SDXL checkpoint expects
// is a property of the ARCHITECTURE — every SDXL fine-tune was trained against the same one, and
// the publishers that omit it omit it precisely because it is the stock file. The row-level
// answer already exists for anyone who disagrees: declare `--vae` by hand and this never runs.
type engineFamilyVae struct {
	Repo  string
	File  string
	S3Key string
}

// 🔴 sd35 is deliberately absent. Its checkpoint is published as MMDiT + VAE in one file and the
// stock autoencoder for it lives inside a GATED repository, so an automatic second download
// would fail with a 403 in a job reconciler where nobody is waiting to read it. A sd35 row that
// really has no VAE therefore gets the mark and the refusal, and its fix is the manual
// declaration the guide describes.
var engineFamilyVaes = map[string]engineFamilyVae{
	// 🔴 SD1.5's autoencoder is NOT SDXL's — the two are different architectures and swapping
	// them decodes to colour mush rather than failing. The stock file is Stability's own
	// ft-MSE fine-tune, which is ungated, so this family can be repaired automatically the way
	// sdxl is (the 403 that keeps sd35 out of this table does not apply).
	"sd15": {
		Repo:  "stabilityai/sd-vae-ft-mse-original",
		File:  "vae-ft-mse-840000-ema-pruned.safetensors",
		S3Key: "image/vae/vae-ft-mse-840000-ema-pruned.safetensors",
	},
	"sdxl": {
		Repo:  "stabilityai/sdxl-vae",
		File:  "sdxl_vae.safetensors",
		S3Key: "image/vae/sdxl_vae.safetensors",
	},
}

// engineVaeFamilies are the families whose template reads the checkpoint's own VAE, and so the
// only ones where "the checkpoint has none" is a fault at all.
var engineVaeFamilies = map[string]bool{"sd15": true, "sdxl": true, "sd35": true}

// engineVaeAsked says whether this row is one the question applies to: an image row that
// dispatches on a family, is not a LoRA, declares one of the families above, and does not
// already declare a `--vae` of its own.
func engineVaeAsked(provider string, m store.EngineModel) bool {
	if engineBaseModelsFor(provider) == nil || engineModelIsLora(m) {
		return false
	}
	if !engineVaeFamilies[strings.TrimSpace(m.BaseModel)] {
		return false
	}
	for _, f := range m.Files {
		if strings.TrimSpace(f.Flag) == "--vae" && strings.TrimSpace(f.S3Key) != "" {
			return false
		}
	}
	return true
}

// engineVaeCheckpoint is the row's own checkpoint file — the unflagged slot, which is the file
// whose header answers the question. Absent for a row that has none, which the files guard
// already refuses for its own reasons.
func engineVaeCheckpoint(m store.EngineModel) (store.EngineModelFile, bool) {
	for _, f := range m.Files {
		if strings.TrimSpace(f.Flag) == "" && strings.TrimSpace(f.S3Key) != "" {
			return f, true
		}
	}
	return store.EngineModelFile{}, false
}

// engineVaeMissing is the mark: this row's checkpoint was READ and carries no VAE, and the row
// declares none either.
//
// 🔴 It is false for a row nobody has read. "Not read" and "has none" are different facts, and
// a mark that folded them would put a red line on every row taken in before the header was ever
// looked at — which is most of them, and which would make the mark worth nothing.
func engineVaeMissing(provider string, m store.EngineModel) bool {
	if !engineVaeAsked(provider, m) {
		return false
	}
	f, ok := engineVaeCheckpoint(m)
	return ok && f.VaeBundled == engineVaeNo
}

// engineVaeUnread says the question applies to this row and nobody has answered it — what the
// panel's scan looks for.
func engineVaeUnread(provider string, m store.EngineModel) bool {
	if !engineVaeAsked(provider, m) {
		return false
	}
	f, ok := engineVaeCheckpoint(m)
	return ok && f.VaeBundled == engineVaeUnknown && engineSafetensorsName(f.S3Key)
}

// engineVaeGuard refuses to switch on a row whose checkpoint is known to carry no VAE.
//
// Like the files guard beside it and unlike the VRAM one, there is no confirm to repeat with.
// This is not a judgement under uncertainty: `generate_image` has no VAE argument, the template
// has no other source of one, and the failure is inside ComfyUI after the box has paid a 1-2.5
// minute checkpoint switch — for EVERY request, on every op, until the row declares a file
// (measured twice on this deployment).
func engineVaeGuard(ctx context.Context, e *engineRuntimeState, id string) *apiError {
	m, ok := engineCatalogModel(ctx, e, id)
	if !ok || !engineVaeMissing(e.def.Provider, m) {
		return nil
	}
	fix := "take a " + strings.TrimSpace(m.BaseModel) + " VAE in and attach it to this row under `--vae`"
	if v, ok := engineFamilyVaes[strings.TrimSpace(m.BaseModel)]; ok {
		fix = "the panel's own button takes " + v.Repo + "/" + v.File + " in and attaches it under `--vae`"
	}
	return &apiError{http.StatusConflict, errCodeEngineVaeMissing,
		m.ID + " is a " + strings.TrimSpace(m.BaseModel) + " checkpoint published with no VAE of its own," +
			" so nothing would encode or decode the picture: every request would fail inside the engine" +
			" after paying the checkpoint switch, whatever the prompt, size or operation. " + fix}
}

// engineVaeOfIngest is the verdict for the file a plan is about to take in, read from its header
// at the source before anything is downloaded.
//
// Asked of a WHOLE checkpoint only, and only for an engine that dispatches on a family: a part
// (`--vae`, `--clip_l`) is not a checkpoint, a LoRA is not one either, and for the llm role the
// question is meaningless. The caller decides that — the planner asks only when the family reads
// a whole checkpoint — and what is checked here is the two things the read itself needs: a
// safetensors destination and an upstream that answered with a URL. The family is deliberately
// NOT part of the condition: the form resolves before anybody has chosen one (the choice is
// offered FROM this answer), so gating on it would mean never reading the header on the one
// screen that can still act on it.
func engineVaeOfIngest(ctx context.Context, provider, kind, key string,
	res engineResolved, tokens *engineHfTokens) string {
	if engineBaseModelsFor(provider) == nil {
		return engineVaeUnknown
	}
	if strings.EqualFold(strings.TrimSpace(kind), engineModelKindLora) {
		return engineVaeUnknown
	}
	if !engineSafetensorsName(key) || strings.TrimSpace(res.DownloadURL) == "" {
		return engineVaeUnknown
	}
	token := ""
	if res.Gated && tokens != nil {
		if t, aerr := tokens.plaintext(ctx); aerr == nil {
			token = t
		}
	}
	verdict, err := engineSafetensorsVae(ctx, res.DownloadURL, token)
	if err != nil {
		// Best-effort, exactly like the GGUF geometry read next door: a header that could not be
		// read leaves the answer unknown, and unknown never marks a row.
		log.Printf("engines: %s: the VAE question went unanswered (%v)", res.Source, err)
		return engineVaeUnknown
	}
	return verdict
}

// engineVaeFollowUp is the second file an ingest promised to take in: the family's VAE, attached
// to the row the first download creates.
//
// It rides in the job's spec rather than being worked out when the job finishes, because that
// happens in the reconciler — where there is no request, no operator and nobody to report a
// resolve failure to. Everything a second job needs is therefore decided while somebody is still
// looking at the screen.
type engineVaeFollowUp struct {
	// Flag is the role the file is attached under. Empty means `--vae`, which is what this type
	// carried before a SPLIT family's other parts (engine_family_parts.go) reused it — the zero
	// value therefore stays the VAE remedy's own meaning and no stored job changes shape.
	Flag  string
	S3Key string
	// Resolved is the download, for the case the bucket does not hold this file yet.
	Resolved engineResolved
	// Staged says the bytes are already here, declared by another row: then there is nothing to
	// download and the follow-up is one write.
	Staged                   bool
	Bytes                    int64
	Source, ArtifactIdentity string
	// Conflict is set when the destination key is ALREADY recorded by something this plan
	// cannot prove is the same file. It is not a download that failed — it is a download that
	// must not be offered: engineIngestDestinationUnused refuses one to a taken key, so the
	// press would end in "the S3 key … is already recorded" minutes after somebody accepted a
	// licence for it. Carried so the panel can say WHICH row or job is holding the key.
	Conflict string
}
