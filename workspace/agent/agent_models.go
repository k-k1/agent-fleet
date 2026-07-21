package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
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
func handleAgentModels(w http.ResponseWriter, r *http.Request) {
	var list []agents.ModelChoice
	switch r.PathValue("kind") {
	case "claude":
		list = claude.Models()
	case "codex":
		list = codex.Models()
	case "opencode":
		for _, id := range opencode.Models() {
			list = append(list, agents.ModelChoice{ID: id, Label: id})
		}
	case "agy":
		list = agy.Models()
	case "copilot":
		// 静的カタログ（copilot CLI に列挙口が無い — docs/36）。未指定は auto。
		list = copilot.Models()
	default:
		httpx.WriteErr(w, http.StatusNotFound, "unknown_kind", "no model catalog for this kind")
		return
	}
	if list == nil {
		list = []agents.ModelChoice{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"models": list})
}
