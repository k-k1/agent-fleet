package agy

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Optional agents capabilities of the agy kind, each delegating to this package's own function.

// ListModels is agents.ModelLister: Models(), unshaped.
func (agentImpl) ListModels(agents.ModelQuery) agents.ModelListing {
	return agents.ModelListing{Models: Models(), Enumerated: -1}
}

// PaneMode is agents.PaneModeReader: this package's PaneMode.
func (agentImpl) PaneMode(pane string) string { return PaneMode(pane) }

// SendWaitsForComposer is agents.PaneModeReader: the "Signing in..." boot screen eats typed
// text entirely (docs/log/32); the composer footer is persistent once drawn.
func (agentImpl) SendWaitsForComposer() bool { return true }

// PromptModal is agents.ModalProbe: the conversation DB's last step (pending.go). It reports
// ONLY the blocking states ("question" / "permission"), so "" means nothing is pending.
func (agentImpl) PromptModal(m session.Meta) string {
	st, _ := Probe(m)
	return st
}

// SkillsLayout is agents.SkillsLayouter: no user-invocable mechanism confirmed yet (docs/log/50
// §7), so the other conventions' SKILL.md trees are offered as foreign entries only.
func (agentImpl) SkillsLayout() agents.SkillsLayout {
	return agents.SkillsLayout{Native: agents.SkillsForeignOnly}
}
