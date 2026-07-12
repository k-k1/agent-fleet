package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// handleOpencodeModels (GET /agents/opencode/models) returns the models the local
// opencode CLI can use right now (provider/model ids, from `opencode models`). The
// list reflects the user's connected providers, so the Console's launch model picker
// reads it live instead of hardcoding a catalog (claude/codex keep Console-side fixed
// lists — their catalogs don't vary per user).
func handleOpencodeModels(w http.ResponseWriter, r *http.Request) {
	list := opencode.Models()
	if list == nil {
		list = []string{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"models": list})
}
