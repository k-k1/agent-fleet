package main

// engine_civitai_red.go — whether this deployment offers Civitai Red as a search source in the
// model catalogue.
//
// civitai.red is the sister domain Civitai split off for NSFW browsing (engine_ingest.go), and
// the tab pointed at it is the only place `nsfw=true` is ever sent. What that puts on screen is
// example images: the card draws Civitai's own preview unblurred and writes `nsfw_level` beside
// it as a number. A deployment whose operator did not ask for that should not meet it by opening
// a panel, so the shipped default is that the source does not exist here.
//
// Two levels, because "may this deployment show it at all" and "is it showing right now" are
// different people's questions:
//
//   - AF_ENGINE_CIVITAI_RED says whether the feature exists here. It is the deployer's, set on
//     the stack, and unset everywhere by default — so a standard build hides the source AND
//     offers no switch for it, rather than showing an administrator a control they were not
//     meant to have.
//   - the engine_civitai_red setting is the super_admin's on/off inside a deployment that does
//     offer it, so covering it up for a while is not a CloudFormation run — the same argument
//     engine_admin.go makes for the engine mode.
//
// 🔴 The gate bites in answerSearch, not in the Console. The ingest routes admit a tenant_admin
// of a tenant granted `allow_engine_ingest` (engine_ingest_perm.go), so a tab the Console does
// not draw is a tab somebody else can still ask for by hand.
//
// What this is NOT: a content filter. Measured 2026-09-14 — the plain `civitai` tab's own
// default query answers models rated `nsfw_level` 15-31, and only the strongest levels are
// missing from it. Hiding this source means the deployment does not OFFER NSFW browsing; it does
// not mean nothing explicit is reachable from the panel. Pasting a civitai.red URL into the
// ingest form still resolves either way, and deliberately: the detail API is domain-blind and
// both hosts share model ids, so refusing the red spelling only asks somebody to retype the
// civitai.com one for the same file.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineCivitaiRedSetting is the deployment-wide row, "on" or "off". Unset means the boot value,
// which is on wherever the feature is offered at all: setting AF_ENGINE_CIVITAI_RED is already
// the act of asking for the source, and making that take a second press in the Console would
// read as a broken switch.
const engineCivitaiRedSetting = "engine_civitai_red"

// engineCivitaiRedOffered is AF_ENGINE_CIVITAI_RED, read once at start. A variable rather than a
// read per request because it is a property of the deployment, not of the moment — and because
// a test has to be able to answer it locally.
var engineCivitaiRedOffered = envBool("AF_ENGINE_CIVITAI_RED", false)

// The catalogue search's sources. The order is the Console's to choose (each tab strip has its
// own), so what travels is a membership list and not a layout.
const engineCivitaiRedSource = "civitai-red"

var engineCatalogSourcesPlain = []string{"hf", "civitai"}

// engineCivitaiRed is the gate: the deployment's permission and the administrator's choice
// within it.
type engineCivitaiRed struct {
	settings store.SettingsStore // nil in tests and before the store exists
	offered  bool
}

// available reports whether this deployment may offer the source at all — the deployer's half,
// and what decides whether the Console draws the switch.
func (g engineCivitaiRed) available() bool { return g.offered }

// on reports whether the source may be searched right now.
//
// An unreadable store answers the boot value rather than "off": the row is an OVERRIDE, and a
// store that cannot be read has not said anything to override it with. That is the same
// tie-break brandResolver.resolve and engineCivitaiTokens.configured already make — a read
// failure never invents a value.
func (g engineCivitaiRed) on(ctx context.Context) bool {
	if !g.offered {
		return false
	}
	if g.settings == nil {
		return true
	}
	v, err := g.settings.GetSetting(ctx, engineCivitaiRedSetting)
	if err != nil {
		log.Printf("engines: civitai-red setting unreadable: %v", err)
		return true
	}
	return strings.TrimSpace(v) != "off"
}

// sources is the source list this deployment's catalogue search offers, for the Console to draw
// its tab strips from. It exists so that "which sources are there" has ONE answer: the three
// tab strips used to carry the list as a literal each, and a gate added to two of three is a
// gate with a way around it.
//
// A fresh slice every time, never the package variable: an answer that shares its backing array
// with the list every other request is about to read is one append away from a wire that changes
// under a caller who did nothing.
func (g engineCivitaiRed) sources(ctx context.Context) []string {
	out := append([]string(nil), engineCatalogSourcesPlain...)
	if g.on(ctx) {
		out = append(out, engineCivitaiRedSource)
	}
	return out
}

// set writes the administrator's choice. Both values are written down — "on" is a row and not a
// deleted one — so that the panel can say the choice was made rather than inferring it from an
// absence it cannot tell from a fresh deployment.
func (g engineCivitaiRed) set(ctx context.Context, enabled bool) error {
	if g.settings == nil {
		return errors.New("no settings store")
	}
	v := "off"
	if enabled {
		v = "on"
	}
	return g.settings.SetSetting(ctx, engineCivitaiRedSetting, v)
}

// engineCivitaiRedUnavailable is the refusal a write gets on a deployment that does not offer
// the source. Not a 404: the route exists, and the reader's next act is on the stack.
func engineCivitaiRedUnavailable() *apiError {
	return &apiError{http.StatusConflict, errCodeEngineCivitaiRedUnavailable,
		"this deployment does not offer the Civitai Red source: set AF_ENGINE_CIVITAI_RED on the control plane"}
}

// engineCivitaiRedOff is the refusal a SEARCH gets. Separate from the one above because the two
// reach different people: this one can reach a granted tenant_admin whose Console simply never
// drew the tab, and it has to read as "not here", not as "you typed it wrong".
func engineCivitaiRedOff() *apiError {
	return &apiError{http.StatusForbidden, errCodeEngineCivitaiRedOff,
		"this deployment does not offer the Civitai Red source"}
}

// --- the admin routes ---------------------------------------------------------

// engineCivitaiRedStatus is what the panel draws: whether the switch exists, and where it is.
type engineCivitaiRedStatus struct {
	Available bool `json:"available"`
	Enabled   bool `json:"enabled"`
}

func (a engineAdminAPI) civitaiRed() engineCivitaiRed {
	return engineCivitaiRed{settings: a.settings, offered: engineCivitaiRedOffered}
}

func (a engineAdminAPI) civitaiRedNow(ctx context.Context) engineCivitaiRedStatus {
	g := a.civitaiRed()
	return engineCivitaiRedStatus{Available: g.available(), Enabled: g.on(ctx)}
}

// getCivitaiRed (GET /api/admin/engines/civitai-red) reads the switch.
func (a engineAdminAPI) getCivitaiRed(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	writeJSON(w, http.StatusOK, a.civitaiRedNow(r.Context()))
}

// putCivitaiRed (PUT, body {enabled:bool}) moves it. super_admin only and audited, like every
// other deployment-wide write.
func (a engineAdminAPI) putCivitaiRed(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	g := a.civitaiRed()
	if !g.available() {
		writeAPIErr(w, engineCivitaiRedUnavailable())
		return
	}
	var b struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	if err := g.set(r.Context(), b.Enabled); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	state := "off"
	if b.Enabled {
		state = "on"
	}
	a.audit(r.Context(), ident, "engine.civitai_red", state)
	log.Printf("engines: Civitai Red source switched %s", state)
	writeJSON(w, http.StatusOK, a.civitaiRedNow(r.Context()))
}
