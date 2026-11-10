package agy

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

var _ agents.EffortPolicy = agentImpl{}

// EffortTraits is agents.EffortPolicy: the effort is part of the model id, so it is never given separately.
func (agentImpl) EffortTraits() agents.EffortTraits {
	return agents.EffortTraits{Rule: agents.EffortInModelID}
}
