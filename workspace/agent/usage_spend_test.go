package main

import (
	"math"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func spendNear(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSpendOfTurnsPricesOneWayPerTurn(t *testing.T) {
	born := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return born.Add(d).Format(time.RFC3339Nano) }
	m := session.Meta{Name: "s", Kind: session.KindClaude, Model: "claude-opus-5", CreatedAt: born.Format(time.RFC3339)}
	turns := []transcript.Turn{
		// Copied from the fork source: before this session existed, never charged to it.
		{Role: "user", TS: at(-time.Hour)},
		{Role: "assistant", Model: "claude-opus-5", InTok: 1_000_000, OutTok: 1_000_000, TS: at(-time.Hour)},
		// Estimated: 1M in × $5 + 100k out × $25 = $7.50.
		{Role: "user", TS: at(time.Minute)},
		{Role: "assistant", Model: "claude-opus-5", InTok: 1_000_000, OutTok: 100_000, TS: at(time.Minute)},
		// Reported: the CLI's own $0.40 replaces the estimate of the same turn's tokens.
		{Role: "user", TS: at(2 * time.Minute)},
		{Role: "assistant", Model: "claude-opus-5", InTok: 1_000_000, OutTok: 1_000_000, CostUSD: 0.40, TS: at(2 * time.Minute)},
		// The open turn counts: the hard limit is for a turn still running.
		{Role: "user", TS: at(3 * time.Minute)},
		{Role: "assistant", Model: "claude-opus-5", OutTok: 40_000, TS: at(3 * time.Minute)},
	}
	sp := spendOfTurns(m, turns)
	if want := 7.50 + 0.40 + 1.00; !spendNear(sp.USD, want) {
		t.Fatalf("spend = %v, want %v", sp.USD, want)
	}
	if !sp.Priced || !sp.Reported || sp.Unpriced {
		t.Fatalf("flags = %+v", sp)
	}
}

func TestSpendOfTurnsFlagsUnpricedTokens(t *testing.T) {
	m := session.Meta{Name: "s", Kind: session.KindMuse, CreatedAt: "bad"}
	sp := spendOfTurns(m, []transcript.Turn{
		{Role: "user"},
		{Role: "assistant", Model: "no-such-model-anywhere", InTok: 10, OutTok: 10},
	})
	if sp.USD != 0 || sp.Priced || !sp.Unpriced {
		t.Fatalf("an unpriced model must be flagged, not read as $0 priced: %+v", sp)
	}
	if sp := spendOfTurns(m, nil); sp.Priced || sp.Unpriced {
		t.Fatalf("no turns = nothing measured: %+v", sp)
	}
}

// CreatedAt keeps whole seconds; a fork made late in the second its copied history ended would
// be charged that history if the cut were CreatedAt (review of #1652). SpendFrom keeps the
// sub-second instant the fork was made.
func TestSpendOfTurnsExcludesHistoryCopiedInTheForksOwnSecond(t *testing.T) {
	sec := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	m := session.Meta{Name: "f", Kind: session.KindClaude, Model: "claude-opus-5",
		CreatedAt: sec.Format(time.RFC3339),
		SpendFrom: sec.Add(900 * time.Millisecond).Format(time.RFC3339Nano)}
	turns := []transcript.Turn{
		// The source's expensive turn, ended 0.1 s into the same second, copied into the fork.
		{Role: "user", TS: sec.Add(50 * time.Millisecond).Format(time.RFC3339Nano)},
		{Role: "assistant", Model: "claude-opus-5", InTok: 1_000_000, OutTok: 1_000_000,
			TS: sec.Add(100 * time.Millisecond).Format(time.RFC3339Nano)},
		// The fork's own turn: 1M in × $5 = $5.
		{Role: "user", TS: sec.Add(5 * time.Second).Format(time.RFC3339Nano)},
		{Role: "assistant", Model: "claude-opus-5", InTok: 1_000_000, TS: sec.Add(6 * time.Second).Format(time.RFC3339Nano)},
	}
	if sp := spendOfTurns(m, turns); !spendNear(sp.USD, 5) {
		t.Fatalf("spend = %v, want only the fork's own $5", sp.USD)
	}
}
