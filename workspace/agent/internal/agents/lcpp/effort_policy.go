package lcpp

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

var _ agents.EffortPolicy = agentImpl{}

// EffortTraits is agents.EffortPolicy: the reasoning amount is fixed by the engine's launch settings.
func (agentImpl) EffortTraits() agents.EffortTraits {
	return agents.EffortTraits{Rule: agents.EffortNever, Reason: "推論量はエンジン側の起動設定で決まります"}
}
