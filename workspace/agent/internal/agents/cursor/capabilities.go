package cursor

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

// Optional agents capabilities of the cursor kind, each delegating to this package's own function.

// ListModels is agents.ModelLister: Models(), unshaped — line-parsed from `cursor-agent models`
// (id - display name, tied to the account — docs/log/40).
func (agentImpl) ListModels(agents.ModelQuery) agents.ModelListing {
	return agents.ModelListing{Models: Models(), Enumerated: -1}
}
