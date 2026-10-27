package opencode

import (
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

var _ agents.Instructions = agentImpl{}

// InstructionsFile is the AF-owned body opencode 1's config points at. It lives under
// ~/.config/agent-fleet so the only writer is af (opencode just reads it).
func InstructionsFile() string {
	return filepath.Join(paths.AgentConfigDir(), "instructions", "opencode.md")
}

// Instructions hands the agent this package's instruction functions (agents.Instructions).
func (agentImpl) Instructions() agents.InstructionsOps {
	return agents.InstructionsOps{
		Shape: func() agents.InstructionsShape {
			if UserInstructionsInAgentsMD() {
				// The preview still reads the AF-owned file, not the AGENTS.md block.
				return agents.InstructionsShape{Delivery: agents.DeliveryCompose, Measure: agents.MeasureBlock,
					Path: AgentsPath(), PreviewPath: InstructionsFile()}
			}
			return agents.InstructionsShape{Delivery: agents.DeliveryConfig, Measure: agents.MeasureConfig, Path: InstructionsFile()}
		},
		Fleet:     ApplyFleetNotes,
		SkillsDir: func() string { return filepath.Join(paths.OpencodeConfigDir(), "skills") },
		User:      func(body string) error { return ApplyUserInstructions(InstructionsFile(), body) },
		Memory:    ApplyMemoryGuide,
	}
}
