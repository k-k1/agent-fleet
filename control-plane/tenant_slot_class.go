// tenant_slot_class.go — the tenant's DEFAULT machine class (docs/log/70 §70.4.3).
//
// Why this is not on PUT /api/admin/tenants/{slug}/limits, which already writes the
// same JSON blob: that endpoint is super_admin-only, and the tenant default is a
// tenant_admin's call. Same split as the tenant's source-network rule (ADR 0047): the
// setting reaches nothing outside this tenant, and it is already bounded from above by
// something only a super_admin can write — allowed_slot_classes, which lives on the
// limits endpoint next to max_workspace_mem for exactly that reason.
//
// So: the operator declares the classes, a super_admin says which of them a tenant may
// use, a tenant_admin picks the tenant's default, and a tenant_admin overrides it per
// member (setUserLimit). Four layers, and only the middle one crosses a tenant border.
package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// tenantSlotClass (GET /api/admin/tenants/{slug}/slot-class) reports the tenant's
// default, the classes it may choose from, and the deployment's own default.
//
// The allowed list is INTERSECTED with what the deployment declares before it is
// shown. A stale allowed_slot_classes naming a class the operator has since removed
// would otherwise put a dead option in the picker, and picking it would resolve back
// to the default with a note — a control that appears to work and does not.
func (a adminAPI) tenantSlotClass(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	lim := a.tenantLimitsFor(r, t.ID)
	sizing := a.mgr.workspaceSizing()
	choices := make([]runtime.WorkspaceSlotClass, 0, len(sizing.SlotClasses))
	for _, c := range sizing.SlotClasses {
		if len(lim.AllowedSlotClasses) == 0 || slices.Contains(lim.AllowedSlotClasses, c.ID) {
			choices = append(choices, c)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant":             t.Slug,
		"slot_class":         lim.SlotClass,
		"classes":            choices,
		"default_slot_class": sizing.DefaultSlotClass,
		// editable is false where the concept does not exist — a runtime with no
		// classes, or a deployment that declared a single unnamed ladder. The screen
		// then says so rather than offering a control that changes nothing.
		"editable": len(sizing.SlotClasses) > 0,
	})
}

// setTenantSlotClass (PUT /api/admin/tenants/{slug}/slot-class {slot_class}).
// "" clears it, i.e. back to the deployment default.
func (a adminAPI) setTenantSlotClass(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	var body struct {
		SlotClass string `json:"slot_class"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	want := strings.TrimSpace(body.SlotClass)
	lim := a.tenantLimitsFor(r, t.ID)
	if want != "" {
		sizing := a.mgr.workspaceSizing()
		declared := slices.ContainsFunc(sizing.SlotClasses, func(c runtime.WorkspaceSlotClass) bool { return c.ID == want })
		allowed := len(lim.AllowedSlotClasses) == 0 || slices.Contains(lim.AllowedSlotClasses, want)
		// Refused rather than stored-and-substituted. resolveSlotClass has to tolerate a
		// stored id that stopped being valid (the operator can drop a class at any
		// redeploy), but an id that is invalid AT THE MOMENT IT IS TYPED is a mistake,
		// and accepting it would show the admin their choice saved while every member
		// silently ran somewhere else.
		if !declared || !allowed {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_slot_class",
				"unknown or not-allowed machine class: " + want})
			return
		}
	}
	lim.SlotClass = want
	lj, err := json.Marshal(lim)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if err := a.mgr.store.SetTenantLimits(r.Context(), t.ID, string(lj)); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// The class is read when the runtime is built, so every memoized runtime in this
	// tenant has to go — otherwise the change reaches only members whose cache happened
	// to be cold.
	a.mgr.evictTenantCache(t.ID)
	writeJSON(w, http.StatusOK, map[string]any{"tenant": t.Slug, "slot_class": want})
}

// tenantLimitsFor reads and parses a tenant's limits, or the zero value. Losing the
// rest of the blob on a read error would silently reset quotas the operator set, so
// callers that WRITE must treat a failure here as "no change is safe" — which the zero
// value is not. Every caller below reads it immediately before writing it back, and a
// store that cannot be read cannot be written either, so the write fails too.
func (a adminAPI) tenantLimitsFor(r *http.Request, tenantID string) tenantLimits {
	t, err := a.mgr.store.GetTenant(r.Context(), tenantID)
	if err != nil {
		return tenantLimits{}
	}
	return parseLimits(t.Limits)
}
