package claude

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// Optional agents capabilities of the claude kind, each delegating to this package's own
// functions.

// ListModels is agents.ModelLister: the fixed tier aliases (Models) followed by the member's
// registered models the aliases do not already name, compared case-insensitively.
func (agentImpl) ListModels(q agents.ModelQuery) agents.ModelListing {
	list := Models()
	seen := make(map[string]bool, len(list))
	for _, model := range list {
		seen[strings.ToLower(model.ID)] = true
	}
	for _, id := range q.CustomModels {
		if key := strings.ToLower(id); !seen[key] {
			list = append(list, agents.ModelChoice{ID: id, Label: id, Efforts: EffortsFor(id)})
			seen[key] = true
		}
	}
	return agents.ModelListing{Models: list, Enumerated: -1}
}
