package usagesvc

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// One tiered model per provider the kinds consult, with the threshold the live catalog carries
// for it (usage_tier.go). gemini spells its tier the old way, context_over_200k.
const tierFixture = `{
 "anthropic": {"models": {
   "claude-haiku-5-5": {"cost": {"input": 0.1, "output": 0.5, "cache_read": 0.01, "cache_write": 0.125,
     "tiers": [{"input": 0.5, "output": 2.5, "cache_read": 0.05, "cache_write": 0.625, "tier": {"type": "context", "size": 100000}}]}},
   "claude-sonnet-5": {"cost": {"input": 2, "output": 10, "tiers": [{"input": 4, "output": 15, "tier": {"type": "context", "size": 200000}}]}}
 }},
 "openai": {"models": {
   "gpt-6-sol": {"cost": {"input": 2, "output": 10, "cache_read": 0.2,
     "tiers": [{"input": 4, "output": 15, "cache_read": 0.4, "tier": {"type": "context", "size": 272000}}]}}
 }},
 "opencode": {"models": {
   "claude-sonnet-4-5": {"cost": {"input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75,
     "tiers": [{"input": 6, "output": 22.5, "cache_read": 0.6, "cache_write": 7.5, "tier": {"type": "context", "size": 200000}}]}}
 }},
 "github-copilot": {"models": {
   "grok-4.7": {"cost": {"input": 2, "output": 6, "cache_read": 0.3,
     "tiers": [{"input": 4, "output": 12, "cache_read": 0.6, "tier": {"type": "context", "size": 200000}}]}}
 }},
 "google": {"models": {
   "gemini-3.1-pro-preview": {"cost": {"input": 2, "output": 12, "cache_read": 0.2,
     "context_over_200k": {"input": 4, "output": 18, "cache_read": 0.4}}}
 }},
 "amazon-bedrock": {"models": {
   "us.anthropic.claude-haiku-5-5": {"cost": {"input": 0.11, "output": 0.55, "cache_read": 0.011,
     "tiers": [{"input": 0.55, "output": 2.75, "cache_read": 0.055, "tier": {"type": "context", "size": 100000}}]}}
 }}
}`

// tierRow is one session turn whose whole input is fresh tokens, so its size is ctx.
func tierRow(kind, model string, ctx int) usagex.Record {
	return usagex.Record{Call: "c", Feature: usagex.FeatureSession, Kind: kind, Model: model,
		In: ctx, Out: 1000, Ctx: ctx, Spend: ctx + 1000, OK: true, Measured: usagex.MeasuredExact}
}

func priceOfRow(t *testing.T, r usagex.Record) float64 {
	t.Helper()
	agg := aggregateUsageRows([]usagex.Record{r}, map[string]bool{})
	if len(agg) != 1 {
		t.Fatalf("want one aggregate, got %d", len(agg))
	}
	for k, a := range agg {
		usd, _, ok := usageEstCostUSDOn(k.Kind, k.Route, k.Model, a)
		if !ok {
			t.Fatalf("%s/%s unpriced", k.Kind, k.Model)
		}
		return usd
	}
	return 0
}

// Each case is priced just below, at and just above its threshold. "Exceeds" is strict: an
// input of exactly the threshold is still the base tier.
func TestUsageTierPricedByCallInputSize(t *testing.T) {
	UseIsolatedUsageDir(t)
	UseUsageCatalog(t, tierFixture)
	cases := []struct {
		kind, model     string
		size            int
		baseIn, baseOut float64
		tierIn, tierOut float64
	}{
		{session.KindClaude, "claude-haiku-5-5", 100000, 0.1, 0.5, 0.5, 2.5},
		{session.KindCodex, "gpt-6-sol", 272000, 2, 10, 4, 15},
		{session.KindOpencode, "claude-sonnet-4-5", 200000, 3, 15, 6, 22.5},
		{session.KindCopilot, "grok-4.7", 200000, 2, 6, 4, 12},
		{session.KindAgy, "gemini-3.1-pro-preview", 200000, 2, 12, 4, 18},
		{session.KindKiro, "us.anthropic.claude-haiku-5-5", 100000, 0.11, 0.55, 0.55, 2.75},
	}
	for _, c := range cases {
		cost := func(ctx int, in, out float64) float64 {
			return (float64(ctx)*in + 1000*out) / 1e6
		}
		for _, k := range []struct {
			name string
			ctx  int
			in   float64
			out  float64
		}{
			{"below", c.size - 1, c.baseIn, c.baseOut},
			{"at", c.size, c.baseIn, c.baseOut},
			{"above", c.size + 1, c.tierIn, c.tierOut},
		} {
			got := priceOfRow(t, tierRow(c.kind, c.model, k.ctx))
			want := cost(k.ctx, k.in, k.out)
			if math.Abs(got-want) > 1e-9 {
				t.Errorf("%s/%s %s (%d): est = %v, want %v", c.kind, c.model, k.name, k.ctx, got, want)
			}
		}
	}
}

