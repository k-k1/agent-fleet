package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// handleAgentModels (GET /agents/{kind}/models) returns the launch-time model
// choices for a kind whose catalog must be read live rather than hardcoded:
//   - codex: `codex debug models` — the /model picker's catalog, refreshed from
//     OpenAI's models endpoint with codex's own subscription auth (id + display name)
//   - opencode: `opencode models` — reflects the user's connected providers (ids only)
//
// claude keeps its Console-side fixed tier aliases (they don't vary per user), so
// it is not served here. An empty list is a valid answer (CLI absent / offline) —
// the Console picker then offers only 既定.
func handleAgentModels(w http.ResponseWriter, r *http.Request) {
	var list []agents.ModelChoice
	switch r.PathValue("kind") {
	case "codex":
		list = codex.Models()
	case "opencode":
		for _, id := range opencode.Models() {
			list = append(list, agents.ModelChoice{ID: id, Label: id})
		}
	default:
		httpx.WriteErr(w, http.StatusNotFound, "unknown_kind", "no model catalog for this kind")
		return
	}
	if list == nil {
		list = []agents.ModelChoice{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"models": list})
}
