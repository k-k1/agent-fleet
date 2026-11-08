package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/routegateway"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Off, the switch registers nothing and injects no issuing token.
func TestProvidedRoutesOffByDefault(t *testing.T) {
	t.Setenv(routegateway.EnvSwitch, "")
	mux := http.NewServeMux()
	registerProvidedRouteRoutes(mux, config{mgr: &manager{}})
	for _, p := range []string{"/internal/route/token", routegateway.Prefix + "/model/x/invoke", "/api/admin/provided-routes/deployment"} {
		if _, pattern := mux.Handler(httptest.NewRequest("PUT", p, nil)); pattern != "" {
			t.Fatalf("%s registered as %q with the switch off", p, pattern)
		}
	}
	at := func(k []byte) []byte { return k }
	if env := providedRouteIssueEnv([]byte("root"), at, "m1"); len(env) != 0 {
		t.Fatalf("issuing token injected with the switch off: %v", env)
	}
	t.Setenv(routegateway.EnvSwitch, "1")
	env := providedRouteIssueEnv([]byte("root"), at, "m1")
	if len(env) != 2 || !strings.HasPrefix(env[0], "AF_ROUTE_ISSUE_TOKEN=afri_") || env[1] != "AF_CLAUDE_BEDROCK_DEPLOYMENT_ROUTE=1" {
		t.Fatalf("switch on: %v", env)
	}
	if mid, ok := routegateway.VerifyIssueToken(routegateway.SignKey([]byte("root")), strings.TrimPrefix(env[0], "AF_ROUTE_ISSUE_TOKEN=")); !ok || mid != "m1" {
		t.Fatalf("the injected token does not verify: %q %v", mid, ok)
	}
}

