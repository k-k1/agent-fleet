package claude

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

// Models returns claude's launch-time model choices. Unlike codex/opencode there is
// no live catalog to query: launch passes `--model <tier alias>` and each alias
// tracks the newest model of its tier, so the list is fixed per build and does not
// vary per user. The Console additionally accepts a full model name for pinning an
// older release; it cannot be listed here because availability varies by account and
// over time. Mirrors the Console's CLAUDE_MODELS (console/src/lib/settings.ts) — keep
// the aliases in sync.
func Models() []agents.ModelChoice {
	return []agents.ModelChoice{
		{ID: "fable", Label: "Fable"},
		{ID: "opus", Label: "Opus"},
		{ID: "sonnet", Label: "Sonnet"},
		{ID: "haiku", Label: "Haiku"},
	}
}
