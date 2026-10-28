package main

// engine_admin_ingest.go — taking a model in: resolve, file listing, the ingest itself and its
// destination checks (ADR 0072 decision 6, ADR 0085).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// --- taking a model in (ADR 0072 decision 6, phase P4) -------------------------

// engineIngestBody is what the panel posts. The SOURCE is one of three shapes; everything else
// is what the catalogue row should say once the bytes are in the bucket.
type engineIngestBody struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// PlanToken is the fingerprint of the plan the person pressed (ADR 0085 decision 4), and it is
	// REQUIRED: the CP re-plans at the press — the form's answer can be minutes old and this is the
	// call that spends money — and a press with no plan behind it is one nobody saw the price of.
	// The refusal sends the caller back through `…/ingest/resolve`, which is where a plan and its
	// token come from.
	PlanToken string             `json:"plan_token"`
	Source    engineIngestSource `json:"source"`

	Description     string   `json:"description"`
	BaseModel       string   `json:"base_model"`
	ContextTokens   int      `json:"context_tokens"`
	MaxOutputTokens int      `json:"max_output_tokens"`
	Sizes           []string `json:"sizes"`
	// Params is what the form says about how to run this model — the author's published
	// settings as a person left them after reading them (engine_params_hint.go fills the form,
	// a human presses the button). Carried through the job and written onto the row the
	// download creates.
	Params *store.EngineParams `json:"params"`
	// LicenseAccepted is REQUIRED, and it is not a formality (ADR 0072 decision 10). A gated
	// repository distributes only to accounts that accepted its terms, and on a multi-tenant
	// deployment the operator accepts on behalf of every member — so the answer is recorded
	// against a person, in the row and in the audit log.
	LicenseAccepted bool `json:"license_accepted"`
}

// resolveIngest (POST …/ingest/resolve) answers "what is this file" without starting anything.
//
// It exists so that the licence, the gating and the size are on screen BEFORE the checkbox that
// accepts the licence — an acceptance offered ahead of the terms is not one.
func (a engineAdminAPI) resolveIngest(w http.ResponseWriter, r *http.Request, g engineIngestGrant) {
	e := a.reg.get(strings.TrimSpace(r.PathValue("key")))
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no such engine"})
		return
	}
	var b engineIngestBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	if aerr := engineSourceAllowedForKind(engineIngestKindFor(e), b.Source); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	res, aerr := engineIngestResolve(r.Context(), b.Source)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// The attention geometry, read HERE as well as at the start of an ingest — the one number
	// this route was missing, and the expensive one to learn late. Measured on a borrowed llm
	// engine: an L4 (24 GB) took 17 GB of weights and then died on `cudaMalloc failed: out of
	// memory ... failed to allocate buffer for kv cache` for the 16 GB the window wanted, four
	// minutes and one purchased GPU after the button was pressed. The weights alone were never
	// the question.
	//
	// Best-effort and silent, exactly as at ingest (engine_gguf.go): a header that cannot be
	// read leaves the field OFF the answer rather than putting a zero on the panel.
	kind := strings.TrimSpace(b.Kind)
	if kind == "" {
		kind = engineIngestKindFor(e)
	}
	geom := engineIngestGeometry(r.Context(), kind, res, a.hfTokens())
	row := engineResolvedRow(res, engineDeploymentTokens{
		hf:      a.hfTokens().configured(r.Context()),
		civitai: a.civitaiTokens().configured(r.Context()),
	}, e.def.Provider, geom)
	// 🔴 The PLAN (ADR 0085 decision 4): what one press would do, priced, with the destination
	// keys the CP has already decided. It is the whole answer to "what will this button cost" —
	// the field-by-field version the form used to assemble a request out of (`family_vae`,
	// `family_main_flag`, `family_parts`) is gone, because every one of those was a decision the
	// form then had to make again.
	plan, v := a.enginePlanFor(r.Context(), g, e, b, res)
	row["plan"] = plan
	// And the other header read, for the image role: does this checkpoint carry the VAE its
	// family decodes with (ADR 0072 follow-up). Here for the same reason the licence is — the
	// fact has to be on screen BEFORE the press, because afterwards it costs a download, a
	// checkpoint switch and a failed generation to learn. Read by the plan, which needs the same
	// answer to decide whether the family's VAE is one of the files.
	if v != engineVaeUnknown {
		row["vae_bundled"] = v
	}
	writeJSON(w, http.StatusOK, row)
}

// engineResolvedRow is what the panel draws before anything is started. `can_ingest` is the
// verdict this route exists for: a gated repository on a deployment with no HF token cannot be
// taken in, and saying so here costs nothing — finding out from a 401 costs a Fargate task and
// a confused administrator.
// engineDeploymentTokens is which accounts this deployment can download AS. Two unrelated
// services, so two bits — and a struct rather than two bools in a row, because the call sites
// that pass them are the ones deciding whether a file is reachable at all.
type engineDeploymentTokens struct {
	hf      bool
	civitai bool
}

