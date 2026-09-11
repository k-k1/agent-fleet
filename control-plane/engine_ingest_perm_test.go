package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
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

// --- the reduced panel ---------------------------------------------------------

// engineReducedAPI is a registry with a catalogue row and an ECS view, so the full row carries
// the fields the reduced one has to be missing. A row with nothing in it would let an empty
// trim pass every assertion below.
func engineReducedAPI(t *testing.T) (engineAdminAPI, *engineRuntimeState, store.Store) {
	t.Helper()
	st := testSettingsStore(t)
	reg, e := newAdminTestRegistry(t, &engineTestECS{desired: 1, running: 1}, st)
	e.catalog = newEngineCatalog(st, "image")
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: "sdxl-base-1.0", Kind: "checkpoint", Enabled: true, Selected: true,
		Files:       []store.EngineModelFile{{S3Key: "image/sdxl.safetensors", Bytes: 6938040714}},
		Description: "SDXL 1.0", BaseModel: "sdxl", VramMiB: 9000,
		License: "openrail++", LicenseName: "CreativeML Open RAIL++-M",
		LicenseURL: "https://example.invalid/licence", CommercialUse: "yes",
		Source: "hf:stabilityai/x", Precision: "fp16",
		LicenseAcceptedBy: "u0", LicenseAcceptedAt: "2026-09-10T00:00:00Z",
		LicenseAcceptedTenant: "t-other", LicenseAcceptedLicense: "openrail++",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}, e, st
}

// engineOperatorOnlyFields is what a granted tenant_admin must NOT be handed. Each one is either
// a control they cannot use or a fact about the box they do not pay for, and the list is spelled
// out rather than derived so that a field ADDED to the operator's row has to be classified
// deliberately — the default for a new field is "it leaks", and a derived list would quietly
// let it through.
var engineOperatorOnlyFields = []string{
	"mode", "enabled", "managed", "warm", "state", "desired", "events", "service_since",
	"box", "classes", "class", "class_default", "class_is_default", "stop_eta",
	"window_secs", "idle_secs", "window_units", "window_counted_secs", "last_demand",
	"vram_need_mib", "vram_need_source", "vram_fits", "has_models", "models",
}

var engineOperatorOnlyModelFields = []string{
	"selected", "default", "files", "file_rows", "sizes", "args", "source", "precision",
	"context_tokens", "max_output_tokens", "vram_mib", "vram_need_mib", "sync_secs",
	"license_accepted_by", "license_accepted_at", "license_accepted_tenant",
	"license_accepted_license",
}

