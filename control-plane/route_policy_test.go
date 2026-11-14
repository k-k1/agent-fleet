package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/routepolicy"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// routePolicyFixture is a CP with the policy routes, a super_admin (root@x), and a tenant with a
// tenant_admin (boss@x) and a member (m@x).
func routePolicyFixture(t *testing.T, ceiling string) (*store.SQL, *http.ServeMux, store.Tenant) {
	st, mux, tn, _ := routePolicyFixtureMgr(t, ceiling, nil)
	return st, mux, tn
}

// routePolicyFixtureMgr is routePolicyFixture with the manager, and wrap to put a store in front
// of the real one.
func routePolicyFixtureMgr(t *testing.T, ceiling string, wrap func(*store.SQL) store.Store) (*store.SQL, *http.ServeMux, store.Tenant, *manager) {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	mgr := p3Manager(t, st)
	if _, err := st.UpsertIdentity(ctx, "root@x", "root-x", "super_admin"); err != nil {
		t.Fatal(err)
	}
	tn, err := st.CreateTenant(ctx, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ email, key, role string }{{"boss@x", "boss-x", "tenant_admin"}, {"m@x", "m-x", "member"}} {
		id, err := st.UpsertIdentity(ctx, p.email, p.key, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.EnsureMembership(ctx, id.ID, tn.ID, p.role); err != nil {
			t.Fatal(err)
		}
	}
	restoreAuthExemptions(t)
	t.Setenv(routepolicy.EnvCeiling, ceiling)
	prev := routePolicyCeiling.Load()
	t.Cleanup(func() { routePolicyCeiling.Store(prev) })
	if err := loadRoutePolicyCeiling(); err != nil {
		t.Fatal(err)
	}
	if err := syncRoutePolicyCeiling(ctx, st); err != nil {
		t.Fatal(err)
	}
	if wrap != nil {
		mgr.store = wrap(st)
	}
	mux := http.NewServeMux()
	registerRoutePolicyRoutes(mux, config{mgr: mgr})
	return st, mux, tn, mgr
}

func decodePolicy(t *testing.T, body string) tenantPolicyWire {
	t.Helper()
	var v tenantPolicyWire
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return v
}

func routeAllowed(v tenantPolicyWire, id string) (bool, []string) {
	for _, r := range v.Routes {
		if r.ID == id {
			return r.Allowed, r.ForbiddenBy
		}
	}
	return false, []string{"missing"}
}

// No policy anywhere: nothing is restricted and every catalogued route is allowed (D8).
func TestRoutePolicyDefaultIsUnrestricted(t *testing.T) {
	_, mux, tn := routePolicyFixture(t, "")
	w := routeAdminDo(mux, "GET", "/api/admin/tenants/"+tn.Slug+"/route-policy", "boss@x", "")
	if w.Code != 200 {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	v := decodePolicy(t, w.Body.String())
	if v.Restricted || v.Revision != 0 || len(v.Routes) == 0 {
		t.Fatalf("default = %+v", v)
	}
	for _, r := range v.Routes {
		if !r.Allowed {
			t.Errorf("%s refused with no policy", r.ID)
		}
	}
}

// The layers are written by their own roles only, each write audited with before and after; the
// tenant_admin's exception never widens the super_admin's cap (D1d, D7a, D8, D15).
func TestRoutePolicyLayers(t *testing.T) {
	st, mux, tn := routePolicyFixture(t, "")
	base := "/api/admin/tenants/" + tn.Slug + "/route-policy"
	noSubs := `{"classes":["member-key","member-cloud","provided","self-hosted"]}`
	if w := routeAdminDo(mux, "PUT", base+"/cap", "boss@x", noSubs); w.Code != http.StatusForbidden {
		t.Fatalf("a tenant_admin wrote the cap: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", base, "m@x", noSubs); w.Code != http.StatusForbidden {
		t.Fatalf("a member wrote the tenant rule: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", "/api/admin/route-policy", "boss@x", "{}"); w.Code != http.StatusForbidden {
		t.Fatalf("a tenant_admin wrote the deployment rule: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", base+"/cap", "root@x", noSubs); w.Code != 200 {
		t.Fatalf("cap: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", base, "boss@x", `{"except":["copilot-login"]}`); w.Code != 200 {
		t.Fatalf("tenant rule: %d %s", w.Code, w.Body)
	}
	v := decodePolicy(t, routeAdminDo(mux, "GET", base, "boss@x", "").Body.String())
	if ok, by := routeAllowed(v, "copilot-login"); ok || strings.Join(by, ",") != routepolicy.LayerTenantCap {
		t.Errorf("copilot under a cap without the exception: allowed %v by %v", ok, by)
	}
	if ok, _ := routeAllowed(v, "anthropic-api"); !ok || !v.Restricted {
		t.Errorf("anthropic-api must stay allowed under no-subscriptions: %+v", v)
	}
	// The tenant_admin clearing their rule leaves the cap in force.
	if w := routeAdminDo(mux, "PUT", base, "boss@x", `{}`); w.Code != 200 {
		t.Fatalf("clear: %d %s", w.Code, w.Body)
	}
	v2 := decodePolicy(t, routeAdminDo(mux, "GET", base, "boss@x", "").Body.String())
	if ok, _ := routeAllowed(v2, "anthropic-subscription"); ok || v2.Revision <= v.Revision {
		t.Errorf("after the tenant rule was cleared: subscription allowed %v, revision %d (was %d)", ok, v2.Revision, v.Revision)
	}
	// Refused at save: an unknown route id, a per-connection exception, an unknown class.
	for _, bad := range []string{`{"except":["no-such-route"]}`, `{"except":["member-llamacpp"]}`, `{"classes":["nope"]}`, `{"colour":1}`} {
		if w := routeAdminDo(mux, "PUT", base, "boss@x", bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", bad, w.Code, w.Body)
		}
	}
	for _, action := range []string{"route_policy.tenant_cap.put", "route_policy.tenant.put"} {
		rows := auditRowsOf(t, st, tn.ID, action)
		if len(rows) == 0 || !strings.Contains(rows[0].Detail, "before=") || !strings.Contains(rows[0].Detail, "after=") {
			t.Errorf("%s: audit rows %+v", action, rows)
		}
	}
}

// Under an operator ceiling, a super_admin cannot widen it, for the deployment rule or a tenant
// cap; narrowing inside it is fine (§8 M1 negative control).
func TestRoutePolicyCeilingRefusesWidening(t *testing.T) {
	_, mux, tn := routePolicyFixture(t, "classes=provided")
	for _, path := range []string{"/api/admin/route-policy", "/api/admin/tenants/" + tn.Slug + "/route-policy/cap"} {
		w := routeAdminDo(mux, "PUT", path, "root@x", `{}`)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "route_policy_exceeds_ceiling") {
			t.Errorf("%s widened to everything: %d %s", path, w.Code, w.Body)
		}
		if w := routeAdminDo(mux, "PUT", path, "root@x", `{"classes":["provided"],"deny":["bedrock-tenant"]}`); w.Code != 200 {
			t.Errorf("%s narrowed: %d %s", path, w.Code, w.Body)
		}
	}
	w := routeAdminDo(mux, "GET", "/api/admin/route-policy", "root@x", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"raw":"classes=provided"`) || !strings.Contains(w.Body.String(), `"source":"deployment"`) {
		t.Errorf("deployment view: %d %s", w.Code, w.Body)
	}
	v := decodePolicy(t, routeAdminDo(mux, "GET", "/api/admin/tenants/"+tn.Slug+"/route-policy", "boss@x", "").Body.String())
	if ok, by := routeAllowed(v, "anthropic-api"); ok || by[0] != routepolicy.LayerCeiling {
		t.Errorf("anthropic-api under classes=provided: allowed %v by %v", ok, by)
	}
}

// An unparsable ceiling refuses the CP's start rather than reading as "allow all" (D9c).
func TestRoutePolicyCeilingRefusesStart(t *testing.T) {
	prev := routePolicyCeiling.Load()
	t.Cleanup(func() { routePolicyCeiling.Store(prev) })
	for _, bad := range []string{"classes=everything", "except=member-llamacpp", "except=no-such-route", "nonsense"} {
		t.Setenv(routepolicy.EnvCeiling, bad)
		if err := loadRoutePolicyCeiling(); err == nil {
			t.Errorf("%q started", bad)
		}
	}
	t.Setenv(routepolicy.EnvCeiling, "")
	if err := loadRoutePolicyCeiling(); err != nil || currentCeiling().rule.Restricted() {
		t.Errorf("no ceiling: %v %+v", err, currentCeiling())
	}
}

// auditDupStore makes every policy write's audit row collide with one already stored, so the
// database refuses it.
type auditDupStore struct {
	store.Store
	inner *store.SQL
}

func (s auditDupStore) PutRoutePolicy(ctx context.Context, scope, tenantID, rule, actor string,
	audit func(before, after store.RoutePolicyRow) store.AuditLog) (store.RoutePolicyRow, store.RoutePolicyRow, error) {
	return s.inner.PutRoutePolicy(ctx, scope, tenantID, rule, actor, func(b, a store.RoutePolicyRow) store.AuditLog {
		l := audit(b, a)
		l.ID = "taken"
		return l
	})
}

// A write whose audit row cannot be recorded is refused and leaves the rule as it was, for every
// layer (D15).
func TestRoutePolicyAuditFailureRefusesTheWrite(t *testing.T) {
	st, mux, tn, _ := routePolicyFixtureMgr(t, "", func(st *store.SQL) store.Store { return auditDupStore{st, st} })
	if err := st.InsertAudit(context.Background(), store.AuditLog{ID: "taken", Action: "x", At: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ path, who, scope, tenant string }{
		{"/api/admin/route-policy", "root@x", store.RoutePolicyDeployment, ""},
		{"/api/admin/tenants/" + tn.Slug + "/route-policy/cap", "root@x", store.RoutePolicyTenantCap, tn.ID},
		{"/api/admin/tenants/" + tn.Slug + "/route-policy", "boss@x", store.RoutePolicyTenant, tn.ID},
	} {
		if w := routeAdminDo(mux, "PUT", c.path, c.who, `{"deny":["openai-api"]}`); w.Code != http.StatusInternalServerError {
			t.Errorf("%s: %d %s, want a refusal", c.path, w.Code, w.Body)
		}
		if r, _ := st.GetRoutePolicy(context.Background(), c.scope, c.tenant); r.Revision != 0 || r.Rule != "{}" {
			t.Errorf("%s: the rule changed without its audit row: %+v", c.path, r)
		}
	}
}

// panicOnSingleRead fails a test that reads the layers one by one instead of in one snapshot.
type panicOnSingleRead struct{ store.Store }

func (panicOnSingleRead) GetRoutePolicy(context.Context, string, string) (store.RoutePolicyRow, error) {
	panic("the layers must be read in one snapshot (GetRoutePolicyLayers)")
}

// The admin read takes every layer and its revision from one snapshot.
func TestRoutePolicyReadIsOneSnapshot(t *testing.T) {
	_, mux, tn, _ := routePolicyFixtureMgr(t, "", func(st *store.SQL) store.Store { return panicOnSingleRead{st} })
	for _, c := range [][2]string{{"/api/admin/route-policy", "root@x"}, {"/api/admin/tenants/" + tn.Slug + "/route-policy", "boss@x"}} {
		if w := routeAdminDo(mux, "GET", c[0], c[1], ""); w.Code != 200 {
			t.Errorf("%s: %d %s", c[0], w.Code, w.Body)
		}
	}
}

// A change of the operator ceiling takes a revision of its own, so it reaches every member's
// effective revision; an unchanged ceiling moves nothing (D9c, D12).
func TestRoutePolicyCeilingChangeTakesARevision(t *testing.T) {
	st, mux, tn := routePolicyFixture(t, "")
	ctx := context.Background()
	get := func() tenantPolicyWire {
		t.Helper()
		return decodePolicy(t, routeAdminDo(mux, "GET", "/api/admin/tenants/"+tn.Slug+"/route-policy", "boss@x", "").Body.String())
	}
	if v := get(); v.Revision != 0 || v.Restricted {
		t.Fatalf("no ceiling: %+v", v)
	}
	restart := func(raw string) {
		t.Helper()
		t.Setenv(routepolicy.EnvCeiling, raw)
		if err := loadRoutePolicyCeiling(); err != nil {
			t.Fatal(err)
		}
		if err := syncRoutePolicyCeiling(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	restart("classes=provided")
	v1 := get()
	if v1.Revision == 0 || !v1.Restricted {
		t.Fatalf("after the ceiling was set: %+v", v1)
	}
	if ok, by := routeAllowed(v1, "anthropic-api"); ok || by[0] != routepolicy.LayerCeiling {
		t.Errorf("anthropic-api under the new ceiling: %v %v", ok, by)
	}
	restart("classes=provided")
	if v := get(); v.Revision != v1.Revision {
		t.Errorf("an unchanged ceiling moved the revision: %d -> %d", v1.Revision, v.Revision)
	}
	restart("")
	if v := get(); v.Revision <= v1.Revision || v.Restricted {
		t.Errorf("after the ceiling was lifted: %+v (was revision %d)", v, v1.Revision)
	}
	if rows := auditRowsOf(t, st, "", "route_policy.ceiling.sync"); len(rows) != 2 {
		t.Errorf("ceiling sync audit rows = %d, want 2", len(rows))
	}
}
