package main

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// gatewayRec is one route.gateway row of a Bedrock call, as routegw writes it.
func gatewayRec(call, route string, in, out int) usagex.Record {
	return usagex.Record{TS: "2026-11-08T09:00:00Z", Call: call, Key: "route.gateway:" + call,
		Feature: usagex.FeatureRouteGateway, Kind: session.KindClaude, Model: "us.anthropic.claude-opus-5-5",
		Route: route, Billing: usagex.BillingMetered, In: in, Out: out, Spend: in + out, OK: true,
		Measured: usagex.MeasuredExact}
}

// ADR 0129 D7, Q0's gate: a provided row and a member row of one model stay apart in the rollup,
// and the member's own bedrock rows aggregate and serialise exactly as they did before provided
// routes existed.
func TestProvidedRouteRowsStayApartFromMemberBedrock(t *testing.T) {
	member := []usagex.Record{gatewayRec("m1", usagex.RouteBedrock, 100, 10), gatewayRec("m2", usagex.RouteBedrock, 200, 20)}
	provided := []usagex.Record{
		gatewayRec("d1", usagex.RouteBedrockDeployment, 1000, 100),
		gatewayRec("t1", usagex.RouteBedrockTenant, 3000, 300),
	}

	alone := aggregateUsageRows(member, map[string]bool{})
	mixed := aggregateUsageRows(append(slices.Clone(member), provided...), map[string]bool{})
	if len(mixed) != 3 {
		t.Fatalf("%d keys, want 3 (bedrock, bedrock-deployment, bedrock-tenant): %+v", len(mixed), mixed)
	}
	mk := keyOf(member[0])
	if mixed[mk] != alone[mk] {
		t.Fatalf("the member's bedrock aggregate moved: alone %+v, beside provided rows %+v", alone[mk], mixed[mk])
	}
	if a := mixed[mk]; a.In != 300 || a.Out != 30 || a.Calls != 2 {
		t.Fatalf("bedrock = %+v, want in 300 out 30 calls 2", a)
	}
	for _, r := range provided {
		if a := mixed[keyOf(r)]; a.In != r.In || a.Calls != 1 {
			t.Errorf("%s = %+v, want only its own call", r.Route, a)
		}
	}

	// The rollup file keeps them apart too, and the member's entry is written byte for byte as a
	// bedrock entry always was.
	day, ok := mergeRollupDay(usageRollupDay{}, "2026-11-08", mixed)
	if !ok || len(day.Entries) != 3 {
		t.Fatalf("rolled up = %+v ok=%v", day, ok)
	}
	b, err := json.Marshal(day)
	if err != nil {
		t.Fatal(err)
	}
	var back usageRollupDay
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	got := map[string]usageAgg{}
	for _, e := range back.Entries {
		got[e.Key.Route] = e.Agg
	}
	if want := []string{usagex.RouteBedrock, usagex.RouteBedrockDeployment, usagex.RouteBedrockTenant}; !slices.Equal(slices.Sorted(maps.Keys(got)), want) {
		t.Fatalf("rolled-up routes = %v, want %v", slices.Sorted(maps.Keys(got)), want)
	}
	if got[usagex.RouteBedrock] != alone[mk] {
		t.Fatalf("bedrock after the round trip = %+v, want %+v", got[usagex.RouteBedrock], alone[mk])
	}
	kb, _ := json.Marshal(mk)
	if string(kb) != `{"feature":"route.gateway","kind":"claude","model":"us.anthropic.claude-opus-5-5","route":"bedrock","billing":"metered","measured":"exact","ok":true}` {
		t.Fatalf("the member bedrock key's wire changed: %s", kb)
	}
}

const providedRouteCatalogFixture = `{
 "amazon-bedrock": {"models": {"anthropic.claude-opus-5-5": {"cost": {"input": 4.4, "output": 22}}}},
 "anthropic": {"models": {"claude-opus-5-5": {"cost": {"input": 4, "output": 20}}}}
}`

// A provided route is priced from the same amazon-bedrock list as the member's bedrock (D4), never
// from the kind's own provider, whose list price differs.
func TestProvidedRoutesPriceFromTheBedrockList(t *testing.T) {
	useIsolatedUsageDir(t)
	useUsageCatalog(t, providedRouteCatalogFixture)
	for _, route := range []string{usagex.RouteBedrock, usagex.RouteBedrockTenant, usagex.RouteBedrockDeployment} {
		p, src, ok := usagePriceOfOn(session.KindClaude, route, "anthropic.claude-opus-5-5")
		if !ok || p.In != 4.4 || !strings.HasPrefix(src, "catalog:amazon-bedrock/") {
			t.Errorf("%s: %+v %q ok=%v, want the amazon-bedrock row", route, p, src, ok)
		}
	}
}

// Q0 catalogues the provided ids and offers them nowhere: no harness declares one, so a create
// naming one is refused as a route the harness does not accept, the same refusal as any other
// undeclared route.
func TestProvidedRoutesAreCataloguedButNotSelectable(t *testing.T) {
	for id, tier := range map[string]routes.Tier{
		usagex.RouteBedrockTenant:     routes.TierTenantKey,
		usagex.RouteBedrockDeployment: routes.TierDeployment,
	} {
		r, ok := routes.Lookup(id)
		if !ok || r.Wire != routes.WireBedrock || r.Credential != tier {
			t.Errorf("%s: %+v ok=%v, want wire bedrock and tier %s", id, r, ok, tier)
		}
		for _, k := range buildRouteCatalog().Kinds {
			if slices.Contains(k.Routes, id) {
				t.Errorf("kind %q declares %s before its phase (ADR 0129 D7)", k.Kind, id)
			}
		}
		claude, _ := sessionxRoutesOf(session.KindClaude)
		if code, err := routes.CheckStatic(session.KindClaude, &routes.Selection{Route: id}, claude); code != routes.CodeRouteUnsupported || err == nil {
			t.Errorf("claude on %s: code %q err %v, want %s", id, code, err, routes.CodeRouteUnsupported)
		}
	}
}

// sessionxRoutesOf is the harness's declared allow-list as the snapshot carries it.
func sessionxRoutesOf(kind string) ([]string, bool) {
	for _, k := range buildRouteCatalog().Kinds {
		if k.Kind == kind {
			return k.Routes, true
		}
	}
	return nil, false
}
