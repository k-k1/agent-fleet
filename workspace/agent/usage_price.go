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
type usagePrice struct {
	In  float64
	Out float64
}

// キャッシュの倍率（Anthropic 公表）。書き込みは 5 分 TTL の 1.25 倍、読み出しは 0.1 倍。
// **転写は 5m と 1h の内訳を残さない**（session_usage.go の CacheCreate は合算）ので、
// 1h TTL（2 倍）で書かれた分はここでは 1.25 倍として数えられる＝推定は下振れしうる。
const (
	usageCacheWriteMult = 1.25
	usageCacheReadMult  = 0.10
)

// usagePrices は正規化済みモデル名 → 単価。**Anthropic の公表単価のみ**を載せている
// （2026-08 時点）。他プロバイダ（gpt-* / qwen* / glm-* …）は、こちらが確かな公表単価を
// 持っていないので**意図的に載せていない** — 適当な既定値で埋めると、根拠の無い数字が
// 「推定額」の顔をして合計に混ざる。
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

// usagePriceOf は正規化して単価を引く。第2戻り値が false = **値付け不可**（推定しない）。
func usagePriceOf(model string) (usagePrice, bool) {
	p, ok := usagePrices[usageNormalizeModel(model)]
	return p, ok
}

// usageEstCostUSD は集計値1つぶんの API 換算相当額（推定）。
//
//	= (in + ccreate×1.25 + cread×0.1) × 入力単価 + out × 出力単価
//
// spend（= in + ccreate + out）ではなく4種を個別に掛ける。キャッシュ読取は spend の定義に
// 入っていないが**課金はされる**（0.1 倍）ので、spend から金額を起こすと長い会話ほど
// 実際より安く出る。
func usageEstCostUSD(model string, a usageAgg) (float64, bool) {
	p, ok := usagePriceOf(model)
	if !ok {
		return 0, false
	}
	in := float64(a.In) + float64(a.CacheCreate)*usageCacheWriteMult + float64(a.CacheRead)*usageCacheReadMult
	return (in*p.In + float64(a.Out)*p.Out) / 1_000_000, true
}
