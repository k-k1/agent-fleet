package kiro

// チャットミラーの ContextBar 用のセッションレベル context 充填率（Track D — docs/log/43 §10）。
// kiro の v2 JSONL 転写には per-turn のトークン数が無い（claude/codex と違う）ので、
// claude 型の転写由来 ContextBar は出せない。代わりに managed（ACP）driver が
// `_kiro.dev/metadata` 通知で運ぶライブの contextUsagePercentage を使う。agy が /context を
// PTY スクレイプするのと同じ agents.ContextReporter seam だが、kiro は値が生きた handle に
// インメモリで載っているので サブプロセス不要・非ブロッキングで即返せる。
//
// %→token 変換: kiro は % を直接くれるが、既存の ContextBar はトークン数ベース（read/
// create/fresh を window に対して描く）。そこで % をモデルの実 context window（カタログ由来）
// に対するトークン数へ変換して単一セグメントとして載せる。window を明示で渡すため、フロント
// 側が tokens/window から % を再計算しても元の % に厳密に一致する（丸め誤差のみ）。managed の
// paneless セッションはミラーが唯一のビューなので、ここがライブ context の主表示になる。
//
// TUI（Terminal 実行）や metadata 未受信の段階では ManagedContext が ok=false を返し、この
// 関数も nil を返す＝ContextBar は出ない（pct のソースが無いので正直に非表示）。

import (
	"math"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// ContextFill implements agents.ContextReporter for the generic /messages handler
// (the chat mirror's poll). Returns nil unless a live managed handle has reported at
// least one _kiro.dev/metadata percentage.
func (agentImpl) ContextFill(m session.Meta) *transcript.Context {
	pct, window, _, _, ok := ManagedContext(m.Name)
	if !ok || window <= 0 {
		return nil
	}
	tokens := int(math.Round(pct / 100 * float64(window)))
	return &transcript.Context{
		Tokens: tokens,
		Window: window,
		At:     time.Now().UTC().Format(time.RFC3339),
	}
}
