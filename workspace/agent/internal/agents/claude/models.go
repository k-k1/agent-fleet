package claude

import (
	"regexp"
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

// legacyHaikuRe matches the Haiku ids that predate effort support: Haiku 4.x and older
// (`claude-haiku-4-5-…`, `haiku-4`) and the Claude 3 generation, which puts the version before
// the tier (`claude-3-5-haiku-…`). The bare `haiku` alias and Haiku 5+ do not match.
// model-id-lint:allow family match for the effort rule, not a model choice
var legacyHaikuRe = regexp.MustCompile(`haiku-[0-4]([^0-9]|$)|claude-3`)

// EffortsFor returns the effort levels model accepts, for a tier alias or a registered full id.
// Haiku 4.x and older take none; Haiku 5.5 takes all five, and the bare `haiku` alias tracks the
// current Haiku so it offers them too. If the alias is pinned to an older model
// (ANTHROPIC_DEFAULT_HAIKU_MODEL), Claude Code drops the rejected effort and retries, so a launch
// does not fail. The Console mirrors this in agentModels.ts (isLegacyHaiku).
func EffortsFor(model string) []string {
	if legacyHaikuRe.MatchString(strings.ToLower(model)) && strings.Contains(strings.ToLower(model), "haiku") {
		return nil
	}
	return append([]string(nil), efforts...)
}