// TestTenantAdminRowIsASubsetOfTheOperators is the containment ADR 0072 open question 11's
// reduced panel rests on.
//
// 🔴 The Console renders both rows from ONE component, and a field the CP does not send is not
// an error there — it is a control that silently stops appearing. So the reduced row has to be a
// strict subset of the operator's, with the same values: a key that drifted to a different shape
// here would be a second wire contract nobody is testing.
//
// The positive control is the first half: the full row is asserted to CONTAIN the operator-only
// fields before the trim is checked for their absence. Without it, a row that never had them
// (an engine with no ECS, no catalogue) would pass the whole test while proving nothing.
func TestTenantAdminRowIsASubsetOfTheOperators(t *testing.T) {
	a, e, _ := engineReducedAPI(t)
	full := a.row(t.Context(), e)

	// Positive control: the fields this test is about are really on the operator's row.
	for _, k := range []string{"mode", "enabled", "managed", "state", "desired", "has_models", "models"} {
		if _, ok := full[k]; !ok {
			t.Fatalf("the fixture's full row has no %q, so dropping it proves nothing: %v", k, full)
		}
	}
	fullModels, _ := full["model_rows"].([]map[string]any)
	if len(fullModels) != 1 {
		t.Fatalf("the fixture has no catalogue row: %v", full["model_rows"])
	}
	for _, k := range []string{"selected", "source", "license_accepted_by", "files"} {
		if _, ok := fullModels[0][k]; !ok {
			t.Fatalf("the fixture's model row has no %q: %v", k, fullModels[0])
		}
	}

	reduced := engineTenantAdminRow(full)

	// (1) Containment: every key that survived carries the operator's own value.
	for k, v := range reduced {
		got, ok := full[k]
		if !ok {
			t.Errorf("the reduced row invented %q — it must be a subset of the operator's", k)
			continue
		}
		if k == "model_rows" {
			continue // compared row by row below
		}
		if fmt.Sprint(got) != fmt.Sprint(v) {
			t.Errorf("%q differs between the two rows: reduced=%v operator=%v", k, v, got)
		}
	}
	// (2) What must not be there.
	for _, k := range engineOperatorOnlyFields {
		if _, ok := reduced[k]; ok {
			t.Errorf("the reduced row carries the operator-only field %q", k)
		}
	}
	// (3) The same, one level down: a trimmed row is where the S3 keys and another tenant's
	// acceptance would otherwise ride.
	rows, _ := reduced["model_rows"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("model_rows did not survive the trim: %v", reduced["model_rows"])
	}
	if rows[0]["id"] != "sdxl-base-1.0" || rows[0]["enabled"] != true {
		t.Errorf("the reduced model row lost what it is for: %v", rows[0])
	}
	if rows[0]["license_name"] != "CreativeML Open RAIL++-M" || rows[0]["base_model"] != "sdxl" {
		t.Errorf("the reduced model row lost the licence or the family: %v", rows[0])
	}
	for _, k := range engineOperatorOnlyModelFields {
		if _, ok := rows[0][k]; ok {
			t.Errorf("the reduced model row carries the operator-only field %q", k)
		}
	}
	for k, v := range rows[0] {
		got, ok := fullModels[0][k]
		if !ok || fmt.Sprint(got) != fmt.Sprint(v) {
			t.Errorf("model field %q is not the operator's own value: reduced=%v operator=%v (present=%v)", k, v, got, ok)
		}
	}
}

