package main

// engine_admin.go — the super-admin toggle for the self-hosted inference engines
// (ADR 0071). The same shape as the VOICEVOX one in tts.go, and deliberately so: the two
// answer the same question about the same kind of thing, and an operator who has learned one
// panel should not have to learn a second.
//
// Why it exists at all. The mode has always been read from a stored setting — `engineMode`,
// via `engineRuntimeState.mode`, which BOTH the controller and the gateway consult — but
// nothing ever wrote it. The only way to switch an engine off was `LlmMode` / `ImageMode` on
// the 60-engines stack, i.e. a CloudFormation run, which is not a control anyone reaches for
// when a GPU is misbehaving at 3am. This adds the missing half: a route that writes the
// setting the rest of the system already obeys.
//
// What "off" means here is worth stating, because it is more than "stop the box":
//   - the engine disappears from `/internal/engine/catalog`, so a Workspace stops writing it
//     into opencode's provider config and `generate_image` stops offering it;
//   - `/engine/<key>/v1/*` answers `503 engine_off` — a refusal, NOT the `engine_waking` the
//     caller retries on (ADR 0071 decision 5);
//   - the controller stops it and keeps it stopped.

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

// engineAdminAPI is the handler set behind the toggle. The stored setting is the only source
// of truth for the mode; the desired count is what the deployment is doing about it, and the
// two are reported separately — the distinction tts.go had to learn (a panel that derives the
// toggle from ECS appears to move on its own the moment the engine stops itself).
type engineAdminAPI struct {
	memberAuth
	reg      *engineRegistry
	settings store.SettingsStore // may be nil (tests)
}

// refuseBorrowedWrite answers 400 and reports true when this engine's catalogue is somebody
// else's to edit (ADR 0079 decision 7).
//
// 🔴 This is where the refusal HAS to live, and ADR 0079's review is why the draft had it in the
// wrong place. The plan was to hand a borrowed row an implementation of store.EngineModelStore
// whose writes refuse — which would refuse nothing, because no write travels through a row's
// catalogue at all: every one of them addresses `a.mgr.store` directly, keyed by the role in the
// request path. The read is redirected (engineCatalog.source); the write is stopped here.
//
// The message names the far deployment rather than saying "not allowed": the operator's next act
// is to go and do it over there, and a refusal that does not say where is a dead end.
func (a engineAdminAPI) refuseBorrowedWrite(w http.ResponseWriter, e *engineRuntimeState, what string) bool {
	if e == nil || !e.def.remote() {
		return false
	}
	writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineNotOurs,
		"engine " + e.def.Key + " is borrowed from " + e.def.URL + ", so " + what +
			" is that deployment's to change — this panel mirrors its catalogue read-only"})
	return true
}

