package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func routeTurn(route, billing string, out int) transcript.Turn {
	return transcript.Turn{Role: "assistant", OutTok: out, InTok: 10, Model: "glm-5.2",
		TS: "2026-11-03T01:00:00Z", Route: route, Billing: billing}
}

// A logical turn takes the route of the calls it folds, and the ledger row carries it. The
// session's Meta (here one with no route anywhere) plays no part.
func TestFoldRecordsPerCallRoute(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user", Text: "q"},
		routeTurn(usagex.RouteOpencodeGo, "", 5),
		routeTurn(usagex.RouteOpencodeGo, "", 7),
	}
	rows := foldTurnRows(turns, true)
	if len(rows) != 1 || rows[0].Route != usagex.RouteOpencodeGo {
		t.Fatalf("rows = %+v, want one row on opencode-go", rows)
	}
	rec := usageRecordOf(session.Meta{Name: "s", Kind: session.KindOpencode, Model: "opencode/glm-5.2"}, rows[0], "")
	if rec.Route != usagex.RouteOpencodeGo {
		t.Fatalf("record route = %q", rec.Route)
	}
	if k := keyOf(rec); k.Route != usagex.RouteOpencodeGo {
		t.Fatalf("key route = %q", k.Route)
	}
}

// One ledger row cannot be split by route without renumbering later turns, so a turn whose
// calls disagree (or only partly have evidence) is written unresolved rather than filed under
// the route of whichever call came last.
func TestFoldMixedRouteTurnIsUnresolved(t *testing.T) {
	for name, calls := range map[string][]transcript.Turn{
		"two routes":         {routeTurn(usagex.RouteOpencodeZen, "", 1), routeTurn(usagex.RouteOpencodeGo, "", 1)},
		"evidence then none": {routeTurn(usagex.RouteOpencodeZen, "", 1), routeTurn("", "", 1)},
		"none then evidence": {routeTurn("", "", 1), routeTurn(usagex.RouteOpencodeZen, "", 1)},
	} {
		rows := foldTurnRows(append([]transcript.Turn{{Role: "user"}}, calls...), true)
		if len(rows) != 1 || rows[0].Route != "" || rows[0].Billing != "" {
			t.Errorf("%s: rows = %+v, want one unresolved row", name, rows)
		}
	}
}

// A cost-only call joins the turn on its side, route included; one waiting for a turn that
// never comes becomes an orphan row that keeps its own route.
func TestFoldCostOnlyRouteJoinsAndOrphans(t *testing.T) {
	cost := func(route string) transcript.Turn {
		return transcript.Turn{Role: "assistant", CostOnly: true, CostUSD: 0.5, Model: "glm-5.2", Route: route, AnchorID: "a"}
	}
	rows := foldTurnRows([]transcript.Turn{{Role: "user"}, routeTurn(usagex.RouteOpencodeZen, "", 1), cost(usagex.RouteOpencodeZen)}, true)
	if len(rows) != 1 || rows[0].Route != usagex.RouteOpencodeZen || rows[0].CostUSD != 0.5 {
		t.Fatalf("joined: %+v", rows)
	}
	rows = foldTurnRows([]transcript.Turn{{Role: "user"}, cost(usagex.RouteOpencodeGo)}, true)
	if len(rows) != 1 || !rows[0].Orphan || rows[0].Route != usagex.RouteOpencodeGo {
		t.Fatalf("orphan: %+v", rows)
	}
	// waiting cost on another route, then a turn on a different one: mixed.
	rows = foldTurnRows([]transcript.Turn{{Role: "user"}, cost(usagex.RouteOpencodeGo), routeTurn(usagex.RouteOpencodeZen, "", 1)}, true)
	if len(rows) != 1 || rows[0].Route != "" {
		t.Fatalf("pending cost joined across routes: %+v", rows)
	}
}