// A row written before the size was recorded carries no ctx: it prices at the base tier however
// big its input, exactly as before #3979. The same goes for a stored rollup entry with no bands.
func TestUsageTierUnknownSizePricesAtBase(t *testing.T) {
	UseIsolatedUsageDir(t)
	UseUsageCatalog(t, tierFixture)
	old := tierRow(session.KindCodex, "gpt-6-sol", 900000)
	old.Ctx = 0
	got := priceOfRow(t, old)
	if want := (900000*2.0 + 1000*10.0) / 1e6; math.Abs(got-want) > 1e-9 {
		t.Fatalf("old-format row est = %v, want the base-tier %v", got, want)
	}

	var a usageAgg
	if err := json.Unmarshal([]byte(`{"spend":901000,"in":900000,"out":1000,"cread":0,"ccreate":0,"calls":1}`), &a); err != nil {
		t.Fatal(err)
	}
	if len(a.Bands) != 0 {
		t.Fatalf("an old rollup entry grew bands: %+v", a.Bands)
	}
	usd, _, _ := usageEstCostUSDOn(session.KindCodex, "", "gpt-6-sol", a)
	if want := (900000*2.0 + 1000*10.0) / 1e6; math.Abs(usd-want) > 1e-9 {
		t.Fatalf("old rollup entry est = %v, want %v", usd, want)
	}
	// A bands-free aggregate marshals without the field, so a downgraded reader sees the old shape.
	b, _ := json.Marshal(a)
	if string(b) != `{"spend":901000,"in":900000,"out":1000,"cread":0,"ccreate":0,"calls":1}` {
		t.Fatalf("marshalled = %s", b)
	}
}

// Calls of one aggregate are priced each at its own tier: no turn is dropped or counted twice
// when a day mixes small and big turns, and cache tokens follow the tier too.
func TestUsageTierSplitsAggregateWithoutDoubleCounting(t *testing.T) {
	UseIsolatedUsageDir(t)
	UseUsageCatalog(t, tierFixture)
	small := usagex.Record{Call: "a", Feature: usagex.FeatureSession, Kind: session.KindClaude, Model: "claude-haiku-5-5",
		In: 10000, Out: 100, CacheRead: 20000, Ctx: 30000, OK: true, Measured: usagex.MeasuredExact}
	big := usagex.Record{Call: "b", Feature: usagex.FeatureSession, Kind: session.KindClaude, Model: "claude-haiku-5-5",
		In: 1000, Out: 200, CacheRead: 150000, CacheCreate: 5000, Ctx: 156000, OK: true, Measured: usagex.MeasuredExact}
	unknown := usagex.Record{Call: "c", Feature: usagex.FeatureSession, Kind: session.KindClaude, Model: "claude-haiku-5-5",
		In: 400000, Out: 50, OK: true, Measured: usagex.MeasuredExact}
	aggs := aggregateUsageRows([]usagex.Record{small, big, unknown}, map[string]bool{})
	if len(aggs) != 1 {
		t.Fatalf("aggregates = %d", len(aggs))
	}
	for k, a := range aggs {
		if a.In != 411000 || a.Out != 350 || a.CacheRead != 170000 || a.CacheCreate != 5000 {
			t.Fatalf("totals changed by banding: %+v", a)
		}
		got, _, _ := usageEstCostUSDOn(k.Kind, k.Route, k.Model, a)
		base := func(in, out, cr, cc float64) float64 { return in*0.1 + out*0.5 + cr*0.01 + cc*0.125 }
		tier := func(in, out, cr, cc float64) float64 { return in*0.5 + out*2.5 + cr*0.05 + cc*0.625 }
		want := (base(10000, 100, 20000, 0) + tier(1000, 200, 150000, 5000) + base(400000, 50, 0, 0)) / 1e6
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("est = %v, want %v", got, want)
		}
	}
}

