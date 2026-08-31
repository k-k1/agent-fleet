package main

// 単価表と「API 換算相当額（推定）」（docs/46 §9-2 の続き）。
//
// 実測コストを返すのは claude の**補助呼び出しだけ**（`total_cost_usd`）。セッション本体は
// 転写から折り込むのでトークンしか無く、金額の列がずっと「—」だった。だが台帳は
// in / out / cache_read / cache_create をモデル別に持っているので、**公表単価を掛ければ
// 推定は出せる**。ここはその掛け算だけを担う。
//
// 非交渉の約束（この2つを崩すと数字が嘘になる）:
//   - **推定と実測は別の値**。usageAgg.CostEstUSD（推定）と CostUSD（実測）は決して
//     足さない・混ぜない。1つの数字に2つの計測法を混ぜたものは、どちらとしても読めない。
//   - **単価表に無いモデルは推定しない**（0 を出さない）。値付けできなかった消費は
//     /usage/series の unpriced_spend で申告し、画面が「N% は値付けできていません」と
//     言えるようにする。「測れていない」を 0 と書かないという ADR0029 §1-c の延長。
//
// 保存はしない（rollup にも書かない）。単価は改定されるので、**読み出しのたびに今の表で
// 掛け直す**方が、古い単価で焼いた金額がファイルに残るより正しい。

import (
	"strings"
	"unicode"
)

// usagePrice は 100万トークンあたりの USD（プロバイダの公表単価）。
//
// CacheRead / CacheWrite は出所が明示している時だけ入る。**0 は「単価 0」ではなく
// 「未提供」**として倍率へ落とす（内蔵表は倍率が公表値なので常に 0 で置いてある）。
// 上流が本当に 0 を公表しているごく安いモデルでは、倍率で置いた分だけ上振れするが、
// 桁で言えば誤差（$0.075/MTok のモデルで $0.09 相当）なのでこの単純さを採る。
type usagePrice struct {
	In         float64
	Out        float64
	CacheRead  float64
	CacheWrite float64
}

// cacheRead / cacheWrite は「出所が言っていればその値、言っていなければ倍率」。
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

// キャッシュの倍率（Anthropic 公表）。書き込みは 5 分 TTL の 1.25 倍、読み出しは 0.1 倍。
// **転写は 5m と 1h の内訳を残さない**（session_usage.go の CacheCreate は合算）ので、
// 1h TTL（2 倍）で書かれた分はここでは 1.25 倍として数えられる＝推定は下振れしうる。
const (
	usageCacheWriteMult = 1.25
	usageCacheReadMult  = 0.10
)

// usagePrices は正規化済みモデル名 → 単価。**Anthropic の公表単価のみ**を人が確認して
// 置いた表（2026-08 時点）。他プロバイダ（gpt-* / qwen* / glm-* …）はここでは持たず、
// カタログ（usage_catalog.go）から引く — 手で写した表を全プロバイダぶん維持するのは
// 続かないし、適当な既定値で埋めると根拠の無い数字が「推定額」の顔をして合計に混ざる。
// カタログも無ければ**値付け不可のまま**（0 を出さない）。
//
// 版込みの生 id（claude-haiku-4-5-20251001）や provider 付き（anthropic/claude-sonnet-5）は
// usageNormalizeModel が畳んでからここを引く。
var usagePrices = map[string]usagePrice{
	// 現行世代
	"claude-fable-5":  {In: 10, Out: 50},
	"claude-mythos-5": {In: 10, Out: 50},
	"claude-opus-5":   {In: 5, Out: 25},
	"claude-opus-4-8": {In: 5, Out: 25},
	"claude-opus-4-7": {In: 5, Out: 25},
	"claude-opus-4-6": {In: 5, Out: 25},
	"claude-opus-4-5": {In: 5, Out: 25},
	"claude-sonnet-5": {In: 2, Out: 10},
	// sonnet 4.6 は 4.5 と同じ $3/$15（sonnet 5 で $2/$10 に下がった）
	"claude-sonnet-4-6": {In: 3, Out: 15},
	"claude-sonnet-4-5": {In: 3, Out: 15},
	"claude-haiku-4-5":  {In: 1, Out: 5},
	// 旧版。折り込みは過去の転写を遡って取り込む（バックフィルは数か月分が一度に入る）ので、
	// 今は使っていないモデルの行が普通に出てくる。
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

// usageNormalizeModel は台帳のモデル名を単価表のキーへ畳む。台帳側は「報告された綴りを
// そのまま系列キーにする」方針（usage_fold.go）なので、版・provider 接頭辞・別名の揺れは
// **引く側**で吸収する。畳めなかったものはそのまま返す（＝表に無ければ値付け不可）。
func usageNormalizeModel(model string) string {
	s := strings.ToLower(strings.TrimSpace(model))
	if s == "" {
		return ""
	}
	// opencode / litellm 系の "provider/model"、Bedrock の "anthropic.claude-…"
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimPrefix(s, "anthropic.")
	// Vertex の "claude-opus-4-5@20251101"
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, "-latest")
	// 版の日付（-20251001）を落とす。**8桁の数字の末尾だけ**を落とす — "claude-opus-4-8" の
	// ような版番号を巻き込まないための桁数指定。
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

// 単価の出所（応答の prices[].src）。**どこの単価かを言わない金額は検算できない。**
const (
	usagePriceSrcBuiltin = "builtin" // このファイルの表（Anthropic の一次単価・検証済み）
	usagePriceSrcCatalog = "catalog" // models.dev（usage_catalog.go）
)

// usagePriceOf は kind と model から単価を引く。第2戻り値が false = **値付け不可**
// （推定しない＝0 を出さない）。
//
// 順序は「**その消費が実際に通った provider**」で決まる（docs/46 §5-c）:
//  1. kind が anthropic 一次に当たる場合（claude など）は内蔵表を先に見る。こちらは
//     公表値を人が確認して置いた表で、コミュニティ由来のカタログより信頼が高い
//     （実測では models.dev の anthropic 値と完全一致した）。
//  2. それ以外はカタログ。**opencode の消費は opencode ゲートウェイの価格**で換算する
//     ＝利用者が実際に払う額に一番近い（2026-08-31 決定）。
//  3. カタログに無ければ内蔵表へ落とす（モデル名が claude 系なら拾える）。
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

// usageEstCostUSD は集計値1つぶんの API 換算相当額（推定）。
//
//	= in×入力単価 + ccreate×キャッシュ書込単価 + cread×キャッシュ読取単価 + out×出力単価
//
// spend（= in + ccreate + out）ではなく4種を個別に掛ける。キャッシュ読取は spend の定義に
// 入っていないが**課金はされる**ので、spend から金額を起こすと長い会話ほど実際より安く出る。
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