func engineResolvedRow(res engineResolved, tokens engineDeploymentTokens, provider string, geom engineKVGeometry) map[string]any {
	row := map[string]any{
		"sha256":         res.SHA256,
		"bytes":          res.Bytes,
		"gated":          res.Gated,
		"commercial_use": engineCommercialUse(res),
		"source":         res.Source,
		// 🔴 Each restriction is answered by ITS OWN account. A Hugging Face token does nothing
		// for a Civitai uploader's login switch, and until the Civitai token was consulted here
		// this said "no" to every login-required asset even on a deployment that had registered
		// one — with the token already wired into the fetch container's `Authorization` header
		// (deploy/aws/ecs/engine-tools/ingest-fetch.sh). The download could have run; the panel
		// refused to start it.
		"can_ingest":               (!res.Gated || tokens.hf) && (!res.LoginRequired || tokens.civitai),
		"deployment_token":         tokens.hf,
		"deployment_civitai_token": tokens.civitai,
	}
	if res.ArtifactIdentity != "" {
		row["artifact_identity"] = res.ArtifactIdentity
	}
	// Told apart from `gated` on purpose: gating is the REPOSITORY's terms, this is a Civitai
	// uploader's switch, and the two are satisfied by accounts on different services.
	//
	// ⚠️ "There is nothing on this deployment that could satisfy it" was true when this note was
	// written and is not any more — engine_civitai_token.go registers the account and the fetch
	// container sends it. What remains true is that the CP cannot tell whether THAT account
	// satisfies THIS uploader (early access is bought per creator), so with a token registered
	// this is a warning and without one it is still a refusal.
	if res.LoginRequired {
		row["login_required"] = true
		if tokens.civitai {
			row["civitai_needs_account"] = true
		}
	}
	// A gated repository WITH a token registered is not yet a yes, and this is the one place
	// that can say so in advance. 🔴 The CP resolves anonymously (decision 6) — it never holds
	// the token — so it cannot ask whether that account accepted THIS repository's terms, and
	// the answer arrives as a 403 on the download instead (measured, ADR 0072 P5 実機検証: one
	// token, FLUX.1-dev through and SD3.5 Medium refused). A warning is therefore all this can
	// honestly be; the CODE for it exists on the failed job, where the status is known.
	if res.Gated && tokens.hf {
		row["gated_needs_acceptance"] = true
	}
	if res.License != "" {
		row["license"] = res.License
	}
	if res.LicenseName != "" {
		row["license_name"] = res.LicenseName
	}
	if res.LicenseURL != "" {
		row["license_url"] = res.LicenseURL
	}
	if res.BaseModel != "" {
		row["base_model"] = res.BaseModel
	}
	// The family this deployment's provider would call that, when it recognises it. A separate
	// field from `base_model` on purpose: one is what the upstream published and the other is a
	// suggestion for the picker, and the day they are folded together is the day an upstream
	// display name gets stored as a family again (ADR 0072 decision 2, P2 実機検証).
	if fam := engineFamilyGuess(provider, res.BaseModel); fam != "" {
		row["base_model_suggest"] = fam
	}
	if len(res.Restrictions) > 0 {
		row["restrictions"] = res.Restrictions
	}
	if len(res.TrainedWords) > 0 {
		row["trained_words"] = res.TrainedWords
	}
	// What the author's own text says about running this, with the sentence it was read out of.
	// Both halves or neither: the numbers are a guess made by a regular expression over somebody
	// else's prose, and the quote is what lets the person at the form see that for themselves.
	if !res.ParamsHint.empty() {
		row["params_hint"] = res.ParamsHint.Params
		if res.ParamsHint.Quote != "" {
			row["params_hint_quote"] = res.ParamsHint.Quote
		}
	}
	// The model's own maximum, offered so nobody reads it off a model card by hand. Sent as
	// what it is — a ceiling, not a setting: 🔴 the 30B in this deployment publishes 262144 and
	// is run at 32768, because the architecture's limit and what fits in an L4 are different
	// questions and only one of them is Hugging Face's to answer.
	if res.ContextLength > 0 {
		row["context_length"] = res.ContextLength
	}
	// What the KV cache costs per 1024 tokens of window. The panel MULTIPLIES this by the
	// window in the form: the cache is linear in the context length, so one number answers
	// every value somebody can type, and the formula itself (engineKVCacheMiB) stays in one
	// place — a second copy of `n_layer × n_head_kv × (k+v) × ctx × 2` in TypeScript is a
	// second thing to keep in step with the day a model declares different key and value
	// widths.
	//
	// 🔴 ABSENT, never 0, when the header was not readable. "Nobody measured it" and "it
	// measured zero" are different facts and this panel draws them differently; a 0 here would
	// be read as a model whose window costs nothing.
	//
	// kv_mib_fixed is the sliding-window layers' share, which the window does NOT multiply —
	// see engineKVPricing.
	if per1k, fixed := engineKVPricing(geom); per1k > 0 || fixed > 0 {
		row["kv_mib_per_1k_tokens"] = per1k
		if fixed > 0 {
			row["kv_mib_fixed"] = fixed
		}
	}
	return row
}