func registerEngineAdminRoutes(mux *http.ServeMux, cfg config, reg *engineRegistry) {
	var settings store.SettingsStore
	if cfg.mgr != nil && cfg.mgr.store != nil {
		settings = cfg.mgr.store
	}
	a := engineAdminAPI{memberAuth{cfg.mgr}, reg, settings}
	// The engine list. NOT super_admin-only: a tenant_admin of a tenant the operator granted
	// `allow_engine_ingest` gets a SUBSET of the same row, which is what the ingest form and the
	// catalogue list are drawn from (ADR 0072 open question 11). Same predicate as the ingest
	// routes below, so there is one answer to "may this person take a model in" and not two.
	mux.HandleFunc("GET /api/admin/engines", a.withIngestAdmin(a.get))
	mux.HandleFunc("PUT /api/admin/engines/{key}", a.withSuperAdmin(a.put))
	mux.HandleFunc("PUT /api/admin/engines/{key}/idle", a.withSuperAdmin(a.putIdle))
	mux.HandleFunc("GET /api/admin/engines/{key}/hourly", a.withSuperAdmin(a.uptime))
	// Whose work the engine was doing (ADR 0079 open question 7). A separate route from
	// /hourly next door because it answers a separate question: that one says the GPU was up,
	// this one says who for — and on a deployment that LENDS its engines it is the only place
	// that answer exists at all.
	mux.HandleFunc("GET /api/admin/engines/{key}/attribution", a.withSuperAdmin(a.attribution))
	// The GPU this role buys (ADR 0074). A separate route from the mode toggle above because
	// it is a separate act with a separate cost: the mode buys a box now, this says what the
	// NEXT box will be.
	mux.HandleFunc("PUT /api/admin/engines/{key}/class", a.withSuperAdmin(a.putClass))
	// Whether this role may buy an INTERRUPTIBLE box. Its own route beside the class for the
	// same reason the class is its own route beside the mode: it is a separate act, and the one
	// being consented to here is not "which card" but "this engine may be taken away mid-answer".
	mux.HandleFunc("PUT /api/admin/engines/{key}/spot", a.withSuperAdmin(a.putSpot))
	// What this deployment excludes from every image this engine makes (ADR 0072 follow-up,
	// negative prompts). Super-admin like the mode and the class: it is a statement about the
	// whole deployment, not about one model or one member.
	mux.HandleFunc("PUT /api/admin/engines/{key}/negative", a.withSuperAdmin(a.putNegative))
	// Stopping the BOX without changing the mode — the second half of a class change, since a
	// new rung reaches new instances only.
	mux.HandleFunc("POST /api/admin/engines/{key}/replace-box", a.withSuperAdmin(a.replaceBox))
	// The model catalogue (ADR 0072 decision 7). A CP route, not an Agent one, so it needs no
	// entry in the agent-proxy allowlist in routes.go — that list exists for endpoints the
	// Workspace Agent implements and the CP forwards.
	mux.HandleFunc("PUT /api/admin/engines/{key}/models/{id}", a.withSuperAdmin(a.putModel))
	// Registering a file that is ALREADY in the bucket, and forgetting one. The ingest below
	// fetches; this only writes down what a staged file is, and it stays because it is the
	// route that needs no `ecs:RunTask` and works on a deployment whose egress is closed.
	mux.HandleFunc("POST /api/admin/engines/{key}/models", a.withSuperAdmin(a.postModel))
	mux.HandleFunc("DELETE /api/admin/engines/{key}/models/{id}", a.withSuperAdmin(a.deleteModel))
	// The discovery button (ADR 0082 decisions 6 and 7): what an external ComfyUI's own
	// checkpoint/LoRA/VAE folders currently hold, read off its /object_info and offered as
	// candidates. Under ingest authority, not super_admin only — the same predicate the ingest
	// form itself uses, since this is the other way a row's files get chosen rather than typed.
	mux.HandleFunc("POST /api/admin/engines/{key}/discover", a.withIngestAdmin(a.discoverModelsGrant))
	// Taking a model IN from Hugging Face / Civitai / a URL (ADR 0072 decision 6, phase P4).
	//
	// These are the ONLY engine routes that are not super_admin: a tenant_admin of a tenant
	// the operator granted `allow_engine_ingest` may drive them too (ADR 0072 open question 11 —
	// engine_ingest_perm.go says why the axis stops here).
	mux.HandleFunc("POST /api/admin/engines/{key}/ingest", a.withIngestAdmin(a.postIngest))
	// And dismissing a job. The list itself is gone (ADR 0085 decision 6 — a job is its
	// destination object's progress in the ledger, not a second list), but a `failed` one is an
	// entry on that key with exactly one act, and this is it. Under the same authority as the
	// ledger rather than super_admin, because the rule is "your own jobs": a granted tenant_admin
	// may forget theirs, and deleteIngest narrows by the same tenant the ledger does.
	mux.HandleFunc("DELETE /api/admin/engines/{key}/ingest/{id}", a.withIngestAdmin(a.deleteIngest))
	// Resolving a source WITHOUT starting anything: what the licence is, whether the repository
	// is gated, how big the file is. The panel calls it while somebody is typing, so that the
	// licence they are about to accept is on screen BEFORE the button that accepts it.
	mux.HandleFunc("POST /api/admin/engines/{key}/ingest/resolve", a.withIngestAdmin(a.resolveIngest))
	// And what the repository HAS, so the filename is picked rather than copied by hand across
	// two windows — the same read, filtered to the files this engine could actually load.
	mux.HandleFunc("POST /api/admin/engines/{key}/ingest/files", a.withIngestAdmin(a.listIngestFiles))
	// And WHICH repository, for somebody who does not already know the name (ADR 0072
	// decision 11). Reads only, filtered to what this engine could load.
	mux.HandleFunc("POST /api/admin/engines/{key}/ingest/search", a.withIngestAdmin(a.searchIngest))
	// A card identifies a repository/model; this read expands it into the versions that the
	// single-operation file picker can choose from.
	mux.HandleFunc("POST /api/admin/engines/{key}/ingest/versions", a.withIngestAdmin(a.versionsIngest))
	// The same read with no engine in the path: a deployment that has not adopted 60-engines
	// has an EMPTY panel, and "there is nothing here" is the worst answer to "what could I
	// run?". Browsing needs no engine because it needs no token, no bucket and no task.
	mux.HandleFunc("POST /api/admin/engines/search", a.withIngestAdmin(a.browseSearch))
	// The operator's Hugging Face token (ADR 0072 decision 6 as revised, phase P5). Not under
	// {key}: one token serves every role, because one ingest task does. There is no GET that
	// returns it — only whether one is registered, by whom and when.
	mux.HandleFunc("GET /api/admin/engines/hf-token", a.withSuperAdmin(a.getHfToken))
	mux.HandleFunc("PUT /api/admin/engines/hf-token", a.withSuperAdmin(a.putHfToken))
	mux.HandleFunc("DELETE /api/admin/engines/hf-token", a.withSuperAdmin(a.deleteHfToken))
	// The operator's Civitai token (engine_civitai_token.go), the same shape and the same
	// reason: one account serves every role, because one ingest task does.
	mux.HandleFunc("GET /api/admin/engines/civitai-token", a.withSuperAdmin(a.getCivitaiToken))
	mux.HandleFunc("PUT /api/admin/engines/civitai-token", a.withSuperAdmin(a.putCivitaiToken))
	mux.HandleFunc("DELETE /api/admin/engines/civitai-token", a.withSuperAdmin(a.deleteCivitaiToken))
	// Whether the catalogue's search offers Civitai Red at all (engine_civitai_red.go). Under
	// engines rather than beside the egress mode because it is a property of this panel's own
	// search, and super_admin like every other deployment-wide write — a granted tenant_admin
	// reads the resulting list off GET /api/admin/engines below and moves nothing.
	mux.HandleFunc("GET /api/admin/engines/civitai-red", a.withSuperAdmin(a.getCivitaiRed))
	mux.HandleFunc("PUT /api/admin/engines/civitai-red", a.withSuperAdmin(a.putCivitaiRed))
	// The bucket read as the ledger, and the two acts that start from it (ADR 0085 decisions 2, 3
	// and 7). One line on purpose: the route table is what three lanes writing this ADR at once
	// would otherwise each append to.
	registerEngineObjectRoutes(mux, a)
	// Re-reading a model page for the name and the picture (ADR 0088), which is how a row taken
	// in before those columns existed gets them. One line for the same reason as the one above.
	registerEngineMetaRoutes(mux, a)
	// The credential another deployment borrows these engines with (ADR 0079 decision 3, P1).
	// Super_admin only and never GET — it opens every engine here, so a tenant-scoped role is
	// not in proportion, and a credential does not belong in a URL. engine_issue_token.go.
	mux.HandleFunc("POST /api/admin/engines/issue-token", a.withSuperAdmin(a.postIssueToken))
}

// get (GET /api/admin/engines) lists every engine with its mode and what ECS is doing — or, for
// a granted tenant_admin, the subset of that row engineTenantAdminRow keeps.
//
// `super_admin` rides on the envelope rather than being left to the client to work out from
// which fields arrived. The panel has to decide whether to draw controls, and inferring that
// from "did `mode` turn up" is a rule that breaks silently the day a field is renamed — in the
// direction that shows a tenant_admin buttons that 403.
func (a engineAdminAPI) get(w http.ResponseWriter, r *http.Request, g engineIngestGrant) {
	out := []map[string]any{}
	for _, e := range a.reg.list() {
		row := a.row(r.Context(), e)
		if !g.super {
			row = engineTenantAdminRow(row)
		}
		out = append(out, row)
	}
	// `catalog_sources` is the search's source list as THIS deployment offers it
	// (engine_civitai_red.go). It rides here because every screen with a source tab strip already
	// reads this route, and because the list has to be known before the first search rather than
	// discovered by being refused one.
	writeJSON(w, http.StatusOK, map[string]any{"engines": out, "super_admin": g.super,
		"catalog_sources": a.civitaiRed().sources(r.Context())})
}

