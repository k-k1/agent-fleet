package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
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
// offers only the default entry.
//
// The order is whatever the upstream of each kind recommends, passed through as is
// (codex's priority order; the enumeration order of cursor / kiro / copilot / agy means
// "newest first, grouped by family"). Only opencode is normalized in the package itself
// (catalog.go), because it has two fetch paths and no single upstream order. The policy
// is explained in agents/modelsort.go.
func handleAgentModels(w http.ResponseWriter, r *http.Request) {
	var list []agents.ModelChoice
	switch r.PathValue("kind") {
	case "claude":
		list = claude.Models()
		seen := make(map[string]bool, len(list))
		for _, model := range list {
			seen[strings.ToLower(model.ID)] = true
		}
		for _, id := range uiprefs.ClaudeCustomModels() {
			if key := strings.ToLower(id); !seen[key] {
				list = append(list, agents.ModelChoice{ID: id, Label: id})
				seen[key] = true
			}
		}
	case "codex":
		list = codex.Models()
	case "cursor":
		// Line-parsed from `cursor-agent models` (id - display name, tied to the
		// account — docs/log/40).
		list = cursor.Models()
	case "kiro":
		// `kiro-cli chat --list-models -f json` (fully machine-readable, tied to the
		// account — docs/log/43).
		list = kiro.Models()
	case "opencode":
		// Shaping of the list only (catalog.go): one key can open both the Zen
		// (pay-as-you-go) and Go (subscription) providers, so the same model name
		// appears under both. Whether Zen is shown follows the user setting (ui-prefs
		// opencodeCatalog), and the order is normalized to Go first then id ascending
		// (the upstream order differs between the daemon and the CLI path). An
		// explicit model is never swallowed here: handleCreateSession validates it
		// against the full, unshaped catalog.
		list = opencode.Catalog(opencode.Models(), uiprefs.OpencodeCatalog())
	case "agy":
		list = agy.Models()
	case "copilot":
		// PTY scrape of the TUI /model picker (live, so it reflects the plan —
		// docs/log/36 addendum; Free offers only Auto, i.e. an empty list).
		// Unspecified means auto routing.
		list = copilot.Models()
	default:
		httpx.WriteErr(w, http.StatusNotFound, "unknown_kind", "no model catalog for this kind")
		return
	}
	// Drop the models the user hides (ui-prefs hiddenModels) last. This is where the
	// Console picker and the MCP list_models meet, so one place covers both (the same
	// shape as opencodeCatalog). An explicitly named hidden model is refused separately
	// by the guard in handleCreateSession.
	list = sessionx.FilterVisibleModels(r.PathValue("kind"), list)
	if list == nil {
		list = []agents.ModelChoice{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"models": list})
}
