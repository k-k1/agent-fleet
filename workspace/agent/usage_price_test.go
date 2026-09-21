package main

import (
	"math"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func TestUsageNormalizeModel(t *testing.T) {
	cases := map[string]string{
		"claude-haiku-4-5-20251001":  "claude-haiku-4-5", // raw versioned id from the transcript
		"claude-opus-4-8":            "claude-opus-4-8",  // version number kept (not 8 digits)
		"anthropic/claude-sonnet-5":  "claude-sonnet-5",  // opencode's provider prefix
		"anthropic.claude-opus-5":    "claude-opus-5",    // Bedrock
		"claude-opus-4-5@20251101":   "claude-opus-4-5",  // Vertex
		"claude-sonnet-4-5-latest":   "claude-sonnet-4-5",
		"  Claude-Opus-5  ":          "claude-opus-5",
		"gpt-5.6-terra":              "gpt-5.6-terra", // unfoldable stays as is, i.e. unpriced
		"claude-sonnet-4-20250514":   "claude-sonnet-4",
		"<synthetic>":                "<synthetic>",
		"claude-3-5-haiku-20241022":  "claude-3-5-haiku",
		"claude-opus-4-1-2025080555": "claude-opus-4-1-2025080555", // trailing digits, but not 8, are kept
	}
	for in, want := range cases {
		if got := usageNormalizeModel(in); got != want {
			t.Errorf("usageNormalizeModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// The estimate multiplies the four kinds of token separately. Cache reads are outside
// spend but are billed, so deriving the amount from spend makes a long conversation look
// cheaper — the easiest mistake to make here.
func TestUsageEstCostUSD(t *testing.T) {
	useIsolatedUsageDir(t) // look at the bare state with no catalog (built-in table only)
	// claude-sonnet-5 = $2/MTok in, $10/MTok out
	a := usageAgg{In: 1_000_000, Out: 1_000_000, CacheCreate: 1_000_000, CacheRead: 1_000_000}
	got, src, ok := usageEstCostUSD(session.KindClaude, "claude-sonnet-5", a)
	if !ok {
		t.Fatal("claude-sonnet-5 came back unpriced")
	}
	// in 2 + ccreate 2×1.25 + cread 2×0.1 + out 10 = 14.7
	if math.Abs(got-14.7) > 1e-9 {
		t.Fatalf("estimate = %v, want 14.7", got)
	}
	if src != usagePriceSrcBuiltin {
		t.Fatalf("source = %q, want %q", src, usagePriceSrcBuiltin)
	}
	if _, _, ok := usageEstCostUSD(session.KindCodex, "gpt-5.6-terra", a); ok {
		t.Fatal("priced a model that is in no catalog and no price table (must return false, not 0)")
	}
	if _, _, ok := usageEstCostUSD(session.KindClaude, "", a); ok {
		t.Fatal("priced a row whose model is unknown")
	}
}

// TestUsageEstCostUSDLcppIsGPUBilledNotDoubleCounted pins ADR 0093 decision 8's price half:
// lcpp consumption is always priced at exactly $0/token (never left "unpriced" either — the
// real cost is billed by GPU-hour, a wholly separate path this file never touches), and that
// holds even when a self-hosted model's name collides with a real, priced catalog/builtin
// entry — the kind check must short-circuit before either table is ever consulted.
func TestUsageEstCostUSDLcppIsGPUBilledNotDoubleCounted(t *testing.T) {
	useIsolatedUsageDir(t)
	a := usageAgg{In: 1_000_000, Out: 1_000_000, CacheCreate: 1_000_000, CacheRead: 1_000_000}

	// Positive: a plain self-hosted model name prices at exactly $0, and is reported as
	// PRICED (ok=true), not unpriced — decision 8 says "token price 0", a real declared
	// price, not "we don't know".
	got, src, ok := usageEstCostUSD(session.KindLcpp, "qwen3-30b", a)
	if !ok {
		t.Fatal("lcpp consumption came back unpriced; decision 8 wants an explicit $0, not unpriced")
	}
	if got != 0 {
		t.Fatalf("estimate = %v, want exactly 0", got)
	}
	if src != usagePriceSrcGPUBilled {
		t.Fatalf("source = %q, want %q", src, usagePriceSrcGPUBilled)
	}

	// Negative control / collision: naming a self-hosted checkpoint after a real, priced
	// model must not let it slip through to the builtin table.
	got, _, ok = usageEstCostUSD(session.KindLcpp, "claude-sonnet-5", a)
	if !ok || got != 0 {
		t.Fatalf("lcpp model colliding with a priced builtin name = (%v, ok=%v), want (0, true)", got, ok)
	}

	// The double-counting scenario itself: the CP's engine-usage gateway posts one ledger row
	// for the GPU call (feature=engine.llm, engines.go's handleEngineUsage) and lcpp's own
	// session fold (usage_fold.go) posts a second row for the same turn (feature=session) —
	// both carry Kind=lcpp for the identical tokens. Summing BOTH rows' estimated cost must
	// still be $0 total, not 2x some nonzero per-token price.
	sessionRowCost, _, _ := usageEstCostUSD(session.KindLcpp, "qwen3-30b", a)
	engineLLMRowCost, _, _ := usageEstCostUSD(session.KindLcpp, "qwen3-30b", a)
	if total := sessionRowCost + engineLLMRowCost; total != 0 {
		t.Fatalf("session-row + engine.llm-row cost = %v, want 0 (the same GPU run must not be billed twice)", total)
	}
}

// fable 5.1 is the one exception: input and output cost the same as 5, but cache read is
// $0.25/MTok. This pins that it is not left to the multiplier ($10 x 0.1 = $1.00). It
// also watches the consequence that a new version has to be added to the table by hand —
// usageNormalizeModel only strips an 8-digit date, so "-1" is not folded away and an
// unlisted version comes out unpriced.
func TestUsageEstCostUSDFable51CacheRead(t *testing.T) {
	useIsolatedUsageDir(t)
	a := usageAgg{CacheRead: 1_000_000}
	got, src, ok := usageEstCostUSD(session.KindClaude, "claude-fable-5-1", a)
	if !ok {
		t.Fatal("claude-fable-5-1 is unpriced (not in the price table, so it lands in unpriced_spend)")
	}
	if src != usagePriceSrcBuiltin {
		t.Fatalf("source = %q, want %q", src, usagePriceSrcBuiltin)
	}
	if math.Abs(got-0.25) > 1e-9 {
		t.Fatalf("cache read of 1M tokens = $%v, want $0.25 (the multiplier would give $1.00)", got)
	}
}

// A session proper (which carries no measured cost) also gets an estimate, returned as a
// value separate from the measured one. Consumption that could not be priced is declared
// in unpriced_spend.
func TestUsageSeriesEstimatesSessionCost(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(0)
	sess := usagex.Record{
		TS: day + "T12:00:00Z", Call: "s1", Feature: usagex.FeatureSession, Kind: session.KindClaude,
		Model: "claude-opus-5", ModelSrc: usagex.ModelReported, Trigger: usagex.TriggerUser,
		In: 1_000_000, Out: 100_000, CacheRead: 2_000_000, CacheCreate: 500_000,
		Spend: usagex.Spend(1_000_000, 500_000, 100_000), OK: true, Measured: usagex.MeasuredExact,
	}
	// A model that is in no price table (codex): kept out of the estimate, into unpriced_spend.
	other := usagex.Record{
		TS: day + "T12:00:00Z", Call: "s2", Feature: usagex.FeatureSession, Kind: session.KindCodex,
		Model: "gpt-5.6-terra", ModelSrc: usagex.ModelReported, Trigger: usagex.TriggerUser,
		In: 900_000, Spend: 900_000, OK: true, Measured: usagex.MeasuredExact,
	}
	writeUsageDay(t, day, sess, other)

	got := getSeries(t, "from="+day+"&to="+day)
	// claude-opus-5 = $5/$25: (1M + 0.5M×1.25 + 2M×0.1) × 5 + 0.1M × 25 = 9.125 + 2.5 = $11.625
	if math.Abs(got.Totals.CostEstUSD-11.625) > 1e-6 {
		t.Fatalf("estimate = %v, want 11.625", got.Totals.CostEstUSD)
	}
	if got.Totals.CostUSD != 0 {
		t.Fatalf("the estimate leaked into the measured value: cost_usd = %v", got.Totals.CostUSD)
	}
	if got.PricedSpend != sess.Spend || got.UnpricedSpend != other.Spend {
		t.Fatalf("priced/unpriced = %d/%d, want %d/%d",
			got.PricedSpend, got.UnpricedSpend, sess.Spend, other.Spend)
	}
	// Folding by an axis must not lose the estimate: it rides in the buckets and the matrix alike.
	sum := 0.0
	for _, b := range got.Buckets {
		for _, a := range b.Series {
			sum += a.CostEstUSD
		}
	}
	if math.Abs(sum-got.Totals.CostEstUSD) > 1e-6 {
		t.Fatalf("bucket sum %v != totals %v", sum, got.Totals.CostEstUSD)
	}
}
