package agy

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

var _ agents.Instructions = agentImpl{}

// Instructions hands the agent this package's instruction functions (agents.Instructions).
func (agentImpl) Instructions() agents.InstructionsOps {
	return agents.InstructionsOps{
		Shape: func() agents.InstructionsShape {
			return agents.InstructionsShape{Delivery: agents.DeliveryCompose, Measure: agents.MeasureBlock, Path: AgentsPath()}
		},
		Fleet:  ApplyFleetNotes,
		User:   ApplyUserInstructions,
		Memory: ApplyMemoryGuide,
	}
}
