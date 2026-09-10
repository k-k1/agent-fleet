package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineIngestPermFixture builds a deployment with three people and two tenants, all on a real
// SQLite store, because the gate's whole job is to read rows: memberships, their status and the
// tenant's limits blob. A stub store would be asserting that the code calls what it calls.
//
//	acme    — allow_engine_ingest = true
//	beta    — no grant
//
//	root@example.com   super_admin, member of nothing
//	akira@example.com  tenant_admin of acme      (granted)
//	bea@example.com    tenant_admin of beta      (not granted)
//	minoru@example.com plain member of acme      (granted tenant, wrong role)
func engineIngestPermFixture(t *testing.T) (engineAdminAPI, map[string]string) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := t.Context()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	acme, err := st.CreateTenant(ctx, "acme", "Acme")
	if err != nil {
		t.Fatalf("tenant acme: %v", err)
	}
	beta, err := st.CreateTenant(ctx, "beta", "Beta")
	if err != nil {
		t.Fatalf("tenant beta: %v", err)
	}
	if err := st.SetTenantLimits(ctx, acme.ID, `{"allow_engine_ingest":true}`); err != nil {
		t.Fatalf("grant: %v", err)
	}

	mgr := &manager{
		store: st, authMode: "proxy", emailHeader: "X-Forwarded-Email",
		superAdmins: map[string]bool{"root@example.com": true},
	}
	person := func(email, tenantID, role string) {
		t.Helper()
		ident, err := st.UpsertIdentity(ctx, email, sanitizeUser(email), mgr.roleHintFor(email))
		if err != nil {
			t.Fatalf("identity %s: %v", email, err)
		}
		if tenantID == "" {
			return
		}
		if _, err := st.EnsureMembership(ctx, ident.ID, tenantID, role); err != nil {
			t.Fatalf("membership %s: %v", email, err)
		}
	}
	person("root@example.com", "", "")
	person("akira@example.com", acme.ID, "tenant_admin")
	person("bea@example.com", beta.ID, "tenant_admin")
	person("minoru@example.com", acme.ID, "member")

	reg, _ := newAdminTestRegistry(t, &engineTestECS{}, testSettingsStore(t))
	return engineAdminAPI{memberAuth{mgr}, reg, st}, map[string]string{"acme": acme.ID, "beta": beta.ID}
}

// gateFor runs the gate for one caller and reports (status, granting tenant id).
func gateFor(t *testing.T, a engineAdminAPI, email string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", nil)
	if email != "" {
		r.Header.Set("X-Forwarded-Email", email)
	}
	g, ok := a.ingestAdminFor(rec, r)
	if !ok {
		return rec.Code, ""
	}
	return http.StatusOK, g.tenantID
}

// TestEngineIngestGateFollowsTheTenantGrant is ADR 0072 open question 11's permission half.
//
// 🔴 The POSITIVE controls are the point of this test, not the refusals. A gate that refuses
// everybody passes every "403" case, and this one is easy to write that way: resolve the
// identity, fail to find a grant for any reason at all, refuse. So the granted tenant_admin and
// the super_admin are asserted first, and the granting tenant id is asserted with them — an
// acceptance recorded against the wrong tenant is the failure nobody would notice, because the
// ingest still works.
func TestEngineIngestGateFollowsTheTenantGrant(t *testing.T) {
	a, tenants := engineIngestPermFixture(t)

	// (1) Positive control: a tenant_admin of the granted tenant is let in, and the grant is
	// attributed to that tenant.
	if code, tid := gateFor(t, a, "akira@example.com"); code != http.StatusOK || tid != tenants["acme"] {
		t.Fatalf("granted tenant_admin: code=%d tenant=%q, want 200 and %q", code, tid, tenants["acme"])
	}
	// (2) Positive control: a super_admin is let in with NO tenant. Empty is the answer, not a
	// gap — the operator acts for the deployment, and stamping a tenant on that acceptance
	// would put a tenant's name on an act it did not perform.
	if code, tid := gateFor(t, a, "root@example.com"); code != http.StatusOK || tid != "" {
		t.Fatalf("super_admin: code=%d tenant=%q, want 200 and no tenant", code, tid)
	}
	// (3) A tenant_admin whose tenant was NOT granted.
	if code, _ := gateFor(t, a, "bea@example.com"); code != http.StatusForbidden {
		t.Errorf("ungranted tenant_admin: code=%d, want 403", code)
	}
	// (4) A plain member OF THE GRANTED TENANT. The grant is on the tenant, so this is the case
	// that separates "this tenant may" from "anyone in this tenant may".
	if code, _ := gateFor(t, a, "minoru@example.com"); code != http.StatusForbidden {
		t.Errorf("member of the granted tenant: code=%d, want 403", code)
	}
	// (5) Nobody at all.
	if code, _ := gateFor(t, a, ""); code != http.StatusUnauthorized {
		t.Errorf("anonymous: code=%d, want 401", code)
	}
}

// TestEngineIngestGateFollowsTheGrantBeingWithdrawn — the grant is read per request, not cached
// against the session. Withdrawing it has to bite on the next call, the same way taking a
// tenant_admin off the roster does: a person who can start a Fargate task and add a row every
// tenant then sees must stop being able to the moment the operator says so.
func TestEngineIngestGateFollowsTheGrantBeingWithdrawn(t *testing.T) {
	a, tenants := engineIngestPermFixture(t)
	// Positive control first: without this the assertion below cannot tell "withdrawn" from
	// "never worked".
	if code, _ := gateFor(t, a, "akira@example.com"); code != http.StatusOK {
		t.Fatalf("before the withdrawal: code=%d, want 200", code)
	}
	if err := a.mgr.store.SetTenantLimits(t.Context(), tenants["acme"], `{"max_sessions":4}`); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if code, _ := gateFor(t, a, "akira@example.com"); code != http.StatusForbidden {
		t.Errorf("after the withdrawal: code=%d, want 403", code)
	}
}

// TestEngineIngestGrantSurvivesTheLimitsRoundTrip pins the field across the seam that has
// dropped one before: limits.go owns the json tags, tenantsrv.Limits is only a projection, and
// a field present in one and absent from the other is silently erased on the next save from the
// admin screen. tenants_test.go's reflect check catches a MISSING field; this catches a field
// that is there but not copied.
func TestEngineIngestGrantSurvivesTheLimitsRoundTrip(t *testing.T) {
	in := tenantLimits{AllowEngineIngest: true, MaxSessions: 3}
	if got := tenantLimitsIn(tenantLimitsOut(in)); !got.AllowEngineIngest {
		t.Fatalf("allow_engine_ingest was lost across the seam: %+v", got)
	}
	// And through the blob itself, which is what is actually stored.
	blob, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !parseLimits(string(blob)).AllowEngineIngest {
		t.Fatalf("allow_engine_ingest was lost through the stored blob: %s", blob)
	}
	// A tenant that was never touched has no grant. The default has to be "no" — a deployment
	// that upgrades into this feature must not find every tenant able to add models.
	if parseLimits("").AllowEngineIngest || parseLimits(`{"max_sessions":1}`).AllowEngineIngest {
		t.Error("an untouched tenant came out granted")
	}
}
