package muse

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

var _ agents.CreatePolicy = agentImpl{}

// CreateTraits is agents.CreatePolicy: muse has a contributor mode the member opts into
// (ui-prefs museContributorMode, #2535).
func (agentImpl) CreateTraits() agents.CreateTraits {
	return agents.CreateTraits{ContributorModePref: "museContributorMode"}
}
