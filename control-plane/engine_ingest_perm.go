package main

// engine_ingest_perm.go — who may take a model into the catalogue (ADR 0072 open question 11,
// phase P5).
//
// The catalogue has NO tenant axis and is not getting one: engine_models keeps its (role, id)
// primary key, one row set per deployment. That is a measured constraint rather than a
// simplification — every enabled model is synced onto the box at every cold start, at ~159 MB/s
// and in series, so a ten-minute cold start holds about five models. Give each tenant its own
// catalogue and five tenants with one model each already exceed it.
//
// So the tenant axis lands on the two places it costs nothing:
//
//  1. HERE — who may START an ingest. A super_admin always, plus a tenant_admin of a tenant the
//     operator granted `allow_engine_ingest`. Nothing else moves: enabling a model, choosing the
//     selected checkpoint, forgetting a row and registering the deployment's Hugging Face token
//     stay super_admin, because each of those decides what every OTHER tenant runs.
//  2. On the row the ingest produces — the acceptance becomes
//     (tenant_id, member_id, accepted_at, license), so "who let this model in" is answerable
//     after the job row is gone.
//
// 🔴 The cost this accepts, and it must be said out loud rather than assumed away: because the
// catalogue is one, EVERY model id is visible from every tenant. A deployment that reads the
// grant as isolation would be wrong, so guide/admin/04 states it.

import (
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineIngestGrant is the caller of an ingest route and the authority they are acting under.
//
// TenantID is EMPTY for a super_admin. That is not a missing value: the operator is authorized
// deployment-wide and has no tenant to be acting for, and writing one in (say, whichever tenant
// header the Console happened to send) would put a tenant's name on an act it did not perform.
type engineIngestGrant struct {
	ident    store.Identity
	tenantID string
}

// ingestAdminFor resolves the caller and answers whether they may drive the ingest routes.
// Writes 401/403 and reports ok=false, like superAdminFor next door.
//
// Order matters: the super_admin check comes first and asks the store nothing. A deployment
// with no memberships at all (AF_PROVISION=invite before the first tenant exists) has to keep
// working for the one person who can set it up.
func (a engineAdminAPI) ingestAdminFor(w http.ResponseWriter, r *http.Request) (engineIngestGrant, bool) {
	ident, aerr := a.mgr.identityFor(r.Context(), r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return engineIngestGrant{}, false
	}
	if ident.Role == "super_admin" {
		return engineIngestGrant{ident: ident}, true
	}
	if a.mgr == nil || a.mgr.store == nil {
		writeAPIErr(w, &apiError{http.StatusForbidden, "forbidden", "super_admin required"})
		return engineIngestGrant{}, false
	}
	// ListMemberships returns ACTIVE rows only, so a tenant_admin taken off the roster stops
	// passing here as soon as the row is deactivated — the same property anyTenantAdminFor
	// relies on.
	ms, err := a.mgr.store.ListMemberships(r.Context(), ident.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return engineIngestGrant{}, false
	}
	for _, mv := range ms {
		if mv.Role != "tenant_admin" {
			continue
		}
		t, err := a.mgr.store.GetTenant(r.Context(), mv.TenantID)
		if err != nil {
			// One unreadable tenant must not decide the answer for the others.
			continue
		}
		if parseLimits(t.Limits).AllowEngineIngest {
			return engineIngestGrant{ident: ident, tenantID: t.ID}, true
		}
	}
	// The refusal names the grant rather than the role, because "super_admin required" would
	// send a tenant_admin who HAS the grant on a deployment that has not switched it on to ask
	// for the wrong thing.
	writeAPIErr(w, &apiError{http.StatusForbidden, "forbidden",
		"taking a model in needs super_admin, or a tenant whose operator allowed model ingest"})
	return engineIngestGrant{}, false
}

// withIngestAdmin is the wrapper form, and the reason the ingest handlers take a grant rather
// than an identity: what they record about the acceptance is the pair, not the person alone.
func (a engineAdminAPI) withIngestAdmin(h func(http.ResponseWriter, *http.Request, engineIngestGrant)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g, ok := a.ingestAdminFor(w, r)
		if !ok {
			return
		}
		h(w, r, g)
	}
}

// auditFor is audit() with the tenant filled in. Kept separate rather than widening audit():
// every other engine action IS deployment-wide (a mode, a class, a selected checkpoint), and a
// tenant id on those rows would claim a scope they do not have.
func (a engineAdminAPI) auditFor(r *http.Request, g engineIngestGrant, action, target string) {
	if a.mgr == nil || a.mgr.store == nil {
		return
	}
	_ = a.mgr.store.InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: g.tenantID, ActorKind: "admin", ActorID: g.ident.ID,
		Action: action, Target: target, At: store.NowTS(),
	})
}