// row is one engine's status line. `ready` is deliberately NOT here: answering it means a health
// call, an engine at desired 0 is the normal state, and a panel that polls this list would
// otherwise dial a sleeping box on every refresh. What ECS says is the honest answer to "is
// it running", and it costs a cached read.
//
// `warm` is a different matter and IS here: it is a bool the controller already maintains on
// its own tick (engineController.warmed), so reading it costs nothing and dials nobody. It is
// worth the field because RUNNING and READY are genuinely different for these engines —
// llama-server binds its port 267 seconds before the weights are in VRAM (measured) — and a
// panel that showed only "running" would report an engine as up through the whole cold start.
//
// The models are a DECLARATION (ADR 0053), never a question put to the engine: the engine is
// asleep most of the time, so anything only it could answer is unanswerable exactly when
// somebody opens this panel. Since ADR 0072 the declaration is the catalogue rather than the
// stack, which is what makes the list on this panel editable at all.
//
// `models` (the flat id list) and `model_rows` both ride: the first is what the panel showed
// before the catalogue existed and what a client written against it still reads.
func (a engineAdminAPI) row(ctx context.Context, e *engineRuntimeState) map[string]any {
	mode := e.mode(ctx)
	cfg := e.controlCfg(ctx)
	catalogue := e.catalog.list(ctx)
	ids := []string{}
	modelRows := []map[string]any{}
	families := engineBaseModelsFor(e.def.Provider)
	for _, m := range catalogue {
		if m.Enabled && !engineModelIsLora(m) {
			ids = append(ids, m.ID)
		}
		mr := engineAdminModelRow(m)
		// Rows this provider cannot generate from, named as such (ADR 0072 decision 2). Only
		// ever true for a provider that dispatches on the family, and it is the ONE thing a
		// panel cannot work out on its own about a row that otherwise looks complete: a seeded
		// row (the seed cannot know the family) and every row written before this was validated
		// look exactly like a working one until somebody waits out a cold start.
		if families != nil && !engineModelIsLora(m) && !engineBaseModelValid(e.def.Provider, m.BaseModel) {
			mr["base_model_missing"] = true
		}
		// And the other half of "this row cannot generate", which declaring a family used to
		// HIDE: the template picked by that family reads files this row does not have (ADR 0072
		// P2 欠落 10). Named as the roles that are missing, because that is what the operator
		// then has to take in.
		if missing := engineMissingFileFlags(e.def.Provider, m); len(missing) > 0 {
			mr["files_missing"] = missing
		}
		// And the half of "this row cannot generate" that no declaration can express: the
		// checkpoint file itself carries no VAE, so the family's template has nothing to decode
		// with (ADR 0072 follow-up). ONE mark, and it stays because it answers "why can this row
		// not be enabled" (ADR 0085 decision 7); the remedy is 揃える, which folds the family's
		// own VAE into the gap it closes. `vae_fix` (which file that would be) and `vae_unread`
		// (nobody has read this header) left with the routes that acted on them.
		if engineVaeMissing(e.def.Provider, m) {
			mr["vae_missing"] = true
		}
		// A LoRA pinned to nothing (ADR 0072 decision 5, the llm half). The adapter reaches the
		// engine through the preset section of the model named in `base_model`, so a base that is
		// disabled or not in this catalogue means the row does nothing at all — and it looks
		// exactly like one that is working: enabled, its file on the box, no error anywhere.
		//
		// Asked of the chat role only. The image role's LoRAs name a ComfyUI FAMILY in the same
		// column and are chosen per request, so "no row has that id" is not a fault there.
		if e.def.api() == engineAPIChat && engineModelIsLora(m) && !engineLoraBasePresent(m, catalogue) {
			mr["lora_base_missing"] = true
		}
		modelRows = append(modelRows, mr)
	}
	row := map[string]any{
		"key":      e.def.Key,
		"api":      e.def.api(),
		"provider": e.def.Provider,
		"models":   ids,
		// Every row, enabled or not: this panel is where an administrator turns one ON, so a
		// list filtered to the enabled ones would have no way to reach the others.
		"model_rows": modelRows,
		// Stated rather than left to be inferred from an empty list, because it is the reason
		// the engine will refuse to start (decideEngineAction's `no_model`) and the panel has
		// to say so instead of offering a toggle that does nothing.
		"has_models": e.catalog.hasModels(ctx),
		"mode":       mode,
		// The INTENT, never the desired count — see the note in tts.go's status.
		"enabled": mode != engineModeOff,
		"managed": e.ecs != nil,
		"warm":    e.warm(ctx),
	}
	// ADR 0076 decision 5's contract, written down so the Console half could be built beside
	// this one. An externally managed row carries the URL — it is the only thing an operator can
	// act on — and OMITS every field that comes from a service this deployment does not have:
	// `state`, `desired`, `box`, `stop_eta`, `idle_secs` and the window. Omitted rather than
	// zeroed: an idle window of 0 is configured to mean "never stops", which is a claim nothing
	// here is entitled to make about somebody else's box.
	if e.def.notManagedHere() {
		// The row's OWN lifecycle, not the constant this branch used to write: a remote row that
		// announced itself as `external` would leave the Console's "another fleet" label nothing
		// to branch on, and "externally managed" is true but unhelpful when there is a fleet with
		// a panel of its own on the other end (ADR 0079 decision 10).
		row["lifecycle"] = e.def.lifecycle()
		row["url"] = e.def.URL
	} else {
		// The demand window, always reported as the length it actually is. A client that
		// hard-codes "last 5 minutes" is wrong the moment an operator sets
		// AF_ENGINE_<KEY>_WINDOW_SEC, and it is the window the START decision is made on.
		row["window_secs"] = int(cfg.window.Seconds())
		row["idle_secs"] = int(engineIdleWindow(cfg).Seconds())
		row["idle_min_secs"] = int(cfg.deadline.Seconds())
	}
	// The families this provider dispatches on, so the panel can offer a CHOICE instead of a
	// free-text box that lets an upstream display name through (ADR 0072 decision 2). Absent
	// for a provider with no opinion, which is what the panel reads as "do not ask".
	if families != nil {
		row["base_models"] = families
	}
	// And how a row may label its FILES. A split model (a diffusion model, a text encoder and a
	// VAE) cannot be declared without these, so a panel that only knew about an S3 key could
	// register no FLUX.2 klein and no Z-Image at all.
	if flags := engineFileFlagsFor(e.def.Provider); flags != nil {
		row["file_flags"] = flags
	}
	// What this deployment excludes from every image (ADR 0072 follow-up, negative prompts).
	// Only offered where a negative prompt can reach anything at all: the chat role has no such
	// thing, and a box that would draw one is a panel asking for a setting nothing reads.
	if e.def.api() == engineAPIImages {
		row["negative_always"] = e.negativeAlways(ctx)
		row["negative_max"] = engineNegativeMaxRunes
	}
	// This build's client vocabulary is {comfy, openai-compat} (ADR 0083 decision 5). A row
	// naming anything else — `sdcpp`, most likely, retired the same ADR — cannot be served no
	// matter what its mode or lifecycle say, and the panel has to say so rather than let the row
	// look like every other one until an operator hears "the image tool disappeared" from a
	// member.
	if !imageProviderServable(e.def) {
		row["provider_unserved"] = true
	}
	// Which model is actually in VRAM, and how often that changed. Both are IN-MEMORY facts of
	// this CP process (see engineServed), and `warm_model` is absent rather than stale whenever
	// the engine is not warm — a named model would say "this request is cheap" about a box that
	// is not even running.
	if served, swaps := e.servedModel(); served != "" || swaps > 0 {
		if served != "" {
			row["warm_model"] = served
		}
		row["model_swaps"] = swaps
	}
	// The GPU ladder (ADR 0074). Absent in full on a deployment that declares none, which is
	// what the panel reads as "this deployment does not choose its box" — as opposed to a
	// ladder of one, which is a real declaration and is shown.
	if classes := e.classList(); len(classes) > 0 {
		sel, _ := e.selectedClass(ctx)
		def, _ := e.defaultClass()
		rungs := make([]map[string]any, 0, len(classes))
		for _, c := range classes {
			rungs = append(rungs, engineClassRow(c))
		}
		row["classes"] = rungs
		row["class"] = engineClassRow(sel)
		row["class_default"] = def.ID
		// The same list with the purchase option on each entry (ADR 0075, contract B). `classes`
		// rides unchanged beside it: this row is read by a Console that may be older than the CP,
		// and a panel that lost its picker because a field was renamed is the failure this
		// deployment has already had once.
		offers := make([]map[string]any, 0, len(classes))
		for _, c := range classes {
			offers = append(offers, engineOfferRow(c))
		}
		row["offers"] = offers
		// Whether this role may buy an interruptible box at all (engineSpotSettingKey). Sent
		// beside the offers rather than derived from them, because the two say different things:
		// a `spot` row is what the OPERATOR declared, and this is what the ADMINISTRATOR accepted.
		// The panel needs both to draw a declared offer as present-but-not-available and to offer
		// the tick box that makes it available.
		row["spot_allowed"] = e.spotAllowed(ctx)
		// 🔴 Now "the operator has not pinned anything", not "the selection equals the first rung"
		// (ADR 0075 decision 8). Unpinned is AUTOMATIC — the offers are filtered by VRAM and tried
		// in order — so an administrator who pinned the offer that happens to be first has still
		// said something, and the badge that offers "back to automatic" has to appear for them.
		row["class_is_default"] = e.selectedClassID(ctx) == ""
		if trail := e.offers.attempts(); len(trail) > 0 {
			rows := make([]map[string]any, 0, len(trail))
			for _, a := range trail {
				rows = append(rows, map[string]any{"id": a.ID, "buy": a.Buy, "result": a.Result})
			}
			// What rule 2 has been through for the demand being served right now. In memory, so a
			// CP replaced mid-start reports none at all rather than somebody else's walk.
			row["offer_trail"] = rows
		}
		// 🔴 `class_apply_error` is gone from this row, and it is gone because the failure it
		// reported cannot happen any more (ADR 0077 decision 8). It said "the rung was stored and
		// the capacity provider refused it", which left the picker showing a card nobody had
		// managed to apply; there is no apply now — a rung is the type set in the next purchase's
		// overrides — so a stored choice is in force the moment it is written. The Console reads
		// the field when it is there and shows no retry banner when it is not.
		if need, source, id := engineVramDemand(catalogue); source != engineVramUnknown {
			// What the largest enabled model wants, and how well that is known. A maximum,
			// not a sum: one model is in VRAM at a time (`--models-max 1`, one checkpoint).
			row["vram_need_mib"] = need
			row["vram_need_source"] = source
			row["vram_need_model"] = id
			row["vram_fits"] = engineClassFits(sel, need)
		} else if len(catalogue) > 0 {
			// 🔴 Absent numbers with a present catalogue is its own answer, and it is NOT
			// "it fits": nobody declared what these models need.
			row["vram_need_source"] = engineVramUnknown
		}
	}
	if e.demand != nil {
		row["window_units"] = e.demand.units()
		// ⚠️ How much of that window this process can actually speak for. The buckets are in
		// memory and nothing else, so a CP replaced a minute ago reports zero requests while
		// somebody is mid-conversation with the engine. Without this field the panel states a
		// confident 0 it has no basis for; with it, the 0 can be drawn as "not counted yet".
		row["window_counted_secs"] = int(e.demand.countedFor().Seconds())
		if last := e.demand.lastAt(ctx); !last.IsZero() {
			// Persisted (engine_<key>_demand_at), so this one DOES survive a restart, and it
			// is what makes the paragraph above safe: the last-wanted time is still true when
			// the count next to it has been reset to zero.
			row["last_demand"] = last.UTC().Format(time.RFC3339)
		}
	}
	if e.ecs == nil {
		return row
	}
	v, err := e.ecs.view(ctx)
	if err != nil {
		row["error"] = err.Error()
		return row
	}
	row["state"] = engineDisplayState(v.state, mode)
	row["desired"] = v.desired
	// Which offer the engine is on, read off THE BOX'S OWN TAGS (ADR 0077 decision 8, replacing
	// ADR 0075 decision 11's read of the service's strategy — there is no strategy now).
	// Deliberately not "the offer this process chose": a CP replaced mid-start remembers nothing,
	// and `af-engine-offer` was written by the call that paid for the hardware. Absent when no
	// box is up, which is a real answer and not a gap.
	if id, buy, ok := e.offerOnBox(ctx); ok {
		row["offer"] = map[string]any{"id": id, "buy": buy}
	}
	// `running` and `rollout` are deliberately NOT added here even though the view carries
	// them. Nothing renders them, and a field on the wire with no reader is a shape the next
	// person has to keep working without knowing what would notice if it broke. The state
	// already folds the two counts, and "why is it stuck" is answered by the events below far
	// better than by the word FAILED.
	//
	// The service events are the ONLY place ECS writes down why a start failed ("no container
	// instances met the placement constraints", a pull failure). An operator staring at an
	// engine stuck in `starting` has nowhere else to read it, and the alternative is a trip to
	// the AWS console for a string the CP already has in hand.
	if len(v.events) > 0 {
		row["events"] = v.eventMessages()
	}
	if !v.lastStart.IsZero() {
		row["service_since"] = v.lastStart.UTC().Format(time.RFC3339)
	}
	// When the BOX started, which is a different fact from when the service last changed —
	// see engineBox. Only a Managed Instances engine has one, and a Fargate engine pays no
	// call to find that out.
	if b, ok := e.ecs.box(ctx); ok {
		box := map[string]any{"id": b.instanceID, "status": b.status}
		if !b.since.IsZero() {
			box["since"] = b.since.UTC().Format(time.RFC3339)
		}
		// What is running RIGHT NOW, which after a class change is not what the capacity
		// provider says: the change reaches the next box only, so these two disagree for as
		// long as the old one lives (ADR 0074 decision 4).
		if b.instanceType != "" {
			box["instance_type"] = b.instanceType
		}
		row["box"] = box
	}
	// When it will stop by itself. Absent — rather than "never" or a far-off date — whenever
	// the question has no answer: pinned on, switched off, already stopped, or no demand mark
	// yet. See engineStopETA for why each of those must not be answered.
	if eta := engineStopETA(mode, v.desired >= 1, e.demandAt(ctx), cfg); !eta.IsZero() {
		row["stop_eta"] = eta.UTC().Format(time.RFC3339)
	}
	return row
}

