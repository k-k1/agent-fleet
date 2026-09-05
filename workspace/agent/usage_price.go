package main

// The price table and the estimated API-equivalent cost (docs/log/46 §9-2).
//
// Only claude's auxiliary calls return a measured cost (`total_cost_usd`). A session's own
// consumption is folded from the transcript, so it carries tokens and nothing else, and its
// money column stayed empty. The ledger does hold in / out / cache_read / cache_create per
// model, so multiplying by the published prices yields an estimate. This file is that
// multiplication and nothing else.
//
// Two non-negotiable promises — break either and the numbers lie:
//   - An estimate and a measurement are different values. usageAgg.CostEstUSD (estimated) and
//     CostUSD (measured) are never added together or mixed. One number holding two ways of
//     measuring cannot be read as either of them.
//   - A model that is not in a price table is not estimated, and no 0 is emitted. Consumption
//     that could not be priced is declared in /usage/series' unpriced_spend, so the screen can
//     say "N% could not be priced". Writing "not measured" as 0 is what ADR0029 §1-c forbids.
//
// Nothing is stored, not even in the rollup. Prices get revised, so multiplying again with
// today's table on every read is more correct than leaving amounts baked at an old price in a
// file.

import (
	"strings"
	"unicode"
)

// usagePrice is USD per million tokens (the provider's published price).
//
// CacheRead / CacheWrite are filled in only when the source states them. A 0 means "not
// provided", not "costs nothing", and falls back to the multiplier (the built-in table always
// leaves them 0 because there the multipliers are the published figures). On a very cheap
// model where upstream really does publish 0 the multiplier overshoots, but by an order of
// magnitude that is noise ($0.09 worth on a $0.075/MTok model), so take the simplicity.
type usagePrice struct {
	In         float64
	Out        float64
	CacheRead  float64
	CacheWrite float64
}

// cacheRead and cacheWrite take the source's value when it states one, the multiplier otherwise.
func (p usagePrice) cacheRead() float64 {
	if p.CacheRead > 0 {
		return p.CacheRead
	}
	return p.In * usageCacheReadMult
}

func (p usagePrice) cacheWrite() float64 {
	if p.CacheWrite > 0 {
		return p.CacheWrite
	}
	return p.In * usageCacheWriteMult
}

// The cache multipliers Anthropic publishes: a write is 1.25x at the 5-minute TTL, a read
// 0.1x. The transcript keeps no 5m/1h breakdown (CacheCreate in session_usage.go is a sum),
// so tokens written at the 1h TTL (2x) are counted here at 1.25x — the estimate can come out
// low.
const (
	usageCacheWriteMult = 1.25
	usageCacheReadMult  = 0.10
)

// usagePrices maps a normalized model name to its price: Anthropic's published prices only,
// placed here by hand after a human checked them (as of 2026-08). Other providers (gpt-* /
// qwen* / glm-* …) are not kept here but looked up in the catalog (usage_catalog.go) —
// maintaining a hand-copied table for every provider does not last, and filling it with
// plausible defaults mixes numbers with no basis into the total wearing the face of an
// estimate. With no catalog either the consumption stays unpriced, and no 0 is emitted.
//
// A raw versioned id (claude-haiku-4-5-20251001) or a provider-prefixed one
// (anthropic/claude-sonnet-5) is folded by usageNormalizeModel before it reaches this table.
var usagePrices = map[string]usagePrice{
	// Current generation.
	// fable 5.1 costs the same as 5 ($10/$50) except for its cache read, $0.25/MTok rather
	// than the 0.1 multiplier's $1.00; the multiplier would inflate it fourfold, so state it.
	// Upstream does not say whether mythos 5.1 has the same cache read, so that one keeps the
	// multiplier.
	"claude-fable-5-1":  {In: 10, Out: 50, CacheRead: 0.25},
	"claude-mythos-5-1": {In: 10, Out: 50},
	"claude-fable-5":    {In: 10, Out: 50},
	"claude-mythos-5":   {In: 10, Out: 50},
	"claude-opus-5":     {In: 5, Out: 25},
	"claude-opus-4-8":   {In: 5, Out: 25},
	"claude-opus-4-7":   {In: 5, Out: 25},
	"claude-opus-4-6":   {In: 5, Out: 25},
	"claude-opus-4-5":   {In: 5, Out: 25},
	"claude-sonnet-5":   {In: 2, Out: 10},
	// sonnet 4.6 is $3/$15, the same as 4.5 (sonnet 5 came down to $2/$10).
	"claude-sonnet-4-6": {In: 3, Out: 15},
	"claude-sonnet-4-5": {In: 3, Out: 15},
	"claude-haiku-4-5":  {In: 1, Out: 5},
	// Older versions. Folding reaches back through past transcripts (a backfill takes in
	// months at once), so rows for models nobody uses today turn up routinely.
	"claude-opus-4-1":   {In: 15, Out: 75},
	"claude-opus-4":     {In: 15, Out: 75},
	"claude-opus-4-0":   {In: 15, Out: 75},
	"claude-sonnet-4":   {In: 3, Out: 15},
	"claude-sonnet-4-0": {In: 3, Out: 15},
	"claude-3-7-sonnet": {In: 3, Out: 15},
	"claude-3-5-sonnet": {In: 3, Out: 15},
	"claude-3-5-haiku":  {In: 0.8, Out: 4},
	"claude-3-haiku":    {In: 0.25, Out: 1.25},
	"claude-3-opus":     {In: 15, Out: 75},
}

