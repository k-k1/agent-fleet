package claude

import (
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

var _ agents.Instructions = agentImpl{}

// Instructions hands the agent this package's instruction functions (agents.Instructions).
func (agentImpl) Instructions() agents.InstructionsOps {
	return agents.InstructionsOps{
		Shape: func() agents.InstructionsShape {
			return agents.InstructionsShape{Delivery: agents.DeliveryFile, Measure: agents.MeasureBlock, Path: UserInstructionsPath()}
		},
		// No Fleet: the policy is baked into the image as claude's managed policy.
		SkillsDir: func() string { return filepath.Join(paths.ClaudeConfigDir(), "skills") },
		User:      ApplyUserInstructions,
		Memory:    ApplyMemoryGuide,
	}
}
