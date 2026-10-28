package lcpp

import (
	"context"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// Optional agents capabilities of the lcpp kind.

// EngineModels is the chat-role engine catalog as a model menu (package main's lcppModels: a
// member's own connection first, else the deployment's "llm" engines). The engine catalog IS
// lcpp's model list (ADR 0093 decision 7), and only package main knows the Control Plane, so it
// sets this at startup, the same seam shape as harness.EngineToken. nil means no catalog.
var EngineModels func(ctx context.Context) []agents.ModelChoice

// ListModels is agents.ModelLister over EngineModels, unshaped.
func (agentImpl) ListModels(q agents.ModelQuery) agents.ModelListing {
	var list []agents.ModelChoice
	if EngineModels != nil {
		list = EngineModels(q.Ctx)
	}
	return agents.ModelListing{Models: list, Enumerated: -1}
}

// SkillsLayout is agents.SkillsLayouter: lcpp drives no CLI, so skills are foreign only (ADR 0093
// decision 5), the same set harness.SystemPrompt's foreignSkillsPrompt reads. Its file tools
// refuse any path outside the session's CWD (harness resolvePath), hence FenceToCWD.
func (agentImpl) SkillsLayout() agents.SkillsLayout {
	return agents.SkillsLayout{Native: agents.SkillsForeignOnly, FenceToCWD: true}
}
