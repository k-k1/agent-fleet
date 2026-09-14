package main

// engine_vae.go — the family VAE a checkpoint that carries none has to borrow (ADR 0072
// follow-up, VAE detection).
//
// engine_safetensors.go answers "does this file bundle a VAE". This file is what the deployment
// then DOES about a "no": mark the row, refuse to switch it on, and offer the one file that
// fixes it — taken in and attached under `--vae`, which is the declaration comfyCheckpointVAE
// reads (workspace/agent/internal/imagegen/comfy_workflows.go).
//
// 🔴 Only the two single-checkpoint families are asked about. flux1, flux2-klein and zimage
// already REQUIRE a `--vae` file to be declared (engineComfyRequiredFlags), so a row of theirs
// that has none is already marked and refused by the files guard — a second mark saying the same
// thing is one too many. sdxl and sd35 are the two whose template falls back to the checkpoint's
// own third output, and that is exactly the fallback that yields None.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
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
	"sdxl": {
		Repo:  "stabilityai/sdxl-vae",
		File:  "sdxl_vae.safetensors",
		S3Key: "image/vae/sdxl_vae.safetensors",
	},
}

// engineVaeFamilies are the families whose template reads the checkpoint's own VAE, and so the
// only ones where "the checkpoint has none" is a fault at all.
var engineVaeFamilies = map[string]bool{"sdxl": true, "sd35": true}

// engineVaeAsked says whether this row is one the question applies to: an image row that
// dispatches on a family, is not a LoRA, declares one of the two families above, and does not
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

// engineVaeSourceOf turns a recorded source back into something engineIngestResolve can read, so
// the header of a file ALREADY in the bucket can be re-read at its origin.
//
// 🔴 It reads the source, never S3. The CP's storage port exposes only object metadata, not a
// byte stream, so the copy this reads is the one still upstream. What that costs is honest and
// bounded: a source that has been taken down, or a Civitai asset whose uploader requires an
// account, leaves the verdict unknown — never "no".
func engineVaeSourceOf(source string) (engineIngestSource, bool) {
	s := strings.TrimSpace(source)
	switch {
	case strings.HasPrefix(s, "hf:"):
		parts := strings.SplitN(strings.TrimPrefix(s, "hf:"), "/", 3)
		if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return engineIngestSource{}, false
		}
		return engineIngestSource{HF: &engineIngestHF{Repo: parts[0] + "/" + parts[1], File: parts[2]}}, true
	case strings.HasPrefix(s, "civitai:"):
		id, err := strconv.Atoi(strings.TrimPrefix(s, "civitai:"))
		if err != nil || id <= 0 {
			return engineIngestSource{}, false
		}
		return engineIngestSource{Civitai: &engineIngestCivitai{VersionID: id}}, true
	case strings.HasPrefix(s, "https://"):
		// A plain-url source needs no resolve at all: the string IS the download. It is returned
		// without a sha256, which engineIngestResolve would refuse — so callers read the URL out
		// of the source directly. Reported here only so the caller knows there is one.
		return engineIngestSource{URL: s}, true
	}
	return engineIngestSource{}, false
}

// engineVaeRead answers the question for one file, over HTTP, using whatever source it records.
//
// Every failure comes back as an apiError with the SAME code, because they are one fact to the
// reader: nobody could look. Which of "no source recorded", "the repository is gone" and "the
// uploader requires an account" it was rides in the message.
func engineVaeRead(ctx context.Context, f store.EngineModelFile, tokens *engineHfTokens) (string, *apiError) {
	src, ok := engineVaeSourceOf(f.Source)
	if !ok {
		return engineVaeUnknown, errVaeNoSource
	}
	url := strings.TrimSpace(src.URL)
	token := ""
	if url == "" {
		res, aerr := engineIngestResolve(ctx, src)
		if aerr != nil {
			return engineVaeUnknown, aerr
		}
		url = res.DownloadURL
		// A gated repository answers metadata anonymously and refuses the file, which is the
		// one case the operator's token is here for (the same reason the GGUF header read uses
		// it). Without a token the verdict stays unknown rather than becoming "no".
		if res.Gated && tokens != nil {
			if t, aerr := tokens.plaintext(ctx); aerr == nil {
				token = t
			}
		}
	}
	if url == "" {
		return engineVaeUnknown, errVaeNoSource
	}
	verdict, err := engineSafetensorsVae(ctx, url, token)
	if err != nil {
		return engineVaeUnknown, &apiError{http.StatusBadGateway, errCodeEngineVaeUnreadable,
			"the file's header could not be read at " + f.Source + " (" + err.Error() + ")"}
	}
	return verdict, nil
}

