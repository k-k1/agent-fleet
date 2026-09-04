package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// useUsageCatalog installs the given JSON as the catalog (the real machine's catalog is
// never visible to the test).
func useUsageCatalog(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_USAGE_CATALOG", p)
	resetUsageCatalogCache(t)
}

// A minimal copy of the real catalog's shape (models.dev). The case that actually occurs
// in the field is the same model id living under both a first-party and a reseller
// provider with different prices.
const catalogFixture = `{
 "anthropic": {"models": {
   "claude-opus-5": {"cost": {"input": 5, "output": 25, "cache_read": 0.5, "cache_write": 6.25}},
   "claude-sonnet-5": {"cost": {"input": 2, "output": 10, "cache_read": 0.2, "cache_write": 2.5}}
 }},
 "openai": {"models": {
   "gpt-5.6-terra": {"cost": {"input": 2, "output": 12, "cache_read": 0.2, "cache_write": 2.5,
     "tiers": [{"input": 4, "output": 18, "tier": {"type": "context", "size": 272000}}]}},
   "gpt-5.6-luna": {"cost": {"input": 1, "output": 6, "cache_read": 0.1}}
 }},
 "opencode": {"models": {
   "gpt-5.6-luna": {"cost": {"input": 0.2, "output": 1.2, "cache_read": 0.02}},
   "claude-sonnet-5": {"cost": {"input": 2.5, "output": 12.5, "cache_read": 0.25}}
 }},
 "sakana": {"models": {"fugu": {"cost": null}}},
 "openrouter": {"models": {
   "anthropic/claude-opus-5": {"cost": {"input": 1.5, "output": 9.25, "cache_read": 0.15}}
 }}
}`

// The cheap reseller / router price must never be picked up: doing so quietly reports a
// false amount. The provider to look under is decided from the kind.
func TestUsageCatalogPicksProviderByKind(t *testing.T) {
	useIsolatedUsageDir(t)
	useUsageCatalog(t, catalogFixture)

	cases := []struct {
		kind, model string
		wantIn      float64
		wantRef     string
	}{
		// claude uses the verified built-in table first; same values as the catalog's anthropic.
		{session.KindClaude, "claude-opus-5", 5, usagePriceSrcBuiltin},
		{session.KindCodex, "gpt-5.6-terra", 2, "catalog:openai/gpt-5.6-terra"},
		// The same model name has a different unit price per kind (opencode is gateway pricing).
		{session.KindCodex, "gpt-5.6-luna", 1, "catalog:openai/gpt-5.6-luna"},
		{session.KindOpencode, "gpt-5.6-luna", 0.2, "catalog:opencode/gpt-5.6-luna"},
		// claude through opencode is priced by opencode, not pre-empted by the built-in table.
		{session.KindOpencode, "claude-sonnet-5", 2.5, "catalog:opencode/claude-sonnet-5"},
		// A provider-prefixed id and a version-bearing id are both folded before the lookup.
		{session.KindOpencode, "opencode-go/gpt-5.6-luna", 0.2, "catalog:opencode/gpt-5.6-luna"},
		{session.KindClaude, "claude-sonnet-5-20260101", 2, usagePriceSrcBuiltin},
	}
	for _, c := range cases {
		p, src, ok := usagePriceOf(c.kind, c.model)
		if !ok {
			t.Errorf("%s/%s: no price was resolved", c.kind, c.model)
			continue
		}
		if p.In != c.wantIn || src != c.wantRef {
			t.Errorf("%s/%s: in=%v src=%q, want in=%v src=%q", c.kind, c.model, p.In, src, c.wantIn, c.wantRef)
		}
	}
	// A model upstream has no price for stays unpriced; it must not report 0.
	if _, _, ok := usagePriceOf(session.KindOpencode, "fugu"); ok {
		t.Error("a model with no cost was given a price")
	}
	// Reseller providers are not in the index at all, so no kind can look one up.
	cat := loadUsageCatalog()
	for _, k := range cat.keys() {
		if len(k) > 11 && k[:11] == "openrouter/" {
			t.Errorf("a reseller provider is in the index: %s", k)
		}
	}
}

