package main

// engine_family_parts.go — the files a SPLIT family needs beside its diffusion model, and the one
// press that takes them in (ADR 0072 decision 2, follow-up to the VAE remedy next door).
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
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
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

// enginePartsMissing is which of a family's parts a row does not have yet, by FLAG. It reads the
// row's own declarations, so a part attached by hand — or one this deployment declared from a
// different repository — is not offered again.
func enginePartsMissing(family string, m store.EngineModel) []engineFamilyPart {
	parts := engineFamilyPartsFor(family)
	if len(parts) == 0 {
		return nil
	}
	have := map[string]bool{}
	for _, f := range m.Files {
		if strings.TrimSpace(f.S3Key) != "" {
			have[strings.TrimSpace(f.Flag)] = true
		}
	}
	var out []engineFamilyPart
	for _, p := range parts {
		if !have[p.Flag] {
			out = append(out, p)
		}
	}
	return out
}

// enginePartsHeld is what the deployment ALREADY has, for a plan that would rather declare than
// download. Two independent things, because they answer different halves of "is it here":
//
//	known    — every S3 key this deployment has a record of, from the catalogue AND from finished
//	           ingest jobs (engineKnownArtifacts), with the upstream identity it was verified
//	           against before upload;
//	storage  — HeadObject, which is the only thing that proves bytes occupy that key NOW.
//
// 🔴 Neither substitutes for the other, which is the rule `reuse_s3_key` is already built on: a
// record without an object is a key whose bytes were purged, and an object without a record is
// bytes nobody can say the provenance of.
type enginePartsHeld struct {
	rows    []store.EngineModel
	known   map[string]*engineKnownArtifact
	storage *engineStorage
}

// enginePartPlan is one part's follow-up, resolved while somebody is still looking at the screen
// — the same rule engineVaePlan is built on, and for the same reason: the second download is
// started by the reconciler, where there is no request and nobody to report a failure to.
//
// `Staged` is the cheap outcome and it is the COMMON one: the Qwen-Image VAE is shared by two
// families, a deployment that runs either already has it, and a part re-downloaded would be the
// same bytes at the same key for a second licence acceptance and a second Fargate task.
func enginePartPlan(ctx context.Context, held enginePartsHeld, p engineFamilyPart) engineVaeFollowUp {
	// 1. A row declares it. This deployment's own declaration, trusted as such — the same thing
	//    the VAE remedy does, and no S3 call is spent on a fact the catalogue already states.
	if staged, here := engineVaeStagedKey(held.rows, engineFamilyVae{Repo: p.Repo, File: p.File, S3Key: p.S3Key}); here {
		return engineVaeFollowUp{
			Flag: p.Flag, S3Key: p.S3Key, Staged: true, Bytes: staged.Bytes,
			Source: staged.Source, ArtifactIdentity: staged.ArtifactIdentity,
		}
	}
	res, aerr := engineIngestResolve(ctx, engineIngestSource{HF: &engineIngestHF{Repo: p.Repo, File: p.File}})
	if aerr != nil {
		// Unreachable today. Reported as a plan with no resolve so the panel can say which part
		// could not be priced, rather than dropping it and offering an incomplete set silently.
		return engineVaeFollowUp{Flag: p.Flag, S3Key: p.S3Key}
	}
	// 2. NO row declares it, and the bytes are there anyway — a job that finished for a row since
	//    forgotten, or the same file taken in for another engine. The resolve above is what makes
	//    this decidable: the identity it just computed is compared against the one recorded
	//    before that upload, so "the same path" is never mistaken for "the same file".
	if fu, ok := enginePartHeldBytes(ctx, held, p, res); ok {
		return fu
	}
	// 3. 🔴 The key is recorded and step 2 could not vouch for it. A download is refused for a
	//    taken destination (engineIngestDestinationUnused), so proposing one here would spend a
	//    licence acceptance and end in "the S3 key … is already recorded" — reported from
	//    af-sandbox 2026-09-15, where an earlier hand-made row held that very key. What the
	//    operator can act on is WHO holds it, so that is what this carries.
	if k := held.known[p.S3Key]; k != nil {
		return engineVaeFollowUp{Flag: p.Flag, S3Key: p.S3Key, Conflict: enginePartConflictWord(k)}
	}
	return engineVaeFollowUp{Flag: p.Flag, S3Key: p.S3Key, Resolved: res}
}

