package muse

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

// Optional agents capabilities of the muse kind, each delegating to this package's own function.

// ListModels is agents.ModelLister: Models(), unshaped.
func (agentImpl) ListModels(agents.ModelQuery) agents.ModelListing {
	return agents.ModelListing{Models: Models(), Enumerated: -1}
}