// engineIngestKindFor says what an engine takes in, which is what the file list is filtered by.
// The catalogue's `kind` is the same word the panel sends when it starts one.
func engineIngestKindFor(e *engineRuntimeState) string {
	if e.def.api() == engineAPIImages {
		return "checkpoint"
	}
	return "gguf"
}

// listIngestFiles (POST …/ingest/files) answers "what does this repository offer", so the
// filename is chosen instead of retyped. 🔴 Measured 2026-09-09: a name one letter short
// (`flux1-dev.safetensor`) is refused correctly and looks exactly like a file that is not
// there, and the person is left comparing two strings across two windows.
//
// It starts nothing, like the resolve, and it is the same read: whatever is picked here is
// resolved out of an answer with the same shape a moment later.
func (a engineAdminAPI) listIngestFiles(w http.ResponseWriter, r *http.Request, _ engineIngestGrant) {
	e := a.reg.get(strings.TrimSpace(r.PathValue("key")))
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no such engine"})
		return
	}
	var b engineIngestBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	if aerr := engineSourceAllowedForKind(engineIngestKindFor(e), b.Source); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	files, aerr := engineIngestList(r.Context(), b.Source, engineIngestKindFor(e))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	answer := map[string]any{"files": files}
	// What the window costs, for the whole repository at once (ADR 0089). The panel prices every
	// quantisation in the list against the chosen instance class from this ONE number, because
	// the KV cache is decided by the attention geometry and the window — neither of which the
	// weights' quantisation changes.
	//
	// 🔴 Read from ONE file and reported with its name. Measured on unsloth/Qwen3.8-27B-GGUF
	// (2026-09-18) the geometry is not quite identical across a repository's own builds —
	// `UD-IQ2_S` declares 64 blocks and `UD-IQ4_XS` declares 65, a 1.6% difference in the cache —
	// so this is a repository-wide estimate and says which file it came from rather than
	// pretending to be every file's answer. The row that is actually taken in gets its own
	// header read at the resolve, which is the number the press is priced from.
	if per1k, fixed, from := a.engineListKV(r.Context(), b.Source, files, engineIngestKindFor(e)); from != "" {
		answer["kv_mib_per_1k_tokens"] = per1k
		if fixed > 0 {
			answer["kv_mib_fixed"] = fixed
		}
		answer["kv_from"] = from
	}
	writeJSON(w, http.StatusOK, answer)
}

// engineListKV reads one candidate's GGUF header so the whole list can be priced.
//
// Best-effort and silent, like every other geometry read: a repository that will not answer a
// ranged GET leaves the field off, and the panel then prices weights alone and says so. It costs
// one 1 MiB request per listing, which is what makes a thirty-file repository affordable to show
// at all — thirty header reads would be thirty.
func (a engineAdminAPI) engineListKV(ctx context.Context, src engineIngestSource,
	files []engineCandidate, kind string) (per1k, fixed int, from string) {
	if !strings.EqualFold(strings.TrimSpace(kind), "gguf") {
		return 0, 0, ""
	}
	for _, f := range files {
		if f.Role != engineCandidateModel {
			continue
		}
		// Resolved rather than composed: the download URL is the source's own business, and
		// composing a second one here is a second spelling to keep in step.
		picked := src
		switch {
		case picked.HF != nil:
			hf := *picked.HF
			hf.File = f.Name
			picked.HF = &hf
		case picked.Civitai != nil:
			civitai := *picked.Civitai
			civitai.File = f.Name
			picked.Civitai = &civitai
		default:
			return 0, 0, ""
		}
		res, aerr := engineIngestResolve(ctx, picked)
		if aerr != nil {
			return 0, 0, ""
		}
		geom := engineIngestGeometry(ctx, kind, res, a.hfTokens())
		if per1k, fixed := engineKVPricing(geom); per1k > 0 || fixed > 0 {
			return per1k, fixed, f.Name
		}
		return 0, 0, ""
	}
	return 0, 0, ""
}