// enginePartConflictWord names what is sitting on the key, in the words the panel can act on:
// the row to attach from or forget, or the job history that recorded it.
func enginePartConflictWord(k *engineKnownArtifact) string {
	ids := make([]string, 0, len(k.ModelIDs))
	for id := range k.ModelIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	switch {
	case len(ids) > 0:
		return "already declared by " + strings.Join(ids, ", ")
	case k.InFlight:
		return "an ingest is still running for this key"
	default:
		return "an earlier ingest recorded this key"
	}
}

// enginePartHeldBytes is step 2 above, and every one of its refusals is a case where downloading
// again is the honest answer.
func enginePartHeldBytes(ctx context.Context, held enginePartsHeld, p engineFamilyPart,
	res engineResolved) (engineVaeFollowUp, bool) {
	k := held.known[p.S3Key]
	switch {
	case k == nil, k.Ambiguous, k.InFlight, !k.Reusable:
		// No record, conflicting records, an upload still running that may replace these bytes,
		// or a record that never carried an identity at all.
		return engineVaeFollowUp{}, false
	case strings.TrimSpace(res.ArtifactIdentity) == "" || k.ArtifactIdentity != res.ArtifactIdentity:
		// The same key holding a different file. Reusing it would attach bytes nobody asked for.
		return engineVaeFollowUp{}, false
	case held.storage == nil:
		// Nothing can prove the object is still there, and a declaration pointing at a purged
		// key is a row that passes every check and fails inside the engine.
		return engineVaeFollowUp{}, false
	}
	if check := held.storage.verify(ctx, p.S3Key); check.State != engineStoragePresent {
		return engineVaeFollowUp{}, false
	}
	return engineVaeFollowUp{
		// The size is the RESOLVE's, which is the same file by the identity check above — the
		// record of the upload carries no length of its own.
		Flag: p.Flag, S3Key: p.S3Key, Staged: true, Bytes: res.Bytes,
		Source: k.Source, ArtifactIdentity: k.ArtifactIdentity,
	}, true
}

// enginePartsPlan is that for every part a row is missing.
func enginePartsPlan(ctx context.Context, held enginePartsHeld, parts []engineFamilyPart) []engineVaeFollowUp {
	out := make([]engineVaeFollowUp, 0, len(parts))
	for _, p := range parts {
		out = append(out, enginePartPlan(ctx, held, p))
	}
	return out
}

// enginePartsPlanRows is the plan as the panel reads it: what each part is, what it costs, and
// whose licence is about to be accepted — beside the checkbox, not after the press.
func enginePartsPlanRows(plan []engineVaeFollowUp, parts []engineFamilyPart) []map[string]any {
	byFlag := map[string]engineFamilyPart{}
	for _, p := range parts {
		byFlag[p.Flag] = p
	}
	out := make([]map[string]any, 0, len(plan))
	for _, fu := range plan {
		p := byFlag[fu.Flag]
		row := map[string]any{"flag": fu.Flag, "repo": p.Repo, "file": p.File, "s3_key": fu.S3Key}
		switch {
		case fu.Conflict != "":
			// Not offerable and not a failure of the upstream: the bytes are here, under a name
			// this plan cannot match. The fix is a human one and the row says which.
			row["conflict"] = fu.Conflict
		case fu.Staged:
			row["staged"] = true
			row["bytes"] = fu.Bytes
		case fu.Resolved.SHA256 != "":
			row["bytes"] = fu.Resolved.Bytes
			if lic := engineLicenceLabel(fu.Resolved); lic != "" {
				row["license"] = lic
			}
		default:
			// 🔴 Said out loud. A part whose upstream could not be read is one this press cannot
			// promise, and an offer that quietly drops it would leave a row still incomplete
			// after a button that said it would complete it.
			row["unreachable"] = true
		}
		out = append(out, row)
	}
	return out
}

// enginePartsBytes is what the whole set will cost, for the one number a person decides on.
func enginePartsBytes(plan []engineVaeFollowUp) int64 {
	var total int64
	for _, fu := range plan {
		if fu.Staged || fu.Conflict != "" {
			continue // already here, and paid for — or not ours to download at all
		}
		total += fu.Resolved.Bytes
	}
	return total
}

