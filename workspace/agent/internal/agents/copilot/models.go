package copilot

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

// Models is copilot's launch-time model catalog. copilot CLI に列挙口が無い
// （/model は TUI 専用・ACP configOptions にも model は無い — docs/36 実測）ため
// 静的リスト。ID 群は TUI /model ピッカーの実測語彙（v1.0.73, 2026-07-21）から
// 採った主要どころ。未指定（既定）は Copilot 側の auto ルーティング。
//
// 注意（実測）: **モデルの可否はプラン依存**。Copilot Free は Auto のみで、明示
// --model は正しい ID でも "Model ... is not available" で起動に失敗する — その
// 場合は既定（auto）で起動すること（カード/ヒントに注記済み）。ドリフト（新
// モデル追加・廃止）はここを直すだけでよい。
//
// Efforts は `--effort` の CLI 受理値（v1.0.73 --help）。DefaultEffort は空 =
// CLI 既定に任せる。
var copilotEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

func Models() []agents.ModelChoice {
	ids := []string{
		"gpt-5-mini",
		"gpt-5.4-mini",
		"gpt-5.4",
		"gpt-5.5",
		"gpt-5.6-sol",
		"gpt-5.3-codex",
		"claude-haiku-4.5",
		"claude-sonnet-4.6",
		"claude-sonnet-5",
		"claude-fable-5",
		"claude-opus-4.8",
		"gemini-3.5-flash",
		"gemini-3.1-pro-preview",
	}
	list := make([]agents.ModelChoice, 0, len(ids))
	for _, id := range ids {
		list = append(list, agents.ModelChoice{ID: id, Label: id, Efforts: copilotEfforts})
	}
	return list
}