// demandAt is the persisted last-wanted mark, or the zero time when there is no counter.
func (e *engineRuntimeState) demandAt(ctx context.Context) time.Time {
	if e.demand == nil {
		return time.Time{}
	}
	return e.demand.lastAt(ctx)
}

// uptime (GET /api/admin/engines/{key}/hourly?from=&to=) is the engine's occupancy history,
// in the same UTC hour buckets and over the same date window as /api/admin/usage/hourly.
//
// It reads engine_hourly, which the engine's own controller fills a tick at a time
// (engine_uptime.go). Nothing is computed from the CURRENT state here: an engine running right
// now says nothing about last Tuesday, and the whole reason for the table is that the CP used
// to throw every observation away.
func (a engineAdminAPI) uptime(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	key := strings.TrimSpace(r.PathValue("key"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	if a.mgr == nil || a.mgr.store == nil {
		writeAPIErr(w, internalErr(errors.New("no store")))
		return
	}
	fromDay, toDay, fromHour, toHour, aerr := usageHourWindow(r, time.Now().UTC())
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	rows, err := a.mgr.store.ListEngineHourly(r.Context(), key, fromHour, toHour)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, buildEngineHourly(key, rows, fromDay, toDay, e.controlCfg(r.Context())))
}

const (
	engineIdleMinSeconds = 60
	engineIdleMaxSeconds = 24 * 60 * 60
)

// putIdle changes how long an on-demand GPU may sit unused before it is stopped.
func (a engineAdminAPI) putIdle(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	key := strings.TrimSpace(r.PathValue("key"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	if e.def.notManagedHere() {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody,
			"engine " + key + " is externally managed and has no local idle timer"})
		return
	}
	var b struct {
		IdleSecs int `json:"idle_secs"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	minimum := engineIdleMinSeconds
	if deadline := int(engineControlCfgFor(e.def).deadline.Seconds()); deadline > minimum {
		minimum = deadline
	}
	if b.IdleSecs < minimum || b.IdleSecs > engineIdleMaxSeconds {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody,
			fmt.Sprintf("idle_secs must be between %d and %d", minimum, engineIdleMaxSeconds)})
		return
	}
	if a.settings == nil {
		writeAPIErr(w, internalErr(errors.New("settings store is unavailable")))
		return
	}
	if err := a.settings.SetSetting(r.Context(), engineSettingsFor(key).idle, strconv.Itoa(b.IdleSecs)); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.audit(r.Context(), ident, "engine."+key+".idle", strconv.Itoa(b.IdleSecs))
	writeJSON(w, http.StatusOK, a.row(r.Context(), e))
}

// engineDisplayState is ttsDisplayState's twin, for the same reason: right after OFF is
// pressed the desired count has not moved yet, and answering "running" there reports the
// opposite of what the administrator just asked for.
func engineDisplayState(raw, mode string) string {
	if mode == engineModeOff && (raw == "running" || raw == "starting") {
		return "stopping"
	}
	return raw
}

// put (PUT /api/admin/engines/{key}) takes {mode:"off"|"on"|"ondemand"} and records it.
// What happens to the desired count is not symmetric, and the asymmetry is not the same as
// the TTS one:
//
//   - "on" starts the box now: somebody pressed a button and is waiting for it.
//   - "ondemand" touches nothing; the controller takes it from here.
//   - "off" stops it HERE, at once. This is where it differs from tts.go, which debounces the
//     stop behind an undo window because an accidental OFF→ON there costs a 2 GB pull and
//     80 seconds. A GPU box is $1.26/hour and holding it through an undo window costs real
//     money for a button nobody may press again; the engine's own cold start (measured 165-197
//     seconds for the image role) is the price of changing your mind, and it is a price the
//     person who just pressed OFF has implicitly accepted.
//
// Audited, like every other super-admin action.
func (a engineAdminAPI) put(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	key := strings.TrimSpace(r.PathValue("key"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	var b struct {
		Mode    string `json:"mode"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	val, aerr := engineModeFromBody(b.Mode, b.Enabled)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// On-demand is a promise to stop the box when nobody wants it, and an externally managed
	// engine has no box here to stop (ADR 0076 decision 5). Refused rather than silently stored
	// as `on`: the setting outlives this row's lifecycle, so a stored `ondemand` would come back
	// as a real mode the day the role moves into the stack.
	if val == engineModeOnDemand && e.def.notManagedHere() {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody,
			"engine " + key + " is externally managed: it is on or off, never on-demand"})
		return
	}

	keys := engineSettingsFor(key)
	if a.settings != nil {
		if err := a.settings.SetSetting(r.Context(), keys.mode, val); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		// When it changed, for the same reason tts.go records it: the controller's own clocks
		// are measured from it and must survive a CP restart.
		if err := a.settings.SetSetting(r.Context(), keys.modeAt, strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
			log.Printf("engines: recording the mode change time for %s failed: %v", key, err)
		}
	}
	// The mode decides whether this engine is IN /internal/engine/catalog at all (see this
	// file's header), so it changes what a Workspace may offer just as much as enabling a model
	// does — and the Agent caches the catalogue for ten minutes. Without this push, `off` leaves
	// every running session offering an engine that now answers 503 engine_off, and `on` leaves
	// generate_image hiding an engine that is ready, for up to that whole TTL. Placed BEFORE the
	// class gate's early return: the mode is stored on that path too. (Measured on af-sandbox,
	// ADR 0072 P2 実機検証: `mode=ondemand` did not reach a running session until the TTL.)
	go notifyEngineCatalogChanged(context.WithoutCancel(r.Context()), a.mgr, key)
	e.ctrl.noteAdminAction() // a cooldown must never refuse the person who pressed the button
	// ON starts the box HERE, so the class gate has to be consulted HERE as well: without it
	// this route is a way around the wait, and the box it buys is the previous rung's (ADR 0074
	// decision 4). Refusing to start is not refusing the request — the mode is stored, and the
	// controller starts the engine as soon as the old box has gone.
	if val == engineModeOn && e.classStartHeld(r.Context()) {
		writeJSON(w, http.StatusOK, a.row(r.Context(), e))
		return
	}
	if e.ecs != nil && (val == engineModeOn || val == engineModeOff) {
		// ON goes through the engine's own start, so this route buys the same box the controller
		// would (ADR 0077 decisions 1 and 2): the offer the gate just chose, bought as one
		// instant fleet, with the desired count following once it has registered. OFF is the
		// plain desired 0 — what happens to the box after that is the controller's next tick
		// (decision 5).
		var err error
		if val == engineModeOn {
			err = e.startEngine(r.Context())
			if errors.Is(err, errEngineBoxRegistering) {
				// The box is bought and the desired count is the controller's next tick away.
				// Reporting a 502 here would tell the administrator the button failed when the
				// engine is on its way up.
				err = nil
			}
		} else {
			err = e.ecs.setEnabled(r.Context(), false)
		}
		if err != nil {
			writeAPIErr(w, &apiError{http.StatusBadGateway, errCodeEngineECSError, "ecs update failed: " + err.Error()})
			return
		}
	}
	if a.mgr != nil && a.mgr.store != nil {
		_ = a.mgr.store.InsertAudit(r.Context(), store.AuditLog{
			ID: store.NewID(), TenantID: "", ActorKind: "admin", ActorID: ident.ID,
			Action: "engine." + key, Target: val, At: store.NowTS(),
		})
	}
	log.Printf("engines: %s set to %s by %s", key, val, ident.ID)
	writeJSON(w, http.StatusOK, a.row(r.Context(), e))
}

