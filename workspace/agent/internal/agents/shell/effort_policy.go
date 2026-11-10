package shell

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

var _ agents.EffortPolicy = Agent{}

// EffortTraits is agents.EffortPolicy: a plain shell takes no effort.
func (Agent) EffortTraits() agents.EffortTraits {
	return agents.EffortTraits{Rule: agents.EffortNever}
}
