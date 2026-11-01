package muse

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

// MoveTarget is agents.MoveTarget: a move may target muse (reads the handover file outside its
// working folder without a prompt; ADR 0115 decision 9). The answer holds for both of its drivers.
func (agentImpl) MoveTarget() agents.MoveTargetState { return agents.MoveTargetMeasured }
