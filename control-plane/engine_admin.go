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

func registerEngineAdminRoutes(mux *http.ServeMux, cfg config, reg *engineRegistry) {
	var settings store.SettingsStore
	if cfg.mgr != nil && cfg.mgr.store != nil {
		settings = cfg.mgr.store
	}
	a := engineAdminAPI{memberAuth{cfg.mgr}, reg, settings}
	mux.HandleFunc("GET /api/admin/engines", a.withSuperAdmin(a.get))
	mux.HandleFunc("PUT /api/admin/engines/{key}", a.withSuperAdmin(a.put))
	mux.HandleFunc("GET /api/admin/engines/{key}/hourly", a.withSuperAdmin(a.uptime))
	// The GPU this role buys (ADR 0074). A separate route from the mode toggle above because
	// it is a separate act with a separate cost: the mode buys a box now, this says what the
	// NEXT box will be.
	mux.HandleFunc("PUT /api/admin/engines/{key}/class", a.withSuperAdmin(a.putClass))
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
	// Taking a model IN from Hugging Face / Civitai / a URL (ADR 0072 decision 6, phase P4),
	// and watching the jobs that does.
	//
	// These six are the ONLY engine routes that are not super_admin: a tenant_admin of a tenant
	// the operator granted `allow_engine_ingest` may drive them too (ADR 0072 open question 11 —
	// engine_ingest_perm.go says why the axis stops here).
	mux.HandleFunc("POST /api/admin/engines/{key}/ingest", a.withIngestAdmin(a.postIngest))
	mux.HandleFunc("GET /api/admin/engines/{key}/ingest", a.withIngestAdmin(a.listIngest))
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
}

// get (GET /api/admin/engines) lists every engine with its mode and what ECS is doing.
func (a engineAdminAPI) get(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	out := []map[string]any{}
	for _, e := range a.reg.list() {
		out = append(out, a.row(r.Context(), e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"engines": out})
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
	cfg := e.controlCfg()
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
		"warm":    e.ctrl.warmed(),
		// The demand window, always reported as the length it actually is. A client that
		// hard-codes "last 5 minutes" is wrong the moment an operator sets
		// AF_ENGINE_<KEY>_WINDOW_SEC, and it is the window the START decision is made on.
		"window_secs": int(cfg.window.Seconds()),
		"idle_secs":   int(engineIdleWindow(cfg).Seconds()),
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
		// Stated rather than left to a comparison in the client: "you are not on the default"
		// is the sentence that stops a temporary experiment from becoming a permanent bill
		// (decision 7), and it must not depend on a client remembering to compute it.
		row["class_is_default"] = sel.ID == def.ID
		// The rung above is what was CHOSEN; this is why the capacity provider does not hold it.
		// Omitted whenever this process has nothing to report, which is what the panel reads as
		// "no claim" — never as "it was applied" (see classApplyError). It is what turns the
		// picker's dead end into a retry: without it, re-selecting the stored rung is "no change"
		// and the Console sends nothing at all.
		if msg := e.classApplyError(); msg != "" {
			row["class_apply_error"] = msg
		}
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
		row["events"] = v.events
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
	writeJSON(w, http.StatusOK, buildEngineHourly(key, rows, fromDay, toDay, e.controlCfg()))
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
		if err := e.ecs.setEnabled(r.Context(), val == engineModeOn); err != nil {
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
	if err := e.applyClass(ctx, c); err != nil {
		// Reported, not logged: the setting was written, so a panel that showed success would
		// promise a card the next start will not buy.
		writeAPIErr(w, &apiError{http.StatusBadGateway, errCodeEngineECSError, err.Error()})
		return
	}
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

// replaceBox (POST /api/admin/engines/{key}/replace-box) stops the running box so the next one
// is bought on the rung that is now chosen (ADR 0074 decision 4).
//
// It moves the desired count and NOTHING else — in particular it does not touch the mode, which
// is the whole difference from pressing "off": an engine pinned `on` must come back by itself,
// and an on-demand one must come back with the next request. What holds the restart until the
// old box has actually left the cluster is the start gate, not this handler.
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
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "enabled, selected, default or base_model is required"})
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
	sel, ok := e.selectedClass(ctx)
	if !ok || sel.VramMiB <= 0 {
		return nil
	}
	for _, m := range e.catalog.list(ctx) {
		if m.ID != id || engineModelIsLora(m) {
			continue
		}
		need, source := engineModelVramNeed(m)
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
	return nil
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
// ⚠️ Nothing here verifies that the S3 key exists. The CP task role has no S3 permission at all
// and none is being added (ADR 0072 review R3), so a typo surfaces in the fetch sidecar's log at
// the next cold start. That is the honest cost of keeping the CP out of the bucket, and it is
// why the panel shows the key back.
func (a engineAdminAPI) postModel(w http.ResponseWriter, r *http.Request, ident store.Identity) {
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
	}
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
		if k := strings.TrimSpace(f.S3Key); k != "" {
			m.Files = append(m.Files, store.EngineModelFile{
				Flag: strings.TrimSpace(f.Flag), S3Key: k, Bytes: f.Bytes,
			})
		}
	}
	if len(m.Files) == 0 {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "at least one file (s3Key) is required"})
		return
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
	ID     string             `json:"id"`
	Kind   string             `json:"kind"`
	S3Key  string             `json:"s3Key"`
	Source engineIngestSource `json:"source"`

	Description     string   `json:"description"`
	BaseModel       string   `json:"base_model"`
	ContextTokens   int      `json:"context_tokens"`
	MaxOutputTokens int      `json:"max_output_tokens"`
	Sizes           []string `json:"sizes"`
	// FileFlag is what this file is WITHIN the model, from the same `file_flags` vocabulary the
	// row already serves to the register form. Empty is a whole checkpoint, which is what every
	// ingest used to be able to say (ADR 0072 P2 欠落 6).
	FileFlag string `json:"file_flag"`
	// Attach says the file joins the row `id` already names rather than creating one. It is the
	// other half of the flag: a FLUX.1 row is four files and they arrive as four downloads.
	Attach bool `json:"attach"`
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
func (a engineAdminAPI) resolveIngest(w http.ResponseWriter, r *http.Request, _ engineIngestGrant) {
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
	res, aerr := engineIngestResolve(r.Context(), b.Source)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, engineResolvedRow(res, a.hfTokens().configured(r.Context())))
}

