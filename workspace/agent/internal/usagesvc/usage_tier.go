package usagesvc

// Context-tiered prices (#3979). Some models bill a whole request at a higher rate once its
// input crosses a threshold (models.dev: cost.tiers[].tier.size, and the older
// cost.context_over_200k). The estimate must therefore be priced per call, with the call's own
// input size, and the rollup (which merges thousands of calls into one number) cannot
// reconstruct that. Hence the bands below.
//
// Where the size comes from: a session turn row carries the input snapshot of the last call of
// the turn (fresh + cache read + cache create, usageRecordOf), written as usagex.Record.Ctx.
// A row without it (written before #3979, an auxiliary call, a subagent delta) has an unknown
// size and is priced at the base tier, exactly as before. Unknown must never round up.

import (
	"sort"
)

// usageTier is the price a model switches to once a request's input exceeds Size tokens
// (models.dev has no marginal reading: vendors bill the whole request at the higher rate).
type usageTier struct {
	Size  int
	Price usagePrice
}

// usageCtxEdges are the input sizes a band boundary may sit on: every threshold models.dev
// carries today (32K..524K, binary and decimal spellings). They are stored in the rollup as a
// band's floor, so the list may only grow — removing or moving one would re-read old bands.
// A band holds the calls whose input is above its floor and not above the next edge.
var usageCtxEdges = []int{32000, 100000, 128000, 200000, 256000, 262144, 272000, 512000, 524288}

// usageBand is the tokens of the calls whose input size falls in one band. The four token
// counts are a SUBSET of the owning usageAgg's totals: whatever is not in a band is priced at
// the base tier.
type usageBand struct {
	Floor       int `json:"floor"`
	In          int `json:"in,omitempty"`
	Out         int `json:"out,omitempty"`
	CacheRead   int `json:"cread,omitempty"`
	CacheCreate int `json:"ccreate,omitempty"`
}

// usageBandFloor is the floor of the band an input of ctx tokens belongs to, 0 when it is
// unknown or at/below the smallest threshold (the base tier for every model).
func usageBandFloor(ctx int) int {
	floor := 0
	for _, e := range usageCtxEdges {
		if ctx > e {
			floor = e
		}
	}
	return floor
}

// addBand folds one call's tokens into the aggregate's bands.
func (a *usageAgg) addBand(ctx, in, out, cread, ccreate int) {
	f := usageBandFloor(ctx)
	if f == 0 {
		return
	}
	a.addBands([]usageBand{{Floor: f, In: in, Out: out, CacheRead: cread, CacheCreate: ccreate}})
}

// addBands merges bands by floor, keeping the slice ordered so a rollup file is deterministic.
// The receiver's slice is never aliased with the argument's.
func (a *usageAgg) addBands(bs []usageBand) {
	if len(bs) == 0 {
		return
	}
	out := append([]usageBand(nil), a.Bands...)
	for _, b := range bs {
		merged := false
		for i := range out {
			if out[i].Floor == b.Floor {
				out[i].In += b.In
				out[i].Out += b.Out
				out[i].CacheRead += b.CacheRead
				out[i].CacheCreate += b.CacheCreate
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Floor < out[j].Floor })
	a.Bands = out
}

// at is the price that applies to a band whose floor is f: the tier with the largest size that
// the whole band is above. A band holds inputs in (f, next edge], so a tier applies to all of it
// when its size is at most f, or f+1 (models.dev spells "over 200K" both as 200000 and as
// 200001). A threshold strictly inside the band is not applied: unknown falls back to the
// cheaper tier, never to a dearer one.
func (p usagePrice) at(f int) usagePrice {
	best := -1
	for i, t := range p.Tiers {
		if t.Size <= f+1 && (best < 0 || t.Size > p.Tiers[best].Size) {
			best = i
		}
	}
	if best < 0 {
		return p
	}
	return p.Tiers[best].Price
}

// usageTierCostUSD prices an aggregate: each band at the tier its floor selects, the remainder
// (totals minus what the bands hold) at the base tier. Without bands or tiers it is the plain
// four-term product, so an aggregate written before #3979 prices exactly as it always did.
func usageTierCostUSD(p usagePrice, a usageAgg) float64 {
	rest := a
	usd := 0.0
	for _, b := range a.Bands {
		tp := p.at(b.Floor)
		usd += float64(b.In)*tp.In + float64(b.CacheCreate)*tp.cacheWrite() +
			float64(b.CacheRead)*tp.cacheRead() + float64(b.Out)*tp.Out
		rest.In -= b.In
		rest.Out -= b.Out
		rest.CacheRead -= b.CacheRead
		rest.CacheCreate -= b.CacheCreate
	}
	// Bands are a subset of the totals by construction; a negative remainder would mean a
	// corrupt row, and a negative price must not subtract from a total.
	usd += float64(max(rest.In, 0))*p.In + float64(max(rest.CacheCreate, 0))*p.cacheWrite() +
		float64(max(rest.CacheRead, 0))*p.cacheRead() + float64(max(rest.Out, 0))*p.Out
	return usd
}