// postIngest (POST …/ingest) is the one press (ADR 0085 decisions 1, 3 and 4).
//
// It resolves the source, re-plans what taking it in would do (engine_plan.go), refuses a plan
// the person cannot have been looking at, and then does exactly what the plan says: the main file
// downloaded, declared or moved, and the family's other files promised as follow-ups.
//
// 🔴 The re-plan is not a formality. The form's answer can be minutes old, and between the two
// calls a key can become held, a licence can change and an object can be purged — this is the
// call that spends a Fargate task and gigabytes of egress, so it decides from a plan it made
// itself.
func (a engineAdminAPI) postIngest(w http.ResponseWriter, r *http.Request, g engineIngestGrant) {
	key := strings.TrimSpace(r.PathValue("key"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	// There is no bucket and no active set on this side for a borrowed role, so an ingest here
	// would stage a file for an engine that will never read it.
	if a.refuseBorrowedWrite(w, e, "taking a model in") {
		return
	}
	ing := a.reg.ingester()
	if ing == nil {
		writeAPIErr(w, &apiError{http.StatusServiceUnavailable, errCodeIngestUnavailable,
			"this deployment's engine stack declares no ingest task — stage the file by hand and register it"})
		return
	}
	var b engineIngestBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	if aerr := engineSourceAllowedForKind(engineIngestKindFor(e), b.Source); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// 🔴 A press with no plan behind it (ADR 0085 decision 4). The plan is what prices the act and
	// what names the destination, so a request without one is asking this route to decide both
	// silently — which is the shape every wall in this ADR's table was found in. Answered as
	// "resolve again" rather than with `next`, because the act it is missing is not one the
	// Console performs on a row: it is the read that makes the card.
	if strings.TrimSpace(b.PlanToken) == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody,
			"plan_token is required: ask POST /api/admin/engines/" + key + "/ingest/resolve what taking " +
				"this source in would do, show the person that plan, and send back its plan_token"})
		return
	}
	if !b.LicenseAccepted {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeIngestNotAccepted,
			"the licence has to be accepted before a model is taken in"})
		return
	}
	res, aerr := engineIngestResolve(r.Context(), b.Source)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	plan, vae := a.enginePlanFor(r.Context(), g, e, b, res)
	main, ok := plan.main()
	if !ok {
		writeAPIErr(w, internalErr(errors.New("the plan named no file to take in")))
		return
	}
	id := plan.ID
	// 🔴 The plan the person pressed against the plan that holds now. A mismatch answers with the
	// fresh plan beside the refusal: telling somebody their answer is stale without handing them
	// the current one only makes them ask again, and between the two calls it can go stale again.
	if tok := strings.TrimSpace(b.PlanToken); tok != plan.PlanToken {
		writeAPIRefusalWith(w, refuse(http.StatusConflict, errCodeEnginePlanStale,
			"what taking "+main.Name+" in would do has changed since that plan was made ("+
				enginePlanChanged(plan)+"): look at the plan beside this error and press again",
			&apiHolder{Kind: "object", Key: main.Key}, &apiNext{Act: "wait", Target: id}), "plan", plan)
		return
	}
	// ⚠️ Refused BEFORE a task is started, for the same reason the gated case below is: without an
	// account the download is the bare `curl: (22) … 401` nine minutes into a Fargate task that
	// ADR 0072 P2 欠落 5 measured. Asked only of a plan that will actually fetch — bytes this
	// deployment already holds were paid for under an account it no longer needs.
	if main.Action == enginePlanDownload {
		if res.LoginRequired && !ing.civitai().configured(r.Context()) {
			writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeIngestCivitaiLogin,
				"the person who uploaded this asset requires a logged-in account to download it, and this " +
					"deployment has no Civitai token registered — register one below, pick another asset, " +
					"or stage the file by hand and register it"})
			return
		}
		if res.Gated && !ing.hfTokens().configured(r.Context()) {
			writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeIngestGatedNoToken,
				"that repository is gated: accept its terms on Hugging Face with the operator's account " +
					"and register that account's token below — it is read by the ingest task only"})
			return
		}
	}
	// The family, which the provider dispatches a workflow on. A row written without one is
	// registered, enabled, offered in generate_image's list — and fails at generation with a
	// message about a template, minutes and a cold start later (ADR 0072 decision 2).
	if !strings.EqualFold(strings.TrimSpace(b.Kind), engineModelKindLora) &&
		!engineBaseModelValid(e.def.Provider, plan.BaseModel) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, fmt.Sprintf(
			"declare base_model as one of %s: this engine runs %s, which picks a workflow by family"+
				" and will not guess one%s",
			strings.Join(engineBaseModelsFor(e.def.Provider), ", "), e.def.Provider,
			engineBaseModelHint(res.BaseModel))})
		return
	}
	// 🔴 An id already in the catalogue is REFUSED, because the row is written by PutEngineModel
	// and that is an upsert on (role, id): the job would download for minutes and then replace a
	// working row's files, licence and enabled state with the new ones, and nobody would connect
	// the two events. Only an id the REQUEST named can reach this — a proposed one is made unique
	// against the catalogue by the plan.
	for _, m := range e.catalog.list(r.Context()) {
		if m.ID != id {
			continue
		}
		writeAPIRefusal(w, refuse(http.StatusConflict, errCodeIngestIDExists,
			"this engine already has a model called "+id+" — press 揃える on that row to give it what it is"+
				" missing, or take this one in under another id",
			&apiHolder{Kind: "row", ID: id}, &apiNext{Act: "complete", Target: id}))
		return
	}
	// The upload task writes its destination before this request can install the catalogue
	// transition, so a key another row or any tenant's job already names is not a vacant
	// filename. Not asked of a reuse: there the destination IS where the bytes are, and whatever
	// record vouched for them is exactly what would answer "taken".
	if main.Action != enginePlanReuse {
		if a.mgr == nil || a.mgr.store == nil {
			writeAPIErr(w, internalErr(errors.New("no store")))
			return
		}
		if ref := engineIngestDestinationUnused(r.Context(), a.mgr.store, a.mgr.store,
			a.engineStorageBytes(), key, main.Key); ref != nil {
			writeAPIRefusal(w, ref)
			return
		}
	}
	// The attention geometry, read from the header of the file about to be taken in — the one
	// moment it can be had, since the CP's S3 port exposes metadata rather than object bytes.
	// Best-effort by design: anything that cannot be read leaves the row at its floor.
	geom := engineIngestGeometry(r.Context(), b.Kind, res, ing.hfTokens())
	// For bytes this deployment already holds, what it recorded about them the first time beats
	// what could be read now: the header read above may have been refused, and the file has not
	// changed since somebody paid for it.
	if main.known != nil {
		// Unless what was recorded predates the per-layer read and this one has it: that record
		// sizes a sliding or convolutional model as if every layer were full.
		if main.known.KVGeom != (engineKVGeometry{}) && (main.known.KVGeom.layered() || !geom.layered()) {
			geom = main.known.KVGeom
		}
		if main.known.VaeBundled != "" {
			vae = main.known.VaeBundled
		}
	}
	followUps, moves := enginePlanFollowUps(plan)
	req := engineIngestRequest{
		Role: key, ModelID: id, Kind: strings.TrimSpace(b.Kind), S3Key: main.Key,
		KVGeom:        geom,
		Description:   strings.TrimSpace(b.Description),
		BaseModel:     plan.BaseModel,
		ContextTokens: b.ContextTokens, MaxOutput: b.MaxOutputTokens, Sizes: b.Sizes,
		Params: engineParamsClean(b.Params),
		// The acceptance, as the tuple ADR 0072 open question 11 asks for. The licence is
		// carried as a STRING rather than re-read from the row later: it is what was on
		// screen when the box was ticked, and upstream relicensing must not rewrite what
		// somebody agreed to.
		AcceptedBy: g.ident.ID, AcceptedTenant: g.tenantID,
		AcceptedLicense: engineLicenceLabel(res),
		Resolved:        res,
		FileFlag:        main.Flag,
		VaeBundled:      vae, PartsFollowUp: followUps, PartsMove: moves,
	}
	if main.Action == enginePlanMove {
		req.MoveFrom = main.Source
	}
	var job store.EngineIngestJob
	switch main.Action {
	case enginePlanReuse, enginePlanMove:
		job, aerr = ing.reuse(r.Context(), req)
	default:
		job, aerr = ing.start(r.Context(), req)
	}
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// The acceptance is audited whether or not the download later succeeds: the person agreed
	// to the terms at this moment, and that is true even if Hugging Face then times out.
	// Scoped to the granting tenant, so a tenant_admin's ingest is readable in that tenant's
	// own audit view rather than only in the deployment-wide one.
	auditTarget := id + " from " + res.Source + " (licence " + engineLicenceLabel(res) + " accepted)"
	if main.Action != enginePlanDownload {
		auditTarget += " by " + main.Action + " of " + main.Source
	}
	a.auditFor(r, g, "engine."+key+".ingest", auditTarget)
	row := engineIngestJobRow(job)
	// Which of the three happened (ADR 0085 decision 1). The job row cannot say it — a move and a
	// download are the same task to the reconciler — and it is the one thing a person watching
	// this press wants to know before the progress bar does anything.
	row["action"] = main.Action
	writeJSON(w, http.StatusOK, row)
}

