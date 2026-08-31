package main

import (
	"math"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestUsageNormalizeModel(t *testing.T) {
	cases := map[string]string{
		"claude-haiku-4-5-20251001":  "claude-haiku-4-5", // 転写の版込み生 id
		"claude-opus-4-8":            "claude-opus-4-8",  // 版番号は落とさない（8桁ではない）
		"anthropic/claude-sonnet-5":  "claude-sonnet-5",  // opencode の provider 付き
		"anthropic.claude-opus-5":    "claude-opus-5",    // Bedrock
		"claude-opus-4-5@20251101":   "claude-opus-4-5",  // Vertex
		"claude-sonnet-4-5-latest":   "claude-sonnet-4-5",
		"  Claude-Opus-5  ":          "claude-opus-5",
		"gpt-5.6-terra":              "gpt-5.6-terra", // 畳めないものはそのまま（＝値付け不可）
		"claude-sonnet-4-20250514":   "claude-sonnet-4",
		"<synthetic>":                "<synthetic>",
		"claude-3-5-haiku-20241022":  "claude-3-5-haiku",
		"claude-opus-4-1-2025080555": "claude-opus-4-1-2025080555", // 8桁でない末尾は落とさない
	}
	for in, want := range cases {
		if got := usageNormalizeModel(in); got != want {
			t.Errorf("usageNormalizeModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// 推定は4種のトークンを個別に掛ける。**キャッシュ読取は spend に入っていないが課金される**
// ので、spend から金額を起こすと長い会話ほど安く出る（ここが一番間違えやすい）。
func TestUsageEstCostUSD(t *testing.T) {
	useIsolatedUsageDir(t) // カタログ無しの素の状態（内蔵表だけ）を見る
	// claude-sonnet-5 = $2/MTok in, $10/MTok out
	a := usageAgg{In: 1_000_000, Out: 1_000_000, CacheCreate: 1_000_000, CacheRead: 1_000_000}
	got, src, ok := usageEstCostUSD(session.KindClaude, "claude-sonnet-5", a)
	if !ok {
		t.Fatal("claude-sonnet-5 が値付け不可になっている")
	}
	// in 2 + ccreate 2×1.25 + cread 2×0.1 + out 10 = 14.7
	if math.Abs(got-14.7) > 1e-9 {
		t.Fatalf("推定額 = %v, want 14.7", got)
	}
	if src != usagePriceSrcBuiltin {
		t.Fatalf("出所 = %q, want %q", src, usagePriceSrcBuiltin)
	}
	if _, _, ok := usageEstCostUSD(session.KindCodex, "gpt-5.6-terra", a); ok {
		t.Fatal("カタログ無しで単価表にも無いモデルを値付けしている（0 を出さず false）")
	}
	if _, _, ok := usageEstCostUSD(session.KindClaude, "", a); ok {
		t.Fatal("モデル不明の行を値付けしている")
	}
}

// セッション本体（実測コストを持たない）にも推定が乗り、実測とは別値で返ること。
// 値付けできなかった消費は unpriced_spend で申告される。
func TestUsageSeriesEstimatesSessionCost(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(0)
	sess := usageRecord{
		TS: day + "T12:00:00Z", Call: "s1", Feature: usageFeatureSession, Kind: session.KindClaude,
		Model: "claude-opus-5", ModelSrc: usageModelReported, Trigger: usageTriggerUser,
		In: 1_000_000, Out: 100_000, CacheRead: 2_000_000, CacheCreate: 500_000,
		Spend: usageSpend(1_000_000, 500_000, 100_000), OK: true, Measured: usageMeasuredExact,
	}
	// 単価表に無いモデル（codex）— 推定に混ぜず unpriced_spend へ。
	other := usageRecord{
		TS: day + "T12:00:00Z", Call: "s2", Feature: usageFeatureSession, Kind: session.KindCodex,
		Model: "gpt-5.6-terra", ModelSrc: usageModelReported, Trigger: usageTriggerUser,
		In: 900_000, Spend: 900_000, OK: true, Measured: usageMeasuredExact,
	}
	writeUsageDay(t, day, sess, other)

	got := getSeries(t, "from="+day+"&to="+day)
	// claude-opus-5 = $5/$25: (1M + 0.5M×1.25 + 2M×0.1) × 5 + 0.1M × 25 = 9.125 + 2.5 = $11.625
	if math.Abs(got.Totals.CostEstUSD-11.625) > 1e-6 {
		t.Fatalf("推定額 = %v, want 11.625", got.Totals.CostEstUSD)
	}
	if got.Totals.CostUSD != 0 {
		t.Fatalf("実測に推定が混ざっている: cost_usd = %v", got.Totals.CostUSD)
	}
	if got.PricedSpend != sess.Spend || got.UnpricedSpend != other.Spend {
		t.Fatalf("priced/unpriced = %d/%d, want %d/%d",
			got.PricedSpend, got.UnpricedSpend, sess.Spend, other.Spend)
	}
	// 軸で畳んでも推定が消えない（バケット・matrix の両方に載る）。
	sum := 0.0
	for _, b := range got.Buckets {
		for _, a := range b.Series {
			sum += a.CostEstUSD
		}
	}
	if math.Abs(sum-got.Totals.CostEstUSD) > 1e-6 {
		t.Fatalf("バケット合計 %v != totals %v", sum, got.Totals.CostEstUSD)
	}
}