// Cache unit prices come from the upstream catalog rather than from a multiplier; only a
// missing entry falls back to the multiplier.
func TestUsageCatalogCachePrices(t *testing.T) {
	useIsolatedUsageDir(t)
	useUsageCatalog(t, catalogFixture)
	a := usageAgg{In: 1_000_000, Out: 1_000_000, CacheRead: 1_000_000, CacheCreate: 1_000_000}
	got, _, ok := usageEstCostUSD(session.KindCodex, "gpt-5.6-terra", a)
	if !ok {
		t.Fatal("no price was resolved")
	}
	// in 2 + out 12 + cread 0.2 + cwrite 2.5 = 16.7 (tiers are not used)
	if math.Abs(got-16.7) > 1e-9 {
		t.Fatalf("estimated cost = %v, want 16.7", got)
	}
	// gpt-5.6-luna, for which upstream states no cache_write, falls back to 1.25x.
	got, _, _ = usageEstCostUSD(session.KindCodex, "gpt-5.6-luna", a)
	// in 1 + out 6 + cread 0.1 + cwrite 1×1.25 = 8.35
	if math.Abs(got-8.35) > 1e-9 {
		t.Fatalf("estimated cost with no cache_write given = %v, want 8.35", got)
	}
}

// A broken or absent catalog must not break the estimate or the series; it falls back to
// the built-in table alone. This depends on an upstream internal file, so if it gives way
// the whole usage screen goes down with it.
func TestUsageCatalogBestEffort(t *testing.T) {
	useIsolatedUsageDir(t)
	useUsageCatalog(t, "{ this is not json")
	if cat := loadUsageCatalog(); cat != nil {
		t.Fatal("broken JSON was accepted as a catalog")
	}
	if _, src, ok := usagePriceOf(session.KindClaude, "claude-opus-5"); !ok || src != usagePriceSrcBuiltin {
		t.Fatalf("did not fall back to the built-in table: src=%q ok=%v", src, ok)
	}
	// An unfamiliar shape (the provider is there but has no models) also counts as no catalog.
	useUsageCatalog(t, `{"anthropic": {"models": {}}}`)
	if cat := loadUsageCatalog(); cat != nil {
		t.Fatal("an empty catalog is being treated as present")
	}
}

// The response carries the unit prices used and what the catalog declared. An amount with
// no stated source cannot be checked, and would change silently when the catalog updates.
func TestUsageSeriesReportsPricesAndCatalog(t *testing.T) {
	useIsolatedUsageDir(t)
	useUsageCatalog(t, catalogFixture)
	day := daysAgo(0)
	mk := func(call, kind, model string, in int) usagex.Record {
		return usagex.Record{
			TS: day + "T12:00:00Z", Call: call, Feature: usagex.FeatureSession, Kind: kind,
			Model: model, ModelSrc: usagex.ModelReported, Trigger: usagex.TriggerUser,
			In: in, Spend: in, OK: true, Measured: usagex.MeasuredExact,
		}
	}
	writeUsageDay(t, day,
		mk("c1", session.KindCodex, "gpt-5.6-luna", 1_000_000),
		mk("c2", session.KindOpencode, "gpt-5.6-luna", 500_000), // same name, different unit price
		mk("c3", session.KindClaude, "claude-opus-5", 2_000_000),
	)
	got := getSeries(t, "from="+day+"&to="+day)
	if got.Catalog == nil || got.Catalog.Models == 0 || got.Catalog.Fetched == "" {
		t.Fatalf("the catalog declaration is missing: %+v", got.Catalog)
	}
	if got.Catalog.Origin != "env" {
		t.Fatalf("catalog origin = %q, want env", got.Catalog.Origin)
	}
	opus := got.Prices["claude-opus-5"]
	if opus.Src != usagePriceSrcBuiltin || opus.In != 5 || opus.CacheRead != 0.5 || opus.CacheWrite != 6.25 {
		t.Fatalf("declared unit prices for claude-opus-5 = %+v", opus)
	}
	// A row whose unit price differs per kind under one name is declared with a
	// representative price (the larger consumer) plus ambiguous.
	luna := got.Prices["gpt-5.6-luna"]
	if !luna.Ambiguous {
		t.Fatal("the unit price is split across kinds but ambiguous is not set")
	}
	if luna.In != 1 {
		t.Fatalf("representative unit price = %v, want 1 (the codex side, which consumed more)", luna.In)
	}
	// The estimate sums per-kind unit prices (1M x $1 + 0.5M x $0.2 + 2M x $5 = 11.1).
	if math.Abs(got.Totals.CostEstUSD-11.1) > 1e-6 {
		t.Fatalf("total estimated cost = %v, want 11.1", got.Totals.CostEstUSD)
	}
	if got.UnpricedSpend != 0 {
		t.Fatalf("unpriced spend remains: %d", got.UnpricedSpend)
	}
	// prices appears in the JSON; the internal spend field does not.
	b, _ := json.Marshal(got.Prices["claude-opus-5"])
	if string(b) == "" || jsonHas(b, "spend") {
		t.Fatalf("prices JSON = %s", b)
	}
}

func jsonHas(b []byte, key string) bool {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	_, ok := m[key]
	return ok
}
