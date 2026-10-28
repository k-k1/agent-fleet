package main

// engine_admin_models.go — the model catalogue writes on the admin panel: switching, adding and
// forgetting a row, and the VRAM and file guards they share (ADR 0072).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// putModel (PUT /api/admin/engines/{key}/models/{id}) is the catalogue's one mutation route
// (ADR 0072 decision 7). The body carries whichever of the three it means:
//
//	{"enabled": true|false}   switch a model on or off — i.e. sync it onto the box, or stop
//	{"selected": true}        the image role's ONE checkpoint (sd-server holds one)
//	{"default": true}         the llm role's answer to a request that named no model
//
// `selected` and `default` are exclusive within a role and the store enforces it in one
// transaction; both also enable, because an administrator who picked a checkpoint has said it
// should be loaded.
//
// ⚠️ What this does NOT do is restart a running engine. Changing the selection takes effect at
// the next start (decision 4): the box holds one checkpoint chosen by a startup flag, and
// silently redeploying the service would kill whatever generation is in flight. The panel says
// so; a deliberate restart is the mode toggle.
func (a engineAdminAPI) putModel(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	key := strings.TrimSpace(r.PathValue("key"))
	id := strings.TrimSpace(r.PathValue("id"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	if a.refuseBorrowedWrite(w, e, "what this engine may load") {
		return
	}
	if a.mgr == nil || a.mgr.store == nil {
		writeAPIErr(w, internalErr(errors.New("no store")))
		return
	}
	var b struct {
		Enabled  *bool `json:"enabled"`
		Selected *bool `json:"selected"`
		Default  *bool `json:"default"`
		// BaseModel corrects the declared checkpoint family, and is the only FIELD this route
		// edits rather than a flag it flips. It is here because a row can be missing one while
		// looking complete in every other way — a seeded row always is, since the seed cannot
		// know a family — and the alternative is registering the whole row again from scratch,
		// which for a split model means re-typing three S3 keys to change one word.
		BaseModel *string `json:"base_model"`
		// NegativePrompt is the row's own "never draw this", edited from the same panel and for
		// the same reason base_model is here: it is prose an administrator tunes after seeing
		// what the checkpoint actually produces, and re-registering a split model's four S3 keys
		// to change one sentence is not an edit anybody makes twice.
		//
		// The empty string is a REAL value — "stop declaring one, use the Agent's own default" —
		// which is why it is a pointer like the rest rather than "empty means unchanged".
		NegativePrompt *string `json:"negative_prompt"`
		// TrainedWords replaces the words an adapter answers to (ADR 0081 decision 5). Ingest
		// writes what Civitai published, and this is how a wrong or missing one is corrected —
		// plenty of LoRAs publish none, and plenty publish a word their files do not use.
		//
		// An empty LIST is a real value ("this adapter has no trigger"), which is why it is a
		// pointer to a slice: `null` is "the body said nothing" and `[]` is "there are none".
		TrainedWords *[]string `json:"trained_words"`
		// Params replaces the row's generation defaults, and `{}` clears them — which is the way
		// back to the family's own recipe once a number has been declared. Same reasoning as
		// BaseModel: it is a field of a row that is otherwise fine, and re-registering the whole
		// row to change `steps` would carry the licence acceptance and the source through a
		// round trip to move one number.
		Params *store.EngineParams `json:"params"`
		// ContextTokens and MaxOutputTokens are ONE declaration and travel together: the row
		// answers max_output_tokens only when context_tokens is above zero (engineAdminModelRow),
		// so moving one alone leaves a row the panel cannot explain. A missing half is read from
		// the stored row, which is what makes "raise the cap" a one-field request.
		//
		// This is the field a wrong value bills for. Measured on a borrowed llm engine: a row
		// still declaring 262144 made llama.cpp ask for a 16 GiB KV cache on top of 17 GB of
		// weights, and the L4 it had just bought answered `cudaMalloc failed: out of memory`
		// four minutes into the cold start. There was no way to correct it from the panel.
		ContextTokens   *int `json:"context_tokens"`
		MaxOutputTokens *int `json:"max_output_tokens"`
		// VramMiB is the operator's own measurement, and 0 WITHDRAWS it — putting the row back on
		// the floor its files imply rather than on a number nobody stands behind any more. A
		// pointer like the rest, so that "said nothing" and "said zero" stay different bodies.
		VramMiB *int `json:"vram_mib"`
		// ConfirmVram is "I have read that this may not fit" (ADR 0074 decision 6). Required
		// only when the model's declared demand exceeds the chosen instance class.
		ConfirmVram bool `json:"confirm_vram"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}

	ctx := r.Context()
	// Anything that puts weights on the card is checked, and all three of these do: enabling
	// syncs the model onto the box, and both `selected` and `default` enable as a side effect.
	// It is checked BEFORE the write, so a refusal leaves the catalogue as it was.
	loading := (b.Enabled != nil && *b.Enabled) ||
		(b.Selected != nil && *b.Selected) || (b.Default != nil && *b.Default)
	// 🔴 Before anything is JUDGED, give the row a chance to stop being unanswerable. Every llm
	// row on both deployments predates the header read and answers its floor — the weights and
	// nothing else — so the VRAM guard below can only ask for a confirmation it has no way to
	// inform, and the panel can offer no fitted window. The bytes are in the bucket and the
	// header is 64 KiB in: read it once, here, where the operator is already making a write and
	// a few hundred milliseconds is not a surprise.
	//
	// Only on a LOADING write, and only for a row that has none: this is a repair, not a poll.
	// Best-effort in both directions — a refused read leaves the row where it was, and a failed
	// STORE is logged and not raised, because the edit the operator actually asked for must not
	// fail over a cache fill.
	if loading {
		a.healGeometry(ctx, e, id)
	}
	if loading && !b.ConfirmVram {
		if aerr := engineVramGuard(ctx, e, id); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	// And whether the row could generate at all. Unlike the VRAM guard this one REFUSES and has
	// no confirm: a template that reads a file the row does not declare is not a judgement call
	// under uncertainty, it is `comfyBuildGraph` returning errComfyMissingFile before it dials
	// anything. Switching such a row on puts its id in generate_image's `model` enum and buys a
	// cold start for a request that cannot succeed (ADR 0072 P2 欠落 10).
	if loading {
		if aerr := engineFilesGuard(ctx, e, id); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		// The same refusal for the same kind of row, read off the checkpoint's own header rather
		// than off the declaration: an SDXL checkpoint published without VAE tensors declares
		// everything its family needs and still cannot decode a picture (ADR 0072 follow-up).
		if aerr := engineVaeGuard(ctx, e, id); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	var (
		found  bool
		err    error
		action string
	)
	switch {
	case b.BaseModel != nil:
		want := strings.TrimSpace(*b.BaseModel)
		isLora := false
		for _, m := range e.catalog.list(ctx) {
			if m.ID == id {
				isLora = engineModelIsLora(m)
			}
		}
		// The same rule the register route applies, for the same reason: a family that names no
		// template leaves a row that can be enabled, appears by name in generate_image's list,
		// and is refused only at generation.
		if !isLora && !engineBaseModelValid(e.def.Provider, want) {
			writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, fmt.Sprintf(
				"base_model must be one of %s (this engine runs %s, which picks a workflow by family"+
					" and will not guess one); %q is not a family",
				strings.Join(engineBaseModelsFor(e.def.Provider), ", "), e.def.Provider, want)})
			return
		}
		found, err = a.mgr.store.SetEngineModelBaseModel(ctx, key, id, want)
		action = "base_model " + want
	case b.NegativePrompt != nil:
		found, err = a.mgr.store.SetEngineModelNegativePrompt(ctx, key, id, strings.TrimSpace(*b.NegativePrompt))
		// The VALUE is deliberately not in the audit line: it is free text an administrator can
		// make as long as they like, and an audit trail is not the place to carry a paragraph.
		action = "negative_prompt"
	case b.TrainedWords != nil:
		// Blank entries are dropped rather than stored: a comma-separated box answers a trailing
		// comma with an empty word, and an empty chip in the pane is a trigger nobody can remove.
		found, err = a.mgr.store.SetEngineModelTrainedWords(ctx, key, id, engineTrimStrings(*b.TrainedWords))
		action = "trained_words"
	case b.Params != nil:
		// Cleaned, not refused: engineParamsClean drops what the provider could not run and
		// keeps the rest, and a set that cleans down to nothing clears the row's declaration.
		p := engineParamsClean(b.Params)
		found, err = a.mgr.store.SetEngineModelParams(ctx, key, id, p)
		action = "params"
		if p == nil {
			action = "params cleared"
		}
	case b.ContextTokens != nil || b.MaxOutputTokens != nil:
		cur, ok := engineCatalogModel(ctx, e, id)
		if !ok {
			break // found stays false, and the 404 below is the answer
		}
		next := cur
		if b.ContextTokens != nil {
			next.ContextTokens = *b.ContextTokens
		}
		if b.MaxOutputTokens != nil {
			next.MaxOutputTokens = *b.MaxOutputTokens
		}
		if next.ContextTokens < 0 || next.MaxOutputTokens < 0 {
			writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody,
				"context_tokens and max_output_tokens cannot be negative; 0 means undeclared"})
			return
		}
		// Clearing the window clears the cap with it. The row answers a cap only alongside a
		// window, so one left behind would be stored, invisible and still read by whatever
		// starts the engine.
		if next.ContextTokens == 0 {
			next.MaxOutputTokens = 0
		}
		if aerr := engineVramGuardEdit(ctx, e, cur, next, b.ConfirmVram); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		found, err = a.mgr.store.SetEngineModelWindow(ctx, key, id, next.ContextTokens, next.MaxOutputTokens)
		action = fmt.Sprintf("window %d/%d", next.ContextTokens, next.MaxOutputTokens)
	case b.VramMiB != nil:
		cur, ok := engineCatalogModel(ctx, e, id)
		if !ok {
			break
		}
		if *b.VramMiB < 0 {
			writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody,
				"vram_mib cannot be negative; 0 withdraws the measurement"})
			return
		}
		next := cur
		next.VramMiB = *b.VramMiB
		if aerr := engineVramGuardEdit(ctx, e, cur, next, b.ConfirmVram); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		found, err = a.mgr.store.SetEngineModelVram(ctx, key, id, next.VramMiB)
		action = fmt.Sprintf("vram_mib %d", next.VramMiB)
	case b.Selected != nil && *b.Selected:
		found, err = a.mgr.store.SetEngineModelSelected(ctx, key, id)
		action = "select"
	case b.Default != nil && *b.Default:
		found, err = a.mgr.store.SetEngineModelDefault(ctx, key, id)
		action = "default"
	case b.Enabled != nil:
		found, err = a.mgr.store.SetEngineModelEnabled(ctx, key, id, *b.Enabled)
		action = "enable"
		if !*b.Enabled {
			action = "disable"
		}
	default:
		// An empty body must not be read as "switch it off", for the same reason the mode
		// route refuses one.
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody,
			"enabled, selected, default, base_model, negative_prompt, trained_words, params," +
				" context_tokens, max_output_tokens or vram_mib is required"})
		return
	}
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineModelUnknown, "no model " + id + " for engine " + key})
		return
	}
	e.catalog.invalidate()

	// The box is told next, and a failure here is REPORTED rather than logged: the row was
	// written, so the panel would otherwise show the new selection while the engine keeps
	// starting with the old one — which is the exact failure "publish the active set" exists to
	// prevent, and it would only be noticed at the next cold start.
	if perr := e.publishActiveSet(ctx); perr != nil {
		writeAPIErr(w, &apiError{http.StatusBadGateway, errCodeEnginePublishFailed, perr.Error()})
		return
	}
	a.audit(ctx, ident, "engine."+key+".model", action+" "+id)
	// Detached: the fan-out dials every running workspace, and nobody is waiting for it. A
	// workspace that misses the push catches up on the catalogue's own 10-minute TTL.
	go notifyEngineCatalogChanged(context.WithoutCancel(ctx), a.mgr, key)
	writeJSON(w, http.StatusOK, a.row(ctx, e))
}

// engineVramGuard is the one place a model is compared with the card before it is switched on
// (ADR 0074 decision 6).
//
// It returns an error ONLY for the case it can state: a demand that is known and that exceeds
// the chosen rung. Three things it deliberately does not do:
//
//   - it does not refuse. The answer names the numbers and the same call with confirm_vram goes
//     through, because quantisation, --offload-to-cpu and things this deployment has not
//     measured are real (the same position ADR 0072 decision 10 takes on licences);
//   - it does not stop a model nobody has measured. `unknown` is not "too big", and asking
//     about every unmeasured model would teach people to click through the one that matters;
//   - it does not run at all where there is no ladder, because then there is no rung to compare
//     against and the box is whatever CloudFormation bought.
func engineVramGuard(ctx context.Context, e *engineRuntimeState, id string) *apiError {
	m, ok := engineCatalogModel(ctx, e, id)
	if !ok {
		return nil
	}
	return engineVramGuardRow(ctx, e, m)
}

// engineVramGuardEdit is the same question asked FORWARD: not "may this row be switched on" but
// "may this row go on being loaded once the value in front of me is written".
//
// It exists because the guard above only ever ran on the way in. A row that was already enabled
// could have its context window raised to anything and nobody looked — which is precisely the
// shape that failed on a borrowed llm engine: 262144 tokens, a 16 GiB KV cache, `cudaMalloc
// failed: out of memory`, four minutes after the GPU box was bought. The check is on the
// CANDIDATE row, never the stored one, because the stored one still fits.
//
// A row nothing would load is not asked about: `enabled`, `selected` and `default` are the three
// states that put weights on the card, and editing a disabled row costs nothing to get wrong.
func engineVramGuardEdit(ctx context.Context, e *engineRuntimeState, cur, next store.EngineModel, confirmed bool) *apiError {
	if confirmed || !(cur.Enabled || cur.Selected || cur.Default) {
		return nil
	}
	return engineVramGuardRow(ctx, e, next)
}

// engineVramGuardRow judges one row VALUE against the chosen rung. Taking the row rather than an
// id is what lets an edit be judged on the numbers it is about to write.
func engineVramGuardRow(ctx context.Context, e *engineRuntimeState, m store.EngineModel) *apiError {
	sel, ok := e.selectedClass(ctx)
	if !ok || sel.VramMiB <= 0 || engineModelIsLora(m) {
		return nil
	}
	need, source := engineModelVramNeed(m)
	// 🔴 A floor for a row that declares a WINDOW is the one estimate that is known to be
	// missing a term which GROWS with that window, and it is the shape that has actually put a
	// card out of memory: af-sandbox's Qwen3.8-27B row declares 262144 and no geometry, so the
	// need came back as 17093 MiB of weights, fitted the 22000 MiB rung, was enabled without a
	// word — and llama.cpp then asked for 16384 MiB of KV cache on top and the L4 answered
	// `cudaMalloc failed: out of memory`. Passing that silently is the bug. The number cannot be
	// computed here (that is what "no geometry" means) — healGeometry has already TRIED, on this
	// same request, and the bucket would not say — so the answer is to say so and let the
	// operator confirm or declare vram_mib.
	//
	// Only when it does not already fail the ordinary comparison below, and only for a row that
	// declares a window: an image checkpoint has none, and for it the weights really are most of
	// the story (ADR 0074's first measurement).
	//
	// `unknown` counts here as well as `floor`, and leaving it out was a hole the size of the
	// original bug: a row whose FILES declare no bytes answers unknown, falls through the
	// `unknown` line below, and is switched on without anybody looking — which is the seeded
	// `qwen3-coder-30b-a3b` row on both deployments, enabled, declaring 32,768 tokens and not
	// one measurable byte.
	if (source == engineVramFloor || source == engineVramUnknown) &&
		m.ContextTokens > 0 && engineClassFits(sel, need) {
		// 🔴 Two different rows land here and they are not missing the same thing. A `floor` row
		// has its weights and no header; an `unknown` one declares no measurable bytes at all —
		// and it may well HAVE a geometry, so telling it "no attention geometry" sends the
		// operator looking for a header that is already read.
		missing := fmt.Sprintf("a KV cache that cannot be sized (no attention geometry) on top of %d MiB of weights", need)
		if source == engineVramUnknown {
			missing = "weights this deployment cannot size (its files declare no bytes) plus whatever cache that window needs"
		}
		return &apiError{http.StatusConflict, errCodeEngineVramConfirm, fmt.Sprintf(
			"%s declares a %d-token window and wants %s, so the %s class's %d MiB cannot be said"+
				" to fit; repeat with confirm_vram, or declare vram_mib",
			m.ID, m.ContextTokens, missing, sel.ID, sel.VramMiB)}
	}
	if source == engineVramUnknown || engineClassFits(sel, need) {
		return nil
	}
	at := "at least "
	if source == engineVramDeclared {
		at = ""
	}
	return &apiError{http.StatusConflict, errCodeEngineVramConfirm, fmt.Sprintf(
		"%s wants %s%d MiB of VRAM and the %s class declares %d MiB; repeat with confirm_vram to enable it anyway",
		m.ID, at, need, sel.ID, sel.VramMiB)}
}

// engineCatalogModel finds one row of an engine's catalogue by id.
func engineCatalogModel(ctx context.Context, e *engineRuntimeState, id string) (store.EngineModel, bool) {
	for _, m := range e.catalog.list(ctx) {
		if m.ID == id {
			return m, true
		}
	}
	return store.EngineModel{}, false
}

// engineFilesGuard refuses to switch on a row whose declared family reads files it does not
// have (ADR 0072 P2 欠落 10).
//
// The check lives HERE rather than where the family is declared, because declaring one is an
// improvement to a broken row and refusing it would leave the row broken AND unfixable. What
// the declaration does instead is put `files_missing` on the panel — the mark that used to
// disappear the moment a family was chosen, which is how `flux1-dev` came to look healthier
// after being told what it was.
func engineFilesGuard(ctx context.Context, e *engineRuntimeState, id string) *apiError {
	for _, m := range e.catalog.list(ctx) {
		if m.ID != id {
			continue
		}
		missing := engineMissingFileFlags(e.def.Provider, m)
		if len(missing) == 0 {
			return nil
		}
		named := make([]string, 0, len(missing))
		for _, f := range missing {
			named = append(named, engineFlagLabel(f))
		}
		return &apiError{http.StatusConflict, errCodeEngineFilesMissing, fmt.Sprintf(
			"%s declares base_model %s, and that workflow reads %s — this row has no such file,"+
				" so it would be offered by name and refused at generation. Take the missing"+
				" part(s) in and attach them to this row first",
			m.ID, m.BaseModel, strings.Join(named, ", "))}
	}
	return nil
}

// engineModelFileBody is one file as the register route takes it — and as the row answers it
// back (`file_rows`), which is what makes "read the row, post it again" a round trip rather
// than a translation.
type engineModelFileBody struct {
	Flag  string `json:"flag"`
	S3Key string `json:"s3Key"`
	Bytes int64  `json:"bytes"`
	// Where this part came from. Here so that the round trip stays one — a row read out of
	// `GET /api/admin/engines` and posted back rebuilds what it was, and a per-file provenance
	// that the answer carries but the register route drops would be silently erased by the one
	// operation that exists to restore a forgotten row (ADR 0072 P6 R2).
	Source string `json:"source"`
	// Whether this file bundles its family's VAE, carried for the same round-trip reason — the row
	// ANSWERS `vae_bundled` per file (engineModelFileRows). Only "yes" and "no" survive
	// (engineVaeVerdict); anything else is "nobody read it", and for the row's own weights that is
	// what sends the route to the file's header instead.
	VaeBundled string `json:"vae_bundled"`
}

// engineFilesFromBody reads the files out of a register body, from whichever of the two names
// carries them.
//
// `files` is the form's own field and wins. When it is there but is not a list of objects, it
// is the row's human-readable `files` riding along in a body that was read back from a row —
// `file_rows` is then the declaration, and the base names are dropped rather than guessed at
// (a base name is not a key: two directories hold `model.safetensors`). With neither, the
// caller gets the shape it got wrong rather than "at least one file is required", which for a
// hand-written body would send them looking in the wrong place.
func engineFilesFromBody(raw json.RawMessage, rows []engineModelFileBody) ([]engineModelFileBody, *apiError) {
	var files []engineModelFileBody
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &files); err == nil && len(files) > 0 {
			return files, nil
		}
		if len(rows) == 0 {
			return nil, &apiError{http.StatusBadRequest, errCodeEngineBadBody,
				`files has to be a list of {"s3Key":…,"flag":…,"bytes":…} — a list of names is what a` +
					` row ANSWERS with, and its machine-readable half is file_rows`}
		}
	}
	return rows, nil
}

// postModel (POST /api/admin/engines/{key}/models) registers a file that is already in the
// models bucket. It is the manual half of what phase P4's ingest will do for itself.
//
// The row is created DISABLED whatever the body says. "The file is in the bucket" and "members
// may use it" are different facts (ADR 0072's rejected "derive the catalogue from ListObjects"),
// and the second is a separate, deliberate press of Enable — which is also what gives an
// administrator a chance to read the licence line before anything is offered.
//
// ⚠️ Nothing in this write verifies that the S3 key exists. The storage endpoint checks every
// server-known key independently and reports present, missing or unknown; keeping registration
// separate preserves the manual route when AWS access is absent or denied. The header read below
// does not change that: it can only ADD a verdict, and a key with nothing at it simply fails to
// answer, exactly as a deployment with no bucket does.
func (a engineAdminAPI) postModel(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	key := strings.TrimSpace(r.PathValue("key"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	if a.refuseBorrowedWrite(w, e, "registering a model") {
		return
	}
	if a.mgr == nil || a.mgr.store == nil {
		writeAPIErr(w, internalErr(errors.New("no store")))
		return
	}
	var b struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
		// 🔴 Raw, because the ROW answers a `files` of its own and it is a list of base NAMES
		// for a person to read. A body posted back from that row therefore carries a `files`
		// this route cannot mean, and a typed field would fail the whole decode with "invalid
		// JSON" — which is what the round trip below hit first.
		Files json.RawMessage `json:"files"`
		// FileRows is the machine-readable half the row answers with, and the one this route
		// reads when `files` is not a list of objects: the JSON a super_admin read out of
		// `GET /api/admin/engines` posts straight back and rebuilds the row (ADR 0072 P6 R2).
		FileRows        []engineModelFileBody `json:"file_rows"`
		Args            []string              `json:"args"`
		ContextTokens   int                   `json:"context_tokens"`
		MaxOutputTokens int                   `json:"max_output_tokens"`
		Sizes           []string              `json:"sizes"`
		Description     string                `json:"description"`
		VramMiB         int                   `json:"vram_mib"`
		License         string                `json:"license"`
		LicenseName     string                `json:"license_name"`
		LicenseURL      string                `json:"license_url"`
		Precision       string                `json:"precision"`
		BaseModel       string                `json:"base_model"`
		// WHERE the bytes came from — `hf:<repo>/<file>`, `civitai:<version>`, a URL. Accepted
		// here and not only written by the ingest, because this route is how a file that is
		// already in the bucket is registered AGAIN: the panel offers a finished ingest job's
		// key, and without this field the rebuilt row loses the one fact migration
		// 0060_engine_model_source.sql exists to keep — an id is short and readable and does
		// not say which vendor published the model, and after the job is gone nothing else does.
		//
		// Free text on purpose: nothing parses it (the machine-readable half was consumed when
		// the job was created) and inventing a shape here would make the round trip lossy.
		Source string `json:"source"`
		// The words an adapter answers to, accepted here for the same reason `source` is: the row
		// ANSWERS them, and a field the answer carries but this route drops is silently erased by
		// the one operation that exists to rebuild a forgotten row.
		TrainedWords []string `json:"trained_words"`
		// The generation defaults, in the same shape the row answers them. A pointer so that
		// "the body said nothing" and "the body said all zeros" stay different bodies.
		Params *store.EngineParams `json:"params"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	id := strings.TrimSpace(b.ID)
	if id == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "id is required"})
		return
	}
	m := store.EngineModel{
		Role: key, ID: id, Kind: strings.TrimSpace(b.Kind),
		Args: b.Args, ContextTokens: b.ContextTokens, MaxOutputTokens: b.MaxOutputTokens,
		Sizes: b.Sizes, Description: strings.TrimSpace(b.Description), VramMiB: b.VramMiB,
		License: strings.TrimSpace(b.License), LicenseName: strings.TrimSpace(b.LicenseName),
		LicenseURL: strings.TrimSpace(b.LicenseURL),
		Precision:  strings.TrimSpace(b.Precision), BaseModel: strings.TrimSpace(b.BaseModel),
		Source:       strings.TrimSpace(b.Source),
		TrainedWords: engineTrimStrings(b.TrainedWords),
		Params:       engineParamsClean(b.Params),
	}
	// 🔴 What is NOT copied across with it: the licence ACCEPTANCE. `license_accepted_by` and
	// its tenant and timestamp are the record of a human act (ADR 0072 decision 10), and a row
	// rebuilt from an old job's key is not that act — the person registering it here may not be
	// the person who accepted anything. The licence TEXT may be re-typed (the field above), the
	// signature may not, so the new row's acceptance stays empty and the panel says
	// "licence not recorded".
	// The commercial-use verdict is READ FROM the licence here exactly as the ingest reads it
	// (ADR 0072 decision 10), rather than being a field this route accepts. Two reasons: the
	// answer is a property of the licence and not of whoever typed it, and a row registered by
	// hand would otherwise be the one place the panel cannot say "non-commercial" — the same
	// model, taken in by the other door, says it.
	//
	// 🔴 Only when a licence was actually given. An empty licence stays an EMPTY verdict rather
	// than `unknown`: "nobody recorded one" and "recorded, and the terms could not be read" are
	// different facts, and the panel draws them differently.
	if m.License != "" || m.LicenseName != "" {
		m.CommercialUse = engineCommercialUse(engineResolved{License: m.License, LicenseName: m.LicenseName})
	}
	files, aerr := engineFilesFromBody(b.Files, b.FileRows)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	for _, f := range files {
		k := strings.TrimSpace(f.S3Key)
		if k == "" {
			continue
		}
		file := store.EngineModelFile{
			Flag: strings.TrimSpace(f.Flag), S3Key: k, Bytes: f.Bytes,
			Source: strings.TrimSpace(f.Source), VaeBundled: engineVaeVerdict(f.VaeBundled),
		}
		// The body did not say, and this is the row's own weights: read the header of the file in
		// the bucket rather than leaving the row with no verdict. A hand-registered SD1.5/SDXL
		// checkpoint is otherwise exactly the row `vae_missing` cannot mark and 揃える cannot
		// repair — and nobody finds out until every request fails inside ComfyUI.
		if file.VaeBundled == engineVaeUnknown && engineVaeMainFile(file.Flag) {
			file.VaeBundled = engineVaeOfObject(r.Context(), e.def.Provider, m.Kind, k, a.engineStorageBytes())
		}
		m.Files = append(m.Files, file)
	}
	if len(m.Files) == 0 {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "at least one file (s3Key) is required"})
		return
	}
	// The same read the VAE question does above, for the other fact a header states. A row that
	// arrives here carries no geometry — this route has no upstream URL to range-GET — so
	// without it every hand-registered and every rebuilt row answers its FLOOR forever, which is
	// the weights and nothing else and therefore fits almost any card. Best-effort: a refused
	// read leaves the row exactly where it would have been.
	if !engineModelIsLora(m) && m.ContextCeiling == 0 && m.KVLayers == 0 {
		if key, ok := engineGeometryFile(m); ok {
			if geom := engineGGUFGeometryOfObject(r.Context(), key, a.engineStorageBytes()); geom.complete() {
				engineApplyGeometry(&m, geom)
			}
		}
	}
	// The family, for a provider that dispatches on one (ADR 0072 decision 2). Refused HERE, in
	// the operator's own words, rather than as a ComfyUI validation error a cold start and a
	// generation later. LoRAs are exempt: their base_model is a compatibility target for a
	// future phase, not a workflow template, so an upstream spelling is legitimate there.
	if !engineModelIsLora(m) && !engineBaseModelValid(e.def.Provider, m.BaseModel) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, fmt.Sprintf(
			"base_model must be one of %s (this engine runs %s, which picks a workflow by family and"+
				" will not guess one); %q is not a family",
			strings.Join(engineBaseModelsFor(e.def.Provider), ", "), e.def.Provider, m.BaseModel)})
		return
	}
	if err := a.mgr.store.PutEngineModel(r.Context(), m); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	e.catalog.invalidate()
	// No publish and no push: the row is disabled, so nothing about the active set or any
	// workspace's catalogue has changed yet. Enabling it is what moves those.
	a.audit(r.Context(), ident, "engine."+key+".model", "register "+id)
	log.Printf("engines: %s catalogue row registered: %s (%d file(s), disabled)", key, id, len(m.Files))
	writeJSON(w, http.StatusOK, a.row(r.Context(), e))
}

// deleteModel forgets a catalogue row. The FILE stays in the bucket: the CP has no
// s3:DeleteObject and is not getting one (ADR 0072 decision 7 — the delete belongs to the
// ingest task, phase P4). Saying so is the point; a route that silently left 7 GB behind while
// looking like a delete is worse than one that does not offer it.
func (a engineAdminAPI) deleteModel(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	key := strings.TrimSpace(r.PathValue("key"))
	id := strings.TrimSpace(r.PathValue("id"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	if a.refuseBorrowedWrite(w, e, "forgetting a model") {
		return
	}
	if a.mgr == nil || a.mgr.store == nil {
		writeAPIErr(w, internalErr(errors.New("no store")))
		return
	}
	// The row has to be read BEFORE it is deleted: the S3 keys are in it, and a purge with no
	// keys silently deletes nothing while reporting success.
	//
	// Every ROLE is read, not just this one, because what decides whether a key may be deleted
	// is whether anything else points at it, and nothing says the two rows are in the same role.
	rows, lerr := a.mgr.store.ListEngineModels(r.Context(), "")
	var keys []string
	for _, m := range rows {
		if m.ID != id || m.Role != key {
			continue
		}
		for _, f := range m.Files {
			if f.S3Key != "" {
				keys = append(keys, f.S3Key)
			}
		}
	}
	// Deleting the bytes cannot be undone, so the request is on record before the row goes,
	// and refused when it cannot be (store.BeginIrreversible). Forgetting alone is not: the
	// row can be registered again.
	var purge *store.AuditIntent
	if r.URL.Query().Get("purge") == "1" {
		var ok bool
		if purge, ok = beginIrreversible(w, r, a.mgr.store, store.AuditLog{
			ActorKind: "admin", ActorID: ident.ID, Action: "engine." + key + ".model", Target: "purge " + id,
		}); !ok {
			return
		}
	}
	found, err := a.mgr.store.DeleteEngineModel(r.Context(), key, id)
	if err != nil {
		if purge != nil {
			purge.Done(r.Context(), "error: "+err.Error(), http.StatusInternalServerError)
		}
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		if purge != nil {
			purge.Done(r.Context(), "no such model", http.StatusNotFound)
		}
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineModelUnknown, "no model " + id + " for engine " + key})
		return
	}
	// ?purge=1 also deletes the bytes — and the CP cannot: it has no s3:DeleteObject and is not
	// getting one (ADR 0072 decision 7). The ingest task does it, in MODE=delete, because that
	// task is the one principal in the deployment allowed to write in that bucket at all.
	purged := ""
	switch {
	case purge != nil && len(keys) == 0 && lerr != nil:
		// No keys because the catalogue could not be read, not because the row had none.
		purged = "the row is gone; whether it had files is unknown: the catalogue could not be read (" +
			lerr.Error() + ")"
	case purge != nil && len(keys) == 0:
		purged = "the row held no file to delete"
	case purge != nil:
		// 🔴 A file may belong to more than one row, and decision 2 says so on purpose:
		// `text_encoders/` is SHARED — SD3.5 and FLUX.1 read the same T5-XXL and CLIP-L, so one
		// ingest is pointed at from both rows' `files[]`. Handing this row's keys straight to
		// MODE=delete therefore breaks models nobody touched, silently and at the next cold
		// start (measured on af-sandbox: `clip_l.safetensors` was pointed at by two rows).
		//
		// Which is why a read that FAILED is not treated as "nothing else uses these": with no
		// answer the only safe act is to leave the bytes alone and say so.
		switch keep, kerr := engineKeysStillUsed(rows, key, id, keys, lerr); {
		case kerr != nil:
			purged = "the row is gone; the files are not: the catalogue could not be read, so" +
				" whether another model still uses these files is unknown (" + kerr.Error() + ")"
		default:
			free := make([]string, 0, len(keys))
			for _, k := range keys {
				if _, shared := keep[k]; !shared {
					free = append(free, k)
				}
			}
			switch ing := a.reg.ingester(); {
			case len(free) == 0:
				purged = "no file was deleted: " + engineKeptBecause(keys, keep)
			case ing == nil:
				purged = "this deployment declares no ingest task, so the files stay in the bucket"
			default:
				if err := ing.deleteObjects(r.Context(), free); err != nil {
					purged = "the row is gone; the files are not: " + err.Error()
				} else {
					purged = "deleting " + strings.Join(free, " ")
				}
				// What was NOT deleted rides along whatever happened to the rest: a purge that
				// reported only the deletions would read as "all of it went".
				if len(free) < len(keys) {
					purged += "; " + engineKeptBecause(keys, keep)
				}
			}
		}
	}
	e.catalog.invalidate()
	// A deleted row may have been enabled, so the box's active set really has changed.
	if perr := e.publishActiveSet(r.Context()); perr != nil {
		if purge != nil {
			purge.Done(r.Context(), purged+"; the active set was not published: "+perr.Error(), http.StatusBadGateway)
		}
		writeAPIErr(w, &apiError{http.StatusBadGateway, errCodeEnginePublishFailed, perr.Error()})
		return
	}
	// A purge's outcome row is its whole record; a second "forget" row would make one request
	// read as two.
	if purge != nil {
		purge.Done(r.Context(), purged, http.StatusOK)
	} else {
		a.audit(r.Context(), ident, "engine."+key+".model", "forget "+id)
	}
	go notifyEngineCatalogChanged(context.WithoutCancel(r.Context()), a.mgr, key)
	row := a.row(r.Context(), e)
	if purged != "" {
		row["purge"] = purged
	}
	writeJSON(w, http.StatusOK, row)
}

// engineKeysStillUsed answers, for the row being forgotten, which of its S3 keys some OTHER row
// also points at. Keyed by S3 key, valued by the model that keeps it alive, because "kept" with
// no name is an answer an operator cannot act on.
//
// The catalogue read is passed in rather than repeated: it has to be the one taken BEFORE the
// row was deleted, or the row being forgotten would be its own reference.
func engineKeysStillUsed(rows []store.EngineModel, role, id string, keys []string, rerr error) (map[string]string, error) {
	if rerr != nil {
		return nil, rerr
	}
	want := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		want[k] = struct{}{}
	}
	used := map[string]string{}
	for _, m := range rows {
		if m.ID == id && m.Role == role {
			continue // the row on its way out is not a reference to itself
		}
		for _, f := range m.Files {
			if _, ok := want[f.S3Key]; ok {
				used[f.S3Key] = m.Role + "/" + m.ID
			}
		}
	}
	return used, nil
}

// engineKeptBecause names each surviving file and what still points at it, in the order the row
// declared them so the sentence reads against the panel.
func engineKeptBecause(keys []string, keep map[string]string) string {
	out := make([]string, 0, len(keep))
	for _, k := range keys {
		if by, ok := keep[k]; ok {
			out = append(out, k+" is still used by "+by)
		}
	}
	return strings.Join(out, ", ")
}


// healGeometry reads a row's GGUF header out of the bucket and stores it, when the row has none.
//
// The gap it closes: engine_gguf.go reads a header at the RESOLVE, over the upstream URL, and a
// row that never went down that road — registered from the bucket, rebuilt from a ledger key, or
// simply taken in before the read existed — has no geometry and never gets one. Both deployments
// are entirely in that state, which is why every llm row answers `vram_need_source: floor` and
// why a row declaring 262,144 tokens could be switched on without a word.
//
// Silent about every failure on purpose. It is a repair attached to a write the operator asked
// for, and none of its outcomes are that operator's problem: an unconfigured bucket, a refused
// read, a file that is not a GGUF, a header past the ceiling, a losing race with another writer.
// The row simply stays where it was, which is where it already is when this is not called at all.
func (a engineAdminAPI) healGeometry(ctx context.Context, e *engineRuntimeState, id string) {
	cur, ok := engineCatalogModel(ctx, e, id)
	// 🔴 The skip is on the CEILING and not on KVLayers, which is what a row read before these
	// columns existed still has. Migration 0062 stored four numbers and multiplied by all of
	// them; such a row carries a geometry that looks present and prices its cache four times too
	// high, and skipping on "has layers" would leave it that way for ever. context_ceiling
	// arrived with the modifiers (0070), so a non-zero one is the mark of a row this code read.
	//
	// ⚠️ Which makes it "at most once per loading write", not "once": a header declaring no
	// `<arch>.context_length` is read again every time. llama.cpp's converter always writes one,
	// so this is a supported-input assumption rather than a leak — see engine_gguf.go's header.
	//
	// And the widths likewise: a row read before them sizes a sliding or convolutional model as
	// if every layer were full — twice gpt-oss-20b's measured cache — so a row without them is
	// read once more, and a layered read is the mark that it need not be again. A header the
	// per-layer fold refuses (a per-layer array that does not cover every block) therefore stays
	// in the "read again on every loading write" set above: the same bounded cost, and a refused
	// bucket read has to be retried anyway.
	if !ok || engineModelIsLora(cur) || (cur.ContextCeiling > 0 && engineRowGeometry(cur).layered()) {
		return
	}
	key, ok := engineGeometryFile(cur)
	if !ok {
		return
	}
	geom := engineGGUFGeometryOfObject(ctx, key, a.engineStorageBytes())
	if !geom.complete() {
		return
	}
	// 🔴 A targeted write. `cur` was read BEFORE a network round trip to object storage, so it
	// is already stale, and a whole-row Put of it would silently revert anything another writer
	// changed while the read was in flight.
	// cur.Files is the declaration the header was read out of — compared on write, so a
	// replacement that landed while the read was in flight leaves this write with nothing to do.
	if _, err := a.mgr.store.SetEngineModelGeometry(ctx, e.def.Key, id, cur.Files, engineStoreKV(geom)); err != nil {
		log.Printf("engines: %s/%s: the geometry was read and could not be stored (%v)", e.def.Key, id, err)
		return
	}
	e.catalog.invalidate()
	log.Printf("engines: %s/%s: attention geometry read from the bucket (%d layers, per-token width %d full + %d sliding over %d, ceiling %d)",
		e.def.Key, id, geom.Layers, geom.FullWidth, geom.SWAWidth, geom.SlidingWindow, geom.Ceiling)
}