// enginePlanFollowUps turns everything after the main file into the promises the reconciler keeps
// once the row exists (engineIngester.followUpVae).
//
// Two values because the follow-up carries no origin of its own: `moves` is, per flag, the key a
// part's bytes are at today, and it is what turns that second job into a server-side `aws s3 mv`
// rather than a download of bytes this deployment has already bought.
func enginePlanFollowUps(plan enginePlan) ([]engineVaeFollowUp, map[string]string) {
	if len(plan.Files) < 2 {
		return nil, nil
	}
	var out []engineVaeFollowUp
	moves := map[string]string{}
	for _, f := range plan.Files[1:] {
		fu := engineVaeFollowUp{Flag: f.Flag, S3Key: f.Key, Resolved: f.resolved}
		switch f.Action {
		case enginePlanReuse:
			fu.Staged, fu.Bytes = true, f.Bytes
			fu.Source, fu.ArtifactIdentity = f.resolved.Source, f.resolved.ArtifactIdentity
			if f.known != nil && f.known.Source != "" {
				fu.Source = f.known.Source
			}
		case enginePlanMove:
			moves[f.Flag] = f.Source
		default:
			// download: carry the conflict reason so followUpFile skips the RunTask rather
			// than racing a refusal in the reconciler where nobody can see it.
			fu.Conflict = f.conflict
		}
		out = append(out, fu)
	}
	if len(moves) == 0 {
		moves = nil
	}
	return out, moves
}