// usageNormalizeModel folds a ledger model name onto a price-table key. The ledger keeps the
// reported spelling as the series key (usage_fold.go), so version suffixes, provider prefixes
// and alias drift are absorbed on the lookup side. Anything it cannot fold is returned as is
// (not in the table means unpriced).
func usageNormalizeModel(model string) string {
	s := strings.ToLower(strings.TrimSpace(model))
	if s == "" {
		return ""
	}
	// "provider/model" from opencode / litellm, "anthropic.claude-…" from Bedrock.
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimPrefix(s, "anthropic.")
	// Vertex's "claude-opus-4-5@20251101".
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, "-latest")
	// Drop the version date (-20251001): only a trailing group of exactly 8 digits, so a
	// version number like "claude-opus-4-8" is not swept up with it.
	if i := strings.LastIndex(s, "-"); i > 0 && len(s)-i-1 == 8 && allDigits(s[i+1:]) {
		s = s[:i]
	}
	return s
}

func allDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return s != ""
}

// Where a price came from (prices[].src in the response). An amount that will not say whose
// price it used cannot be checked.
const (
	usagePriceSrcBuiltin = "builtin" // the table in this file (Anthropic's primary prices, verified)
	usagePriceSrcCatalog = "catalog" // models.dev (usage_catalog.go)
)

// usagePriceOf looks up the price for a kind and model. A false bool return means the
// consumption cannot be priced: no estimate, and no 0 either.
//
// The order is decided by the provider the consumption actually went through
// (docs/log/46 §5-c):
//  1. When the kind hits Anthropic directly (claude and friends), consult the built-in table
//     first: published figures a human checked, more trustworthy than the community-sourced
//     catalog (measured: it matched models.dev's anthropic values exactly).
//  2. Otherwise the catalog. opencode consumption is converted at the opencode gateway's
//     prices, which is closest to what the user actually pays (decided 2026-08-31).
//  3. Not in the catalog either: fall back to the built-in table, which still finds a
//     claude-family model name.
func usagePriceOf(kind, model string) (usagePrice, string, bool) {
	base := usageNormalizeModel(model)
	order := usageCatalogOrder(kind)
	builtinFirst := len(order) > 0 && order[0] == "anthropic"
	if builtinFirst {
		if p, ok := usagePrices[base]; ok {
			return p, usagePriceSrcBuiltin, true
		}
	}
	if p, ref, ok := usageCatalogLookup(kind, model); ok {
		return p, usagePriceSrcCatalog + ":" + ref, true
	}
	if !builtinFirst {
		if p, ok := usagePrices[base]; ok {
			return p, usagePriceSrcBuiltin, true
		}
	}
	return usagePrice{}, "", false
}

// usageEstCostUSD is the estimated API-equivalent cost of one aggregate.
//
//	= in×input + ccreate×cache-write + cread×cache-read + out×output
//
// The four kinds are multiplied separately instead of using spend (= in + ccreate + out).
// Cache reads are outside the definition of spend but are billed, so deriving the amount from
// spend makes a conversation look cheaper the longer it runs.
func usageEstCostUSD(kind, model string, a usageAgg) (float64, string, bool) {
	p, src, ok := usagePriceOf(kind, model)
	if !ok {
		return 0, "", false
	}
	usd := float64(a.In)*p.In +
		float64(a.CacheCreate)*p.cacheWrite() +
		float64(a.CacheRead)*p.cacheRead() +
		float64(a.Out)*p.Out
	return usd / 1_000_000, src, true
}