// errVaeNoSource is the honest answer for a row nobody can re-read: a seeded row, or one
// registered by hand from a key already in the bucket. There is no origin recorded, the CP
// cannot open the bucket, and the verdict stays unknown.
var errVaeNoSource = &apiError{http.StatusBadRequest, errCodeEngineVaeUnreadable,
	"this row records no source to read the file's header from — it was seeded or registered by" +
		" hand, and the Control Plane cannot open the bucket. Declare `--vae` by hand if the" +
		" checkpoint needs one"}

// engineVaeStagedKey answers whether the family's VAE is already in the bucket, as the key some
// other row declares for it. A second download of a file this deployment already holds is 335 MB
// and several minutes for something an attach does instantly.
func engineVaeStagedKey(rows []store.EngineModel, v engineFamilyVae) (store.EngineModelFile, bool) {
	for _, m := range rows {
		for _, f := range m.Files {
			if strings.TrimSpace(f.S3Key) == v.S3Key {
				return f, true
			}
		}
	}
	return store.EngineModelFile{}, false
}

// engineVaeOfIngest is the verdict for the file an ingest form is looking at, read from its
// header at the source before anything is downloaded.
//
// Asked of a WHOLE checkpoint only, and only for an engine that dispatches on a family: a part
// (`--vae`, `--clip_l`) is not a checkpoint, a LoRA is not one either, and for the llm role the
// question is meaningless. The family is deliberately NOT part of the condition — the form
// resolves before anybody has chosen one (the choice is offered FROM this answer), so gating on
// it would mean never reading the header on the one screen that can still act on it.
func engineVaeOfIngest(ctx context.Context, provider string, b engineIngestBody,
	res engineResolved, tokens *engineHfTokens) string {
	if engineBaseModelsFor(provider) == nil || b.Attach || b.Replace {
		return engineVaeUnknown
	}
	if strings.TrimSpace(b.FileFlag) != "" || strings.EqualFold(strings.TrimSpace(b.Kind), engineModelKindLora) {
		return engineVaeUnknown
	}
	if !engineSafetensorsName(b.S3Key) || strings.TrimSpace(res.DownloadURL) == "" {
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

// engineVaeFamilyOf is the family a resolve should plan a VAE for: what the operator has already
// declared, and otherwise what this deployment would suggest from the upstream's own words.
func engineVaeFamilyOf(provider string, b engineIngestBody, res engineResolved) string {
	if fam := strings.TrimSpace(b.BaseModel); fam != "" {
		return fam
	}
	return engineFamilyGuess(provider, res.BaseModel)
}

// engineVaeFollowUp is the second file an ingest promised to take in: the family's VAE, attached
// to the row the first download creates.
//
// It rides in the job's spec rather than being worked out when the job finishes, because that
// happens in the reconciler — where there is no request, no operator and nobody to report a
// resolve failure to. Everything a second job needs is therefore decided while somebody is still
// looking at the screen.
type engineVaeFollowUp struct {
	S3Key string
	// Resolved is the download, for the case the bucket does not hold this file yet.
	Resolved engineResolved
	// Staged says the bytes are already here, declared by another row: then there is nothing to
	// download and the follow-up is one write.
	Staged                   bool
	Bytes                    int64
	Source, ArtifactIdentity string
}

// engineVaePlan works out what the deployment would do for a checkpoint that carries no VAE, and
// is shared by the ingest form (which shows it before the press) and by the press itself.
//
// Nil when there is nothing to offer: an unknown family, or one this deployment holds no default
// for. sd35 is the second case by design — see engineFamilyVaes.
func engineVaePlan(ctx context.Context, rows []store.EngineModel, family string) (*engineVaeFollowUp, engineFamilyVae, bool) {
	v, ok := engineFamilyVaes[strings.TrimSpace(family)]
	if !ok {
		return nil, engineFamilyVae{}, false
	}
	if staged, here := engineVaeStagedKey(rows, v); here {
		return &engineVaeFollowUp{
			S3Key: v.S3Key, Staged: true, Bytes: staged.Bytes,
			Source: staged.Source, ArtifactIdentity: staged.ArtifactIdentity,
		}, v, true
	}
	res, aerr := engineIngestResolve(ctx, engineIngestSource{HF: &engineIngestHF{Repo: v.Repo, File: v.File}})
	if aerr != nil {
		return nil, v, true
	}
	return &engineVaeFollowUp{S3Key: v.S3Key, Resolved: res}, v, true
}

// engineVaePlanRow is that plan as the ingest form reads it, so the licence somebody is about to
// accept and the size they are about to pay for are on screen beside the checkbox.
func engineVaePlanRow(plan *engineVaeFollowUp, v engineFamilyVae) map[string]any {
	row := map[string]any{"repo": v.Repo, "file": v.File, "s3Key": v.S3Key}
	switch {
	case plan == nil:
		// The table has an entry and the resolve failed. Said as "we know which file, we could
		// not reach it" rather than omitted, because the omission reads as "this family has no
		// answer", which is a different and more permanent thing.
		row["unreachable"] = true
	case plan.Staged:
		row["staged"] = true
		row["bytes"] = plan.Bytes
	default:
		row["license"] = engineLicenceLabel(plan.Resolved)
		row["bytes"] = plan.Resolved.Bytes
	}
	return row
}

// engineVaeScanMax bounds one scan. The reads are sequential and each is an HTTP request to
// somebody else's server, so a catalogue of a hundred rows must not turn one panel load into a
// hundred upstream calls — and the rows that are left keep their unread mark, which is what
// brings the next scan back to them.
const engineVaeScanMax = 12

// engineVaeScanFails is how many refusals end a scan. An upstream that is down, rate-limiting or
// unreachable from this deployment says so on the first request and on every one after it, and
// the rows left unread simply stay unread.
const engineVaeScanFails = 2

// scanVae (POST …/models/vae-scan) reads the headers of the checkpoints nobody has read yet and
// writes each verdict onto its file.
//
// It answers with what it did rather than with the catalogue: the panel re-reads the rows itself
// afterwards, and a second copy of a row's shape here would be a second thing to keep in step.
func (a engineAdminAPI) scanVae(w http.ResponseWriter, r *http.Request, g engineIngestGrant) {
	key := strings.TrimSpace(r.PathValue("key"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	// The catalogue of a borrowed engine is the far deployment's document, and so is the fact
	// this would write onto it.
	if a.refuseBorrowedWrite(w, e, "reading a checkpoint's header") {
		return
	}
	ctx := r.Context()
	results := []map[string]any{}
	left, failed := 0, 0
	for _, m := range e.catalog.list(ctx) {
		if !engineVaeUnread(e.def.Provider, m) {
			continue
		}
		// 🔴 Stop asking an upstream that is refusing. Measured on af-sandbox (2026-09-13):
		// Civitai answered 503, and every row whose source is a Civitai version would have been
		// three more requests to the host that had just said no — on a screen anybody can
		// reload. The rows that were not read keep their unread mark, which is what brings the
		// next scan back to them once the upstream is up.
		if len(results) >= engineVaeScanMax || failed >= engineVaeScanFails {
			left++
			continue
		}
		row := a.readVaeOnto(ctx, key, m)
		if _, bad := row["unreadable"]; bad {
			failed++
		}
		results = append(results, row)
	}
	if len(results) > 0 {
		e.catalog.invalidate()
	}
	out := map[string]any{"read": results}
	// Said out loud rather than left for the panel to infer from a count: an operator who sees
	// three marks appear has to know whether that was all of them.
	if left > 0 {
		out["left"] = left
	}
	writeJSON(w, http.StatusOK, out)
}

// readVaeOnto reads one row's checkpoint and records the verdict, answering the line the panel
// prints. A failed read is reported and NOT written: unknown is what the row already says, and
// writing it back would only move `updated_at`.
func (a engineAdminAPI) readVaeOnto(ctx context.Context, role string, m store.EngineModel) map[string]any {
	f, ok := engineVaeCheckpoint(m)
	if !ok {
		return map[string]any{"id": m.ID, "vae_bundled": engineVaeUnknown}
	}
	verdict, aerr := engineVaeRead(ctx, f, a.hfTokens())
	if aerr != nil {
		return map[string]any{"id": m.ID, "vae_bundled": engineVaeUnknown, "unreadable": aerr.message}
	}
	f.VaeBundled = verdict
	if _, err := a.mgr.store.ReplaceEngineModelFile(ctx, role, m.ID, f, nil); err != nil {
		return map[string]any{"id": m.ID, "vae_bundled": verdict, "unreadable": err.Error()}
	}
	return map[string]any{"id": m.ID, "vae_bundled": verdict}
}

// engineVaeFixBody is one press of the panel's VAE button.
type engineVaeFixBody struct {
	// Check asks for the verdict and the plan without taking anything in. It is what the panel
	// calls first, so the licence somebody is about to accept is on screen before the press that
	// accepts it — the same order the ingest form follows.
	Check bool `json:"check"`
	// LicenseAccepted is the human act. Absent for a fix that needs no download, because there
	// is nothing new to accept: the file is already in this deployment's bucket under a licence
	// somebody accepted when it was taken in.
	LicenseAccepted bool `json:"licenseAccepted"`
	// Force attaches the VAE even though the header could not be read. The escape exists for the
	// case the panel cannot see: a source that has been taken down, and an operator who has
	// watched the row fail in the engine and knows what it needs.
	Force bool `json:"force"`
}

// fixVae (POST …/models/{id}/vae) is the one press that ends the fault: read the header, and if
// the checkpoint really carries no VAE, give the row the family's own.
//
// Three outcomes and they are deliberately different words. `none` means the header says the
// checkpoint has one after all — the mark was a question, and this is its answer. `attached` is
// the cheap path: this deployment already holds the file, so the row gains a declaration and
// nothing is downloaded. `job_started` is the ingest, which finishes minutes later in the
// reconciler and attaches the file itself.
func (a engineAdminAPI) fixVae(w http.ResponseWriter, r *http.Request, g engineIngestGrant) {
	key, id := strings.TrimSpace(r.PathValue("key")), strings.TrimSpace(r.PathValue("id"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	if a.refuseBorrowedWrite(w, e, "giving a row a VAE") {
		return
	}
	var b engineVaeFixBody
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b)
	}
	ctx := r.Context()
	m, ok := engineCatalogModel(ctx, e, id)
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineModelUnknown, "no model " + id + " for engine " + key})
		return
	}
	family := strings.TrimSpace(m.BaseModel)
	if !engineVaeAsked(e.def.Provider, m) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody,
			id + " is not a row this question applies to: it either declares a `--vae` of its own already," +
				" is a LoRA, or its family does not decode with the checkpoint's own VAE"})
		return
	}
	// The verdict is taken FRESH on every press rather than read off the row: the row's copy can
	// be minutes or months old, and this is the call that is about to spend money on it.
	//
	// 🔴 But a re-read that FAILS must not take the fix down with it. Measured on af-sandbox
	// (2026-09-13): this row's source is `civitai:<id>`, Civitai answered 503, and the whole
	// remedy — which downloads from Hugging Face and never touches Civitai — was refused because
	// the DIAGNOSIS could not be repeated. What the deployment already knows is on the row, so a
	// recorded "no" stands when the upstream is unreachable, and the answer says the re-read
	// failed rather than pretending it happened.
	stored := engineVaeUnknown
	if f, has := engineVaeCheckpoint(m); has {
		stored = strings.TrimSpace(f.VaeBundled)
	}
	verdict := engineVaeUnknown
	unreadable := ""
	if f, has := engineVaeCheckpoint(m); has {
		v, aerr := engineVaeRead(ctx, f, a.hfTokens())
		if aerr != nil {
			unreadable = aerr.message
		} else {
			verdict = v
			f.VaeBundled = v
			if _, err := a.mgr.store.ReplaceEngineModelFile(ctx, key, id, f, nil); err == nil {
				e.catalog.invalidate()
			}
		}
	}
	// What this deployment KNOWS about the file: this read, or the one that marked the row.
	known := verdict
	if known == engineVaeUnknown {
		known = stored
	}
	if known == engineVaeYes {
		writeJSON(w, http.StatusOK, map[string]any{"vae_bundled": known, "action": "none"})
		return
	}
	v, hasDefault := engineFamilyVaes[family]
	if !hasDefault {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody,
			"this deployment has no default VAE for the " + family + " family — take one in and attach it" +
				" to " + id + " under `--vae`"})
		return
	}
	// Nothing known and nothing readable is not evidence of a fault, so it does not buy a
	// download on its own. The operator can still say they know (`force`) — the case no read can
	// reach, such as a source that has been taken down.
	if known != engineVaeNo && !b.Force {
		writeAPIErr(w, &apiError{http.StatusBadGateway, errCodeEngineVaeUnreadable,
			engineFirstNonEmpty(unreadable, "the checkpoint's header could not be read") +
				" — nothing was taken in. Repeat with force to attach " + v.Repo + "/" + v.File + " anyway"})
		return
	}
	if b.Check {
		// The same shape the ingest form reads, built by the same function: what would be taken
		// in, under what licence, and whether this deployment already holds it. A second spelling
		// of that answer here is a second thing to keep in step with the panel.
		follow, _, _ := engineVaePlan(ctx, e.catalog.list(ctx), family)
		plan := engineVaePlanRow(follow, v)
		plan["vae_bundled"] = known
		plan["action"] = "ingest"
		// Said out loud: the plan below rests on what the row recorded earlier, because the
		// source would not answer now. The fix itself does not go near that source.
		if unreadable != "" {
			plan["recheck_failed"] = unreadable
		}
		if follow != nil && follow.Staged {
			// Nothing to accept and nothing to download: the bytes are already this
			// deployment's, under a licence accepted when they were taken in.
			plan["action"] = "attach"
		}
		writeJSON(w, http.StatusOK, plan)
		return
	}
	staged, alreadyHere := engineVaeStagedKey(e.catalog.list(ctx), v)
	if alreadyHere {
		file := store.EngineModelFile{
			Flag: "--vae", S3Key: v.S3Key, Bytes: staged.Bytes, Source: staged.Source,
			ArtifactIdentity: staged.ArtifactIdentity,
			// The VAE is a VAE: saying so on the file keeps the scan from ever asking about it.
			VaeBundled: engineVaeYes,
		}
		found, err := a.mgr.store.AppendEngineModelFile(ctx, key, id, file)
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		if !found {
			writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineModelUnknown, "no model " + id + " for engine " + key})
			return
		}
		e.catalog.invalidate()
		a.auditFor(r, g, "engine."+key+".model.vae", id+" attached "+v.S3Key+" (already staged)")
		writeJSON(w, http.StatusOK, map[string]any{"vae_bundled": known, "action": "attached", "s3Key": v.S3Key})
		return
	}
	if !b.LicenseAccepted {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeIngestNotAccepted,
			"taking " + v.Repo + "/" + v.File + " in needs its licence accepted first"})
		return
	}
	job, aerr := a.startFamilyVae(ctx, r, g, e, id, v)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"vae_bundled": known, "action": "job_started", "job": engineIngestJobRow(job),
	})
}

