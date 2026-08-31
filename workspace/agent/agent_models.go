package main

import (
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// handleAgentModels (GET /agents/{kind}/models) returns the launch-time model
// choices per kind:
//   - claude: fixed tier aliases (claude.Models) — no live catalog exists; launch
//     takes `--model <alias>` and the alias tracks its tier's newest model. The
//     Console picker keeps its own copy (settings.ts CLAUDE_MODELS); this serves
//     the MCP list_models so assistants resolve claude ids the same way as the
//     other kinds.
//   - codex: `codex debug models` — the /model picker's catalog, refreshed from
//     OpenAI's models endpoint with codex's own subscription auth (id + display name)
//   - opencode: `opencode models` — reflects the user's connected providers (ids only)
//   - agy: `agy models` — display names, accepted verbatim by `agy --model`
//
// An empty list is a valid answer (CLI absent / offline) — the Console picker then
// offers only 既定.
//
// 並び順は各 kind の上流の推奨順をそのまま出す（codex の priority 順、cursor / kiro /
// copilot / agy の列挙順は「新しい順・系列ごと」で意味を持つ）。取得経路が 2 つあって
// 上流順が一意に決まらない opencode だけ、パッケージ側（catalog.go）で正規化する。
// ポリシーの説明は agents/modelsort.go。
func handleAgentModels(w http.ResponseWriter, r *http.Request) {
	var list []agents.ModelChoice
	switch r.PathValue("kind") {
	case "claude":
		list = claude.Models()
		seen := make(map[string]bool, len(list))
		for _, model := range list {
			seen[strings.ToLower(model.ID)] = true
		}
		for _, id := range claudeCustomModelsPref() {
			if key := strings.ToLower(id); !seen[key] {
				list = append(list, agents.ModelChoice{ID: id, Label: id})
				seen[key] = true
			}
		}
	case "codex":
		list = codex.Models()
	case "cursor":
		// `cursor-agent models` の行パース（id - 表示名・アカウント連動 — docs/log/40）。
		list = cursor.Models()
	case "kiro":
		// `kiro-cli chat --list-models -f json`（完全機械可読・アカウント連動 — docs/log/43）。
		list = kiro.Models()
	case "opencode":
		// 一覧の整形だけ（catalog.go）: 1 本のキーが Zen（従量）と Go（サブスク）の
		// 両プロバイダを開けるので、同名モデルが両方に並ぶ。Zen の表示可否はユーザー設定
		// （ui-prefs opencodeCatalog）に従い、並びは Go 先頭＋id 昇順に正規化する
		// （daemon 由来と CLI 由来で上流順が違うため）。モデル指定の検証は
		// handleCreateSession が整形前の全カタログで行うので、明示指定は握り潰さない。
		list = opencode.Catalog(opencode.Models(), opencodeCatalogPref())
	case "agy":
		list = agy.Models()
	case "copilot":
		// TUI /model ピッカーの PTY スクレイプ（プラン反映ライブ取得 — docs/log/36 追補。
		// Free は Auto のみ＝空リスト）。未指定は auto ルーティング。
		list = copilot.Models()
	default:
		httpx.WriteErr(w, http.StatusNotFound, "unknown_kind", "no model catalog for this kind")
		return
	}
	// 使わないモデル（ui-prefs hiddenModels）を最後に落とす。ここが Console の
	// ピッカーと MCP list_models の合流点なので、1 箇所で両方に効く（opencodeCatalog
	// と同じ構図）。明示指定は handleCreateSession のガードが別に断る。
	list = filterVisibleModels(r.PathValue("kind"), list)
	if list == nil {
		list = []agents.ModelChoice{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"models": list})
}
