package lcpp

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

var _ agents.Instructions = agentImpl{}

// Instructions: lcpp writes its own system prompt, so there is nothing to apply. The harness
// re-reads the saved state at the start of every turn, which is why the row is the switch and
// "applied" is always true — running sessions included.
func (agentImpl) Instructions() agents.InstructionsOps {
	return agents.InstructionsOps{
		Shape: func() agents.InstructionsShape {
			return agents.InstructionsShape{Delivery: agents.DeliveryPrompt, Measure: agents.MeasurePrompt}
		},
	}
}