// TestEngineListAnswersEachCallerTheRowTheyMayHave drives the ROUTE, because the trim only
// protects anything if `get` actually applies it — and because `super_admin` on the envelope is
// what the panel branches on.
func TestEngineListAnswersEachCallerTheRowTheyMayHave(t *testing.T) {
	a, _, _ := engineReducedAPI(t)
	list := func(g engineIngestGrant) (bool, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		a.get(rec, httptest.NewRequest("GET", "/api/admin/engines", nil), g)
		if rec.Code != http.StatusOK {
			t.Fatalf("get = %d (%s)", rec.Code, rec.Body.String())
		}
		var out struct {
			Engines []map[string]any `json:"engines"`
			Super   bool             `json:"super_admin"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(out.Engines) != 1 {
			t.Fatalf("engines = %v", out.Engines)
		}
		return out.Super, out.Engines[0]
	}

	// Positive control: the operator still gets everything, including the flag that says so.
	super, row := list(engineIngestGrant{ident: store.Identity{ID: "u0"}, super: true})
	if !super || row["mode"] == nil || row["state"] == nil {
		t.Fatalf("the operator's own answer was trimmed: super=%v row=%v", super, row)
	}

	super, row = list(engineIngestGrant{ident: store.Identity{ID: "u1"}, tenantID: "t-acme"})
	if super {
		t.Error("super_admin came back true for a tenant_admin — the panel draws controls off this")
	}
	// What the ingest form cannot be built without.
	for _, k := range []string{"key", "api", "provider", "model_rows"} {
		if _, ok := row[k]; !ok {
			t.Errorf("the tenant_admin's row is missing %q, which the ingest form needs: %v", k, row)
		}
	}
	for _, k := range engineOperatorOnlyFields {
		if _, ok := row[k]; ok {
			t.Errorf("the route handed a tenant_admin the operator-only field %q", k)
		}
	}
}

// TestIngestJobsAreNarrowedToTheCallersTenant — the job list is what somebody watches for the
// ten minutes before a catalogue row exists, so it has to be the caller's own.
//
// 🔴 The case that matters is the OPERATOR's jobs, which carry no tenant at all. A filter
// written as "narrow when a tenant was resolved" hands exactly those to a tenant_admin, and the
// list still looks plausible — which is why the empty-tenant job is in this fixture.
func TestIngestJobsAreNarrowedToTheCallersTenant(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := t.Context()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	put := func(id, tenant string) {
		t.Helper()
		if err := st.PutEngineIngestJob(ctx, store.EngineIngestJob{
			ID: id, Role: "image", ModelID: id, S3Key: "image/" + id,
			State: store.EngineIngestDone, TenantID: tenant,
		}); err != nil {
			t.Fatalf("put %s: %v", id, err)
		}
	}
	put("j-acme", "t-acme")
	put("j-beta", "t-beta")
	put("j-operator", "") // started by a super_admin: no tenant to be acting for

	ids := func(js []store.EngineIngestJob) []string {
		out := []string{}
		for _, j := range js {
			out = append(out, j.ID)
		}
		sort.Strings(out)
		return out
	}

	// Positive control: the operator sees all three, INCLUDING the untenanted one — which is
	// the only place those are visible at all.
	all, err := st.ListEngineIngestJobs(ctx, "image", 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := ids(all); strings.Join(got, ",") != "j-acme,j-beta,j-operator" {
		t.Fatalf("the operator's list = %v, want all three", got)
	}
	mine, err := st.ListEngineIngestJobsByTenant(ctx, "image", "t-acme", 20)
	if err != nil {
		t.Fatalf("by tenant: %v", err)
	}
	if got := ids(mine); strings.Join(got, ",") != "j-acme" {
		t.Fatalf("acme's list = %v, want [j-acme] only", got)
	}
	// A tenant with nothing of its own gets nothing — not the operator's.
	if got, err := st.ListEngineIngestJobsByTenant(ctx, "image", "t-gamma", 20); err != nil || len(got) != 0 {
		t.Fatalf("a tenant that started nothing = %v (err %v), want empty", ids(got), err)
	}
	// 🔴 And the empty tenant is a VALUE, not "no filter". This is the assertion that fails if
	// the narrowing is ever written as "filter when the tenant is not empty": that version
	// answers this call with all three rows, and a caller whose tenant did not resolve would be
	// handed the whole deployment's downloads while the list still looked plausible.
	if got, err := st.ListEngineIngestJobsByTenant(ctx, "image", "", 20); err != nil ||
		strings.Join(ids(got), ",") != "j-operator" {
		t.Fatalf("the empty tenant = %v (err %v), want [j-operator] only", ids(got), err)
	}
}

// TestIngestJobRecordsTheGrantingTenant closes the loop through the ROUTE: the filter above can
// only work if postIngest wrote the tenant down in the first place.
func TestIngestJobRecordsTheGrantingTenant(t *testing.T) {
	api := &fakeIngestECS{}
	ing, st := testIngester(t, api, nil)
	req := ingestReq()
	req.AcceptedTenant = "t-acme"
	job, aerr := ing.start(t.Context(), req)
	if aerr != nil {
		t.Fatalf("start: %v", aerr.message)
	}
	got, ok, err := st.GetEngineIngestJob(t.Context(), job.ID)
	if err != nil || !ok {
		t.Fatalf("read back: %v %v", ok, err)
	}
	if got.TenantID != "t-acme" {
		t.Errorf("the job forgot whose grant it ran under: %q", got.TenantID)
	}
	// And the operator's own job stays untenanted, which is what keeps it out of every
	// tenant's list.
	operator := ingestReq()
	operator.AcceptedTenant = "" // a super_admin acts for the deployment, not for a tenant
	plain, aerr := ing.start(t.Context(), operator)
	if aerr != nil {
		t.Fatalf("start (operator): %v", aerr.message)
	}
	if got, _, _ := st.GetEngineIngestJob(t.Context(), plain.ID); got.TenantID != "" {
		t.Errorf("a super_admin's job was stamped with tenant %q", got.TenantID)
	}
}