// A kind that records no evidence stays unresolved however its session is configured.
func TestFoldWithoutEvidenceLeavesRouteEmpty(t *testing.T) {
	rows := foldTurnRows([]transcript.Turn{{Role: "user"}, routeTurn("", "", 3)}, true)
	rec := usageRecordOf(session.Meta{Name: "c", Kind: session.KindClaude, Model: "claude-opus-5"}, rows[0], "")
	if rec.Route != "" || rec.Billing != "" {
		t.Fatalf("record = route %q billing %q, want both empty", rec.Route, rec.Billing)
	}
}

func routeRec(call, route, billing string, spend int) usagex.Record {
	return usagex.Record{Call: call, Feature: usagex.FeatureSession, Kind: session.KindOpencode,
		Model: "glm-5.2", Route: route, Billing: billing, Out: spend, Spend: spend, OK: true,
		Measured: usagex.MeasuredExact}
}

// Rows that differ in route or billing stay apart; rows that do not differ still merge; and a
// call whose rows now carry different routes is still counted exactly once.
func TestAggregateKeepsRouteAndBillingApart(t *testing.T) {
	rows := []usagex.Record{
		routeRec("c1", usagex.RouteOpencodeZen, "", 10),
		routeRec("c2", usagex.RouteOpencodeGo, "", 10),
		routeRec("c3", usagex.RouteOpencodeGo, "", 10),
		routeRec("c4", "", usagex.BillingMetered, 10),
		routeRec("c5", "", "", 10),
		routeRec("c6", "", "", 10),
	}
	agg := aggregateUsageRows(rows, map[string]bool{})
	if len(agg) != 4 {
		t.Fatalf("%d keys, want 4 (zen, go, metered, unrecorded): %+v", len(agg), agg)
	}
	if a := agg[keyOf(rows[1])]; a.Spend != 20 || a.Calls != 2 {
		t.Errorf("opencode-go = %+v, want spend 20 calls 2", a)
	}
	if a := agg[keyOf(rows[4])]; a.Spend != 20 || a.Calls != 2 {
		t.Errorf("unrecorded = %+v, want spend 20 calls 2", a)
	}

	split := []usagex.Record{
		routeRec("same", usagex.RouteOpencodeZen, "", 3),
		routeRec("same", usagex.RouteOpencodeGo, "", 9),
	}
	calls := 0
	for _, a := range aggregateUsageRows(split, map[string]bool{}) {
		calls += a.Calls
	}
	if calls != 1 {
		t.Fatalf("one call split over two routes counted %d times", calls)
	}
}

// The rollup is kept forever: a file written before the fields existed must still load, with
// the new fields empty, and new entries must round-trip.
func TestRollupEntryCompatibility(t *testing.T) {
	old := `{"src":["2026-10-01"],"e":[{"k":{"feature":"session","kind":"opencode","model":"glm-5.2","measured":"exact","ok":true},"v":{"spend":5,"in":1,"out":4,"cread":0,"ccreate":0,"calls":1}}]}`
	var day usageRollupDay
	if err := json.Unmarshal([]byte(old), &day); err != nil {
		t.Fatal(err)
	}
	if k := day.Entries[0].Key; k.Route != "" || k.Billing != "" || k.Kind != "opencode" {
		t.Fatalf("old key = %+v", k)
	}
	// Merging a new routed contribution into the old day keeps both entries.
	oldKey := day.Entries[0].Key
	newKey := oldKey
	newKey.Route = usagex.RouteOpencodeGo
	merged, ok := mergeRollupDay(day, "2026-10-02", map[usageKey]usageAgg{newKey: {Spend: 7, Calls: 1}})
	if !ok || len(merged.Entries) != 2 {
		t.Fatalf("merged = %+v ok=%v", merged, ok)
	}
	b, _ := json.Marshal(merged)
	var back usageRollupDay
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Entries) != 2 || back.Entries[0].Key == back.Entries[1].Key {
		t.Fatalf("round trip lost a dimension: %s", b)
	}
	// An unrecorded key serialises exactly as before the fields existed.
	ob, _ := json.Marshal(oldKey)
	if string(ob) != `{"feature":"session","kind":"opencode","model":"glm-5.2","measured":"exact","ok":true}` {
		t.Fatalf("old key wire changed: %s", ob)
	}
}

