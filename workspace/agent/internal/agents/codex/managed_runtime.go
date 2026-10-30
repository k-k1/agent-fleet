package codex

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

var _ agents.ManagedRuntime = agentImpl{}

// ManagedRuntime hands sessionx this package's handle functions (agents.ManagedRuntime).
func (agentImpl) ManagedRuntime() agents.ManagedRuntimeOps {
	return agents.ManagedRuntimeOps{
		Alive:        ManagedAlive,
		Busy:         ManagedBusy,
		InFlight:     ManagedInFlight,
		Drop:         DropHandle,
		RemoveLedger: RemoveLedger,
	}
}
