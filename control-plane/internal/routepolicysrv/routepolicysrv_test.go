package routepolicysrv

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/routepolicy"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// fakeCP is the CP seam over a real store: the caller is named by X-Who, and roles come from
// the store (super_admin on the identity, tenant_admin on the membership).
type fakeCP struct{ st store.Store }

func (f fakeCP) Store() store.Store { return f.st }

// testRoot is the fake CP's signing root; VerifyBridge checks a token under it with no epoch.
var testRoot = []byte(strings.Repeat("k", 32))

func (fakeCP) SigningRoot() []byte { return testRoot }

func (fakeCP) VerifyBridge(_ context.Context, signKey []byte, token string, verify func([]byte, string) (string, bool)) (string, bool) {
	return verify(signKey, token)
}

func (f fakeCP) who(w http.ResponseWriter, r *http.Request) (store.Identity, bool) {
	id, ok, err := f.st.GetIdentityByUserKey(r.Context(), r.Header.Get("X-Who"))
	if err != nil || !ok {
		writeAPIErr(w, &APIError{http.StatusUnauthorized, "unauthenticated", "no such caller"})
		return store.Identity{}, false
	}
	return id, true
}

func (f fakeCP) SuperAdminFor(w http.ResponseWriter, r *http.Request) (store.Identity, bool) {
	id, ok := f.who(w, r)
	if ok && id.Role != "super_admin" {
		writeAPIErr(w, &APIError{http.StatusForbidden, "forbidden", "super_admin required"})
		return id, false
	}
	return id, ok
}

func (f fakeCP) TenantAdminFor(w http.ResponseWriter, r *http.Request, slug string) (store.Identity, store.Tenant, bool) {
	id, ok := f.who(w, r)
	if !ok {
		return id, store.Tenant{}, false
	}
	t, found, err := f.st.GetTenantBySlug(r.Context(), slug)
	if err != nil || !found {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return id, t, false
	}
	if id.Role == "super_admin" {
		return id, t, true
	}
	m, has, err := f.st.GetMembership(r.Context(), id.ID, t.ID)
	if err != nil || !has || m.Role != "tenant_admin" {
		writeAPIErr(w, &APIError{http.StatusForbidden, "forbidden", "tenant admin required"})
		return id, t, false
	}
	return id, t, true
}

// routePolicyFixture is the policy routes over a real store, with a super_admin (root-x), and a
// tenant with a tenant_admin (boss-x) and a member (m-x).
func routePolicyFixture(t *testing.T, ceiling string) (*store.SQL, *http.ServeMux, store.Tenant) {
	st, mux, tn := routePolicyFixtureWrap(t, ceiling, nil)
	return st, mux, tn
}