// engineClassRow is one rung as the panel reads it. The price rides only when the operator
// declared one: a missing figure is printed as nothing, never as 0 (ADR 0074 decision 1).
func engineClassRow(c engineClass) map[string]any {
	row := map[string]any{
		"id":       c.ID,
		"label":    c.label(),
		"vram_mib": c.VramMiB,
		"types":    c.Types,
	}
	if c.UsdPerHour > 0 {
		row["usd_per_hour"] = c.UsdPerHour
	}
	return row
}

// engineOfferRow is the same rung as an OFFER: the rung's own fields plus the purchase option
// (ADR 0075 contract B). Always present, never inferred from the absence of something — `od` is
// what an ADR 0074 ladder means, and the panel has to be able to say so.
func engineOfferRow(c engineClass) map[string]any {
	row := engineClassRow(c)
	row["buy"] = c.buy()
	return row
}

// putClass (PUT /api/admin/engines/{key}/class) takes {"class":"<id>"} and records which GPU
// this role buys next (ADR 0074 decision 2).
//
// What it does NOT do is replace a running box. The API is explicit that a change "only
// applies to new Amazon ECS Managed Instances", so a panel that reported success while the old
// card kept answering would be stating the opposite of the truth; the answer carries
// `class_replace_pending` instead, and replacing is the mode toggle — an act with a cold start
// attached, pressed by somebody who has been told so.
//
// The rung is written to the capacity provider here as well as before every start. Here so the
// operator learns at once that it could not be written (a missing IAM grant is otherwise
// discovered at the next cold start, on the wrong card); before every start because a
// CloudFormation update puts the stack's declaration back and tells nobody.
func (a engineAdminAPI) putClass(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	key := strings.TrimSpace(r.PathValue("key"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	var b struct {
		Class string `json:"class"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	id := strings.TrimSpace(b.Class)
	list := e.classList()
	if len(list) == 0 {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineClassUnknown,
			"engine " + key + " declares no instance classes"})
		return
	}
	// The empty id is "unpin" (ADR 0075 decision 8), and it is a real request rather than a
	// no-op: a pinned engine cannot fall through to the next offer, so the way back to automatic
	// has to be reachable from the panel. It is the only value that removes the stored setting.
	if id == "" {
		a.unpinClass(w, r, ident, key, e)
		return
	}
	c, ok := engineClassByID(list, id)
	if !ok {
		// A rung nobody declared does not exist. This is also the line that keeps the CP from
		// writing arbitrary instance requirements: everything reaching the capacity provider
		// came out of the operator's ladder.
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineClassUnknown,
			"no instance class " + id + " for engine " + key})
		return
	}
	ctx := r.Context()
	if a.settings != nil {
		if err := a.settings.SetSetting(ctx, engineClassSettingKey(key), c.ID); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
	}
	// 🔴 Nothing is written to AWS here any more (ADR 0077 decision 8). The stored choice IS the
	// declaration: the next purchase puts this offer's instance types into the fleet's overrides,
	// so there is no second copy of it to keep in step and no window in which the panel shows a
	// rung the hardware does not hold.
	a.audit(ctx, ident, "engine."+key+".class", c.ID)
	log.Printf("engines: %s instance class set to %s by %s", key, c.ID, ident.ID)
	row := a.row(ctx, e)
	// True when a box is up that this rung does not cover: the change is saved and has not
	// reached anything yet.
	//
	// The Console does not read this one — it derives the same fact from `box.instance_type`
	// against the rung's types, which is what keeps it correct after a later refresh merges a
	// row that carries no flag. It rides for the client that only sees this answer, and it is
	// computed from exactly the same two inputs so the two can never disagree.
	if b, on := e.ecs.box(ctx); on && b.instanceType != "" && !engineClassHasType(c, b.instanceType) {
		row["class_replace_pending"] = true
	}
	writeJSON(w, http.StatusOK, row)
}

// unpinClass is `{"class": ""}`: the stored choice is removed and the role goes back to choosing
// automatically (ADR 0075 decision 8) — VRAM-filtered offers, tried in declaration order.
//
// It takes effect on the next purchase, like a pin: the offer list is walked from the top,
// VRAM-filtered, the next time this engine starts. Nothing reaches AWS here (ADR 0077 decision 8).
func (a engineAdminAPI) unpinClass(w http.ResponseWriter, r *http.Request, ident store.Identity, key string, e *engineRuntimeState) {
	ctx := r.Context()
	if a.settings != nil {
		if err := a.settings.SetSetting(ctx, engineClassSettingKey(key), ""); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
	}
	a.audit(ctx, ident, "engine."+key+".class", "")
	log.Printf("engines: %s instance class unpinned by %s (choosing automatically)", key, ident.ID)
	writeJSON(w, http.StatusOK, a.row(ctx, e))
}

// putSpot (PUT /api/admin/engines/{key}/spot) takes {"spot": true|false} and records whether this
// role may buy an INTERRUPTIBLE box (engineSpotSettingKey).
//
// The operator declares which offers exist; this is the administrator accepting what the `spot`
// ones cost when the box is taken away — the answer in flight is lost and the next one waits out
// a cold start. Two halves of one decision, deliberately kept apart: the declaration is made once
// in CloudFormation by whoever stands the stack up, and the interruption is lived with by whoever
// runs the engine.
//
// Like the class, it reaches the NEXT purchase and not the box that is up. Switching it off does
// not hand back a Spot box that is answering right now, which the panel says beside the tick
// rather than implying by echoing the new value back.
//
// Ticking it on is refused where the role declares no `spot` offer: consent is given to a list
// somebody is looking at, and a stored yes that predates the declaration would buy an
// interruptible box the moment an operator added a row, on an authority given for something
// else. Un-ticking is always allowed — the way back from a yes cannot depend on a declaration.
func (a engineAdminAPI) putSpot(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	key := strings.TrimSpace(r.PathValue("key"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	var b struct {
		Spot bool `json:"spot"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	list := e.classList()
	if len(list) == 0 {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineClassUnknown,
			"engine " + key + " declares no instance classes"})
		return
	}
	if b.Spot && !engineClassesHaveSpot(list) {
		writeAPIErr(w, &apiError{http.StatusConflict, errCodeEngineBadBody,
			"engine " + key + " declares no spot offer to accept interruption for"})
		return
	}
	ctx := r.Context()
	val := ""
	if b.Spot {
		val = "true"
	}
	if a.settings != nil {
		if err := a.settings.SetSetting(ctx, engineSpotSettingKey(key), val); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
	}
	state := "off"
	if b.Spot {
		state = "on"
	}
	a.audit(ctx, ident, "engine."+key+".spot", state)
	log.Printf("engines: %s interruptible boxes %s by %s", key, state, ident.ID)
	writeJSON(w, http.StatusOK, a.row(ctx, e))
}

// putNegative (PUT /api/admin/engines/{key}/negative) records what this deployment excludes from
// every image this engine makes (ADR 0072 follow-up, negative prompts). One text box, one
// setting row, applied to every request whoever made it and whichever checkpoint answers.
//
// 🔴 It is NOT a content filter, and the panel must not describe it as one. The words reach the
// sampler through the negative branch of classifier-free guidance, which is a nudge and not a
// gate: the two distilled families here have no such branch at all (the result says so in its
// warnings), and even on a guided one a determined prompt outweighs it. A deployment that needs
// a guarantee needs one somewhere this cannot give it.
//
// The empty string CLEARS it, and that is the only way back — the same shape as unpinning a
// class. No separate DELETE route, because "excluded: nothing" is a value an operator sets from
// the same box they typed it into.
//
// It takes effect on the Agent's own catalogue TTL (10 minutes) or at the next push, like a
// model being enabled. Nothing restarts: this changes what the next graph SAYS, not what the box
// is running.
func (a engineAdminAPI) putNegative(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	key := strings.TrimSpace(r.PathValue("key"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	// The far administrator's exclusion list rides on the catalogue this row mirrors, so a local
	// one would be a second answer to the same question that only this deployment can see.
	if a.refuseBorrowedWrite(w, e, "what every image excludes") {
		return
	}
	if a.settings == nil {
		writeAPIErr(w, internalErr(errors.New("no settings store")))
		return
	}
	keys := engineSettingsFor(key)
	if keys.negative == "" {
		writeAPIErr(w, &apiError{http.StatusConflict, errCodeEngineBadBody,
			"engine " + key + " has nothing to exclude"})
		return
	}
	var b struct {
		Negative string `json:"negative"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	// Bounded because it rides on every catalogue answer to every workspace, and a paragraph
	// pasted in here would be paid for by every session on every refresh. Long enough for the
	// list anyone actually writes, short enough that it cannot become a document.
	want := strings.TrimSpace(b.Negative)
	if len([]rune(want)) > engineNegativeMaxRunes {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, fmt.Sprintf(
			"the excluded keywords are %d characters, and the limit is %d — this is a keyword list, not a policy document",
			len([]rune(want)), engineNegativeMaxRunes)})
		return
	}
	ctx := r.Context()
	if err := a.settings.SetSetting(ctx, keys.negative, want); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// The VALUE is not audited, for the same reason the model row's is not: it is free text, and
	// what an audit trail needs is that somebody changed it and whether anything is set now.
	state := "cleared"
	if want != "" {
		state = "set"
	}
	a.audit(ctx, ident, "engine."+key+".negative", state)
	log.Printf("engines: %s excluded keywords %s by %s", key, state, ident.ID)
	// The Agent caches the catalogue this rides on, so a change that nobody is told about takes
	// up to 10 minutes to reach a running session. The same detached fan-out a model change uses.
	go notifyEngineCatalogChanged(context.WithoutCancel(ctx), a.mgr, key)
	writeJSON(w, http.StatusOK, a.row(ctx, e))
}

// replaceBox (POST /api/admin/engines/{key}/replace-box) stops the running box so the next one
// is bought on the rung that is now chosen (ADR 0074 decision 4).
//
// It moves the desired count and NOTHING else — in particular it does not touch the mode, which
// is the whole difference from pressing "off": an engine pinned `on` must come back by itself,
// and an on-demand one must come back with the next request. The box itself is ended by the
// controller's next tick, once the task has gone (ADR 0077 decision 5), and what holds the
// restart until it has actually left the cluster is the start gate, not this handler.
//
// ⚠️ This buys a cold start (measured 527-586 s for llm, 165-197 s for image) and the panel says
// so before the press. It is the price ADR 0071's `offGrace: 0` already charges for changing
// one's mind about a GPU.
func (a engineAdminAPI) replaceBox(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	key := strings.TrimSpace(r.PathValue("key"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	if e.ecs == nil {
		writeAPIErr(w, &apiError{http.StatusConflict, errCodeEngineECSError, "engine " + key + " is not managed here"})
		return
	}
	ctx := r.Context()
	// A cooldown must never refuse the person who pressed the button, exactly as the mode
	// toggle does not.
	if e.ctrl != nil {
		e.ctrl.noteAdminAction()
	}
	if err := e.ecs.setEnabled(ctx, false); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadGateway, errCodeEngineECSError, "ecs update failed: " + err.Error()})
		return
	}
	a.audit(ctx, ident, "engine."+key+".replace_box", e.selectedClassID(ctx))
	log.Printf("engines: %s box replacement requested by %s", key, ident.ID)
	writeJSON(w, http.StatusOK, a.row(ctx, e))
}

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
	found, err := a.mgr.store.DeleteEngineModel(r.Context(), key, id)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineModelUnknown, "no model " + id + " for engine " + key})
		return
	}
	// ?purge=1 also deletes the bytes — and the CP cannot: it has no s3:DeleteObject and is not
	// getting one (ADR 0072 decision 7). The ingest task does it, in MODE=delete, because that
	// task is the one principal in the deployment allowed to write in that bucket at all.
	purged := ""
	if r.URL.Query().Get("purge") == "1" && len(keys) > 0 {
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
		a.audit(r.Context(), ident, "engine."+key+".model", "purge "+id+": "+purged)
	}
	e.catalog.invalidate()
	// A deleted row may have been enabled, so the box's active set really has changed.
	if perr := e.publishActiveSet(r.Context()); perr != nil {
		writeAPIErr(w, &apiError{http.StatusBadGateway, errCodeEnginePublishFailed, perr.Error()})
		return
	}
	a.audit(r.Context(), ident, "engine."+key+".model", "forget "+id)
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

// audit records one super-admin action against the catalogue. Same ledger, same shape as the
// mode toggle next door.
func (a engineAdminAPI) audit(ctx context.Context, ident store.Identity, action, target string) {
	if a.mgr == nil || a.mgr.store == nil {
		return
	}
	_ = a.mgr.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: "", ActorKind: "admin", ActorID: ident.ID,
		Action: action, Target: target, At: store.NowTS(),
	})
}

// engineModeFromBody reads {mode} — or {enabled} from a client written against a two-valued
// toggle — and refuses anything else. "" with no `enabled` is a client that sent an empty
// body, which must not be read as "switch it off".
func engineModeFromBody(mode string, enabled *bool) (string, *apiError) {
	switch mode {
	case engineModeOff, engineModeOn, engineModeOnDemand:
		return mode, nil
	case "":
		if enabled == nil {
			return "", &apiError{http.StatusBadRequest, errCodeEngineBadBody, "mode is required"}
		}
		if *enabled {
			return engineModeOn, nil
		}
		return engineModeOff, nil
	default:
		return "", &apiError{http.StatusBadRequest, errCodeEngineBadBody, "unknown mode: " + mode}
	}
}

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
	if kv := engineKVCacheMiB(geom, 1024); kv > 0 {
		row["kv_mib_per_1k_tokens"] = kv
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
	if kv, from := a.engineListKV(r.Context(), b.Source, files, engineIngestKindFor(e)); kv > 0 {
		answer["kv_mib_per_1k_tokens"] = kv
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
	files []engineCandidate, kind string) (int, string) {
	if !strings.EqualFold(strings.TrimSpace(kind), "gguf") {
		return 0, ""
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
			return 0, ""
		}
		res, aerr := engineIngestResolve(ctx, picked)
		if aerr != nil {
			return 0, ""
		}
		geom := engineIngestGeometry(ctx, kind, res, a.hfTokens())
		if kv := engineKVCacheMiB(geom, 1024); kv > 0 {
			return kv, f.Name
		}
		return 0, ""
	}
	return 0, ""
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
		if main.known.KVGeom != (engineKVGeometry{}) {
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

// engineFileFlagValid checks a declared file role against the provider's vocabulary.
//
// The empty flag — a whole checkpoint — is always allowed, including for a provider with no
// vocabulary at all: that is what every ingest wrote before this field existed, and a
// deployment whose engine is sdcpp must keep taking models in.
//
// The ingest no longer asks (ADR 0085 decision 1 — the family says what each file is). The rule
// survives for `complete`, whose `choices` names a role for a slot the operator is filling.
func engineFileFlagValid(provider, flag string) *apiError {
	if flag == "" {
		return nil
	}
	vocab := engineFileFlagsFor(provider)
	if vocab == nil {
		return &apiError{http.StatusBadRequest, errCodeEngineBadBody,
			"this engine runs " + provider + ", which loads one whole checkpoint — it has no file roles to declare"}
	}
	for _, f := range vocab {
		if f == flag {
			return nil
		}
	}
	named := make([]string, 0, len(vocab))
	for _, f := range vocab {
		if f != "" {
			named = append(named, f)
		}
	}
	return &apiError{http.StatusBadRequest, errCodeEngineBadBody, fmt.Sprintf(
		"file_flag must be empty (a whole checkpoint) or one of %s; %q is not a file role %s reads",
		strings.Join(named, ", "), flag, provider)}
}

// engineAttachAllowed is the gate on adding a part to an existing row. Three refusals, each of
// which would otherwise be discovered as a row that generates nothing:
//
//   - no such row. An attach names its target by id, and a typo would otherwise download for
//     minutes and then write a file nothing points at;
//   - no flag. The unlabelled slot is THE checkpoint and a row has one; attaching a second
//     would leave the last writer deciding which file the loader gets;
//   - that role is taken. Same reason, said before the download rather than after it.
//
// 🔴 It is no longer reached from `POST …/ingest` (ADR 0085 decision 3 took `attach` off that
// route). It stays because the rule does: `complete` fills a free slot and has to answer the same
// three questions, and its refusals are the ones that must carry a holder and a next act.
func engineAttachAllowed(row *store.EngineModel, id, key, flag string) *apiRefusal {
	if row == nil {
		return refuse(http.StatusNotFound, errCodeEngineModelUnknown,
			"no model "+id+" for engine "+key+" to attach this file to",
			nil, &apiNext{Act: "register", Target: id})
	}
	if flag == "" {
		return refuse(http.StatusBadRequest, errCodeEngineBadBody,
			"attaching a file to "+id+" needs file_flag: an unlabelled file is the checkpoint itself, and a row has one",
			nil, nil)
	}
	for _, f := range row.Files {
		if strings.TrimSpace(f.Flag) == flag {
			return refuse(http.StatusConflict, errCodeIngestIDExists,
				id+" already declares a "+flag+" file ("+f.S3Key+") — swap it with replace, or take this in as its own",
				&apiHolder{Kind: "row", ID: id, Key: f.S3Key}, &apiNext{Act: "replace", Target: id})
		}
	}
	return nil
}

// engineReplaceAllowed is the gate on swapping a file a row already holds. It is the mirror of
// engineAttachAllowed and deliberately not a relaxation of it:
//
//   - no such row. Same refusal, same reason — a typo would download for minutes and then write
//     a file nothing points at;
//   - no file in that slot. THIS is the one that keeps the two acts apart: replacing a slot
//     nothing occupies is attaching, and quietly doing that would turn a mistyped flag into a
//     second checkpoint on a row that is supposed to have one. The refusal names the act that
//     WAS meant, because from the panel the two are one press apart.
//
// 🔴 The empty flag is allowed here and refused by the attach gate, which is the whole point:
// the unlabelled file is the checkpoint, a row has exactly one, and it was until now the one
// file in the catalogue that no ingest could ever change.
//
// Reached from `complete` rather than from the ingest since ADR 0085 decision 3, for the same
// reason as its neighbour above.
func engineReplaceAllowed(row *store.EngineModel, id, key, s3key, flag string, reuse bool) *apiRefusal {
	if row == nil {
		return refuse(http.StatusNotFound, errCodeEngineModelUnknown,
			"no model "+id+" for engine "+key+" to replace a file of",
			nil, &apiNext{Act: "register", Target: id})
	}
	for _, f := range row.Files {
		if strings.TrimSpace(f.Flag) == flag {
			// A download uploads before the catalogue swap. Reusing the old key would therefore
			// overwrite bytes the active row still names, defeating the promise that replacement
			// retains its previous object. Verified reuse is the safe exception: no upload occurs,
			// and its immutable identity check makes a same-key operation idempotent.
			if !reuse && strings.TrimSpace(f.S3Key) == strings.TrimSpace(s3key) {
				return refuse(http.StatusConflict, errCodeIngestIDExists,
					"a replacement download needs a new S3 key; the current object must remain available at "+f.S3Key,
					&apiHolder{Kind: "row", ID: id, Key: f.S3Key}, &apiNext{Act: "replace", Target: id})
			}
			return nil
		}
	}
	return refuse(http.StatusBadRequest, errCodeEngineBadBody,
		id+" declares no "+engineFlagLabel(flag)+" file, so there is nothing to replace"+
			" — take this in as a part of that row instead",
		nil, &apiNext{Act: "complete", Target: id})
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
	if !ok || engineModelIsLora(cur) || cur.ContextCeiling > 0 {
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
	if _, err := a.mgr.store.SetEngineModelGeometry(ctx, e.def.Key, id, cur.Files, store.EngineModelKV{
		Layers: geom.Layers, HeadsKV: geom.HeadsKV, KeyLen: geom.KeyLen, ValueLen: geom.ValLen,
		NextN: geom.NextN, FullAttnInterval: geom.FullAttnInterval, Ceiling: geom.Ceiling,
	}); err != nil {
		log.Printf("engines: %s/%s: the geometry was read and could not be stored (%v)", e.def.Key, id, err)
		return
	}
	e.catalog.invalidate()
	log.Printf("engines: %s/%s: attention geometry read from the bucket (%d layers, %d caching, ceiling %d)",
		e.def.Key, id, geom.Layers, geom.cacheLayers(), geom.Ceiling)
}
