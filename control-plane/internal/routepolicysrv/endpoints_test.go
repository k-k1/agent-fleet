package routepolicysrv

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// Endpoint approval through the REST (ADR 0138 §2, §8 M1): the tenant_admin approves exact hosts
// only, inside what the layers above let through and never a vendor's; a super_admin's patterns
// may not widen the ceiling's; the Agent receives the approved hosts and the per-connection
// routes allowed on them.
func TestEndpointApprovalThroughTheLayers(t *testing.T) {
	st, mux, tn := routePolicyFixture(t, "destinations=aws-bedrock,self-hosted;endpoints=*.corp.example")
	tenantPath := "/api/admin/tenants/" + tn.Slug + "/route-policy"
	refused := func(path, who, body, code string) {
		t.Helper()
		w := routeAdminDo(mux, "PUT", path, who, body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), code) {
			t.Errorf("PUT %s %s: %d %s, want 400 %s", path, body, w.Code, w.Body, code)
		}
	}
	refused(tenantPath, "boss-x", `{"endpoints":["llm.other.example"]}`, "route_policy_endpoint_not_approvable")
	refused(tenantPath, "boss-x", `{"endpoints":["api.openai.com"]}`, "invalid_route_policy")
	refused(tenantPath, "boss-x", `{"endpoints":["*.corp.example"]}`, "invalid_route_policy")
	refused(tenantPath+"/cap", "root-x", `{"destinations":["aws-bedrock","self-hosted"],"endpoints":["*.example"]}`, "endpoint patterns")
	refused("/api/admin/route-policy", "root-x", `{"endpoints":["*.anthropic.com"]}`, "invalid_route_policy")

	if w := routeAdminDo(mux, "PUT", tenantPath, "boss-x", `{"endpoints":["LLM.Corp.Example."]}`); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"endpoints":["llm.corp.example"]`) {
		t.Fatalf("approving an allowed host: %d %s", w.Code, w.Body)
	}
	ctx := context.Background()
	p, err := MemberPolicyOf(ctx, st, tn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Endpoints, []string{"llm.corp.example"}) || !slices.Equal(p.EndpointRoutes, []string{"custom-api", "member-llamacpp"}) {
		t.Errorf("member policy: endpoints %v, routes %v", p.Endpoints, p.EndpointRoutes)
	}
	if slices.Contains(p.Allowed, "member-llamacpp") {
		t.Error("member-llamacpp allowed by id, whatever its endpoint")
	}

	// A cap that narrows later leaves the host stored but no longer approved, and a new approval
	// outside it is refused.
	if w := routeAdminDo(mux, "PUT", tenantPath+"/cap", "root-x", `{"destinations":["aws-bedrock","self-hosted"],"endpoints":["gpu.corp.example"]}`); w.Code != 200 {
		t.Fatalf("cap: %d %s", w.Code, w.Body)
	}
	v := decodePolicy(t, routeAdminDo(mux, "GET", tenantPath, "boss-x", "").Body.String())
	if len(v.Endpoints) != 1 || v.Endpoints[0].Approved || !slices.Equal(v.Endpoints[0].ExcludedBy, []string{"tenant_cap"}) {
		t.Errorf("tenant view after the cap: %+v", v.Endpoints)
	}
	if p, _ := MemberPolicyOf(ctx, st, tn.ID); len(p.Endpoints) != 0 || len(p.EndpointRoutes) != 0 {
		t.Errorf("a host the cap excludes reached the Agent: %v %v", p.Endpoints, p.EndpointRoutes)
	}
	refused(tenantPath, "boss-x", `{"endpoints":["llm.corp.example","x.corp.example"]}`, "route_policy_endpoint_not_approvable")

	// A layer that does not allow the self-hosted destination sends no endpoint at all.
	if w := routeAdminDo(mux, "PUT", tenantPath+"/cap", "root-x", `{"destinations":["aws-bedrock"]}`); w.Code != 200 {
		t.Fatalf("cap: %d %s", w.Code, w.Body)
	}
	if p, _ := MemberPolicyOf(ctx, st, tn.ID); len(p.Endpoints) != 0 || len(p.EndpointRoutes) != 0 {
		t.Errorf("endpoints sent while self-hosted is not allowed: %v %v", p.Endpoints, p.EndpointRoutes)
	}
}

// Under a ceiling with a CIDR range the tenant_admin still approves exact IPs only: one inside the
// range is approved and reaches the Agent, one outside it, a host name and a range are refused,
// and a cap that narrows to a smaller range later excludes the stored IP.
func TestEndpointApprovalUnderCIDR(t *testing.T) {
	st, mux, tn := routePolicyFixture(t, "destinations=aws-bedrock,self-hosted;endpoints=10.20.0.0/16")
	tenantPath := "/api/admin/tenants/" + tn.Slug + "/route-policy"
	refused := func(body, code string) {
		t.Helper()
		w := routeAdminDo(mux, "PUT", tenantPath, "boss-x", body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), code) {
			t.Errorf("PUT %s: %d %s, want 400 %s", body, w.Code, w.Body, code)
		}
	}
	refused(`{"endpoints":["10.20.0.0/16"]}`, "invalid_route_policy")
	refused(`{"endpoints":["10.20.8.0/24"]}`, "invalid_route_policy")
	refused(`{"endpoints":["10.21.0.1"]}`, "route_policy_endpoint_not_approvable")
	refused(`{"endpoints":["llm.corp.example"]}`, "route_policy_endpoint_not_approvable")
	refused(`{"endpoints":["10.20.5.7","fd12::5"]}`, "route_policy_endpoint_not_approvable")

	if w := routeAdminDo(mux, "PUT", tenantPath, "boss-x", `{"endpoints":["10.20.5.7","::ffff:10.20.9.9"]}`); w.Code != 200 {
		t.Fatalf("approving IPs inside the range: %d %s", w.Code, w.Body)
	}
	ctx := context.Background()
	p, err := MemberPolicyOf(ctx, st, tn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Endpoints, []string{"10.20.5.7", "10.20.9.9"}) {
		t.Errorf("member policy endpoints = %v", p.Endpoints)
	}

	// A cap range narrower than the ceiling's holds the stored IPs to itself; a cap wider than the
	// ceiling's is refused.
	if w := routeAdminDo(mux, "PUT", tenantPath+"/cap", "root-x", `{"destinations":["aws-bedrock","self-hosted"],"endpoints":["10.0.0.0/8"]}`); w.Code != 400 {
		t.Errorf("a cap wider than the ceiling: %d %s", w.Code, w.Body)
	}
	if w := routeAdminDo(mux, "PUT", tenantPath+"/cap", "root-x", `{"destinations":["aws-bedrock","self-hosted"],"endpoints":["10.20.5.0/24"]}`); w.Code != 200 {
		t.Fatalf("a narrower cap: %d %s", w.Code, w.Body)
	}
	if p, _ := MemberPolicyOf(ctx, st, tn.ID); !slices.Equal(p.Endpoints, []string{"10.20.5.7"}) {
		t.Errorf("after the cap: endpoints = %v, want only 10.20.5.7", p.Endpoints)
	}
}

// With no restriction the Agent receives no endpoint: every route is allowed anyway.
func TestEndpointsUnderNoRestriction(t *testing.T) {
	st, mux, tn := routePolicyFixture(t, "")
	if w := routeAdminDo(mux, "PUT", "/api/admin/tenants/"+tn.Slug+"/route-policy", "boss-x", `{"endpoints":["llm.corp.example"]}`); w.Code != 200 {
		t.Fatalf("approving with no restriction: %d %s", w.Code, w.Body)
	}
	p, err := MemberPolicyOf(context.Background(), st, tn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Restricted || len(p.Endpoints) != 0 || len(p.EndpointRoutes) != 0 {
		t.Errorf("unrestricted member policy: %+v", p)
	}
}
