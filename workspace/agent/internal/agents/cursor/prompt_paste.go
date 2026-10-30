package cursor

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

var _ agents.PromptPaste = agentImpl{}

// PasteNeeded: every prompt is pasted (agents.PasteAlways).
func (agentImpl) PasteNeeded(text string) bool { return agents.PasteAlways(text) }
