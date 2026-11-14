package usagesvc

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// Two credentials on one route are two rollup keys (ADR 0138 D5, the M0 gate's "the rollup keeps
// two Anthropic keys apart"); rows on the same credential still merge.
func TestAggregateKeepsConnectionsApart(t *testing.T) {
	rec := func(call, conn string) usagex.Record {
		return usagex.Record{Call: call, Feature: usagex.FeatureSession, Kind: session.KindClaude, Model: "claude-opus-5",
			Route: routes.RouteAnthropicAPI, Connection: conn, Out: 5, Spend: 5, OK: true, Measured: usagex.MeasuredExact}
	}
	rows := []usagex.Record{rec("c1", "ANTHROPIC_API_KEY@k1"), rec("c2", "ANTHROPIC_API_KEY@k2"), rec("c3", "ANTHROPIC_API_KEY@k1")}
	agg := aggregateUsageRows(rows, map[string]bool{})
	if len(agg) != 2 {
		t.Fatalf("%d keys, want 2 (one per key): %+v", len(agg), agg)
	}
	if a := agg[keyOf(rows[0])]; a.Spend != 10 || a.Calls != 2 {
		t.Errorf("key 1 = %+v, want spend 10 calls 2", a)
	}
	if !validUsageDim("connection") || usageDimValue(keyOf(rows[1]), "connection") != "ANTHROPIC_API_KEY@k2" {
		t.Error("connection must be a queryable dimension")
	}
}

// The migration: a rollup day written before the field existed loads with no connection, a new
// contribution split by connection merges beside it, and an unrecorded key's wire is unchanged.
func TestRollupConnectionMigration(t *testing.T) {
	old := `{"src":["2026-11-01"],"e":[{"k":{"feature":"session","kind":"claude","model":"claude-opus-5","measured":"exact","ok":true,"route":"anthropic-api"},"v":{"spend":5,"in":1,"out":4,"cread":0,"ccreate":0,"calls":1}}]}`
	var day usageRollupDay
	if err := json.Unmarshal([]byte(old), &day); err != nil {
		t.Fatal(err)
	}
	oldKey := day.Entries[0].Key
	if oldKey.Connection != "" || oldKey.Route != routes.RouteAnthropicAPI {
		t.Fatalf("old key = %+v", oldKey)
	}
	k1, k2 := oldKey, oldKey
	k1.Connection, k2.Connection = "ANTHROPIC_API_KEY@k1", "ANTHROPIC_API_KEY@k2"
	merged, ok := mergeRollupDay(day, "2026-11-02", map[usageKey]usageAgg{k1: {Spend: 3, Calls: 1}, k2: {Spend: 4, Calls: 1}})
	if !ok || len(merged.Entries) != 3 {
		t.Fatalf("merged = %+v ok=%v, want the old entry and one per key", merged, ok)
	}
	b, _ := json.Marshal(merged)
	var back usageRollupDay
	if err := json.Unmarshal(b, &back); err != nil || len(back.Entries) != 3 {
		t.Fatalf("round trip: %v %s", err, b)
	}
	ob, _ := json.Marshal(oldKey)
	if string(ob) != `{"feature":"session","kind":"claude","model":"claude-opus-5","route":"anthropic-api","measured":"exact","ok":true}` {
		t.Fatalf("an unrecorded connection changed the key's wire: %s", ob)
	}
}

