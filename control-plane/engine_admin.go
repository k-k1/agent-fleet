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
	mux.HandleFunc("POST /api/admin/engines/{key}/ingest", a.withSuperAdmin(a.postIngest))
	mux.HandleFunc("GET /api/admin/engines/{key}/ingest", a.withSuperAdmin(a.listIngest))
	// Resolving a source WITHOUT starting anything: what the licence is, whether the repository
	// is gated, how big the file is. The panel calls it while somebody is typing, so that the
	// licence they are about to accept is on screen BEFORE the button that accepts it.
	mux.HandleFunc("POST /api/admin/engines/{key}/ingest/resolve", a.withSuperAdmin(a.resolveIngest))
	// And what the repository HAS, so the filename is picked rather than copied by hand across
	// two windows — the same read, filtered to the files this engine could actually load.
	mux.HandleFunc("POST /api/admin/engines/{key}/ingest/files", a.withSuperAdmin(a.listIngestFiles))
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
	for _, m := range catalogue {
		if m.Enabled && !engineModelIsLora(m) {
			ids = append(ids, m.ID)
		}
		modelRows = append(modelRows, engineAdminModelRow(m))
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
	e.ctrl.noteAdminAction() // a cooldown must never refuse the person who pressed the button
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
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}

	ctx := r.Context()
	var (
		found  bool
		err    error
		action string
	)
	switch {
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
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "enabled, selected or default is required"})
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
		ID    string `json:"id"`
		Kind  string `json:"kind"`
		Files []struct {
			Flag  string `json:"flag"`
			S3Key string `json:"s3Key"`
			Bytes int64  `json:"bytes"`
		} `json:"files"`
		Args            []string `json:"args"`
		ContextTokens   int      `json:"context_tokens"`
		MaxOutputTokens int      `json:"max_output_tokens"`
		Sizes           []string `json:"sizes"`
		Description     string   `json:"description"`
		VramMiB         int      `json:"vram_mib"`
		License         string   `json:"license"`
		LicenseName     string   `json:"license_name"`
		LicenseURL      string   `json:"license_url"`
		Precision       string   `json:"precision"`
		BaseModel       string   `json:"base_model"`
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
	for _, f := range b.Files {
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
	var keys []string
	if rows, lerr := a.mgr.store.ListEngineModels(r.Context(), key); lerr == nil {
		for _, m := range rows {
			if m.ID != id {
				continue
			}
			for _, f := range m.Files {
				if f.S3Key != "" {
					keys = append(keys, f.S3Key)
				}
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
		if ing := a.reg.ingester(); ing != nil {
			if err := ing.deleteObjects(r.Context(), keys); err != nil {
				purged = "the row is gone; the files are not: " + err.Error()
			} else {
				purged = "deleting " + strings.Join(keys, " ")
			}
		} else {
			purged = "this deployment declares no ingest task, so the files stay in the bucket"
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
func (a engineAdminAPI) resolveIngest(w http.ResponseWriter, r *http.Request, _ store.Identity) {
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
	writeJSON(w, http.StatusOK, engineResolvedRow(res, a.reg.ingestDef()))
}

// engineResolvedRow is what the panel draws before anything is started. `can_ingest` is the
// verdict this route exists for: a gated repository on a deployment with no HF token cannot be
// taken in, and saying so here costs nothing — finding out from a 401 costs a Fargate task and
// a confused administrator.
func engineResolvedRow(res engineResolved, def engineIngestDef) map[string]any {
	row := map[string]any{
		"sha256":           res.SHA256,
		"bytes":            res.Bytes,
		"gated":            res.Gated,
		"commercial_use":   engineCommercialUse(res),
		"source":           res.Source,
		"can_ingest":       !res.Gated || def.HasToken,
		"deployment_token": def.HasToken,
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
func (a engineAdminAPI) listIngestFiles(w http.ResponseWriter, r *http.Request, _ store.Identity) {
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
func (a engineAdminAPI) postIngest(w http.ResponseWriter, r *http.Request, ident store.Identity) {
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
	// 🔴 An id already in the catalogue is REFUSED, because the row is written by PutEngineModel
	// and that is an upsert on (role, id) — correct for the seed and for registering a staged
	// file, catastrophic here. The job would download for minutes and then replace a working
	// row's files and licence with the new ones AND set enabled=false, so the engine would lose
	// the checkpoint it starts with and nobody would connect the two events.
	//
	// Refusing is also the honest reading of what an ingest is: it CREATES a row (disabled, for
	// an administrator to turn on). Replacing the bytes under an id is a different act, and
	// forgetting the old row first says so out loud.
	for _, m := range e.catalog.list(r.Context()) {
		if m.ID == id {
			writeAPIErr(w, &apiError{http.StatusConflict, errCodeIngestIDExists,
				"this engine already has a model called " + id + " — forget that row first, or choose another id"})
			return
		}
	}
	res, aerr := engineIngestResolve(r.Context(), b.Source)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// ⚠️ Refused BEFORE a task is started. Without the token the download is a 401 nine minutes
	// into a Fargate task, and the message that reaches the panel is an exit code.
	if res.Gated && !ing.def.HasToken {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeIngestGatedNoToken,
			"that repository is gated: accept its terms on Hugging Face with the operator's account " +
				"and give the stack an HfTokenSecretArn — the token is read by the ingest task only"})
		return
	}
	job, aerr := ing.start(r.Context(), engineIngestRequest{
		Role: key, ModelID: id, Kind: strings.TrimSpace(b.Kind), S3Key: s3key,
		Description:   strings.TrimSpace(b.Description),
		BaseModel:     engineFirstNonEmpty(strings.TrimSpace(b.BaseModel), res.BaseModel),
		ContextTokens: b.ContextTokens, MaxOutput: b.MaxOutputTokens, Sizes: b.Sizes,
		AcceptedBy: ident.ID, Resolved: res,
	})
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// The acceptance is audited whether or not the download later succeeds: the operator agreed
	// to the terms at this moment, and that is true even if Hugging Face then times out.
	a.audit(r.Context(), ident, "engine."+key+".ingest",
		id+" from "+res.Source+" (licence "+engineLicenceLabel(res)+" accepted)")
	writeJSON(w, http.StatusOK, engineIngestJobRow(job))
}

// listIngest (GET …/ingest) is the job list, reconciled against ECS first so that what it
// reports is what ECS thinks rather than what this table last heard.
func (a engineAdminAPI) listIngest(w http.ResponseWriter, r *http.Request, _ store.Identity) {
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