// routePolicyFixtureWrap is routePolicyFixture with wrap putting a store in front of the real one.
func routePolicyFixtureWrap(t *testing.T, ceiling string, wrap func(*store.SQL) store.Store) (*store.SQL, *http.ServeMux, store.Tenant) {
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
	if _, err := st.UpsertIdentity(ctx, "root-x", "root-x", "super_admin"); err != nil {
		t.Fatal(err)
	}
	tn, err := st.CreateTenant(ctx, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ email, key, role string }{{"boss-x", "boss-x", "tenant_admin"}, {"m-x", "m-x", "member"}} {
		id, err := st.UpsertIdentity(ctx, p.email, p.key, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.EnsureMembership(ctx, id.ID, tn.ID, p.role); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(routepolicy.EnvCeiling, ceiling)
	prev := routePolicyCeiling.Load()
	t.Cleanup(func() { routePolicyCeiling.Store(prev) })
	if err := LoadCeiling(); err != nil {
		t.Fatal(err)
	}
	if err := SyncCeiling(ctx, st); err != nil {
		t.Fatal(err)
	}
	var cp store.Store = st
	if wrap != nil {
		cp = wrap(st)
	}
	mux := http.NewServeMux()
	Register(mux, fakeCP{cp})
	return st, mux, tn
}

// routeAdminDo calls the mux as who (an identity's user key).
func routeAdminDo(mux *http.ServeMux, method, path, who, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Who", who)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// auditRowsOf lists the audit rows with the action; tenantID "" spans every tenant.
func auditRowsOf(t *testing.T, st store.Store, tenantID, action string) []store.AuditLog {
	t.Helper()
	logs, err := st.ListAuditByTenant(context.Background(), tenantID, 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditLog
	for _, l := range logs {
		if l.Action == action {
			out = append(out, l)
		}
	}
	return out
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
	w := routeAdminDo(mux, "GET", "/api/admin/tenants/"+tn.Slug+"/route-policy", "boss-x", "")
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
	if w := routeAdminDo(mux, "PUT", base+"/cap", "boss-x", noSubs); w.Code != http.StatusForbidden {
		t.Fatalf("a tenant_admin wrote the cap: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", base, "m-x", noSubs); w.Code != http.StatusForbidden {
		t.Fatalf("a member wrote the tenant rule: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", "/api/admin/route-policy", "boss-x", "{}"); w.Code != http.StatusForbidden {
		t.Fatalf("a tenant_admin wrote the deployment rule: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", base+"/cap", "root-x", noSubs); w.Code != 200 {
		t.Fatalf("cap: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", base, "boss-x", `{"except":["copilot-login"]}`); w.Code != 200 {
		t.Fatalf("tenant rule: %d %s", w.Code, w.Body)
	}
	v := decodePolicy(t, routeAdminDo(mux, "GET", base, "boss-x", "").Body.String())
	if ok, by := routeAllowed(v, "copilot-login"); ok || strings.Join(by, ",") != routepolicy.LayerTenantCap {
		t.Errorf("copilot under a cap without the exception: allowed %v by %v", ok, by)
	}
	if ok, _ := routeAllowed(v, "anthropic-api"); !ok || !v.Restricted {
		t.Errorf("anthropic-api must stay allowed under no-subscriptions: %+v", v)
	}
	// The tenant_admin clearing their rule leaves the cap in force.
	if w := routeAdminDo(mux, "PUT", base, "boss-x", `{}`); w.Code != 200 {
		t.Fatalf("clear: %d %s", w.Code, w.Body)
	}
	v2 := decodePolicy(t, routeAdminDo(mux, "GET", base, "boss-x", "").Body.String())
	if ok, _ := routeAllowed(v2, "anthropic-subscription"); ok || v2.Revision <= v.Revision {
		t.Errorf("after the tenant rule was cleared: subscription allowed %v, revision %d (was %d)", ok, v2.Revision, v.Revision)
	}
	// Refused at save: an unknown route id, a per-connection exception, an unknown class.
	for _, bad := range []string{`{"except":["no-such-route"]}`, `{"except":["member-llamacpp"]}`, `{"classes":["nope"]}`, `{"colour":1}`} {
		if w := routeAdminDo(mux, "PUT", base, "boss-x", bad); w.Code != http.StatusBadRequest {
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
		w := routeAdminDo(mux, "PUT", path, "root-x", `{}`)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "route_policy_exceeds_ceiling") {
			t.Errorf("%s widened to everything: %d %s", path, w.Code, w.Body)
		}
		if w := routeAdminDo(mux, "PUT", path, "root-x", `{"classes":["provided"],"deny":["bedrock-tenant"]}`); w.Code != 200 {
			t.Errorf("%s narrowed: %d %s", path, w.Code, w.Body)
		}
	}
	w := routeAdminDo(mux, "GET", "/api/admin/route-policy", "root-x", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"raw":"classes=provided"`) || !strings.Contains(w.Body.String(), `"source":"deployment"`) {
		t.Errorf("deployment view: %d %s", w.Code, w.Body)
	}
	v := decodePolicy(t, routeAdminDo(mux, "GET", "/api/admin/tenants/"+tn.Slug+"/route-policy", "boss-x", "").Body.String())
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
		if err := LoadCeiling(); err == nil {
			t.Errorf("%q started", bad)
		}
	}
	t.Setenv(routepolicy.EnvCeiling, "")
	if err := LoadCeiling(); err != nil || currentCeiling().rule.Restricted() {
		t.Errorf("no ceiling: %v %+v", err, currentCeiling())
	}
}

// auditDupStore makes every policy write's audit row collide with one already stored, so the
// database refuses it.
type auditDupStore struct {
	store.Store
	inner *store.SQL
}

func (s auditDupStore) PutRoutePolicyIf(ctx context.Context, scope, tenantID, rule, actor string, expect *int64,
	audit func(before, after store.RoutePolicyRow) store.AuditLog) (store.RoutePolicyRow, store.RoutePolicyRow, error) {
	return s.inner.PutRoutePolicyIf(ctx, scope, tenantID, rule, actor, expect, func(b, a store.RoutePolicyRow) store.AuditLog {
		l := audit(b, a)
		l.ID = "taken"
		return l
	})
}

// A write whose audit row cannot be recorded is refused and leaves the rule as it was, for every
// layer (D15).
func TestRoutePolicyAuditFailureRefusesTheWrite(t *testing.T) {
	st, mux, tn := routePolicyFixtureWrap(t, "", func(st *store.SQL) store.Store { return auditDupStore{st, st} })
	if err := st.InsertAudit(context.Background(), store.AuditLog{ID: "taken", Action: "x", At: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ path, who, scope, tenant string }{
		{"/api/admin/route-policy", "root-x", store.RoutePolicyDeployment, ""},
		{"/api/admin/tenants/" + tn.Slug + "/route-policy/cap", "root-x", store.RoutePolicyTenantCap, tn.ID},
		{"/api/admin/tenants/" + tn.Slug + "/route-policy", "boss-x", store.RoutePolicyTenant, tn.ID},
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
	_, mux, tn := routePolicyFixtureWrap(t, "", func(st *store.SQL) store.Store { return panicOnSingleRead{st} })
	for _, c := range [][2]string{{"/api/admin/route-policy", "root-x"}, {"/api/admin/tenants/" + tn.Slug + "/route-policy", "boss-x"}} {
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
		return decodePolicy(t, routeAdminDo(mux, "GET", "/api/admin/tenants/"+tn.Slug+"/route-policy", "boss-x", "").Body.String())
	}
	if v := get(); v.Revision != 0 || v.Restricted {
		t.Fatalf("no ceiling: %+v", v)
	}
	restart := func(raw string) {
		t.Helper()
		t.Setenv(routepolicy.EnvCeiling, raw)
		if err := LoadCeiling(); err != nil {
			t.Fatal(err)
		}
		if err := SyncCeiling(ctx, st); err != nil {
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

// The ceiling enforced is this CP's own, from its environment: a database edit of the recorded
// ceiling, or another CP recording a looser one, never widens what this CP answers. It re-records
// its own first, so the revision it answers at is the one its ceiling was recorded at (D9c).
func TestRoutePolicyStoredCeilingCannotWidenTheEnvironment(t *testing.T) {
	st, mux, tn := routePolicyFixture(t, "classes=provided")
	ctx := context.Background()
	get := func() tenantPolicyWire {
		t.Helper()
		w := routeAdminDo(mux, "GET", "/api/admin/tenants/"+tn.Slug+"/route-policy", "boss-x", "")
		if w.Code != 200 {
			t.Fatalf("get: %d %s", w.Code, w.Body)
		}
		return decodePolicy(t, w.Body.String())
	}
	for _, loosen := range []struct{ name, actor string }{{"a database edit", "someone"}, {"another CP's ceiling", "operator"}} {
		_, edited, err := st.PutRoutePolicy(ctx, store.RoutePolicyCeiling, "", "{}", loosen.actor, nil)
		if err != nil {
			t.Fatal(err)
		}
		v := get()
		if ok, by := routeAllowed(v, "openai-api"); ok || len(by) == 0 || by[0] != routepolicy.LayerCeiling {
			t.Errorf("after %s: openai-api allowed %v (by %v)", loosen.name, ok, by)
		}
		if v.Revision <= edited.Revision || !v.Restricted {
			t.Errorf("after %s: revision %d (the loosening was %d), restricted %v", loosen.name, v.Revision, edited.Revision, v.Restricted)
		}
		if row, _ := st.GetRoutePolicy(ctx, store.RoutePolicyCeiling, ""); row.Rule != `{"classes":["provided"]}` {
			t.Errorf("after %s: the recorded ceiling was not restored: %+v", loosen.name, row)
		}
	}
}

// fightingCeilingStore records a looser ceiling after every ceiling this CP records, as a CP with
// another ceiling starting and reading in turn would.
type fightingCeilingStore struct {
	store.Store
	inner *store.SQL
}

func (s fightingCeilingStore) PutRoutePolicy(ctx context.Context, scope, tenantID, rule, actor string,
	audit func(before, after store.RoutePolicyRow) store.AuditLog) (store.RoutePolicyRow, store.RoutePolicyRow, error) {
	b, a, err := s.inner.PutRoutePolicy(ctx, scope, tenantID, rule, actor, audit)
	if err == nil && scope == store.RoutePolicyCeiling {
		_, _, err = s.inner.PutRoutePolicy(ctx, scope, tenantID, "{}", "other-cp", nil)
	}
	return b, a, err
}

// When the recorded ceiling never settles on this CP's own, nothing is answered rather than an
// answer labelled with a revision another CP gives other content.
func TestRoutePolicyUnsettledCeilingAnswersNothing(t *testing.T) {
	st, mux, tn := routePolicyFixtureWrap(t, "classes=provided", func(st *store.SQL) store.Store { return fightingCeilingStore{st, st} })
	if _, _, err := st.PutRoutePolicy(context.Background(), store.RoutePolicyCeiling, "", "{}", "other-cp", nil); err != nil {
		t.Fatal(err)
	}
	if w := routeAdminDo(mux, "GET", "/api/admin/tenants/"+tn.Slug+"/route-policy", "boss-x", ""); w.Code != http.StatusInternalServerError {
		t.Errorf("unsettled ceiling: %d %s, want a refusal", w.Code, w.Body)
	}
}

// bridgeGet is the Agent's pull with a bearer.
func bridgeGet(mux *http.ServeMux, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", BridgePath, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func memberOf(t *testing.T, st *store.SQL, tn store.Tenant, key string) string {
	t.Helper()
	ctx := context.Background()
	id, ok, err := st.GetIdentityByUserKey(ctx, key)
	if err != nil || !ok {
		t.Fatalf("identity %s: %v %v", key, ok, err)
	}
	m, ok, err := st.GetMembership(ctx, id.ID, tn.ID)
	if err != nil || !ok {
		t.Fatalf("membership %s: %v %v", key, ok, err)
	}
	return m.ID
}

// The Agent's pull answers its own tenant's effective policy with the snapshot's revision; with
// no policy anywhere it is unrestricted at revision 0. A bad or foreign-purpose token, or a
// removed member, gets nothing.
func TestBridgePull(t *testing.T) {
	st, mux, tn := routePolicyFixture(t, "")
	mid := memberOf(t, st, tn, "m-x")
	tok := MintToken(SignKey(testRoot), mid)
	decode := func(w *httptest.ResponseRecorder) MemberPolicy {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("pull: %d %s", w.Code, w.Body)
		}
		var p MemberPolicy
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p0 := decode(bridgeGet(mux, tok))
	if p0.Restricted || p0.CeilingRestricted || p0.Revision != 0 || len(p0.Allowed) == 0 {
		t.Fatalf("no policy: %+v", p0)
	}
	if w := routeAdminDo(mux, "PUT", "/api/admin/tenants/"+tn.Slug+"/route-policy", "boss-x", `{"classes":["member-key"]}`); w.Code != 200 {
		t.Fatalf("tenant rule: %d %s", w.Code, w.Body)
	}
	p1 := decode(bridgeGet(mux, tok))
	if !p1.Restricted || p1.CeilingRestricted || p1.Revision <= p0.Revision {
		t.Fatalf("after the tenant rule: %+v", p1)
	}
	for _, id := range p1.Allowed {
		if id == "anthropic-subscription" || id == "member-llamacpp" {
			t.Errorf("%s allowed under classes=member-key", id)
		}
	}
	for _, bad := range []string{"", "afc_nope.x", MintToken(SignKey([]byte("another root")), mid), "afb_" + strings.TrimPrefix(tok, "afc_")} {
		if w := bridgeGet(mux, bad); w.Code != http.StatusUnauthorized {
			t.Errorf("token %q: %d", bad, w.Code)
		}
	}
	if err := st.DeleteMembership(context.Background(), mid); err != nil {
		t.Fatal(err)
	}
	if w := bridgeGet(mux, tok); w.Code != http.StatusUnauthorized {
		t.Errorf("a removed member was answered: %d %s", w.Code, w.Body)
	}
}

// The start environment carries the same answer the pull gives, and the ceiling's flag.
func TestPolicyEnv(t *testing.T) {
	st, _, tn := routePolicyFixture(t, "classes=provided")
	env := PolicyEnv(context.Background(), st, tn.ID)
	if len(env) != 1 || !strings.HasPrefix(env[0], EnvPolicy+"=") {
		t.Fatalf("env = %v", env)
	}
	var p MemberPolicy
	if err := json.Unmarshal([]byte(strings.TrimPrefix(env[0], EnvPolicy+"=")), &p); err != nil {
		t.Fatal(err)
	}
	if !p.Restricted || !p.CeilingRestricted || p.Revision == 0 || slicesContains(p.Allowed, "anthropic-api") || !slicesContains(p.Allowed, "bedrock-deployment") {
		t.Errorf("start policy = %+v", p)
	}
}

func slicesContains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// layersFailStore fails the policy read, as a database outage would.
type layersFailStore struct{ store.Store }

func (layersFailStore) GetRoutePolicyLayers(context.Context, string) (store.RoutePolicyLayers, error) {
	return store.RoutePolicyLayers{}, errors.New("database unavailable")
}

// When the policy cannot be computed at a start, a restricting ceiling still reaches the Agent as
// the unknown marker (it then allows nothing until it pulls), and with no ceiling nothing is sent.
func TestPolicyEnvWhenThePolicyCannotBeRead(t *testing.T) {
	st, _, tn := routePolicyFixture(t, "classes=provided")
	old := nowUnix
	t.Cleanup(func() { nowUnix = old })
	nowUnix = func() int64 { return 1_800_000_000 }
	oldID := newMarkerID
	t.Cleanup(func() { newMarkerID = oldID })
	newMarkerID = func() string { return "m1" }
	if env := PolicyEnv(context.Background(), layersFailStore{st}, tn.ID); len(env) != 1 || env[0] != EnvPolicy+`={"unknown":true,"id":"m1","issuedAt":1800000000}` {
		t.Errorf("restricting ceiling, unreadable policy: %v", env)
	}
	st2, _, tn2 := routePolicyFixture(t, "")
	if env := PolicyEnv(context.Background(), layersFailStore{st2}, tn2.ID); len(env) != 0 {
		t.Errorf("no ceiling, unreadable policy: %v, want nothing", env)
	}
	env := PolicyEnv(context.Background(), st2, tn2.ID)
	if len(env) != 1 || !strings.Contains(env[0], `"issuedAt":1800000000`) {
		t.Errorf("a computed start policy carries when it was issued: %v", env)
	}
}

// Every start's unknown marker has its own id.
func TestUnknownMarkerIDsDiffer(t *testing.T) {
	if a, b := newMarkerID(), newMarkerID(); a == b || len(a) != 24 {
		t.Errorf("marker ids %q %q", a, b)
	}
}

// TestRoutePolicyServesTheVocabulary pins that both policy GETs carry the vocabulary the rule
// validation accepts, so the Console needs no copy of its own: every served class, destination and
// route id is accepted in a rule, the one value Validate refuses (unclassified) is not offered,
// and the two answers agree.
func TestRoutePolicyServesTheVocabulary(t *testing.T) {
	_, mux, _ := routePolicyFixture(t, "")
	type vocabAnswer struct {
		Vocabulary *vocabularyWire `json:"vocabulary"`
	}
	read := func(path, who string) vocabularyWire {
		w := routeAdminDo(mux, "GET", path, who, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s as %s: %d %s", path, who, w.Code, w.Body)
		}
		var a vocabAnswer
		if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
		if a.Vocabulary == nil {
			t.Fatalf("GET %s carries no vocabulary: %s", path, w.Body)
		}
		return *a.Vocabulary
	}
	dep := read("/api/admin/route-policy", "root-x")
	ten := read("/api/admin/tenants/acme/route-policy", "boss-x")
	if !reflect.DeepEqual(dep, ten) {
		t.Errorf("the two GETs disagree:\n%+v\n%+v", dep, ten)
	}
	if len(dep.Classes) == 0 || len(dep.Destinations) == 0 || len(dep.Routes) == 0 {
		t.Fatalf("empty vocabulary: %+v", dep)
	}
	if slices.Contains(dep.Destinations, routepolicy.Unclassified) {
		t.Errorf("unclassified is offered as a destination, which Validate refuses in an allow list")
	}
	// Whatever is served is accepted by the validation, and the one value that is refused is not.
	ids := []string{}
	for _, r := range dep.Routes {
		ids = append(ids, r.ID)
	}
	accepts := routepolicy.Rule{Classes: &dep.Classes, Destinations: &dep.Destinations}
	if err := accepts.Validate(); err != nil {
		t.Errorf("Validate refuses the served classes and destinations: %v", err)
	}
	for _, r := range dep.Routes {
		if r.PerConnection {
			continue
		}
		if err := (routepolicy.Rule{Deny: []string{r.ID}, Except: []string{r.ID}}).Validate(); err != nil {
			t.Errorf("Validate refuses the served route %s: %v", r.ID, err)
		}
	}
	// Every catalogued route is served, none more.
	var want []string
	for _, r := range agentroutes.Routes() {
		want = append(want, r.ID)
	}
	if !slices.Equal(ids, want) {
		t.Errorf("served route ids %v, catalogue %v", ids, want)
	}
	// A member, who cannot read the policy, cannot read the vocabulary either.
	if w := routeAdminDo(mux, "GET", "/api/admin/tenants/acme/route-policy", "m-x", ""); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "vocabulary") {
		t.Errorf("a member's read: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "GET", "/api/admin/route-policy", "boss-x", ""); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "vocabulary") {
		t.Errorf("a tenant admin's deployment read: %d %s", w.Code, w.Body)
	}
}
