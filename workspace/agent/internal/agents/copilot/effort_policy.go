package copilot

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

var _ agents.EffortPolicy = agentImpl{}

// EffortTraits is agents.EffortPolicy: an effort is checked against the catalog row, except that Auto rejects --effort (program.go drops it there).
func (agentImpl) EffortTraits() agents.EffortTraits {
	return agents.EffortTraits{Rule: agents.EffortByCatalog, AutoRejectsEffort: true}
}
