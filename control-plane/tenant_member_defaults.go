package main

// Tenant defaults for members' Console (ADR 0114 decision 8, P2).
//
//	tenant_admin ──▶ GET/PUT /api/admin/tenants/{slug}/member-defaults
//	member       ──▶ GET /api/me/ui-state                 (carries them as `defaults`)
//
// Today one key: the agent a new member is pointed at first. Tenants differ in which agent they
// pay for, so the first-run card names that one instead of offering a choice the member cannot
// make. It is a pointer, not a restriction: connecting another agent still completes the step.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const tenantMemberDefaultsMaxBody = 4 << 10

// memberDefaultAgents are the kinds a tenant may name: the ones a member connects with their own
// account. shell and ssm need no connection, lcpp is governed by engine access, and agy is
// experimental. This is CP policy, not a catalog fact: the Console derives its
// MEMBER_DEFAULT_AGENTS from the registry's memberDefault cap, and
// console/src/agents/derivedKindLists.test.ts reads this line and holds the two equal, in order
// (SESSION_KINDS order, which is also the order of the refusal text below).
var memberDefaultAgents = []string{"claude", "codex", "cursor", "copilot", "kiro", "opencode", "muse"}

// tenantMemberDefaultsWire is the stored document, the admin PUT body and, for members, the
// `defaults` of GET /api/me/ui-state. Read by core/store/uiState.ts and the tenant editor
// (features/settings/tenant/tenantMemberDefaults.tsx). Add a key here, never an open map.
type tenantMemberDefaultsWire struct {
	Agent string `json:"agent"` // "" = no default
}

func (d tenantMemberDefaultsWire) validate() error {
	if d.Agent == "" {
		return nil
	}
	for _, k := range memberDefaultAgents {
		if d.Agent == k {
			return nil
		}
	}
	return fmt.Errorf("agent must be one of %s, or empty", strings.Join(memberDefaultAgents, ", "))
}

// tenantMemberDefaultsAdminWire is the admin GET/PUT response.
type tenantMemberDefaultsAdminWire struct {
	Tenant    string                   `json:"tenant"`
	Defaults  tenantMemberDefaultsWire `json:"defaults"`
	UpdatedBy string                   `json:"updated_by,omitempty"`
	UpdatedAt string                   `json:"updated_at,omitempty"`
}

// loadTenantMemberDefaults reads the stored document; no row is the zero document. A row that
// no longer decodes (written by a newer CP, then rolled back) is also the zero document: a
// missing pointer costs a member nothing, a failed request would cost them the page.
func loadTenantMemberDefaults(r *http.Request, st store.TenantMemberDefaultsStore, tenantID string) (store.TenantMemberDefaults, tenantMemberDefaultsWire, error) {
	row, ok, err := st.GetTenantMemberDefaults(r.Context(), tenantID)
	if err != nil || !ok {
		return row, tenantMemberDefaultsWire{}, err
	}
	var d tenantMemberDefaultsWire
	if json.Unmarshal([]byte(row.Defaults), &d) != nil || d.validate() != nil {
		return row, tenantMemberDefaultsWire{}, nil
	}
	return row, d, nil
}

// tenantMemberDefaults (GET /api/admin/tenants/{slug}/member-defaults).
func (a adminAPI) tenantMemberDefaults(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	row, d, err := loadTenantMemberDefaults(r, a.mgr.store, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, tenantMemberDefaultsAdminWire{Tenant: t.Slug, Defaults: d, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt})
}

// setTenantMemberDefaults (PUT /api/admin/tenants/{slug}/member-defaults) replaces the document.
func (a adminAPI) setTenantMemberDefaults(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	d, aerr := decodeTenantMemberDefaults(w, r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	b, err := json.Marshal(d)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	row := store.TenantMemberDefaults{TenantID: t.ID, Defaults: string(b), UpdatedBy: ident.ID, UpdatedAt: store.NowTS()}
	if err := a.mgr.store.PutTenantMemberDefaults(r.Context(), row); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	_ = a.mgr.store.InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.member_defaults", Target: t.Slug,
		Detail: "agent=" + d.Agent, At: row.UpdatedAt,
	})
	writeJSON(w, http.StatusOK, tenantMemberDefaultsAdminWire{Tenant: t.Slug, Defaults: d, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt})
}

// decodeTenantMemberDefaults reads a PUT body strictly: a misspelt key would otherwise be dropped
// and the admin would believe the default says something it does not.
func decodeTenantMemberDefaults(w http.ResponseWriter, r *http.Request) (tenantMemberDefaultsWire, *apiError) {
	var d tenantMemberDefaultsWire
	var in *tenantMemberDefaultsWire
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, tenantMemberDefaultsMaxBody))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return d, &apiError{http.StatusRequestEntityTooLarge, "body_too_large", "member defaults are at most 4 KiB"}
	}
	if err != nil {
		return d, &apiError{http.StatusBadRequest, "bad_request", err.Error()}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return d, &apiError{http.StatusBadRequest, "invalid_defaults", "invalid member defaults: " + err.Error()}
	}
	// The save replaces the document, so `null` must not read as "clear everything".
	if in == nil {
		return d, &apiError{http.StatusBadRequest, "invalid_defaults", "member defaults must be an object"}
	}
	d = *in
	// A second Decode must reach the end: dec.More() lets `{"agent":"codex"}]` through.
	if extra := dec.Decode(&struct{}{}); !errors.Is(extra, io.EOF) {
		return d, &apiError{http.StatusBadRequest, "invalid_defaults", "unexpected data after the defaults object"}
	}
	if err := d.validate(); err != nil {
		return d, &apiError{http.StatusBadRequest, "invalid_defaults", err.Error()}
	}
	return d, nil
}
