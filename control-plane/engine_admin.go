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
// The models are the STACK'S declaration (ADR 0053), never a question put to the engine: the
// engine is asleep most of the time, so anything only it could answer is unanswerable exactly
// when somebody opens this panel. `llm` is one process holding one GGUF, so the declaration is
// also the truth about what is loaded — which is why `warm` is what qualifies it.
func (a engineAdminAPI) row(ctx context.Context, e *engineRuntimeState) map[string]any {
	mode := e.mode(ctx)
	cfg := e.controlCfg()
	row := map[string]any{
		"key":      e.def.Key,
		"api":      e.def.api(),
		"provider": e.def.Provider,
		"models":   e.def.Models,
		"mode":     mode,
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
		writeAPIErr(w, &apiError{http.StatusNotFound, "engine_unknown", "no engine " + key})
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
		writeAPIErr(w, &apiError{http.StatusNotFound, "engine_unknown", "no engine " + key})
		return
	}
	var b struct {
		Mode    string `json:"mode"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "invalid JSON"})
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
			writeAPIErr(w, &apiError{http.StatusBadGateway, "engine_ecs_error", "ecs update failed: " + err.Error()})
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

// engineModeFromBody reads {mode} — or {enabled} from a client written against a two-valued
// toggle — and refuses anything else. "" with no `enabled` is a client that sent an empty
// body, which must not be read as "switch it off".
func engineModeFromBody(mode string, enabled *bool) (string, *apiError) {
	switch mode {
	case engineModeOff, engineModeOn, engineModeOnDemand:
		return mode, nil
	case "":
		if enabled == nil {
			return "", &apiError{http.StatusBadRequest, "bad_body", "mode is required"}
		}
		if *enabled {
			return engineModeOn, nil
		}
		return engineModeOff, nil
	default:
		return "", &apiError{http.StatusBadRequest, "bad_body", "unknown mode: " + mode}
	}
}