// enginePlanChanged is the one sentence a stale plan owes its reader: what the CP would do NOW,
// as a cost and a count, so the refusal is not "something changed, look again".
func enginePlanChanged(plan enginePlan) string {
	acts := map[string]int{}
	for _, f := range plan.Files {
		acts[f.Action]++
	}
	return fmt.Sprintf("%d file(s), %d to download, %d already here, %d to move, %d bytes",
		len(plan.Files), acts[enginePlanDownload], acts[enginePlanReuse], acts[enginePlanMove],
		plan.BytesToDownload)
}

// deleteIngest (DELETE …/ingest/{id}) forgets ONE job of the history.
//
// It is what a `failed` entry in the ledger is dismissed with (ADR 0085 decision 6), and the last
// route of the job history to survive it. There is no timer and no prune, and
// store.DeleteEngineIngestJob says why: a `done` row is the only written record of an S3 key
// until a catalogue row points at it.
//
// Three refusals, and they are deliberately not three sentences:
//
//   - `pending` / `running` → 409, and this is the one that matters. Deleting the ROW does not
//     stop the TASK: the ECS task keeps downloading, finishes, and writes its catalogue row
//     with nobody waiting for it — a model appearing out of nothing minutes after somebody
//     deleted the only trace of where it came from. The reconciler would also have no row left
//     to move to `done`, so the outcome (and a sha256 mismatch, if that is what happened) is
//     lost;
//   - another ROLE's job, another TENANT's job, and an id that never existed → all 404 with the
//     same words. The ledger a granted tenant_admin reads is narrowed to their own tenant (ADR
//     0072 open question 11) and the delete has to be narrowed by the same rule, or the reduced
//     panel is a read-only view of one tenant with a delete button for every tenant. 404 and
//     not 403 because "you may not touch job X" tells a tenant_admin that job X exists.
//
// 🔴 Not refused for a BORROWED role, unlike every write next door. The job ledger is this
// deployment's own — the far catalogue is mirrored, its jobs are not — so refusing here would
// strand the rows a role left behind when it became borrowed. The panel still offers no button
// there, because that whole screen is read-only for a mirror.
func (a engineAdminAPI) deleteIngest(w http.ResponseWriter, r *http.Request, g engineIngestGrant) {
	key := strings.TrimSpace(r.PathValue("key"))
	id := strings.TrimSpace(r.PathValue("id"))
	if a.reg.get(key) == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	if a.mgr == nil || a.mgr.store == nil {
		writeAPIErr(w, internalErr(errors.New("no store")))
		return
	}
	j, ok, err := a.mgr.store.GetEngineIngestJob(r.Context(), id)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || j.Role != key || (!g.super && j.TenantID != g.tenantID) {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeIngestJobUnknown,
			"no ingest job " + id + " for engine " + key})
		return
	}
	if j.State == store.EngineIngestPending || j.State == store.EngineIngestRunning {
		writeAPIRefusal(w, refuse(http.StatusConflict, errCodeIngestJobLive,
			"that job is still running: forgetting the row would not stop the task, which keeps"+
				" downloading and writes its catalogue row when it finishes — wait for it to end,"+
				" and forget it then",
			&apiHolder{Kind: "task", ID: id, Key: j.S3Key}, &apiNext{Act: "wait", Target: id}))
		return
	}
	if _, err := a.mgr.store.DeleteEngineIngestJob(r.Context(), id); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// Audited with the KEY in it, because that is the fact the deletion destroys: after this
	// row is gone the audit line is the last place the s3 key of a file still in the bucket is
	// written down.
	a.auditFor(r, g, "engine."+key+".ingest",
		"forget job "+id+" ("+j.ModelID+" "+j.S3Key+", "+j.State+")")
	// The jobs that are left, so the panel does not have to ask again — and so what it draws is
	// the server's answer rather than one it edited locally.
	body, err := a.ingestListBody(r, g, key)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// ingestListBody is the `{"jobs": …}` answer the delete gives back, narrowed to the caller's
// authority — the same narrowing the ledger applies, so dismissing a job cannot show a caller
// jobs the ledger would have hidden.
func (a engineAdminAPI) ingestListBody(r *http.Request, g engineIngestGrant, key string) (map[string]any, error) {
	jobs, err := a.ingestJobsFor(r, g, key)
	if err != nil {
		return nil, err
	}
	// EVERY role's rows, not this one's: a text encoder taken in under `image` is pointed at by
	// rows of whatever role loads it, and "is this key written down anywhere" has to mean
	// anywhere. A read that FAILS is not fatal to the list — the jobs are what this route is
	// for — and leaves every row saying nothing points at it, which is the cautious half: the
	// panel warns harder before forgetting.
	rows, rerr := a.mgr.store.ListEngineModels(r.Context(), "")
	if rerr != nil {
		log.Printf("engines: ingest list could not read the catalogue (%v) — job rows will not say what still uses their files", rerr)
		rows = nil
	}
	out := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		row := engineIngestJobRow(j)
		if by := engineIngestKeyUsedBy(rows, j.S3Key); by != "" {
			row["key_used_by"] = by
		}
		out = append(out, row)
	}
	return map[string]any{"jobs": out}, nil
}

