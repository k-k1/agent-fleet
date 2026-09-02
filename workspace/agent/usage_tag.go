package main

// 使用量タグと、プロバイダ層1点記録の入口（docs/log/46 §3-a / ADR0029 §3）。
//
// usage を解析しているのはプロバイダ実装の内側（claudeChat.send / parseCodexExecEvents /
// parseOpencodeRunEvents / cursorChat.send / oneShotHeadless）で、そこは既にモデルも
// トークンも持っている。足りないのは「何のための呼び出しか」だけなので、それだけを
// context.Context に載せて運ぶ。消費源の変更は1箇所1行で済み、記録点は増えない。

import (
	"context"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// usageTagOf はタグを取り出す。タグの無い呼び出しは feature=unknown として必ず記録する
// （タグの付け忘れで消費が見えなくなる方が、unknown が混ざるより悪い）。
// 型と context への出し入れそのものは internal/usagex（別名は alias_usagex.go）。
// 既定の feature=unknown だけがここに残るのは、usageFeature* が usage_ledger.go に
// あり、そちらの移送がフェーズ2だから。
func usageTagOf(ctx context.Context) usageTag {
	if t, ok := usagex.TagOf(ctx); ok {
		return t
	}
	return usageTag{Feature: usageFeatureUnknown}
}

// chatTurnUsageTag はアシスタントチャット1ターン分のタグ。SeedVerb（Files 由来の翻訳/
// 要約スレッド）は feature を増やさず verb のサブ次元として割る — 独立カテゴリとして
// 見たいが、機能の enum を増やすと Console の色・i18n・フィルタ全部に波及する（docs/log/46 §1-a）。
func chatTurnUsageTag(c *chatConversation, trigger string) usageTag {
	return usageTag{
		Feature: usageFeatureAssistantChat, Trigger: trigger, Ref: c.ID, Verb: c.SeedVerb,
	}
}

// usageTokens は1モデル分のトークン内訳。
type usageTokens struct {
	In          int
	Out         int
	CacheRead   int
	CacheCreate int
}

// usageModelRow は「1呼び出しの中で実際に課金された1モデル分」。claude は modelUsage で
// モデル毎の内訳を返すので、これが複数になりうる（同じ Call で束ねて1呼び出しと数える）。
type usageModelRow struct {
	Model    string // 正規名（claude の canonicalModel）
	ModelRaw string // 生 id（版込み）
	Tokens   usageTokens
	CostUSD  float64
}

// usageCall は1回の LLM 呼び出しをプロバイダ層が見たまま表したもの。各プロバイダ関数の
// 先頭でゼロ値を作って defer に積み、分かった分だけ埋めていく — 成功・失敗・早期 return の
// どの経路でも必ず1回記録されるのがこの形の主目的。
type usageCall struct {
	Kind     string          // 実行したエージェント種別（要求ではなく実行結果）
	ModelReq string          // 要求した値（"" = CLI の既定に委ねた）
	Models   []usageModelRow // モデル別内訳。空なら Totals から1行を起こす
	Totals   usageTokens     // Models が空のときのトークン
	CostUSD  float64         // 実測コスト（claude のみ）
	OK       bool
	Measured string // 空なら Totals/Models の中身から推定する
}

// setTotals は「モデル別内訳の無い」プロバイダ（codex/opencode/cursor）用の記録口。
func (c *usageCall) setTotals(in, out, cread, ccreate int) {
	c.Totals = usageTokens{In: in, Out: out, CacheRead: cread, CacheCreate: ccreate}
}

// add は同じ呼び出しの中で二度撃った分を足す（codex 一発呼び出しのモデル外しリトライ）。
// 別プロセスを2回起動しているので、入力側もスナップショットではなく実消費の合算になる。
func (t usageTokens) add(o usageTokens) usageTokens {
	return usageTokens{
		In: t.In + o.In, Out: t.Out + o.Out,
		CacheRead: t.CacheRead + o.CacheRead, CacheCreate: t.CacheCreate + o.CacheCreate,
	}
}

func (t usageTokens) any() bool { return t.In+t.Out+t.CacheRead+t.CacheCreate > 0 }

// fallbackTotals は「モデル別内訳が取れなかった時」だけ効く縮退の記録口。claude は通常
// result の modelUsage でモデル別に割れるが、**利用者の停止操作や result 前の異常終了では
// modelUsage が来ない**。そこで残っている usage スナップショットを採る — ここで何も採らないと
// 実際に使ったコンテキストが「トークン 0 / measured=none」として消え、止めた回だけ消費が
// 見えなくなる（止めるのは重いターンほど多い）。
//
// measured は呼び出し側が申告する: 完結した result 由来なら空（＝exact 判定に委ねる）、
// 途中のスナップショット由来なら partial。
func (c *usageCall) fallbackTotals(t usageTokens, measured string) {
	if len(c.Models) > 0 || c.Totals.any() || !t.any() {
		return
	}
	c.Totals = t
	if measured != "" {
		c.Measured = measured
	}
}

// recordUsageCall は台帳へ1呼び出し分（＝1行以上）を書く。ctx はタグを読むためだけに
// 使うので、キャンセル済み ctx（利用者の停止操作・タイムアウト）でも記録は必ず残る。
func recordUsageCall(ctx context.Context, c *usageCall, started time.Time) {
	if !usageEnabled() {
		return
	}
	tag := usageTagOf(ctx)
	origin, originConv := usageOriginOf(tag.Ref)
	base := usageRecord{
		TS:         time.Now().UTC().Format(time.RFC3339),
		Call:       randUUID(),
		Feature:    tag.Feature,
		Trigger:    tag.Trigger,
		Origin:     origin,
		OriginConv: originConv,
		Kind:       c.Kind,
		ModelReq:   c.ModelReq,
		Ref:        tag.Ref,
		Verb:       tag.Verb,
		MS:         int(time.Since(started).Milliseconds()),
		OK:         c.OK,
	}
	rows := make([]usageRecord, 0, max(1, len(c.Models)))
	if len(c.Models) == 0 {
		r := base
		r.Model, r.ModelSrc = usageModelFallback(c.ModelReq)
		r.In, r.Out = c.Totals.In, c.Totals.Out
		r.CacheRead, r.CacheCreate = c.Totals.CacheRead, c.Totals.CacheCreate
		r.CostUSD = c.CostUSD
		r.Spend = usageSpend(r.In, r.CacheCreate, r.Out)
		r.Measured = c.measuredOr(c.Totals)
		rows = append(rows, r)
	}
	for _, m := range c.Models {
		r := base
		r.Model, r.ModelRaw, r.ModelSrc = m.Model, m.ModelRaw, usageModelReported
		if r.Model == "" {
			r.Model = m.ModelRaw // canonicalModel の無い報告は生 id をそのまま系列キーに
		}
		r.In, r.Out = m.Tokens.In, m.Tokens.Out
		r.CacheRead, r.CacheCreate = m.Tokens.CacheRead, m.Tokens.CacheCreate
		r.CostUSD = m.CostUSD
		r.Spend = usageSpend(r.In, r.CacheCreate, r.Out)
		r.Measured = c.measuredOr(m.Tokens)
		rows = append(rows, r)
	}
	appendUsageRows(rows)
}

// measuredOr は自己申告の計測精度。プロバイダが明示していれば従い、していなければ
// 「トークンが1つでも取れたか」で exact / none を決める（失敗ターンは none）。
func (c *usageCall) measuredOr(t usageTokens) string {
	if c.Measured != "" {
		return c.Measured
	}
	if t.In+t.Out+t.CacheRead+t.CacheCreate > 0 {
		return usageMeasuredExact
	}
	return usageMeasuredNone
}

// usageModelFallback はモデルを報告しない CLI（codex/cursor/agy）向けの縮退。要求値が
// あれば requested、無ければ default_unknown — 「CLI の既定（通常フラッグシップ）で
// 走っている」ことが1列で見えるのが狙い（docs/log/46 §2-b）。
func usageModelFallback(req string) (model, src string) {
	if req == "" {
		return "", usageModelUnknown
	}
	return req, usageModelRequest
}

// usageOriginOf は ref からセッションの出自を解決する（ADR0029 §6）。行へ焼き込むので
// セッションが削除されても集計が壊れない。会話スコープの ref（アシスタント会話 id）は
// 出自の軸を持たないので空を返す — origin はセッションの軸。
func usageOriginOf(ref string) (origin, conv string) {
	if ref == "" || !session.ValidName(ref) {
		return "", ""
	}
	m, ok := session.ReadMeta(ref)
	if !ok {
		return "", ""
	}
	return session.OriginOf(m), m.OriginConv
}
