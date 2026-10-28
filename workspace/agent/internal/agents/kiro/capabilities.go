package kiro

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

// Optional agents capabilities of the kiro kind, each delegating to this package's own function.

// ListModels is agents.ModelLister: Models(), unshaped — `kiro-cli chat --list-models -f json`
// (fully machine-readable, tied to the account — docs/log/43).
func (agentImpl) ListModels(agents.ModelQuery) agents.ModelListing {
	return agents.ModelListing{Models: Models(), Enumerated: -1}
}
