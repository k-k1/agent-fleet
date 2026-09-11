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
	// super is carried rather than derived from an empty tenantID. The two happen to coincide
	// today, and a reader who leans on that writes `tenantID == ""` at the next call site — at
	// which point the operator and a caller whose tenant could not be resolved become the same
	// thing, and the wrong one of them gets the whole deployment's panel.
	super bool
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
		return engineIngestGrant{ident: ident, super: true}, true
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

// --- the reduced panel (ADR 0072 open question 11, phase P5) --------------------
//
// `GET /api/admin/engines` answers a granted tenant_admin too, with a SUBSET of the operator's
// row. A subset and not a second shape: the Console renders both from one component, and a field
// the CP does not send is simply absent there — no error, no log, just a control that quietly
// stops appearing. Building the reduced row by COPYING named keys out of the full one makes the
// containment true by construction rather than by two lists staying in step, and
// engine_ingest_perm_test.go asserts it against a real row.
//
// What is kept is exactly what the ingest form and the catalogue list need:
//
//   - `key` / `api` / `provider` — which engine this is, and what kind of file it takes;
//   - `base_models` / `file_flags` — the family and per-file vocabularies the ingest form is
//     built from. Without them a split model cannot be described at all;
//   - `model_rows` — trimmed in turn (below), so an id is not taken in twice.
//
// What is dropped is everything that is about the BOX or about changing what other tenants run:
// the mode, the GPU class ladder, the ECS state, the events, the box, the stop ETA, the demand
// window, the VRAM verdict, `has_models`. None of them is secret; all of them are controls or
// numbers the reader cannot act on, and a panel full of those reads as "you may do this" until
// the button 403s.
var engineTenantAdminFields = []string{
	"key", "api", "provider", "base_models", "file_flags",
}

// engineTenantAdminModelFields is one catalogue row as a tenant_admin sees it: enough to know
// which ids are taken, what each one is and under what licence — and nothing that would be a
// control (`selected`, `default`), a bucket path (`files`, `file_rows`), a cost signal
// (`vram_*`, `sync_secs`) or another tenant's business (`license_accepted_*`).
var engineTenantAdminModelFields = []string{
	"id", "kind", "enabled", "description", "base_model",
	"license", "license_name", "license_url", "commercial_use",
}

// engineTenantAdminRow trims one full engine row. Keys absent from the full row stay absent —
// copying a nil in would turn "the CP said nothing" into "the CP said null", which the Console
// draws as a value.
func engineTenantAdminRow(full map[string]any) map[string]any {
	out := pickKeys(full, engineTenantAdminFields)
	if rows, ok := full["model_rows"].([]map[string]any); ok {
		trimmed := make([]map[string]any, 0, len(rows))
		for _, m := range rows {
			trimmed = append(trimmed, pickKeys(m, engineTenantAdminModelFields))
		}
		out["model_rows"] = trimmed
	}
	return out
}

func pickKeys(src map[string]any, keys []string) map[string]any {
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		if v, ok := src[k]; ok {
			out[k] = v
		}
	}
	return out
}

// ingestJobsFor is listIngest's read, narrowed to the caller's authority. The super_admin branch
// is the unfiltered list, which is also the only way the operator's own jobs (no tenant) are
// ever visible.
func (a engineAdminAPI) ingestJobsFor(r *http.Request, g engineIngestGrant, key string) ([]store.EngineIngestJob, error) {
	if g.super {
		return a.mgr.store.ListEngineIngestJobs(r.Context(), key, 20)
	}
	return a.mgr.store.ListEngineIngestJobsByTenant(r.Context(), key, g.tenantID, 20)
}