// A session row takes the launch record's route and binding where its own evidence leaves the
// gap, and only there (ADR 0138 D11).
func TestStampLaunch(t *testing.T) {
	launch := &routes.Launch{Route: routes.RouteChatGPTSubscription, Binding: "chatgpt-subscription@aa"}
	codex := session.Meta{Name: "c", Kind: session.KindCodex, LaunchRoute: launch}
	ts := "2026-11-15T10:00:00Z"
	for name, tc := range map[string]struct {
		m              session.Meta
		route, conn    string
		wantR, wantCon string
	}{
		"gap filled":            {codex, "", "", launch.Route, launch.Binding},
		"own route agrees":      {codex, launch.Route, "", launch.Route, launch.Binding},
		"own route differs":     {codex, routes.RouteOpenAIAPI, "", routes.RouteOpenAIAPI, ""},
		"own binding kept":      {codex, launch.Route, "x@1", launch.Route, "x@1"},
		"no record (pre-M0)":    {session.Meta{Name: "c", Kind: session.KindCodex}, "", "", "", ""},
		"unresolved record":     {session.Meta{Name: "c", Kind: session.KindCodex, LaunchRoute: &routes.Launch{}}, "", "", "", ""},
		"opencode, no evidence": {session.Meta{Name: "o", Kind: session.KindOpencode, LaunchRoute: &routes.Launch{Route: routes.RouteAnthropicAPI, Binding: "a@1"}}, "", "", "", ""},
		// A dynamic owner's matching route id does not prove the launch credential served it.
		"opencode, same route":       {session.Meta{Name: "o", Kind: session.KindOpencode, LaunchRoute: &routes.Launch{Route: routes.RouteOpencodeZen, Binding: "z@1"}}, routes.RouteOpencodeZen, "", routes.RouteOpencodeZen, ""},
		"lcpp, same route, no proof": {session.Meta{Name: "l", Kind: session.KindLcpp, LaunchRoute: &routes.Launch{Route: routes.RouteMemberLlamacpp, Binding: "m@old"}}, routes.RouteMemberLlamacpp, "", routes.RouteMemberLlamacpp, ""},
		"lcpp, no route":             {session.Meta{Name: "l", Kind: session.KindLcpp, LaunchRoute: &routes.Launch{Route: routes.RouteAFEngineLLM, Binding: "af-engine-llm@llm"}}, "", "", "", ""},
		"before the last switch":     {session.Meta{Name: "s", Kind: session.KindCodex, LaunchRoute: launch, RouteSwitches: []session.RouteSwitch{{At: "2026-11-15T11:00:00Z"}}}, "", "", "", ""},
		"after the last switch":      {session.Meta{Name: "s", Kind: session.KindCodex, LaunchRoute: launch, RouteSwitches: []session.RouteSwitch{{At: "2026-11-15T09:00:00Z"}}}, "", "", launch.Route, launch.Binding},
	} {
		rec := usagex.Record{TS: ts, Route: tc.route, Connection: tc.conn}
		stampLaunch(tc.m, &rec)
		if rec.Route != tc.wantR || rec.Connection != tc.wantCon {
			t.Errorf("%s: route %q conn %q, want %q %q", name, rec.Route, rec.Connection, tc.wantR, tc.wantCon)
		}
	}
	// Through the fold: a codex turn with no per-call evidence is recorded on its launch.
	rows := foldTurnRows([]transcript.Turn{{Role: "user"}, routeTurn("", "", 3)}, true)
	if rec := usageRecordOf(codex, rows[0], ""); rec.Route != launch.Route || rec.Connection != launch.Binding {
		t.Errorf("folded row = route %q conn %q", rec.Route, rec.Connection)
	}
	// A claude turn that crosses the switch: a subscription call before it, an API call after. The
	// calls disagree, so the turn is unresolved, and the launch record (now the API key) must not
	// claim it although the turn ends after the switch.
	switched := session.Meta{Name: "w", Kind: session.KindClaude,
		LaunchRoute:   &routes.Launch{Route: routes.RouteAnthropicAPI, Binding: "ANTHROPIC_API_KEY@new"},
		RouteSwitches: []session.RouteSwitch{{At: "2026-11-03T01:00:00Z"}}}
	before, after := routeTurn(routes.RouteAnthropicSubscription, "", 2), routeTurn(routes.RouteAnthropicAPI, "", 3)
	before.TS, after.TS = "2026-11-03T00:59:00Z", "2026-11-03T01:01:00Z"
	rows = foldTurnRows([]transcript.Turn{{Role: "user"}, before, after}, true)
	for _, rec := range usageRecordsOf(switched, rows[0]) {
		if rec.Connection != "" {
			t.Errorf("a turn across the switch was stamped: route %q conn %q", rec.Route, rec.Connection)
		}
	}
	// Per-call connection evidence travels like route evidence.
	lt := routeTurn(usagex.RouteMemberLcpp, usagex.BillingSelfHosted, 3)
	lt.Connection = "member-llamacpp@e1"
	rows = foldTurnRows([]transcript.Turn{{Role: "user"}, lt}, true)
	if rec := usageRecordOf(session.Meta{Name: "l", Kind: session.KindLcpp}, rows[0], ""); rec.Connection != "member-llamacpp@e1" {
		t.Errorf("per-call connection = %q", rec.Connection)
	}
}

