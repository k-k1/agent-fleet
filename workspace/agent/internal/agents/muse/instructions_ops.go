package muse

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

var _ agents.Instructions = agentImpl{}

// Instructions hands the agent this package's instruction functions (agents.Instructions).
func (agentImpl) Instructions() agents.InstructionsOps {
	return agents.InstructionsOps{
		Shape: func() agents.InstructionsShape {
			// One AGENTS.md shared with the fleet policy, so "applied" has to be the user-notes
			// block's presence: the file alone would also be there for the fleet block or the
			// member's own text (ADR 0095 decision 12).
			return agents.InstructionsShape{Delivery: agents.DeliveryCompose, Measure: agents.MeasureBlock, Path: AgentsPath()}
		},
		Fleet: ApplyFleetNotes,
		// No install step and no lock-file entry: a hand-dropped SKILL.md is listed by
		// `muse skills list --source user` (measured, gate B1-5).
		SkillsDir: SkillsDir,
		User:      ApplyUserInstructions,
		Memory:    ApplyMemoryGuide,
	}
}