// startFamilyVae takes the family's VAE in and attaches it to one row. Shared by the panel's
// button and by the follow-up an ingest schedules for itself, so that both spend the licence
// acceptance the same way and both land as an ATTACH — the row keeps everything else it has.
func (a engineAdminAPI) startFamilyVae(ctx context.Context, r *http.Request, g engineIngestGrant,
	e *engineRuntimeState, id string, v engineFamilyVae) (store.EngineIngestJob, *apiError) {
	ing := a.reg.ingester()
	if ing == nil {
		return store.EngineIngestJob{}, &apiError{http.StatusServiceUnavailable, errCodeIngestUnavailable,
			"this deployment's engine stack declares no ingest task — stage " + v.File + " by hand and attach it"}
	}
	res, aerr := engineIngestResolve(ctx, engineIngestSource{HF: &engineIngestHF{Repo: v.Repo, File: v.File}})
	if aerr != nil {
		return store.EngineIngestJob{}, aerr
	}
	job, aerr := ing.start(ctx, engineIngestRequest{
		Role: e.def.Key, ModelID: id, S3Key: v.S3Key,
		AcceptedBy: g.ident.ID, AcceptedTenant: g.tenantID, AcceptedLicense: engineLicenceLabel(res),
		Resolved: res, FileFlag: "--vae", Attach: true,
	})
	if aerr != nil {
		return store.EngineIngestJob{}, aerr
	}
	a.auditFor(r, g, "engine."+e.def.Key+".ingest",
		v.S3Key+" for "+id+" from "+res.Source+" (licence "+engineLicenceLabel(res)+" accepted)")
	return job, nil
}