// engineResolvedRow is what the panel draws before anything is started. `can_ingest` is the
// verdict this route exists for: a gated repository on a deployment with no HF token cannot be
// taken in, and saying so here costs nothing — finding out from a 401 costs a Fargate task and
// a confused administrator.
func engineResolvedRow(res engineResolved, hasToken bool) map[string]any {
	row := map[string]any{
		"sha256":           res.SHA256,
		"bytes":            res.Bytes,
		"gated":            res.Gated,
		"commercial_use":   engineCommercialUse(res),
		"source":           res.Source,
		"can_ingest":       (!res.Gated || hasToken) && !res.LoginRequired,
		"deployment_token": hasToken,
	}
	// Told apart from `gated` on purpose. Gating is the repository's terms and a registered
	// token satisfies them; this is a Civitai uploader's switch, and there is nothing on this
	// deployment that could satisfy it — so a panel that folded the two would send somebody to
	// the token field to fix something a token cannot fix (ADR 0072 P2 欠落 5).
	if res.LoginRequired {
		row["login_required"] = true
	}
	// A gated repository WITH a token registered is not yet a yes, and this is the one place
	// that can say so in advance. 🔴 The CP resolves anonymously (decision 6) — it never holds
	// the token — so it cannot ask whether that account accepted THIS repository's terms, and
	// the answer arrives as a 403 on the download instead (measured, ADR 0072 P5 実機検証: one
	// token, FLUX.1-dev through and SD3.5 Medium refused). A warning is therefore all this can
	// honestly be; the CODE for it exists on the failed job, where the status is known.
	if res.Gated && hasToken {
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
	// The model's own maximum, offered so nobody reads it off a model card by hand. Sent as
	// what it is — a ceiling, not a setting: 🔴 the 30B in this deployment publishes 262144 and
	// is run at 32768, because the architecture's limit and what fits in an L4 are different
	// questions and only one of them is Hugging Face's to answer.
	if res.ContextLength > 0 {
		row["context_length"] = res.ContextLength
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
	files, aerr := engineIngestList(r.Context(), b.Source, engineIngestKindFor(e))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// postIngest (POST …/ingest) resolves the source and starts the task.
func (a engineAdminAPI) postIngest(w http.ResponseWriter, r *http.Request, g engineIngestGrant) {
	key := strings.TrimSpace(r.PathValue("key"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
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
	id, s3key := strings.TrimSpace(b.ID), strings.TrimSpace(b.S3Key)
	if id == "" || s3key == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "id and s3Key are required"})
		return
	}
	if !b.LicenseAccepted {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeIngestNotAccepted,
			"the licence has to be accepted before a model is taken in"})
		return
	}
	// What this file IS within the model. Checked against the provider's own vocabulary rather
	// than taken as text: an unknown flag is silently dropped by the Agent's resolver (a
	// catalogue newer than the box must degrade, not fail), so a typo here would produce a row
	// whose part is simply never passed to any loader.
	flag := strings.TrimSpace(b.FileFlag)
	if aerr := engineFileFlagValid(e.def.Provider, flag); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// 🔴 An id already in the catalogue is REFUSED, because the row is written by PutEngineModel
	// and that is an upsert on (role, id) — correct for the seed and for registering a staged
	// file, catastrophic here. The job would download for minutes and then replace a working
	// row's files and licence with the new ones AND set enabled=false, so the engine would lose
	// the checkpoint it starts with and nobody would connect the two events.
	//
	// Refusing is also the honest reading of what an ingest is: it CREATES a row (disabled, for
	// an administrator to turn on). Replacing the bytes under an id is a different act, and
	// forgetting the old row first says so out loud.
	//
	// `attach` is that other act, said out loud: the file joins the named row as one more PART
	// and nothing else about the row is touched. It is what makes a split model assemblable by
	// ingest alone (欠落 6) — until it existed, the three components of a FLUX.1 row had to be
	// taken in as throwaway rows and the real row re-typed through `POST /models`.
	var existing *store.EngineModel
	for _, m := range e.catalog.list(r.Context()) {
		if m.ID != id {
			continue
		}
		if !b.Attach {
			writeAPIErr(w, &apiError{http.StatusConflict, errCodeIngestIDExists,
				"this engine already has a model called " + id + " — forget that row first, or choose another id"})
			return
		}
		row := m
		existing = &row
	}
	if b.Attach {
		if aerr := engineAttachAllowed(existing, id, key, flag); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	res, aerr := engineIngestResolve(r.Context(), b.Source)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// ⚠️ Refused BEFORE a task is started, for the same reason as the gated case below — except
	// that no token exists that would help. The asset's uploader requires an account, this
	// deployment has none for Civitai, and the alternative is the bare `curl: (22) … 401` nine
	// minutes in that ADR 0072 P2 欠落 5 measured.
	if res.LoginRequired {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeIngestCivitaiLogin,
			"the person who uploaded this asset requires a logged-in account to download it, and this " +
				"deployment ingests anonymously — pick another asset, or stage the file by hand and register it"})
		return
	}
	// ⚠️ Refused BEFORE a task is started. Without the token the download is a 401 nine minutes
	// into a Fargate task, and the message that reaches the panel is an exit code.
	if res.Gated && !ing.tokens.configured(r.Context()) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeIngestGatedNoToken,
			"that repository is gated: accept its terms on Hugging Face with the operator's account " +
				"and register that account's token below — it is read by the ingest task only"})
		return
	}
	// ⚠️ The operator's declaration first, and the repository's own string ONLY when it happens
	// to be a family this provider knows. Hugging Face and Civitai publish a display name —
	// "SDXL 1.0", "Flux.1 D" — which is descriptive metadata, not the dispatch key ADR 0072
	// decision 2 defines; storing it as the family produced rows that looked complete in the
	// panel and then refused to generate (measured on af-sandbox, ADR 0072 P2 実機検証). For a
	// provider with no vocabulary nothing dispatches on it, so the upstream string rides as
	// before and is worth keeping.
	base := strings.TrimSpace(b.BaseModel)
	if base == "" && engineBaseModelValid(e.def.Provider, res.BaseModel) {
		base = strings.TrimSpace(res.BaseModel)
	}
	// An attach writes no family: the row it joins declared one when it was created, and asking
	// for it again is asking for a second answer to a question already settled.
	if !b.Attach && !strings.EqualFold(strings.TrimSpace(b.Kind), engineModelKindLora) && !engineBaseModelValid(e.def.Provider, base) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, fmt.Sprintf(
			"declare base_model as one of %s: this engine runs %s, which picks a workflow by family"+
				" and will not guess one%s",
			strings.Join(engineBaseModelsFor(e.def.Provider), ", "), e.def.Provider,
			engineBaseModelHint(res.BaseModel))})
		return
	}
	job, aerr := ing.start(r.Context(), engineIngestRequest{
		Role: key, ModelID: id, Kind: strings.TrimSpace(b.Kind), S3Key: s3key,
		Description:   strings.TrimSpace(b.Description),
		BaseModel:     base,
		ContextTokens: b.ContextTokens, MaxOutput: b.MaxOutputTokens, Sizes: b.Sizes,
		// The acceptance, as the tuple ADR 0072 open question 11 asks for. The licence is
		// carried as a STRING rather than re-read from the row later: it is what was on
		// screen when the box was ticked, and upstream relicensing must not rewrite what
		// somebody agreed to.
		AcceptedBy: g.ident.ID, AcceptedTenant: g.tenantID,
		AcceptedLicense: engineLicenceLabel(res),
		Resolved:        res,
		FileFlag:        flag, Attach: b.Attach,
	})
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// The acceptance is audited whether or not the download later succeeds: the person agreed
	// to the terms at this moment, and that is true even if Hugging Face then times out.
	// Scoped to the granting tenant, so a tenant_admin's ingest is readable in that tenant's
	// own audit view rather than only in the deployment-wide one.
	a.auditFor(r, g, "engine."+key+".ingest",
		id+" from "+res.Source+" (licence "+engineLicenceLabel(res)+" accepted)")
	writeJSON(w, http.StatusOK, engineIngestJobRow(job))
}

