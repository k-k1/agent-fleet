package kiro

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

var _ agents.Instructions = agentImpl{}

// Instructions hands the agent this package's instruction functions (agents.Instructions).
func (agentImpl) Instructions() agents.InstructionsOps {
	return agents.InstructionsOps{
		Shape: func() agents.InstructionsShape {
			return agents.InstructionsShape{Delivery: agents.DeliveryFile, Measure: agents.MeasureFile, Path: UserInstructionsPath()}
		},
		Fleet:  ApplyFleetNotes,
		User:   ApplyUserInstructions,
		Memory: ApplyMemoryGuide,
	}
}
