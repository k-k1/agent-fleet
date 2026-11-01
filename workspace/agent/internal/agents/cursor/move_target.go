package cursor

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

// MoveTarget is agents.MoveTarget: unmeasured. The bare CLI's read of a file outside the working
// folder does not cover the driver's trust handling and flags, so moves stay off until it has been
// measured through the real driver (ADR 0115, 2026-10-20 addendum).
func (agentImpl) MoveTarget() agents.MoveTargetState { return agents.MoveTargetUnmeasured }
