package muse

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

// Optional agents capabilities of the muse kind, each delegating to this package's own function.

// ListModels is agents.ModelLister: Models(), unshaped — `model/list` over the session protocol
// (ADR 0095 decision 10): muse has no `models` subcommand, so the catalog is a wire query against
// a host. Empty until the binary is installed AND a credential is stored.
func (agentImpl) ListModels(agents.ModelQuery) agents.ModelListing {
	return agents.ModelListing{Models: Models(), Enumerated: -1}
}
