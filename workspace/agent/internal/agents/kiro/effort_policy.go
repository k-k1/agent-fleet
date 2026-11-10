package kiro

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

var _ agents.EffortPolicy = agentImpl{}

// EffortTraits is agents.EffortPolicy: the catalog lists no efforts yet the CLI takes one (--effort); the CLI judges.
func (agentImpl) EffortTraits() agents.EffortTraits {
	return agents.EffortTraits{Rule: agents.EffortCLIJudges}
}
