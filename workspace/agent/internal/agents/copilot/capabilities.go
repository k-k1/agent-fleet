package copilot

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

// Optional agents capabilities of the copilot kind, each delegating to this package's own function.

// ListModels is agents.ModelLister: Models(), unshaped — a PTY scrape of the TUI /model picker
// (live, so it reflects the plan — docs/log/36 addendum; Free offers only Auto, i.e. an empty
// list). Unspecified means auto routing.
func (agentImpl) ListModels(agents.ModelQuery) agents.ModelListing {
	return agents.ModelListing{Models: Models(), Enumerated: -1}
}
