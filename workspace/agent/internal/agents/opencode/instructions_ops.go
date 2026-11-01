package opencode

import (
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

var _ agents.Instructions = agentImpl{}

// InstructionsFile is where opencode 1 read the member's instructions from (its config pointed at
// it). opencode 2 does not apply that array, so the file is only ever removed now
// (ApplyUserInstructions).
func InstructionsFile() string {
	return filepath.Join(paths.AgentConfigDir(), "instructions", "opencode.md")
}

// Instructions hands the agent this package's instruction functions (agents.Instructions).
func (agentImpl) Instructions() agents.InstructionsOps {
	return agents.InstructionsOps{
		Shape: func() agents.InstructionsShape {
			// The global AGENTS.md also holds the fleet and memory blocks, so the preview shows the
			// member's block only.
			return agents.InstructionsShape{Delivery: agents.DeliveryCompose, Measure: agents.MeasureBlock,
				Path: AgentsPath(), PreviewBlock: "user-notes"}
		},
		Fleet:     ApplyFleetNotes,
		SkillsDir: func() string { return filepath.Join(paths.OpencodeConfigDir(), "skills") },
		User:      func(body string) error { return ApplyUserInstructions(InstructionsFile(), body) },
		Memory:    ApplyMemoryGuide,
	}
}
