package usagex

// プロバイダ層 1 点記録（docs/log/46 §3-a / ADR0029 §3）。
//
// usage を解析しているのはプロバイダ実装の内側（claudeChat.send / parseCodexExecEvents /
// parseOpencodeRunEvents / cursorChat.send / oneShotHeadless）で、そこは既にモデルも
// トークンも持っている。足りないのは「何のための呼び出しか」だけなので、それだけを
// context.Context に載せて運ぶ（Tag / WithTag / TagOf）。消費源の変更は1箇所1行で済み、
// 記録点は増えない。
//
// ⚠️ Tokens / Call のメソッドが公開名なのは、main から呼ぶには公開するしかないため
// （Go はメソッドをエイリアスできない）。移送前は setTotals / add / any / fallbackTotals /
// measuredOr だった。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TagOrUnknown はタグを取り出す。タグの無い呼び出しは feature=unknown として必ず記録する
// （タグの付け忘れで消費が見えなくなる方が、unknown が混ざるより悪い）。
func TagOrUnknown(ctx context.Context) Tag {
	if t, ok := TagOf(ctx); ok {
		return t
	}
	return Tag{Feature: FeatureUnknown}
}

// Tokens は1モデル分のトークン内訳。
type Tokens struct {
	In          int
	Out         int
	CacheRead   int
	CacheCreate int
}

// ModelRow は「1呼び出しの中で実際に課金された1モデル分」。claude は modelUsage で
// モデル毎の内訳を返すので、これが複数になりうる（同じ Call で束ねて1呼び出しと数える）。
type ModelRow struct {
	Model    string // 正規名（claude の canonicalModel）
	ModelRaw string // 生 id（版込み）
	Tokens   Tokens
	CostUSD  float64
}

// Call は1回の LLM 呼び出しをプロバイダ層が見たまま表したもの。各プロバイダ関数の
// 先頭でゼロ値を作って defer に積み、分かった分だけ埋めていく — 成功・失敗・早期 return の
// どの経路でも必ず1回記録されるのがこの形の主目的。
type Call struct {
	Kind     string     // 実行したエージェント種別（要求ではなく実行結果）
	ModelReq string     // 要求した値（"" = CLI の既定に委ねた）
	Models   []ModelRow // モデル別内訳。空なら Totals から1行を起こす
	Totals   Tokens     // Models が空のときのトークン
	CostUSD  float64    // 実測コスト（claude のみ）
	OK       bool
	Measured string // 空なら Totals/Models の中身から推定する
}

// setTotals は「モデル別内訳の無い」プロバイダ（codex/opencode/cursor）用の記録口。
func (c *Call) SetTotals(in, out, cread, ccreate int) {
	c.Totals = Tokens{In: in, Out: out, CacheRead: cread, CacheCreate: ccreate}
}

// add は同じ呼び出しの中で二度撃った分を足す（codex 一発呼び出しのモデル外しリトライ）。
// 別プロセスを2回起動しているので、入力側もスナップショットではなく実消費の合算になる。
func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{
		In: t.In + o.In, Out: t.Out + o.Out,
		CacheRead: t.CacheRead + o.CacheRead, CacheCreate: t.CacheCreate + o.CacheCreate,
	}
}

func (t Tokens) Any() bool { return t.In+t.Out+t.CacheRead+t.CacheCreate > 0 }

// fallbackTotals は「モデル別内訳が取れなかった時」だけ効く縮退の記録口。claude は通常
// result の modelUsage でモデル別に割れるが、**利用者の停止操作や result 前の異常終了では
// modelUsage が来ない**。そこで残っている usage スナップショットを採る — ここで何も採らないと
// 実際に使ったコンテキストが「トークン 0 / measured=none」として消え、止めた回だけ消費が
// 見えなくなる（止めるのは重いターンほど多い）。
//
// measured は呼び出し側が申告する: 完結した result 由来なら空（＝exact 判定に委ねる）、
// 途中のスナップショット由来なら partial。
func (c *Call) FallbackTotals(t Tokens, measured string) {
	if len(c.Models) > 0 || c.Totals.Any() || !t.Any() {
		return
	}
	c.Totals = t
	if measured != "" {
		c.Measured = measured
	}
}