// Filling a route from the launch record never moves a price: no route a launch can fill (a
// fixed route of a kind that is not per-call) has providers of its own in the price lookup, which
// would otherwise be consulted before the kind's.
func TestLaunchFillsNoPricedRoute(t *testing.T) {
	for _, k := range sessionx.RegisteredKinds() {
		a, _ := sessionx.RegisteredAgent(k)
		if _, per := a.(agents.PerCallRouter); per {
			continue
		}
		for _, r := range agents.FixedRoutesOf(a) {
			if _, priced := usageRouteProviders[r]; priced {
				t.Errorf("kind %q's fixed route %q has price providers %v; filling it from the launch record would move an estimate",
					k, r, usageRouteProviders[r])
			}
		}
	}
	if !slices.Contains(agents.FixedRoutesOf(sessionx.AgentOf(session.KindOpencode)), routes.RouteOpencodeZen) {
		t.Error("the guard above relies on opencode being the per-call kind that owns the priced routes")
	}
}

// An auxiliary call's route is attribution only (ADR 0138 M0): a chat or one-shot row now
// recorded on opencode-go is estimated exactly as it was with no route, while a session turn's
// per-call route keeps pricing as it always did.
func TestAuxiliaryRouteDoesNotMovePrices(t *testing.T) {
	UseIsolatedUsageDir(t)
	UseUsageCatalog(t, routeCatalogFixture)
	resetUsageFold(t)
	day := daysAgo(0)
	est := func(feature, route string) float64 {
		t.Helper()
		UseIsolatedUsageDir(t)
		UseUsageCatalog(t, routeCatalogFixture)
		r := at(row("c1", feature, session.KindOpencode, "glm-5.2", 0), day)
		r.In, r.Spend, r.Route = 1_000_000, 1_000_000, route
		writeUsageDay(t, day, r)
		return getSeries(t, "from="+day+"&to="+day).Totals.CostEstUSD
	}
	for _, f := range []string{usagex.FeatureAssistantChat, usagex.FeatureTitleSession, usagex.FeatureToolImagegen} {
		before, after := est(f, ""), est(f, usagex.RouteOpencodeGo)
		if before != after || before != 1 {
			t.Errorf("%s: estimate %v with no route, %v on opencode-go; want both the kind's 1", f, before, after)
		}
	}
	if got := est(usagex.FeatureSession, usagex.RouteOpencodeGo); got != 0.5 {
		t.Errorf("a session turn's per-call route priced %v, want 0.5 as before", got)
	}
}

// consoleUsageAPI is the Console's typed client for /usage/series. This test reads outside the
// module, so a mutation of that file alone needs go test -count=1 to be seen.
const consoleUsageAPI = "../../../../console/src/features/usage/api.ts"

// The Console's closed UsageDim union and the server's accepted dimensions are one vocabulary:
// a dimension the server accepts but the type lacks cannot be requested by a typed caller.
func TestUsageDimsMatchTheConsole(t *testing.T) {
	b, err := os.ReadFile(consoleUsageAPI)
	if err != nil {
		t.Fatalf("read %s: %v", consoleUsageAPI, err)
	}
	src := string(b)
	i := strings.Index(src, "export type UsageDim =")
	if i < 0 {
		t.Fatal("no UsageDim type in the Console's usage client")
	}
	body := src[i:]
	body = body[:strings.Index(body, ";")]
	var ts []string
	for _, m := range regexp.MustCompile(`\|\s*"([a-z_]+)"`).FindAllStringSubmatch(body, -1) {
		ts = append(ts, m[1])
	}
	if len(ts) == 0 {
		t.Fatal("UsageDim parsed to nothing (positive control)")
	}
	if got, want := slices.Sorted(slices.Values(ts)), slices.Sorted(slices.Values(usageDims)); !slices.Equal(got, want) {
		t.Errorf("Console UsageDim = %v, server dims = %v", got, want)
	}
}