// engineIngestKeyUsedBy names the catalogue row that already points at this job's file, or "".
//
// One answer to two questions the panel asks in opposite directions:
//
//   - registering the key AGAIN: a shared key is NORMAL and must not be refused. Decision 2
//     says so on purpose — `text_encoders/` is one file SD3.5 and FLUX.1 both read (measured on
//     af-sandbox: `clip_l.safetensors` pointed at by two rows) — so the panel names who has it
//     rather than blocking;
//   - FORGETTING the job: while nothing points at the key, this row is the last written record
//     of it, and deleting it leaves bytes in the bucket that nothing can name again.
//
// 🔴 It says who POINTS at the file. It never says the file is there: only the ledger's own read
// of the bucket does (engine_objects.go), and `deleteModel?purge=1` can delete the bytes while
// leaving the job `done` for ever.
func engineIngestKeyUsedBy(rows []store.EngineModel, s3key string) string {
	if strings.TrimSpace(s3key) == "" {
		return ""
	}
	for _, m := range rows {
		for _, f := range m.Files {
			if f.S3Key == s3key {
				return m.Role + "/" + m.ID
			}
		}
	}
	return ""
}

// engineIngestJobRow is one job as the panel reads it. Nearly all of the SPEC stays off the
// wire: it is this process's own shape, and a job list is not where a catalogue row is edited.
//
// Two of its fields do ride along, and only because a finished job is how a file that is
// already in the bucket gets registered again — the panel fills `POST /models` from this row
// (the bytes are staged, so re-taking it in would be an ingest of something already here). What
// that form cannot derive from a key is what the file was taken in AS, and both ways of getting
// it wrong are silent until the next cold start: a LoRA registered as a checkpoint is a row
// that starts nothing, and a text encoder registered with no flag becomes the checkpoint of its
// own row.
func engineIngestJobRow(j store.EngineIngestJob) map[string]any {
	row := map[string]any{
		"id": j.ID, "model_id": j.ModelID, "s3_key": j.S3Key,
		"source": j.Source, "state": j.State, "created_at": j.CreatedAt,
	}
	// A job started before a field existed decodes with it empty, and a spec that does not
	// parse leaves both empty. Either way the form opens with one fewer answer filled in —
	// never with a wrong one.
	var spec struct{ Kind, FileFlag string }
	if json.Unmarshal([]byte(j.Spec), &spec) == nil {
		if spec.Kind != "" {
			row["kind"] = spec.Kind
		}
		if spec.FileFlag != "" {
			row["file_flag"] = spec.FileFlag
		}
	}
	if j.Message != "" {
		row["message"] = j.Message
	}
	if j.Bytes > 0 {
		row["bytes"] = j.Bytes
	}
	// What the operator has to DO about it, when the task's own words say. The message stays as
	// it is — it is the task's sentence and sometimes the only detail there is — and this rides
	// beside it so the panel can add the action in the reader's language.
	if code := engineIngestFailureCode(j.Source, j.Message); code != "" {
		row["code"] = code
	}
	return row
}

func engineLicenceLabel(res engineResolved) string {
	return engineFirstNonEmpty(res.LicenseName, res.License, "unstated")
}

func engineFirstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// engineIngestDestinationUnused refuses an upload destination before RunTask is called. S3 has no
// compare-and-swap with the catalogue transition, so treating a recorded key as reusable for a
// different download would let the upload overwrite it even when the later installation fails.
// Job rows are checked across tenants without revealing which tenant recorded the key: tenant
// visibility applies to the panel, not to protecting a shared role prefix from an overwrite.
//
// 🔴 A finished job holds the key only while BYTES ARE AT IT. Decision 6's holder is an object, or
// a task that could still write one — and a `done` job is provenance ON an object, not a record OF
// one, which is the same sentence the ledger already applies on the read side (engineLedgerJoin).
// Without the bucket read below, 消す on an orphan left the key permanently unusable: the object
// was gone, the ledger correctly stopped drawing it, and taking the same model in again was
// refused with "it holds bytes a record still describes" — a claim about bytes that no longer
// existed, whose only offered way out was `dismiss_job`. Measured on af-sandbox 2026-09-16 (build
// e370f0e0) with `nuclearAnimeHybridSfw_v1NoVAE.safetensors`, deleted minutes earlier from the
// same panel.
//
// `storage` may be nil, and an unreadable bucket keeps the key HELD: an inability to look is not
// proof the bytes are gone, and the refusal then says which of the two it is (the direction that
// costs a retry, rather than the one that overwrites a file somebody paid for).
func engineIngestDestinationUnused(ctx context.Context, models store.EngineModelStore, jobs store.EngineIngestStore,
	storage *engineStorage, role, s3key string) *apiRefusal {
	if models == nil || jobs == nil {
		return &apiRefusal{apiError: internalErr(errors.New("no store"))}
	}
	// Scan every role so an old malformed row pointing across role prefixes remains protected.
	rows, err := models.ListEngineModels(ctx, "")
	if err != nil {
		return &apiRefusal{apiError: internalErr(err)}
	}
	return enginePartDestinationCheck(ctx, rows, jobs, storage, role, s3key)
}

