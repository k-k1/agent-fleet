package usagex

// 使用量まわりの**下位層**（ADR 0067 ウェーブ B / AG-LOWER フェーズ1）。
//
// ここには「main の何にも依存しない型と context の運搬」だけを置く。台帳への記録
// （recordUsageCall）と、それが要る定数（usageFeature* / usageMeasured* / usageModel*）は
// usage_ledger.go 側にあり、そちらは PREP が編集中のためフェーズ2で移す。
// usageTokens / usageModelRow / usageCall も、メソッド名を公開するとメソッド呼び出しが
// 所有外のファイル（chat_providers.go・usage_provider_test.go・usage_ledger_test.go）で
// 壊れるため、usage_ledger.go と同じフェーズ2でまとめて動かす。

import (
	"context"
	"regexp"
	"strings"
)

// Tag は「この呼び出しは何のためか」。プロバイダ層は内容を解釈せず、そのまま台帳へ
// 転記する（新しい feature を足しても provider 側は無変更）。
type Tag struct {
	Feature string // usageFeature*
	Trigger string // usageTrigger*
	Ref     string // セッション名 or アシスタント会話 id
	Verb    string // assistant.chat のサブ次元（translate|summarize）
}

type tagKeyT struct{}

var tagKey tagKeyT

// WithTag は呼び出し側13箇所が打つ唯一の1行。
func WithTag(ctx context.Context, t Tag) context.Context {
	return context.WithValue(ctx, tagKey, t)
}

// TagOf はタグを取り出す。ok=false は「タグが無い（または Feature が空）」で、
// その場合に何を既定にするかは呼び出し側が決める（main の usageTagOf が
// feature=unknown を入れる — タグの付け忘れで消費が見えなくなる方が、unknown が
// 混ざるより悪い）。
func TagOf(ctx context.Context) (Tag, bool) {
	if ctx != nil {
		if t, ok := ctx.Value(tagKey).(Tag); ok && t.Feature != "" {
			return t, true
		}
	}
	return Tag{}, false
}

// ContextUsage is the CURRENT context fill: the last main-chain assistant event's
// input snapshot (cache read / cache creation / fresh input), like the Console's
// ContextBar. Absent until the session's first assistant reply; after claude's
// auto-compaction it reflects the post-compaction (smaller) context.
type ContextUsage struct {
	Tokens int `json:"tokens"` // read + create + fresh
	Read   int `json:"read"`
	Create int `json:"create"`
	Fresh  int `json:"fresh"`
	Window int `json:"window,omitempty"` // context-window size the pct is against
	// windowSource: "recorded" = the agent reported its real window (codex
	// model_context_window); "estimated" = guessed from the model name.
	WindowSource string  `json:"windowSource,omitempty"`
	Pct          float64 `json:"pct,omitempty"` // 0–100, tokens/window
	Model        string  `json:"model,omitempty"`
}

// smallWindowClaudeRe は「200k 側」の Claude だけを列挙する。Claude は Opus 4.6 /
// Sonnet 4.6 以降 1M ネイティブで、今後出るモデルも 1M 前提なので、大きい方を
// 列挙して新モデルのたびに追記する（＝漏れたら 200k に誤認される）運用をやめ、
// 既定 1M・小さいものだけ例外、に反転してある。
//   - haiku 系（4.5 まで 200k）
//   - Claude 3.x 以前（claude-2 / claude-3-*）
//   - Opus 4.0/4.1/4.5・Sonnet 4.0/4.5。日付入りIDは opus-4-20250514 の形なので
//     「4-2」も旧世代側に含める（1M 側の 4-6/4-7/4-8 とは重ならない）。
var smallWindowClaudeRe = regexp.MustCompile(`haiku|claude-[123]|opus-4-[0125]|sonnet-4-[025]`)

// WindowGuess (旧 contextWindowGuess) mirrors the Console's contextWindow() (ContextBar.tsx — keep
// the two in sync). Order: 272k for GPT-5.x (codex normally records its real
// window, so this is the fallback — e.g. the assistant chat's `codex exec`, whose
// events don't carry it) → 200k for the legacy Claude generations above → 1M for
// every other Claude → for non-Claude unknowns, 200k with a grow-to-fit fallback
// when the observed usage already exceeds it.
func WindowGuess(model string, used int) int {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "gpt-5"):
		return 272_000
	case smallWindowClaudeRe.MatchString(m):
		return 200_000
	case strings.Contains(m, "claude"):
		return 1_000_000
	}
	if used > 200_000 {
		return 1_000_000
	}
	return 200_000
}
