package ssm

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

var _ agents.EffortPolicy = Agent{}

// EffortTraits is agents.EffortPolicy: an SSM host takes no effort.
func (Agent) EffortTraits() agents.EffortTraits {
	return agents.EffortTraits{Rule: agents.EffortNever}
}