// enginePartsHeld gathers the two records a plan reuses bytes from. It is on the admin API
// because both of its sources are: the catalogue and the job history are read through the same
// grant the caller already holds, and the storage checker lives on the ingester.
func (a engineAdminAPI) enginePartsHeld(ctx context.Context, g engineIngestGrant, role string) enginePartsHeld {
	held := enginePartsHeld{}
	if e := a.reg.get(role); e != nil {
		held.rows = e.catalog.list(ctx)
	}
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

// --- the one press that completes a row ------------------------------------------------------

// enginePartsFixBody is what the button sends. `check` asks what WOULD happen, which is how the
// panel shows the size and the licence before anything is spent — the same two-step the VAE
// remedy uses.
type enginePartsFixBody struct {
	Check           bool `json:"check"`
	LicenseAccepted bool `json:"license_accepted"`
}

// fixParts (POST …/models/{id}/parts) gives an incomplete split-family row the files its family
// reads, in one press.
//
// 🔴 This is the remedy for rows that ALREADY exist, and it is why the feature is not only a
// checkbox on the ingest form: a deployment's rows were taken in before the checkbox existed,
// and "take the diffusion model in again, with the box ticked this time" is not a repair — it is
// a second copy of a 4 GB file.
//
// Four outcomes, the first three in the same words as the VAE remedy: `none` (nothing is
// missing), `attached` (every part was already this deployment's, so the row gained declarations
// and nothing was downloaded), `job_started` (at least one download is running, and it attaches
// itself when it lands) — and `moving`, which is this row's own weights being relocated inside
// the bucket because they were staged where no loader lists them (engine_file_move.go).
func (a engineAdminAPI) fixParts(w http.ResponseWriter, r *http.Request, g engineIngestGrant) {
	key, id := strings.TrimSpace(r.PathValue("key")), strings.TrimSpace(r.PathValue("id"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	if a.refuseBorrowedWrite(w, e, "completing a row") {
		return
	}
	var b enginePartsFixBody
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
	// 🔴 The row's OWN weights come first, and they are why this endpoint no longer answers only
	// about the part list. A split family's checkpoint staged under `image/checkpoints/` is a
	// file the row is holding and no loader can read (engine_file_move.go); with the parts
	// already attached, the answer to "揃える" was `none` beside a badge still reading
	// `不足: --diffusion-model`. Reported from af-sandbox 2026-09-15.
	fix, hasFix := engineMainFileFixFor(key, family, m)
	if len(engineFamilyPartsFor(family)) == 0 && !hasFix {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody,
			"this deployment knows no part list for the " + family + " family — attach the files it reads by hand"})
		return
	}
	missing := enginePartsMissing(family, m)
	if len(missing) == 0 && !hasFix {
		writeJSON(w, http.StatusOK, map[string]any{"action": "none"})
		return
	}
	held := a.enginePartsHeld(ctx, g, key)
	plan := enginePartsPlan(ctx, held, missing)
	if b.Check {
		action := "attach"
		if hasFix {
			action = "move"
		}
		for _, fu := range plan {
			if fu.Conflict != "" {
				action = "conflict"
				break
			}
			if !fu.Staged {
				action = "ingest"
			}
			if !fu.Staged && fu.Resolved.SHA256 == "" {
				// 🔴 A part nobody can price is a press that cannot be promised. Said here
				// rather than after the licence was accepted.
				action = "unreachable"
				break
			}
		}
		if hasFix {
			// `main_file` rides beside the parts and NEVER inside `bytes`: nothing is downloaded
			// for it, and a size beside a licence line reads as what this press will fetch.
			// Written out as its own literal rather than added to a map — the wire golden reads
			// these call sites as source (wiremap_golden_test.go), and a map built in a variable
			// is a shape nothing would check again.
			writeJSON(w, http.StatusOK, map[string]any{
				"action": action, "parts": enginePartsPlanRows(plan, missing),
				"bytes": enginePartsBytes(plan), "main_file": engineMainFileFixRow(fix),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"action": action, "parts": enginePartsPlanRows(plan, missing),
			"bytes": enginePartsBytes(plan),
		})
		return
	}
	// The weights first: a row whose parts attach while its own checkpoint stays unreadable is
	// still a row nobody can enable, and the move is the slow half (a server-side copy of up to
	// 13 GB), so it is started before the cheap writes rather than after them.
	var started []map[string]any
	moved := false
	if hasFix {
		if ref := a.engineMainFileMovable(ctx, held, key, id, fix); ref != nil {
			writeAPIRefusal(w, ref)
			return
		}
		job, aerr := a.engineStartMainFileMove(ctx, g, key, id, fix)
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		a.auditFor(r, g, "engine."+key+".model.parts",
			id+" moving "+fix.From+" to "+fix.To+" as "+fix.Flag)
		started = append(started, engineIngestJobRow(job))
		moved = true
	}
	// Everything already here is one write each, and it costs nothing to accept.
	attached := 0
	for i, fu := range plan {
		p := missing[i]
		if fu.Staged {
			file := store.EngineModelFile{
				Flag: p.Flag, S3Key: fu.S3Key, Bytes: fu.Bytes,
				Source: fu.Source, ArtifactIdentity: fu.ArtifactIdentity,
			}
			if p.Flag == "--vae" {
				file.VaeBundled = engineVaeYes
			}
			found, err := a.mgr.store.AppendEngineModelFile(ctx, key, id, file)
			if err != nil {
				writeAPIErr(w, internalErr(err))
				return
			}
			if !found {
				writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineModelUnknown,
					"no model " + id + " for engine " + key})
				return
			}
			e.catalog.invalidate()
			a.auditFor(r, g, "engine."+key+".model.parts", id+" attached "+fu.S3Key+" (already staged)")
			attached++
			continue
		}
		// 🔴 The key is taken by something this plan could not match. Refused HERE, where the
		// answer can name what is holding it — a download to that key is refused anyway, and the
		// message it earns ("the S3 key … is already recorded") says nothing about what to do.
		if fu.Conflict != "" {
			writeAPIRefusal(w, refuse(http.StatusConflict, errCodeEngineBadBody,
				p.Flag+" would land at "+fu.S3Key+", which is "+fu.Conflict+
					" — attach that file to this row from the panel, or forget the row holding it and press again",
				&apiHolder{Kind: "object", Key: fu.S3Key}, &apiNext{Act: "complete", Target: id}))
			return
		}
		if fu.Resolved.SHA256 == "" {
			writeAPIErr(w, &apiError{http.StatusBadGateway, errCodeIngestUnavailable,
				p.Repo + "/" + p.File + " could not be resolved, so " + p.Flag + " was not taken in"})
			return
		}
		// 🔴 The licence is asked for ONCE, for the whole set, and only when something is
		// actually going to be downloaded: the staged parts above were accepted when they were
		// taken in, and asking again for bytes this deployment already owns is a dialog that
		// teaches people to click through dialogs.
		if !b.LicenseAccepted {
			writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeIngestNotAccepted,
				"taking " + p.Repo + "/" + p.File + " in needs its licence accepted first"})
			return
		}
		ing := a.reg.ingester()
		if ing == nil {
			writeAPIErr(w, &apiError{http.StatusServiceUnavailable, errCodeIngestUnavailable,
				"this deployment's engine stack declares no ingest task — stage " + p.File + " by hand and attach it"})
			return
		}
		job, aerr := ing.start(ctx, engineIngestRequest{
			Role: key, ModelID: id, S3Key: fu.S3Key,
			AcceptedBy: g.ident.ID, AcceptedTenant: g.tenantID,
			AcceptedLicense: engineLicenceLabel(fu.Resolved),
			Resolved:        fu.Resolved, FileFlag: p.Flag, Attach: true,
		})
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		a.auditFor(r, g, "engine."+key+".ingest",
			fu.S3Key+" for "+id+" from "+fu.Resolved.Source+" (licence "+engineLicenceLabel(fu.Resolved)+" accepted)")
		started = append(started, engineIngestJobRow(job))
	}
	downloads := len(started)
	if moved {
		downloads--
	}
	action := "attached"
	switch {
	case downloads > 0:
		action = "job_started"
	case moved:
		// Said apart from a download on purpose: a move finishes in minutes with no bytes
		// crossing the internet, and a panel that called it a download would have an operator
		// watching for a transfer that is never going to appear.
		action = "moving"
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": action, "attached": attached, "jobs": started})
}
