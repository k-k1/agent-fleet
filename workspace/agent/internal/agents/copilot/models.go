package copilot

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

// Models is copilot's launch-time model catalog. copilot CLI に列挙口が無い
// （/model は TUI 専用・ACP configOptions にも model は無い — docs/36 実測）ため
// 静的リスト。未指定（既定）は Copilot 側の auto ルーティング。ID は `--model`
// にそのまま渡る。ドリフト検知は live テスト（models_live_test.go, opt-in）で
// 各 ID の受理を実呼び出し確認する — 週次リリースの CLI なので、増減はここを
// 直すだけでよい。
//
// Efforts は `--effort` の CLI 受理値（v1.0.73 --help）。DefaultEffort は空 =
// CLI 既定に任せる。
var copilotEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

func Models() []agents.ModelChoice {
	return []agents.ModelChoice{
		{ID: "gpt-5-mini", Label: "GPT-5 mini（追加コストなし枠）", Efforts: copilotEfforts},
		{ID: "claude-haiku-4.5", Label: "Claude Haiku 4.5", Efforts: copilotEfforts},
		{ID: "claude-sonnet-4.6", Label: "Claude Sonnet 4.6", Efforts: copilotEfforts},
		{ID: "gpt-5.4", Label: "GPT-5.4", Efforts: copilotEfforts},
		{ID: "gemini-3-pro", Label: "Gemini 3 Pro", Efforts: copilotEfforts},
	}
}