// RecordCall は台帳へ1呼び出し分（＝1行以上）を書く。ctx はタグを読むためだけに
// 使うので、キャンセル済み ctx（利用者の停止操作・タイムアウト）でも記録は必ず残る。
func RecordCall(ctx context.Context, c *Call, started time.Time) {
	if !Enabled() {
		return
	}
	tag := TagOrUnknown(ctx)
	origin, originConv := originOf(tag.Ref)
	base := Record{
		TS:         time.Now().UTC().Format(time.RFC3339),
		Call:       newCallID(),
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
	rows := make([]Record, 0, max(1, len(c.Models)))
	if len(c.Models) == 0 {
		r := base
		r.Model, r.ModelSrc = ModelFallback(c.ModelReq)
		r.In, r.Out = c.Totals.In, c.Totals.Out
		r.CacheRead, r.CacheCreate = c.Totals.CacheRead, c.Totals.CacheCreate
		r.CostUSD = c.CostUSD
		r.Spend = Spend(r.In, r.CacheCreate, r.Out)
		r.Measured = c.MeasuredOr(c.Totals)
		rows = append(rows, r)
	}
	for _, m := range c.Models {
		r := base
		r.Model, r.ModelRaw, r.ModelSrc = m.Model, m.ModelRaw, ModelReported
		if r.Model == "" {
			r.Model = m.ModelRaw // canonicalModel の無い報告は生 id をそのまま系列キーに
		}
		r.In, r.Out = m.Tokens.In, m.Tokens.Out
		r.CacheRead, r.CacheCreate = m.Tokens.CacheRead, m.Tokens.CacheCreate
		r.CostUSD = m.CostUSD
		r.Spend = Spend(r.In, r.CacheCreate, r.Out)
		r.Measured = c.MeasuredOr(m.Tokens)
		rows = append(rows, r)
	}
	AppendRows(rows)
}

// measuredOr は自己申告の計測精度。プロバイダが明示していれば従い、していなければ
// 「トークンが1つでも取れたか」で exact / none を決める（失敗ターンは none）。
func (c *Call) MeasuredOr(t Tokens) string {
	if c.Measured != "" {
		return c.Measured
	}
	if t.In+t.Out+t.CacheRead+t.CacheCreate > 0 {
		return MeasuredExact
	}
	return MeasuredNone
}

// ModelFallback はモデルを報告しない CLI（codex/cursor/agy）向けの縮退。要求値が
// あれば requested、無ければ default_unknown — 「CLI の既定（通常フラッグシップ）で
// 走っている」ことが1列で見えるのが狙い（docs/log/46 §2-b）。
func ModelFallback(req string) (model, src string) {
	if req == "" {
		return "", ModelUnknown
	}
	return req, ModelRequest
}

// originOf は ref からセッションの出自を解決する（ADR0029 §6）。行へ焼き込むので
// セッションが削除されても集計が壊れない。会話スコープの ref（アシスタント会話 id）は
// 出自の軸を持たないので空を返す — origin はセッションの軸。
func originOf(ref string) (origin, conv string) {
	if ref == "" || !session.ValidName(ref) {
		return "", ""
	}
	m, ok := session.ReadMeta(ref)
	if !ok {
		return "", ""
	}
	return session.OriginOf(m), m.OriginConv
}

// newCallID は台帳の Call 列（1呼び出しが複数モデル行に割れたときに束ねる id）。
// RFC-4122 v4。移送前は main の randUUID()（chat_store.go）を呼んでいたが、
// あちらは AG-CHAT 所有＝ウェーブ C まで動かせないので、ここに同じものを持つ。
//
// ⚠️ 「写した純関数」の債務: internal/browserx/uuid.go の randUUID と同型で、
// 回収ウェーブで 1 本化する対象（CP-STORE util.go / testutil、AG-BROWSER randUUID、
// CP-AUTH env.go に続く）。UUID v4 は標準仕様なので分岐の余地は無いが、置き場が増えた。
func newCallID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}
