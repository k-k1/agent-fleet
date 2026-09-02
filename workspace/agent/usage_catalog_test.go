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

// useUsageCatalog は指定 JSON をカタログとして差す（実機のカタログは見せない）。
func useUsageCatalog(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_USAGE_CATALOG", p)
	resetUsageCatalogCache(t)
}

// 実カタログ（models.dev）の形を最小で写したもの。**同じモデル id が一次 provider と
// 再販 provider の両方に居て価格が違う**、が実機で起きている形。
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

// ★ 再販・ルータ経由の安い値を拾ってはいけない（拾うと静かに嘘の金額が出る）。
// 引く provider は kind から決める。
func TestUsageCatalogPicksProviderByKind(t *testing.T) {
	useIsolatedUsageDir(t)
	useUsageCatalog(t, catalogFixture)

	cases := []struct {
		kind, model string
		wantIn      float64
		wantRef     string
	}{
		// claude は内蔵表（検証済み）が先。カタログの anthropic と同値。
		{session.KindClaude, "claude-opus-5", 5, usagePriceSrcBuiltin},
		{session.KindCodex, "gpt-5.6-terra", 2, "catalog:openai/gpt-5.6-terra"},
		// 同じモデル名でも kind で単価が変わる（opencode はゲートウェイ価格）。
		{session.KindCodex, "gpt-5.6-luna", 1, "catalog:openai/gpt-5.6-luna"},
		{session.KindOpencode, "gpt-5.6-luna", 0.2, "catalog:opencode/gpt-5.6-luna"},
		// opencode 経由の claude は opencode の価格（内蔵表に先を越されない）。
		{session.KindOpencode, "claude-sonnet-5", 2.5, "catalog:opencode/claude-sonnet-5"},
		// provider 接頭辞つき id・版込み id も畳んでから引く。
		{session.KindOpencode, "opencode-go/gpt-5.6-luna", 0.2, "catalog:opencode/gpt-5.6-luna"},
		{session.KindClaude, "claude-sonnet-5-20260101", 2, usagePriceSrcBuiltin},
	}
	for _, c := range cases {
		p, src, ok := usagePriceOf(c.kind, c.model)
		if !ok {
			t.Errorf("%s/%s: 値付けできていない", c.kind, c.model)
			continue
		}
		if p.In != c.wantIn || src != c.wantRef {
			t.Errorf("%s/%s: in=%v src=%q, want in=%v src=%q", c.kind, c.model, p.In, src, c.wantIn, c.wantRef)
		}
	}
	// 上流が価格を持たないモデルは値付け不可のまま（0 を出さない）。
	if _, _, ok := usagePriceOf(session.KindOpencode, "fugu"); ok {
		t.Error("cost が無いモデルを値付けしている")
	}
	// 再販 provider は索引に入れていない＝どの kind からも引かれない。
	cat := loadUsageCatalog()
	for _, k := range cat.keys() {
		if len(k) > 11 && k[:11] == "openrouter/" {
			t.Errorf("再販 provider を索引に入れている: %s", k)
		}
	}
}

// カタログのキャッシュ単価は倍率ではなく**上流の値**を使う。無い項目だけ倍率で置く。
func TestUsageCatalogCachePrices(t *testing.T) {
	useIsolatedUsageDir(t)
	useUsageCatalog(t, catalogFixture)
	a := usageAgg{In: 1_000_000, Out: 1_000_000, CacheRead: 1_000_000, CacheCreate: 1_000_000}
	got, _, ok := usageEstCostUSD(session.KindCodex, "gpt-5.6-terra", a)
	if !ok {
		t.Fatal("値付けできていない")
	}
	// in 2 + out 12 + cread 0.2 + cwrite 2.5 = 16.7（tiers は使わない）
	if math.Abs(got-16.7) > 1e-9 {
		t.Fatalf("推定額 = %v, want 16.7", got)
	}
	// cache_write を上流が言っていない gpt-5.6-luna は 1.25 倍で置く。
	got, _, _ = usageEstCostUSD(session.KindCodex, "gpt-5.6-luna", a)
	// in 1 + out 6 + cread 0.1 + cwrite 1×1.25 = 8.35
	if math.Abs(got-8.35) > 1e-9 {
		t.Fatalf("cache_write 未提供時の推定額 = %v, want 8.35", got)
	}
}

// 壊れたカタログ・不在のカタログで**推定と系列が壊れない**（内蔵表だけに落ちる）。
// 上流の内部ファイルに依存する以上、ここが崩れると使用量画面ごと落ちる。
func TestUsageCatalogBestEffort(t *testing.T) {
	useIsolatedUsageDir(t)
	useUsageCatalog(t, "{ this is not json")
	if cat := loadUsageCatalog(); cat != nil {
		t.Fatal("壊れた JSON をカタログとして受け入れている")
	}
	if _, src, ok := usagePriceOf(session.KindClaude, "claude-opus-5"); !ok || src != usagePriceSrcBuiltin {
		t.Fatalf("内蔵表に落ちていない: src=%q ok=%v", src, ok)
	}
	// 知らない形（provider は居るがモデルが無い）も「カタログ無し」に倒す。
	useUsageCatalog(t, `{"anthropic": {"models": {}}}`)
	if cat := loadUsageCatalog(); cat != nil {
		t.Fatal("空のカタログを有りとして扱っている")
	}
}

// 応答に「使った単価」と「カタログの申告」が載る。金額だけ出して出所を言わないと検算できず、
// カタログが更新された時に額が黙って変わる。
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
		mk("c2", session.KindOpencode, "gpt-5.6-luna", 500_000), // 同名・別単価
		mk("c3", session.KindClaude, "claude-opus-5", 2_000_000),
	)
	got := getSeries(t, "from="+day+"&to="+day)
	if got.Catalog == nil || got.Catalog.Models == 0 || got.Catalog.Fetched == "" {
		t.Fatalf("カタログの申告が無い: %+v", got.Catalog)
	}
	if got.Catalog.Origin != "env" {
		t.Fatalf("カタログの出所 = %q, want env", got.Catalog.Origin)
	}
	opus := got.Prices["claude-opus-5"]
	if opus.Src != usagePriceSrcBuiltin || opus.In != 5 || opus.CacheRead != 0.5 || opus.CacheWrite != 6.25 {
		t.Fatalf("claude-opus-5 の単価申告 = %+v", opus)
	}
	// 同名で kind ごとに単価が違う行は、代表（消費の大きい方）＋ ambiguous で申告する。
	luna := got.Prices["gpt-5.6-luna"]
	if !luna.Ambiguous {
		t.Fatal("kind で単価が割れているのに ambiguous を立てていない")
	}
	if luna.In != 1 {
		t.Fatalf("代表単価 = %v, want 1（消費の大きい codex 側）", luna.In)
	}
	// 推定額は kind ごとの単価で足される（1M×$1 + 0.5M×$0.2 + 2M×$5 = 11.1）。
	if math.Abs(got.Totals.CostEstUSD-11.1) > 1e-6 {
		t.Fatalf("推定額合計 = %v, want 11.1", got.Totals.CostEstUSD)
	}
	if got.UnpricedSpend != 0 {
		t.Fatalf("値付け不可が残っている: %d", got.UnpricedSpend)
	}
	// prices は JSON に出る（内部フィールド spend は出ない）。
	b, _ := json.Marshal(got.Prices["claude-opus-5"])
	if string(b) == "" || jsonHas(b, "spend") {
		t.Fatalf("prices の JSON = %s", b)
	}
}

func jsonHas(b []byte, key string) bool {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	_, ok := m[key]
	return ok
}
