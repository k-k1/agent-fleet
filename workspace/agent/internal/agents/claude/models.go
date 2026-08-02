package claude

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

// Models returns claude's launch-time model choices. Unlike codex/opencode there is
// no live catalog to query: launch passes `--model <tier alias>` and each alias
// tracks the newest model of its tier, so the list is fixed per build and does not
// vary per user. User-registered full ids are appended by handleAgentModels from UI
// prefs; this package owns only the built-in aliases. Mirrors the Console's
// CLAUDE_MODELS (console/src/lib/settings.ts) — keep the aliases in sync.
func Models() []agents.ModelChoice {
	return []agents.ModelChoice{
		{ID: "fable", Label: "Fable"},
		{ID: "opus", Label: "Opus"},
		{ID: "sonnet", Label: "Sonnet"},
		{ID: "haiku", Label: "Haiku"},
	}
}
