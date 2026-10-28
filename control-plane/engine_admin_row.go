package main

// engine_admin_row.go — the admin engine list (GET /api/admin/engines) and the row() builder
// every engine admin write answers with.

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
	body := map[string]any{"engines": out, "super_admin": g.super,
		"catalog_sources": a.civitaiRed().sources(r.Context())}
	// The LAN ComfyUI panel's state rides here rather than on a read of its own, so opening the
	// engines screen stays one request (engine_comfy_panel.go). Operator-only, like the panel.
	if g.super {
		body["comfy_lan"] = a.comfyLanStatus(r.Context())
	}
	writeJSON(w, http.StatusOK, body)
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
