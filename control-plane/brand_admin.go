package main

// The Admin modal's half of per-deployment branding (brand.go). Colour and label started
// as AF_BRAND_* alone, which meant a redeploy — and on ECS a stack update — to change a
// favicon; the deployment-wide settings that already live in the Admin modal (egress mode,
// the TTS engine, the model catalogue) set the shape this follows: the environment is the
// boot value, deployment_setting holds what an administrator chose, and the stored row wins.
//
// super_admin only, like every other deployment-wide write: this is one setting for
// everyone who signs in, not a per-tenant or per-user preference.

import (
	"encoding/json"
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

type brandAdminAPI struct {
	memberAuth
	brand *brandResolver
}

func registerBrandAdminRoutes(mux *http.ServeMux, cfg config) {
	a := brandAdminAPI{memberAuth{cfg.mgr}, cfg.brand}
	mux.HandleFunc("GET /api/admin/brand", a.withSuperAdmin(a.get))
	mux.HandleFunc("PUT /api/admin/brand", a.withSuperAdmin(a.put))
	// Handing the deployment back to AF_BRAND_* is a distinct act from "choose teal with
	// no label", and the two must not collapse into each other: on a deployment whose
	// environment names a colour they produce different results.
	mux.HandleFunc("DELETE /api/admin/brand", a.withSuperAdmin(a.del))
}

// brandStatus is what the modal draws: the value in force, where it came from, the palette
// to choose from, and what a reset would fall back to.
type brandStatus struct {
	Color  string `json:"color"` // preset name in force
	Hex    string `json:"hex"`
	Label  string `json:"label"`
	Name   string `json:"name"`   // the app name as it now reads, e.g. "[dev] Agent Fleet"
	Source string `json:"source"` // admin | env | default
	// Env is what AF_BRAND_* says, so the modal can name what "follow the environment"
	// would return to instead of just promising a change.
	Env      brandOverride `json:"env"`
	Presets  []brandPreset `json:"presets"`
	MaxLabel int           `json:"max_label"`
}

// brandNow, not "status": TestWireMapGolden resolves the argument of writeJSON by METHOD
// NAME alone, so a second `status` in this package would be reported with ttsAdminAPI's key
// set — a diff nobody wrote, frozen the moment the golden is retaken.
func (a brandAdminAPI) brandNow(r *http.Request) brandStatus {
	b := a.brand.get(r.Context())
	env := brandConfig{}
	if a.brand != nil {
		env = a.brand.env
	}
	source := "default"
	switch {
	case a.brand.stored(r.Context()) != nil:
		source = "admin"
	case env.active():
		source = "env"
	}
	return brandStatus{
		Color: b.colorName, Hex: b.hex, Label: b.label, Name: b.name("Agent Fleet"),
		Source:   source,
		Env:      brandOverride{Color: env.colorName, Label: env.label},
		Presets:  brandPalette,
		MaxLabel: brandLabelMax,
	}
}

func (a brandAdminAPI) get(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	writeJSON(w, http.StatusOK, a.brandNow(r))
}

func (a brandAdminAPI) put(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	var b brandOverride
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "invalid JSON"})
		return
	}
	// The palette is closed on the server too. The modal only offers these, but the
	// endpoint is reachable without it, and an unknown name stored here would show up as
	// "the colour I picked did nothing" long after the request that caused it.
	if _, ok := buildBrand(b.Color, ""); !ok {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_color", "unknown colour: " + b.Color})
		return
	}
	// Store the sanitised label, not what arrived: what is written down has to be what the
	// title bar will show, or the modal reads back something it did not save.
	b.Label = sanitizeBrandLabel(b.Label)
	if err := a.brand.override(r.Context(), &b); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.audit(r, ident, "brand.set", b.Color+" "+b.Label)
	writeJSON(w, http.StatusOK, a.brandNow(r))
}

func (a brandAdminAPI) del(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	if err := a.brand.override(r.Context(), nil); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.audit(r, ident, "brand.reset", "")
	writeJSON(w, http.StatusOK, a.brandNow(r))
}

// audit records who changed the deployment's identity. Cosmetic or not, it is the thing
// that makes one environment look like another, so "who made staging look like production"
// has to be answerable.
func (a brandAdminAPI) audit(r *http.Request, ident store.Identity, action, target string) {
	if a.mgr == nil || a.mgr.store == nil {
		return
	}
	_ = a.mgr.store.InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: "", ActorKind: "admin", ActorID: ident.ID,
		Action: action, Target: target, HTTPStatus: http.StatusOK, At: store.NowTS(),
	})
}
