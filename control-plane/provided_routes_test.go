package main

import (
	"context"
	"encoding/json"
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
	if len(env) != 1 || !strings.HasPrefix(env[0], "AF_ROUTE_ISSUE_TOKEN=afri_") {
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
}
