package chatx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/modelfallback"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// oldRecommendedAssistantModelV is the kind switch recommendedAssistantModelV held before the
// rules became a table. A deliberate literal copy: the table must answer exactly what it did
// for every registered kind, for an alias or case variant the exact-match switch never matched,
// and for an unregistered kind.
func oldRecommendedAssistantModelV(v visibility, agent string) string {
	switch agent {
	case session.KindClaude:
		return claudeFirstVisible(v, claudeChatTiers)
	case session.KindCodex:
		return codexNewestLuna(v)
	case session.KindOpencode:
		return recommendedCatalogModel(v.ids(agent, opencode.Models()), modelfallback.ChatOpencodeGo,
			v.model(agent, defaultOpencodeChatModel))
	case session.KindAgy:
		return agyNamedModel(v, defaultAgyChatModel)
	}
	return ""
}

func TestAssistantModelRulesMatchOldSwitch(t *testing.T) {
	// opencode.Models execs the CLI on every call (a failed read is not cached); with no CLI on
	// PATH it answers at once, and both sides of the comparison read the same empty catalog.
	t.Setenv("PATH", t.TempDir())
	useCatalogs(t, codexCatalog0926, []string{defaultAgyChatModel, "gemini-3.5-pro"}, nil, nil)
	probes := append([]string{"", "Claude", "CODEX", " cursor", "unknown-kind", "claude-code"}, agentreg.Kinds()...)
	for name, v := range map[string]visibility{
		"saved":    prefsVisibility,
		"explicit": {explicit: true, hidden: []string{"sonnet", "gpt-6-luna"}},
	} {
		for _, kind := range probes {
			if got, want := recommendedAssistantModelV(v, kind), oldRecommendedAssistantModelV(v, kind); got != want {
				t.Errorf("%q (%s visibility): %q, old switch %q", kind, name, got, want)
			}
		}
	}
	// Each ruled kind must answer something, or the equivalence above compares empty with empty.
	for _, kind := range []string{session.KindClaude, session.KindCodex, session.KindAgy} {
		if oldRecommendedAssistantModelV(prefsVisibility, kind) == "" {
			t.Errorf("%s has no recommendation here; the comparison proves nothing for it", kind)
		}
	}
}