// Bands survive the rollup: they merge by floor across days and files, and the series response
// does not expose them.
func TestUsageTierBandsMergeAndStayOutOfTheResponse(t *testing.T) {
	a := usageAgg{In: 300000, Out: 10}
	a.addBand(300000, 300000, 10, 0, 0)
	b := usageAgg{In: 280000, Out: 20}
	b.addBand(280000, 280000, 20, 0, 0)
	c := usageAgg{In: 150000, Out: 5}
	c.addBand(150000, 150000, 5, 0, 0)
	var sum usageAgg
	sum.add(a)
	sum.add(b)
	sum.add(c)
	want := []usageBand{{Floor: 128000, In: 150000, Out: 5}, {Floor: 272000, In: 580000, Out: 30}}
	if len(sum.Bands) != 2 || sum.Bands[0] != want[0] || sum.Bands[1] != want[1] {
		t.Fatalf("bands = %+v, want %+v", sum.Bands, want)
	}
	if len(a.Bands) != 1 || a.Bands[0].In != 300000 {
		t.Fatalf("add mutated its argument: %+v", a.Bands)
	}
	// Stored and read back.
	raw, _ := json.Marshal(sum)
	var back usageAgg
	if err := json.Unmarshal(raw, &back); err != nil || len(back.Bands) != 2 {
		t.Fatalf("round trip: %v %+v", err, back)
	}
}

// A threshold that sits inside a band (64000 between the 32000 and 100000 edges) is not applied
// to the band: unknown rounds down to the cheaper tier. 200001, a spelling of "over 200K", is.
func TestUsageTierThresholdOffTheEdges(t *testing.T) {
	p := usagePrice{In: 1, Out: 1, Tiers: []usageTier{
		{Size: 64000, Price: usagePrice{In: 2, Out: 2}},
		{Size: 200001, Price: usagePrice{In: 3, Out: 3}},
	}}
	if got := p.at(32000).In; got != 1 {
		t.Errorf("band 32000 got in=%v, want base 1", got)
	}
	if got := p.at(100000).In; got != 2 {
		t.Errorf("band 100000 got in=%v, want 2", got)
	}
	if got := p.at(200000).In; got != 3 {
		t.Errorf("band 200000 got in=%v, want 3", got)
	}
}

// The built-in table wins for claude, but a catalog row for the same model at the same base
// rate lends its tiers, so a model priced there still gets them.
func TestUsageTierGraftedOntoBuiltinPrice(t *testing.T) {
	UseIsolatedUsageDir(t)
	UseUsageCatalog(t, tierFixture)
	p, src, ok := usagePriceOf(session.KindClaude, "claude-sonnet-5")
	if !ok || src != usagePriceSrcBuiltin || len(p.Tiers) != 1 || p.Tiers[0].Size != 200000 {
		t.Fatalf("price = %+v src=%q ok=%v", p, src, ok)
	}
	got := priceOfRow(t, tierRow(session.KindClaude, "claude-sonnet-5", 250000))
	if want := (250000*4.0 + 1000*15.0) / 1e6; math.Abs(got-want) > 1e-9 {
		t.Fatalf("est = %v, want %v", got, want)
	}
	// A catalog row at a different base rate is a different price list: nothing is grafted.
	UseUsageCatalog(t, `{"anthropic":{"models":{"claude-sonnet-5":{"cost":{"input":9,"output":9,"tiers":[{"input":99,"output":99,"tier":{"size":1000}}]}}}}}`)
	if p, _, _ := usagePriceOf(session.KindClaude, "claude-sonnet-5"); len(p.Tiers) != 0 {
		t.Fatalf("tiers grafted across different base prices: %+v", p.Tiers)
	}
}

// catalogTiers reads the new spelling, the old one, and prefers the new when both are there.
func TestCatalogTiersParsing(t *testing.T) {
	var f catalogFile
	body := `{"openai":{"models":{
	  "both":{"cost":{"input":1,"output":2,"tiers":[{"input":3,"output":4,"tier":{"size":50000}}],"context_over_200k":{"input":9,"output":9}}},
	  "old":{"cost":{"input":1,"output":2,"context_over_200k":{"input":3,"output":4}}},
	  "none":{"cost":{"input":1,"output":2}},
	  "partial":{"cost":{"input":1,"output":2,"tiers":[{"input":3,"tier":{"size":50000}}]}}}}}`
	if err := json.Unmarshal([]byte(body), &f); err != nil {
		t.Fatal(err)
	}
	m := f["openai"].Models
	if ts := catalogTiers(m["both"].Cost); len(ts) != 1 || ts[0].Size != 50000 || ts[0].Price.In != 3 {
		t.Errorf("both: %+v", ts)
	}
	if ts := catalogTiers(m["old"].Cost); len(ts) != 1 || ts[0].Size != 200000 || ts[0].Price.Out != 4 {
		t.Errorf("old: %+v", ts)
	}
	if ts := catalogTiers(m["none"].Cost); len(ts) != 0 {
		t.Errorf("none: %+v", ts)
	}
	if ts := catalogTiers(m["partial"].Cost); len(ts) != 0 {
		t.Errorf("partial tier without output was kept: %+v", ts)
	}
}

