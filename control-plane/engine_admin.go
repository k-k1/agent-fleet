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