// The deployment source is the super_admin's to register, with an audit row.
func TestProvidedRouteDeploymentSourceAdmin(t *testing.T) {
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
	mgr.master32 = []byte(strings.Repeat("r", 32))
	if _, err := st.UpsertIdentity(ctx, "root@x", "root-x", "super_admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertIdentity(ctx, "plain@x", "plain-x", ""); err != nil {
		t.Fatal(err)
	}
	restoreAuthExemptions(t)
	mux := http.NewServeMux()
	providedRouteRoutes(mux, mgr, routegateway.New(providedRouteDeps(mgr, nil, routegateway.Config{})))
	do := func(method, who, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/admin/provided-routes/deployment", strings.NewReader(body))
		r.Header.Set("X-Forwarded-Email", who)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	const role = "arn:aws:iam::123456789012:role/af-model-route/bedrock"
	body := `{"role_arn":"` + role + `","region":"us-east-1","models":["us.anthropic.claude-opus-5-5"]}`
	if w := do("PUT", "plain@x", body); w.Code != http.StatusForbidden {
		t.Fatalf("a non-super_admin registered: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "root@x", ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"source":null}` {
		t.Fatalf("empty get: %d %s", w.Code, w.Body)
	}
	w := do("PUT", "root@x", body)
	if w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	var out struct{ Source routegateway.SourceView }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Source.ID == "" || !out.Source.Active || out.Source.Region != "us-east-1" || out.Source.PolicyVersion != 1 ||
		out.Source.Binding != out.Source.ID+"#"+out.Source.Fingerprint {
		t.Fatalf("registered = %+v", out.Source)
	}
	logs, err := st.ListAuditByTenant(ctx, "", 10)
	if err != nil || len(logs) != 1 || logs[0].Action != "provided_route.deployment_source.put" || logs[0].Target != out.Source.ID ||
		!strings.Contains(logs[0].Detail, "region=us-east-1") {
		t.Fatalf("audit = %+v %v", logs, err)
	}
	// A caller cannot name an external id, or anything else the CP decides.
	if w := do("PUT", "root@x", `{"role_arn":"`+role+`","region":"us-east-1","models":["us.anthropic.claude-opus-5-5"],"external_id":"mine"}`); w.Code != 400 {
		t.Fatalf("an unknown field was accepted: %d %s", w.Code, w.Body)
	}
	if w := do("PUT", "root@x", `{"role_arn":"`+role+`","region":"us-east-1,eu-west-1","models":["us.anthropic.claude-opus-5-5"]}`); w.Code != 400 {
		t.Fatalf("two Regions were accepted: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "root@x", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"binding":"`+out.Source.Binding+`"`) {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	if w := do("PUT", "root@x", `{"role_arn":"not-an-arn","region":"us-east-1","models":["us.anthropic.claude-opus-5-5"]}`); w.Code != 400 ||
		!strings.Contains(w.Body.String(), "invalid_source") {
		t.Fatalf("a bad role ARN: %d %s", w.Code, w.Body)
	}
	if w := do("PUT", "root@x", body+` {"region":"eu-west-1"}`); w.Code != 400 {
		t.Fatalf("trailing JSON was accepted: %d %s", w.Code, w.Body)
	}
	// A store that cannot take the write is the server's failure, never a bad registration.
	deps := providedRouteDeps(mgr, nil, routegateway.Config{})
	deps.Store = failingSourceStore{deps.Store}
	failing := http.NewServeMux()
	providedRouteRoutes(failing, mgr, routegateway.New(deps))
	r := httptest.NewRequest("PUT", "/api/admin/provided-routes/deployment", strings.NewReader(body))
	r.Header.Set("X-Forwarded-Email", "root@x")
	fw := httptest.NewRecorder()
	failing.ServeHTTP(fw, r)
	if fw.Code != http.StatusInternalServerError {
		t.Fatalf("a store failure answered %d %s", fw.Code, fw.Body)
	}
}

type failingSourceStore struct{ routegateway.Store }

func (failingSourceStore) PutProvidedRouteSource(context.Context, store.ProvidedRouteSourceInput) (store.ProvidedRouteSource, error) {
	return store.ProvidedRouteSource{}, errors.New("database is locked")
}

// providedRouteTenantFixture is a CP with the gateway's routes on, a registered deployment
// source, and a tenant with a tenant_admin, a member and a super_admin.
func providedRouteTenantFixture(t *testing.T) (st *store.SQL, mux *http.ServeMux, tn store.Tenant, member string) {
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
	mgr.master32 = []byte(strings.Repeat("r", 32))
	if _, err := st.UpsertIdentity(ctx, "root@x", "root-x", "super_admin"); err != nil {
		t.Fatal(err)
	}
	if tn, err = st.CreateTenant(ctx, "acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ email, key, role string }{{"boss@x", "boss-x", "tenant_admin"}, {"m@x", "m-x", "member"}} {
		id, err := st.UpsertIdentity(ctx, p.email, p.key, "")
		if err != nil {
			t.Fatal(err)
		}
		mv, err := st.EnsureMembership(ctx, id.ID, tn.ID, p.role)
		if err != nil {
			t.Fatal(err)
		}
		if p.role == "member" {
			member = mv.ID
		}
	}
	restoreAuthExemptions(t)
	prev := activeProvidedRouteGateway.Load()
	t.Cleanup(func() { activeProvidedRouteGateway.Store(prev) })
	g := routegateway.New(providedRouteDeps(mgr, nil, routegateway.Config{MaxInFlight: 4}))
	mux = http.NewServeMux()
	providedRouteRoutes(mux, mgr, g)
	if _, err := g.PutDeploymentSource(ctx, routegateway.SourceInput{RoleARN: "arn:aws:iam::123456789012:role/af-model-route/bedrock",
		Region: "us-east-1", Models: []string{"us.anthropic.claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	return st, mux, tn, member
}

func routeAdminDo(mux *http.ServeMux, method, path, who, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Forwarded-Email", who)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// The super_admin grants with a ceiling; the tenant_admin narrows and budgets, never grants; every
// write leaves an audit row.
func TestProvidedRouteTenantAdmin(t *testing.T) {
	st, mux, tn, member := providedRouteTenantFixture(t)
	base := "/api/admin/tenants/" + tn.Slug + "/provided-routes/deployment"
	grant := `{"granted":true,"ceiling_micro":50000000}`
	if w := routeAdminDo(mux, "PUT", base+"/grant", "boss@x", grant); w.Code != http.StatusForbidden {
		t.Fatalf("a tenant_admin granted: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", base+"/grant", "root@x", `{"granted":true}`); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "invalid_input") {
		t.Fatalf("a grant without a ceiling: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", base+"/grant", "root@x", grant+` {}`); w.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", "/api/admin/tenants/nope/provided-routes/deployment/grant", "root@x", grant); w.Code != http.StatusNotFound {
		t.Fatalf("unknown tenant: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", base+"/grant", "root@x", grant); w.Code != 200 {
		t.Fatalf("grant: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", base+"/policy", "m@x", `{"members_only":false,"member_default_micro":1}`); w.Code != http.StatusForbidden {
		t.Fatalf("a member set the policy: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", base+"/policy", "boss@x", `{"members_only":true,"member_default_micro":20000000}`); w.Code != 200 {
		t.Fatalf("policy: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", base+"/members", "boss@x", `{"membership_id":"elsewhere","granted":true}`); w.Code != http.StatusNotFound {
		t.Fatalf("a seat of no tenant: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", base+"/members", "boss@x", `{"membership_id":"`+member+`","granted":true,"budget_micro":-5}`); w.Code != http.StatusBadRequest {
		t.Fatalf("a negative budget: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", base+"/members", "boss@x", `{"membership_id":"`+member+`","granted":true,"budget_micro":7000000}`); w.Code != 200 {
		t.Fatalf("member: %d %s", w.Code, w.Body)
	}
	w := routeAdminDo(mux, "GET", base, "boss@x", "")
	var v routegateway.TenantView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || !v.Granted || *v.CeilingMicro != 50_000_000 || !v.MembersOnly ||
		v.MemberDefaultMicro != 20_000_000 || len(v.Members) != 2 {
		t.Fatalf("view: %d %s", w.Code, w.Body)
	}
	for _, m := range v.Members {
		if m.MembershipID == member && (!m.Granted || m.EffectiveMicro != 7_000_000) {
			t.Fatalf("member view = %+v", m)
		}
	}
	logs, err := st.ListAuditByTenant(context.Background(), tn.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, l := range logs {
		seen[l.Action] = true
	}
	for _, a := range []string{"provided_route.deployment_grant", "provided_route.deployment_policy", "provided_route.deployment_member"} {
		if !seen[a] {
			t.Errorf("no %s audit row in %+v", a, logs)
		}
	}
	if len(logs) != 3 {
		t.Errorf("refused writes left audit rows: %+v", logs)
	}
}

// The create paths' pre-wake refusal: nothing for another route, the switch-off refusal when the
// gateway is not running, and the gateway's own reasons when it is.
func TestProvidedRouteCreateRefusal(t *testing.T) {
	ctx := context.Background()
	prev := activeProvidedRouteGateway.Load()
	activeProvidedRouteGateway.Store(nil)
	if aerr := providedRouteCreateRefusal(ctx, "m1", "bedrock-deployment"); aerr == nil || aerr.status != http.StatusConflict ||
		aerr.code != "route_connection_unavailable" {
		t.Fatalf("switch off: %+v", aerr)
	}
	activeProvidedRouteGateway.Store(prev)
	_, mux, tn, member := providedRouteTenantFixture(t)
	for _, route := range []string{"", "bedrock", "anthropic-api"} {
		if aerr := providedRouteCreateRefusal(ctx, member, route); aerr != nil {
			t.Fatalf("%q refused: %+v", route, aerr)
		}
	}
	if aerr := providedRouteCreateRefusal(ctx, member, " bedrock-deployment "); aerr == nil || aerr.code != "route_not_granted" ||
		aerr.status != http.StatusForbidden {
		t.Fatalf("not granted: %+v", aerr)
	}
	base := "/api/admin/tenants/" + tn.Slug + "/provided-routes/deployment"
	routeAdminDo(mux, "PUT", base+"/grant", "root@x", `{"granted":true,"ceiling_micro":50000000}`)
	if aerr := providedRouteCreateRefusal(ctx, member, "bedrock-deployment"); aerr == nil || aerr.code != "route_budget_exhausted" {
		t.Fatalf("budget zero: %+v", aerr)
	}
	routeAdminDo(mux, "PUT", base+"/policy", "boss@x", `{"members_only":false,"member_default_micro":1000000}`)
	if aerr := providedRouteCreateRefusal(ctx, member, "bedrock-deployment"); aerr != nil {
		t.Fatalf("usable: %+v", aerr)
	}
	// MCP create_session and a schedule's fire reach the same refusal.
	if err := (cpDeps{}).ProvidedRouteErr(ctx, "no-such-membership", "bedrock-deployment"); err == nil {
		t.Fatal("MCP: an unknown membership passed")
	}
}
