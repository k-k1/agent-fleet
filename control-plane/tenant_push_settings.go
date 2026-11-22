package main

// Tenant setting for Web Push (#4750): whether a push may carry a question's text.
//
//	tenant_admin ──▶ GET/PUT /api/admin/tenants/{slug}/push-settings
//
// Off by default: the push services (Apple, Google, Mozilla) relay the encrypted payload, and a
// tenant may not accept that for the wording of a question. The setting is stored whether or not
// AF_WEB_PUSH is on, so an admin can prepare it before the operator switches Web Push on.

import (
	"encoding/json"
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

type tenantPushSettingsWire struct {
	Tenant      string `json:"tenant,omitempty"`
	IncludeText bool   `json:"includeText"`
}

func (a adminAPI) tenantPushSettings(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	on, err := a.mgr.store.GetTenantPushIncludeText(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, tenantPushSettingsWire{Tenant: t.Slug, IncludeText: on})
}

func (a adminAPI) setTenantPushSettings(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	var in struct {
		IncludeText *bool `json:"includeText"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || in.IncludeText == nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", `body must be {"includeText": true|false}`})
		return
	}
	if err := a.mgr.store.PutTenantPushIncludeText(r.Context(), t.ID, *in.IncludeText, ident.ID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	detail := "include_text=false"
	if *in.IncludeText {
		detail = "include_text=true"
	}
	_ = a.mgr.store.InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.push_settings", Target: t.Slug, Detail: detail, At: store.NowTS(),
	})
	writeJSON(w, http.StatusOK, tenantPushSettingsWire{Tenant: t.Slug, IncludeText: *in.IncludeText})
}