// enginePartDestinationCheck is the row-and-job guard with a pre-fetched row list, so the parts
// loop (enginePlanFor) can check each download key without re-listing the whole catalogue per key.
// The row list must have been fetched with an empty role filter (all roles), for the same reason
// engineIngestDestinationUnused uses one: an old malformed row pointing across role prefixes must
// still be protected.
func enginePartDestinationCheck(ctx context.Context, rows []store.EngineModel, jobs store.EngineIngestStore,
	storage *engineStorage, role, s3key string) *apiRefusal {
	for _, model := range rows {
		for _, file := range model.Files {
			if strings.TrimSpace(file.S3Key) == s3key {
				return ingestDestinationTaken(s3key, "the row "+model.ID+" declares it",
					&apiHolder{Kind: "row", ID: model.ID, Key: s3key},
					&apiNext{Act: "complete", Target: model.ID})
			}
		}
	}
	if jobs == nil {
		return nil
	}
	job, recorded, err := jobs.EngineIngestJobForS3Key(ctx, role, s3key)
	if err != nil {
		return &apiRefusal{apiError: internalErr(err)}
	}
	if !recorded {
		return nil
	}
	switch storage.verify(ctx, s3key).State {
	case engineStorageMissing:
		// The job is a memory of bytes that are not there. Nothing can be overwritten, so nothing
		// is refused — and the operator never learns this job exists, which is right: it is not a
		// thing they did wrong.
		return nil
	case engineStoragePresent:
		return ingestDestinationTaken(s3key, "an earlier ingest job recorded it",
			&apiHolder{Kind: "job", ID: job.ID, Key: s3key},
			&apiNext{Act: "dismiss_job", Target: job.ID})
	default:
		return ingestDestinationUnverifiable(s3key, job.ID)
	}
}

// ingestDestinationTaken names WHO holds the key, because the two holders have different ways
// out and the refusal used to offer neither: a row is completed (or another destination chosen),
// while a job is dismissed from the ingest list. Without that, an operator reading "already
// recorded" has nothing to look for — reported from af-sandbox 2026-09-15, where the key was
// held by a failed attempt at the very repair being retried.
//
// The sentence stays for a log and for a reader with no Console; the two fields are what the
// Console turns into the button on the error line (ADR 0085 decision 5).
func ingestDestinationTaken(s3key, by string, holder *apiHolder, next *apiNext) *apiRefusal {
	return refuse(http.StatusConflict, errCodeEngineBadBody,
		"the S3 key "+s3key+" is already recorded ("+by+"); it holds bytes a record still describes, so this"+
			" download would overwrite them", holder, next)
}

// ingestDestinationUnverifiable is the same refusal with the one difference that decides what the
// operator should do: the bytes were not seen. It is a separate sentence rather than the one above
// because that one asserts the object is there, and asserting it out of a HeadObject that was
// refused or timed out is how "dismiss the job" gets pressed on a key nobody looked at.
//
// The next act is `wait`, not `dismiss_job`: forgetting the job would remove the deployment's only
// written address for bytes that may well still be in the bucket, and the question here is not one
// the operator can answer either. It carries no target — what is being waited for is the BUCKET,
// not the job — while the holder still names the job, because that is what a person reading this
// has to go and look at.
func ingestDestinationUnverifiable(s3key, jobID string) *apiRefusal {
	return refuse(http.StatusConflict, errCodeEngineBadBody,
		"the S3 key "+s3key+" is recorded by an earlier ingest job, and the bucket could not be asked whether"+
			" anything is still at it — access denied, a missing configuration and a timeout all answer this way."+
			" Not being able to look is not proof the key is free, so the download is refused rather than"+
			" allowed to overwrite a file somebody paid for. Press again once the bucket answers",
		&apiHolder{Kind: "job", ID: jobID, Key: s3key}, &apiNext{Act: "wait"})
}

// engineBaseModelHint quotes what the repository called this model, so the refusal above ends
// with the one fact that makes the choice obvious. Silent when the repository said nothing —
// a dangling "(the repository says "")" would be noise where the operator needs a decision.
func engineBaseModelHint(upstream string) string {
	if u := strings.TrimSpace(upstream); u != "" {
		return fmt.Sprintf(" (the repository calls it %q)", u)
	}
	return ""
}
