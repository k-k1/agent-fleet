package routegateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/providedroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/routeprice"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The pre-wake check answers what a call would: each reason a call is refused is a refusal here,
// and a usable route comes with the binding a selection is stored with and what is left.
func TestUsable(t *testing.T) {
	e := newPREnv(t)
	ctx := context.Background()
	src := e.source()
	u, ref := e.g.Usable(ctx, e.mid, providedroutes.RouteDeployment)
	if ref != nil || u.Binding != src.Binding() || u.Region != "us-east-1" || u.RemainingMicro != 100_000_000 ||
		u.Reset != "2026-12-01T00:00:00Z" || len(u.Models) != 1 {
		t.Fatalf("usable = %+v %v", u, ref)
	}
	code := func() string {
		t.Helper()
		_, ref := e.g.Usable(ctx, e.mid, providedroutes.RouteDeployment)
		if ref == nil {
			return ""
		}
		return ref.Code
	}
	// Spend shrinks what is left; the tighter level wins.
	adm, err := e.st.AdmitProvidedRouteCall(ctx, store.ProvidedRouteCall{CallID: "c1", MembershipID: e.mid, TenantID: e.tenant.ID,
		Source: providedroutes.SourceDeployment, Binding: src.Binding(), Model: prTestModel, API: providedroutes.OpInvoke,
		PriceVersion: routeprice.Version(), ReservedMicro: 30_000_000, AdmittedAt: e.clock().Format(time.RFC3339Nano), CPInstance: "cp1"},
		store.ProvidedRouteLimits{MemberBudgetMicro: 100_000_000})
	if err != nil || !adm.Admitted {
		t.Fatalf("admit: %+v %v", adm, err)
	}
	if u, _ := e.g.Usable(ctx, e.mid, providedroutes.RouteDeployment); u.RemainingMicro != 70_000_000 {
		t.Fatalf("remaining after a reservation = %d", u.RemainingMicro)
	}
	if err := e.g.SetGrant(ctx, e.tenant.ID, GrantInput{Granted: true, CeilingMicro: ptrI64(40_000_000)}); err != nil {
		t.Fatal(err)
	}
	if u, _ := e.g.Usable(ctx, e.mid, providedroutes.RouteDeployment); u.RemainingMicro != 10_000_000 {
		t.Fatalf("remaining under the tenant ceiling = %d", u.RemainingMicro)
	}
	if err := e.g.SetGrant(ctx, e.tenant.ID, GrantInput{Granted: true, CeilingMicro: ptrI64(30_000_000)}); err != nil {
		t.Fatal(err)
	}
	_, ref = e.g.Usable(ctx, e.mid, providedroutes.RouteDeployment)
	if ref == nil || ref.Code != "route_budget_exhausted" || ref.Status != http.StatusServiceUnavailable ||
		!strings.Contains(ref.Message, "the tenant's monthly ceiling") || !strings.Contains(ref.Message, "spend limit") {
		t.Fatalf("ceiling used up: %+v", ref)
	}
	if err := e.g.SetGrant(ctx, e.tenant.ID, GrantInput{Granted: true, CeilingMicro: ptrI64(100_000_000)}); err != nil {
		t.Fatal(err)
	}
	if err := e.g.SetMember(ctx, e.tenant.ID, MemberInput{MembershipID: e.mid, BudgetMicro: ptrI64(30_000_000)}); err != nil {
		t.Fatal(err)
	}
	if _, ref = e.g.Usable(ctx, e.mid, providedroutes.RouteDeployment); ref == nil || !strings.Contains(ref.Message, "your monthly budget") {
		t.Fatalf("member budget used up: %+v", ref)
	}
	if err := e.g.SetMember(ctx, e.tenant.ID, MemberInput{MembershipID: e.mid, BudgetMicro: ptrI64(0)}); err != nil {
		t.Fatal(err)
	}
	if c := code(); c != "route_budget_exhausted" {
		t.Fatalf("budget zero: %q", c)
	}
	if err := e.g.SetMember(ctx, e.tenant.ID, MemberInput{MembershipID: e.mid}); err != nil {
		t.Fatal(err)
	}
	if c := code(); c != "" {
		t.Fatalf("back on the default: %q", c)
	}
	if err := e.g.SetPolicy(ctx, e.tenant.ID, PolicyInput{MembersOnly: true, MemberDefaultMicro: 100_000_000}); err != nil {
		t.Fatal(err)
	}
	if c := code(); c != "route_member_not_granted" {
		t.Fatalf("members only, not granted: %q", c)
	}
	if err := e.g.SetMember(ctx, e.tenant.ID, MemberInput{MembershipID: e.mid, Granted: true}); err != nil {
		t.Fatal(err)
	}
	if c := code(); c != "" {
		t.Fatalf("members only, granted: %q", c)
	}
	e.register(prTestRole, "us-east-1", false, prTestModel)
	if c := code(); c != "route_source_inactive" {
		t.Fatalf("inactive source: %q", c)
	}
	e.register(prTestRole, "us-east-1", true, prTestModel)
	if err := e.g.SetGrant(ctx, e.tenant.ID, GrantInput{}); err != nil {
		t.Fatal(err)
	}
	if c := code(); c != "route_not_granted" {
		t.Fatalf("grant off: %q", c)
	}
	if _, ref := e.g.Usable(ctx, e.mid, providedroutes.RouteTenant); ref == nil || ref.Code != "route_unsupported" {
		t.Fatalf("tenant route: %+v", ref)
	}
	if _, ref := e.g.Usable(ctx, "no-such-membership", providedroutes.RouteDeployment); ref == nil || ref.Code != "route_membership_inactive" {
		t.Fatalf("unknown membership: %+v", ref)
	}
}