func TestRouteAndBillingDims(t *testing.T) {
	k := usageKey{Route: usagex.RouteOpencodeGo}
	if !validUsageDim("route") || !validUsageDim("billing") {
		t.Fatal("route and billing must be queryable dimensions")
	}
	if usageDimValue(k, "route") != usagex.RouteOpencodeGo {
		t.Error("route dim")
	}
	// Unrecorded billing reads as unknown, never as included.
	if got := usageDimValue(k, "billing"); got != usagex.BillingUnknown {
		t.Errorf("empty billing reads %q, want unknown", got)
	}
	f, bad := parseUsageFilter("billing:unknown,route:opencode-*")
	if bad != "" || !f.match(k) {
		t.Errorf("filter on the new dims: bad=%q", bad)
	}
}

const routeCatalogFixture = `{
 "opencode": {"models": {"glm-5.2": {"cost": {"input": 1, "output": 4}}}},
 "opencode-go": {"models": {"glm-5.2": {"cost": {"input": 0.5, "output": 2}}}},
 "anthropic": {"models": {"claude-opus-5": {"cost": {"input": 5, "output": 25}}}}
}`

// Existing data prices exactly as before: with no route, the lookup is the kind's. A recorded
// route is tried first and the kind's table is the fallback.
func TestPriceLookupRouteFirstThenKind(t *testing.T) {
	useIsolatedUsageDir(t)
	useUsageCatalog(t, routeCatalogFixture)

	// Empty route: identical to usagePriceOf, and still the kind's gateway price.
	p, src, ok := usagePriceOfOn(session.KindOpencode, "", "glm-5.2")
	q, qsrc, qok := usagePriceOf(session.KindOpencode, "glm-5.2")
	if !ok || !qok || p != q || src != qsrc || src != "catalog:opencode/glm-5.2" || p.In != 1 {
		t.Fatalf("empty route: %v %q vs %v %q", p, src, q, qsrc)
	}
	// A route with its own provider wins over the kind's.
	p, src, ok = usagePriceOfOn(session.KindOpencode, usagex.RouteOpencodeGo, "glm-5.2")
	if !ok || p.In != 0.5 || src != "catalog:opencode-go/glm-5.2" {
		t.Fatalf("opencode-go: %v %q ok=%v", p, src, ok)
	}
	// The route's provider has no row for the model: fall back to the kind's table.
	p, src, ok = usagePriceOfOn(session.KindClaude, usagex.RouteOpencodeGo, "claude-opus-5")
	if !ok || p.In != 5 {
		t.Fatalf("fallback: %v %q ok=%v", p, src, ok)
	}
	// A route the table does not know changes nothing.
	p, _, ok = usagePriceOfOn(session.KindOpencode, "no-such-route", "glm-5.2")
	if !ok || p.In != 1 {
		t.Fatalf("unmapped route: %v", p)
	}
	// Self-hosted stays $0 whatever the route says.
	p, src, ok = usagePriceOfOn(session.KindLcpp, usagex.RouteMemberLcpp, "glm-5.2")
	if !ok || p.In != 0 || src != usagePriceSrcGPUBilled {
		t.Fatalf("lcpp: %v %q", p, src)
	}
	// And the aggregate estimate follows the key's route.
	a := usageAgg{In: 1_000_000}
	if got, _, _ := usageEstCostUSDOn(session.KindOpencode, usagex.RouteOpencodeGo, "glm-5.2", a); got != 0.5 {
		t.Errorf("estimate on opencode-go = %v, want 0.5", got)
	}
	if got, _, _ := usageEstCostUSD(session.KindOpencode, "glm-5.2", a); got != 1 {
		t.Errorf("estimate with no route = %v, want 1", got)
	}
}

func billedTurn(billing string, in, out int) transcript.Turn {
	return transcript.Turn{Role: "assistant", InTok: in, OutTok: out, Model: "gpt-5.6", TS: "2026-11-03T01:00:00Z",
		Route: usagex.RouteOpencodeGo, Billing: billing}
}