// The fold stamps the call's input size on a session turn's row, and leaves it off the rows
// whose tokens are not one call's snapshot.
func TestUsageRecordOfStampsCtx(t *testing.T) {
	m := session.Meta{Name: "s", Kind: session.KindCodex}
	r := usageTurnRow{TS: "2026-11-01T00:00:00Z", Idx: 1, Model: "gpt-6-sol",
		Tokens: usagex.Tokens{In: 1000, CacheRead: 300000, CacheCreate: 20, Out: 5}}
	if rec := usageRecordOf(m, r, ""); rec.Ctx != 301020 {
		t.Errorf("own-transcript row ctx = %d, want 301020", rec.Ctx)
	}
	if rec := usageRecordOf(m, r, "agent1"); rec.Ctx != 0 {
		t.Errorf("subagent delta row ctx = %d, want 0 (unknown)", rec.Ctx)
	}
}

// The session budget prices a turn the same way the usage view does.
func TestSpendOfRowsUsesTier(t *testing.T) {
	UseIsolatedUsageDir(t)
	UseUsageCatalog(t, tierFixture)
	m := session.Meta{Kind: session.KindCodex, Name: "s"}
	row := func(in int) usageTurnRow {
		return usageTurnRow{Model: "gpt-6-sol", Tokens: usagex.Tokens{In: in, Out: 1000}}
	}
	below := spendOfRows(m, []usageTurnRow{row(272000)}, nil).USD
	above := spendOfRows(m, []usageTurnRow{row(272001)}, nil).USD
	if want := (272000*2.0 + 1000*10.0) / 1e6; math.Abs(below-want) > 1e-9 {
		t.Errorf("at threshold = %v, want %v", below, want)
	}
	if want := (272001*4.0 + 1000*15.0) / 1e6; math.Abs(above-want) > 1e-9 {
		t.Errorf("above threshold = %v, want %v", above, want)
	}
}

// End to end through the ledger file, the rollup (a past day is folded into it) and the series
// response: tiered rows are priced per call, an old-format row beside them is not, and the
// response carries no bands.
func TestUsageTierSeriesEndToEnd(t *testing.T) {
	UseIsolatedUsageDir(t)
	UseUsageCatalog(t, tierFixture)
	mk := func(day, call string, in, ctx int) usagex.Record {
		return usagex.Record{TS: day + "T12:00:00Z", Call: call, Feature: usagex.FeatureSession,
			Kind: session.KindCodex, Model: "gpt-6-sol", ModelSrc: usagex.ModelReported,
			Trigger: usagex.TriggerUser, In: in, Ctx: ctx, Spend: in, OK: true, Measured: usagex.MeasuredExact}
	}
	past, today := daysAgo(2), daysAgo(0)
	writeUsageDay(t, past, mk(past, "p1", 300000, 300000), mk(past, "p2", 100000, 100000))
	writeUsageDay(t, today, mk(today, "t1", 400000, 400000), mk(today, "t2", 500000, 0)) // t2: old format
	got := getSeries(t, "from="+past+"&to="+today)
	want := (300000*4.0 + 100000*2.0 + 400000*4.0 + 500000*2.0) / 1e6
	if math.Abs(got.Totals.CostEstUSD-want) > 1e-9 {
		t.Fatalf("estimate = %v, want %v", got.Totals.CostEstUSD, want)
	}
	if len(got.Totals.Bands) != 0 {
		t.Fatalf("bands leaked into the response: %+v", got.Totals.Bands)
	}
	for _, b := range got.Buckets {
		for k, a := range b.Series {
			if len(a.Bands) != 0 {
				t.Fatalf("bands leaked into bucket %s/%s", b.T, k)
			}
		}
	}
}
