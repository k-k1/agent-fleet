package main

// The aggregation side of cursor's stop-hook rows (#2286): the hook adds tokens to a turn the
// transcript fold already counts as a call, and a redelivered stop must not count twice.

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// hookRow is the shape internal/agents/cursor/hooks.go writes for the multi-step fixture.
func hookRow(day, generation string) usagex.Record {
	r := at(row("g-"+generation, usagex.FeatureSession, session.KindCursor, "", 835), day)
	r.Ref, r.Key, r.TokensOnly = "slot01", "cursor-stop:"+generation, true
	r.In, r.Out, r.CacheRead = 639, 196, 63296
	r.Measured, r.ModelRaw, r.ModelSrc = usagex.MeasuredPartial, "default", usagex.ModelUnknown
	return r
}

// The fold writes the turn's call (measured none, no tokens); the hook row adds the tokens.
func TestCursorHookRowAddsTokensWithoutAddingACall(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(0)
	fold := at(row("fold-1", usagex.FeatureSession, session.KindCursor, "", 0), day)
	fold.Ref, fold.Idx, fold.Measured = "slot01", 1, usagex.MeasuredNone
	writeUsageDay(t, day, fold, hookRow(day, "gen-1"))

	got := getSeries(t, "from="+day+"&to="+day)
	if got.Totals.Calls != 1 || got.Totals.Spend != 835 {
		t.Fatalf("totals = %+v, want calls 1 (the fold's) / spend 835 (the hook's)", got.Totals)
	}
}

// An interrupted or retried delivery appends the same generation twice; one is counted.
func TestCursorHookRowIsCountedOncePerGeneration(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(0)
	writeUsageDay(t, day, hookRow(day, "gen-1"), hookRow(day, "gen-1"), hookRow(day, "gen-2"))
	got := getSeries(t, "from="+day+"&to="+day)
	if got.Totals.Spend != 2*835 {
		t.Fatalf("spend = %d, want %d (one per generation)", got.Totals.Spend, 2*835)
	}
	if n := len(usagex.ReadRows()); n != 3 {
		t.Fatalf("raw = %d rows, want 3 (the ledger itself is never rewritten)", n)
	}
}

// An unpriced selector ("default" is Auto, not a model) must not get a guessed price.
func TestCursorHookRowOfAutoIsUnpriced(t *testing.T) {
	if usd, _, ok := usageEstCostUSD(session.KindCursor, "", usageAgg{In: 639, Out: 196, CacheRead: 63296}); ok {
		t.Fatalf("priced Auto at %v", usd)
	}
}
