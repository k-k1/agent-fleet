package claude

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// efforts are the `--effort` levels Claude Code accepts. The CLI has no catalog to report them
// per model, so they are declared here; list_models hands them to a parent session choosing a
// child's effort, and create_session validates against them. Mirrors the Console's
// FALLBACK_EFFORTS.claude (console/src/lib/agentModels.ts).
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// Models returns claude's launch-time model choices. Unlike codex/opencode there is
// no live catalog to query: launch passes `--model <tier alias>` and each alias
// tracks the newest model of its tier, so the list is fixed per build and does not
// vary per user. User-registered full ids are appended by handleAgentModels from UI
// prefs; this package owns only the built-in aliases. Mirrors the Console's
// CLAUDE_MODELS (console/src/lib/settings.ts) — keep the aliases in sync.
func Models() []agents.ModelChoice {
	return []agents.ModelChoice{
		{ID: "fable", Label: "Fable", Efforts: EffortsFor("fable")},
		{ID: "opus", Label: "Opus", Efforts: EffortsFor("opus")},
		{ID: "sonnet", Label: "Sonnet", Efforts: EffortsFor("sonnet")},
		{ID: "haiku", Label: "Haiku", Efforts: EffortsFor("haiku")},
	}
}

// EffortsFor returns the effort levels model accepts, for a tier alias or a registered full id.
// Haiku takes none — the Console's launch picker hides effort for it for the same reason.
func EffortsFor(model string) []string {
	if strings.Contains(strings.ToLower(model), "haiku") {
		return nil
	}
	return append([]string(nil), efforts...)
}
