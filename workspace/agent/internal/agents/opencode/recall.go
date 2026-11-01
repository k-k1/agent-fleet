package opencode

// Reading back the agent (build/plan) and model a conversation switched to in the TUI, so
// a resume does not revert it (#987).

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// RecallSettings reads the model and agent the conversation's own record names. The model is
// recalled so the meta, and a fork or recreate built from it, follow the conversation. A switch
// made in the TUI with no prompt after it may not be recorded at all.
func (agentImpl) RecallSettings(m session.Meta) agents.RecalledSettings {
	return recallSettingsV2(v2TargetFor(m), m) // the session record names its model and agent
}
