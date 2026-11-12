package usagesvc

import (
	"encoding/json"
	"maps"
	"reflect"
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
	if !reflect.DeepEqual(mixed[mk], alone[mk]) {
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
	if !reflect.DeepEqual(got[usagex.RouteBedrock], alone[mk]) {
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
	UseIsolatedUsageDir(t)
	UseUsageCatalog(t, providedRouteCatalogFixture)
	for _, route := range []string{usagex.RouteBedrock, usagex.RouteBedrockTenant, usagex.RouteBedrockDeployment} {
		p, src, ok := usagePriceOfOn(session.KindClaude, route, "anthropic.claude-opus-5-5")
		if !ok || p.In != 4.4 || !strings.HasPrefix(src, "catalog:amazon-bedrock/") {
			t.Errorf("%s: %+v %q ok=%v, want the amazon-bedrock row", route, p, src, ok)
		}
	}
}

// The route gateway writes the row of every call on a provided route, as on the member's bedrock,
// so the transcript's rows of those calls are dropped and each call is counted once.
func TestProvidedRouteTranscriptRowsAreLeftToTheGateway(t *testing.T) {
	for _, route := range []string{usagex.RouteBedrock, usagex.RouteBedrockTenant, usagex.RouteBedrockDeployment} {
		m := session.Meta{Name: "p01", Kind: session.KindClaude, Route: &routes.Selection{Route: route}}
		recs := []usagex.Record{{TS: "2026-11-08T09:00:00Z", Ref: "p01", Route: route}, {TS: "2026-11-08T09:00:00Z", Ref: "p01"}}
		if got := dropGatewayCounted(m, recs); len(got) != 0 {
			t.Errorf("%s: kept %+v, want every row left to the gateway", route, got)
		}
	}
	if gatewayCounted("anthropic-api") || gatewayCounted("") {
		t.Error("a route the gateway does not serve is gateway-counted")
	}
}