// POST /internal/route/check takes the issuing token and keeps the refusal's own status.
func TestCheckEndpoint(t *testing.T) {
	e := newPREnv(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/route/check", e.g.Check)
	check := func(auth, body string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodPost, "/internal/route/check", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+auth)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}
	issuer := MintIssueToken(e.signKey, e.mid)
	code, body := check(issuer, `{"route":"bedrock-deployment"}`)
	var u UsableRoute
	if code != 200 || json.Unmarshal(body, &u) != nil || u.Binding != e.source().Binding() {
		t.Fatalf("check: %d %s", code, body)
	}
	if code, _ := check(e.token(u.Binding, TokenTTL), `{"route":"bedrock-deployment"}`); code != http.StatusUnauthorized {
		t.Fatalf("a route token checked: %d", code)
	}
	for _, b := range []string{`{"route":"bedrock-deployment"} {}`, `{"route":"bedrock-deployment","binding":"x"}`, `[`} {
		if code, body := check(issuer, b); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", b, code, body)
		}
	}
	if err := e.g.SetGrant(context.Background(), e.tenant.ID, GrantInput{}); err != nil {
		t.Fatal(err)
	}
	if code, body := check(issuer, `{"route":"bedrock-deployment"}`); code != http.StatusForbidden || !strings.Contains(string(body), "route_not_granted") {
		t.Fatalf("grant off: %d %s", code, body)
	}
}

// Grant and budget inputs the CP refuses before the store sees them, typed so the API answers 400.
func TestAdminInputs(t *testing.T) {
	e := newPREnv(t)
	ctx := context.Background()
	var invalid *InvalidInputError
	for _, err := range []error{
		e.g.SetGrant(ctx, e.tenant.ID, GrantInput{Granted: true}),
		e.g.SetGrant(ctx, e.tenant.ID, GrantInput{Granted: true, CeilingMicro: ptrI64(0)}),
		e.g.SetGrant(ctx, e.tenant.ID, GrantInput{CeilingMicro: ptrI64(-1)}),
		e.g.SetPolicy(ctx, e.tenant.ID, PolicyInput{MemberDefaultMicro: -1}),
		e.g.SetMember(ctx, e.tenant.ID, MemberInput{MembershipID: e.mid, BudgetMicro: ptrI64(-1)}),
	} {
		if !errors.As(err, &invalid) {
			t.Errorf("%v, want an InvalidInputError", err)
		}
	}
	grant, err := e.st.GetProvidedRouteGrant(ctx, e.tenant.ID, providedroutes.SourceDeployment)
	if err != nil || !grant.Granted || *grant.CeilingMicro != 100_000_000 {
		t.Fatalf("a refused input changed the grant: %+v %v", grant, err)
	}
	// Off without a ceiling is a valid state: nothing is granted.
	if err := e.g.SetGrant(ctx, e.tenant.ID, GrantInput{}); err != nil {
		t.Fatal(err)
	}
	v, err := e.g.View(ctx, e.tenant.ID, []store.MemberInfo{{MembershipID: e.mid, UserKey: "key-m", Email: "m@x"}})
	if err != nil || v.Granted || v.CeilingMicro != nil || v.MemberDefaultMicro != 100_000_000 || len(v.Members) != 1 ||
		v.Members[0].EffectiveMicro != 100_000_000 || v.Month != "2026-11" {
		t.Fatalf("view = %+v %v", v, err)
	}
}