// A turn whose calls are all proven but billed differently becomes one ledger row per billing
// segment. Together they carry exactly the totals of the unsplit turn, share the call id (one
// call in the aggregation) and the turn's idx, and replaying them adds nothing.
func TestFoldSplitsTurnAtProvenBillingBoundary(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user", Text: "q"},
		billedTurn(usagex.BillingSubscriptionIncluded, 100, 5),
		billedTurn(usagex.BillingSubscriptionIncluded, 120, 6),
		billedTurn(usagex.BillingSubscriptionExtra, 150, 7),
	}
	rows := foldTurnRows(turns, true)
	if len(rows) != 1 || rows[0].Tokens.In != 150 || rows[0].Tokens.Out != 18 || rows[0].Billing != "" {
		t.Fatalf("rows = %+v, want one unsplit row with In 150 / Out 18 and no billing", rows)
	}
	m := session.Meta{Name: "s", Kind: session.KindCodex}
	recs := usageRecordsOf(m, rows[0])
	if len(recs) != 2 {
		t.Fatalf("records = %+v, want 2 segments", recs)
	}
	if recs[0].Billing != usagex.BillingSubscriptionIncluded || recs[1].Billing != usagex.BillingSubscriptionExtra {
		t.Errorf("billings = %q, %q", recs[0].Billing, recs[1].Billing)
	}
	var in, out int
	for _, r := range recs {
		in, out = in+r.In, out+r.Out
		if r.Idx != rows[0].Idx || r.Route != usagex.RouteOpencodeGo {
			t.Errorf("record %+v: want idx %d and route opencode-go", r, rows[0].Idx)
		}
	}
	if in != 150 || out != 18 {
		t.Errorf("segments sum to in=%d out=%d, want 150/18 (the unsplit totals)", in, out)
	}
	if recs[0].Out != 11 || recs[1].Out != 7 || recs[1].In != 150 || recs[0].In != 0 {
		t.Errorf("segment tokens = %+v / %+v", recs[0], recs[1])
	}
	if recs[0].Call == "" || recs[0].Call != recs[1].Call {
		t.Errorf("calls %q / %q, want one shared id", recs[0].Call, recs[1].Call)
	}
	if recs[0].Key != "" || recs[1].Key == "" {
		t.Errorf("keys %q / %q: the first keeps the idx sequence, the rest are keyed", recs[0].Key, recs[1].Key)
	}
	agg := aggregateUsageRows(recs, map[string]bool{})
	calls := 0
	for _, a := range agg {
		calls += a.Calls
	}
	if calls != 1 {
		t.Errorf("aggregation counts %d calls, want 1", calls)
	}
	seen := usageDedupIndex{}
	again := append(append([]usagex.Record{}, recs...), usageRecordsOf(m, rows[0])...)
	accepted := 0
	for _, r := range again {
		if seen.accept(r, time.Now()) {
			accepted++
		}
	}
	if accepted != 2 {
		t.Errorf("a replay of the same turn was accepted %d times in total, want 2 (each segment once)", accepted)
	}
}

// An unproven call anywhere in the turn leaves the whole turn unresolved: where its charge began
// is not known. A turn whose calls agree is not split.
func TestFoldDoesNotSplitWithoutProof(t *testing.T) {
	for name, calls := range map[string][]transcript.Turn{
		"unproven between": {billedTurn(usagex.BillingSubscriptionIncluded, 1, 1), billedTurn("", 1, 1), billedTurn(usagex.BillingSubscriptionExtra, 1, 1)},
		"unproven last":    {billedTurn(usagex.BillingSubscriptionIncluded, 1, 1), billedTurn("", 1, 1)},
		"all agree":        {billedTurn(usagex.BillingSubscriptionIncluded, 1, 1), billedTurn(usagex.BillingSubscriptionIncluded, 2, 1)},
	} {
		rows := foldTurnRows(append([]transcript.Turn{{Role: "user"}}, calls...), true)
		if len(rows) != 1 {
			t.Fatalf("%s: rows = %+v", name, rows)
		}
		if recs := usageRecordsOf(session.Meta{Name: "s", Kind: session.KindCodex}, rows[0]); len(recs) != 1 {
			t.Errorf("%s: %d records, want 1", name, len(recs))
		}
	}
}
