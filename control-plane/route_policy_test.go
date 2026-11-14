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
	mux := http.NewServeMux()
	registerRoutePolicyRoutes(mux, config{mgr: mgr})
	return st, mux, tn
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