// listIngest (GET …/ingest) is the job list, reconciled against ECS first so that what it
// reports is what ECS thinks rather than what this table last heard.
func (a engineAdminAPI) listIngest(w http.ResponseWriter, r *http.Request, _ engineIngestGrant) {
	key := strings.TrimSpace(r.PathValue("key"))
	if a.reg.get(key) == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	if ing := a.reg.ingester(); ing != nil {
		ing.reconcile(r.Context())
	}
	if a.mgr == nil || a.mgr.store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"jobs": []any{}})
		return
	}
	jobs, err := a.mgr.store.ListEngineIngestJobs(r.Context(), key, 20)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, engineIngestJobRow(j))
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

// engineIngestJobRow is one job as the panel reads it. The SPEC is not on the wire: it is this
// process's own shape, it holds nothing the panel does not already have, and a job list is not
// where a catalogue row should be edited.
func engineIngestJobRow(j store.EngineIngestJob) map[string]any {
	row := map[string]any{
		"id": j.ID, "model_id": j.ModelID, "s3_key": j.S3Key,
		"source": j.Source, "state": j.State, "created_at": j.CreatedAt,
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
func engineAttachAllowed(row *store.EngineModel, id, key, flag string) *apiError {
	if row == nil {
		return &apiError{http.StatusNotFound, errCodeEngineModelUnknown,
			"no model " + id + " for engine " + key + " to attach this file to"}
	}
	if flag == "" {
		return &apiError{http.StatusBadRequest, errCodeEngineBadBody,
			"attaching a file to " + id + " needs file_flag: an unlabelled file is the checkpoint itself, and a row has one"}
	}
	for _, f := range row.Files {
		if strings.TrimSpace(f.Flag) == flag {
			return &apiError{http.StatusConflict, errCodeIngestIDExists,
				id + " already declares a " + flag + " file (" + f.S3Key + ") — forget the row, or take this in as its own"}
		}
	}
	return nil
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
