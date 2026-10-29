package sessionx

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

// liveStateTraits is kind's agents.LiveStateTraits by exact registry key: zero for a kind
// without them and for a spelling that is no kind. No kind with live-state traits is reached
// through the claude fallback, so an empty or legacy kind gets none.
func liveStateTraits(kind string) agents.LiveStateTraits {
	a, _ := RegisteredAgent(kind)
	if t, ok := a.(agents.LiveStater); ok {
		return t.LiveStateTraits()
	}
	return agents.LiveStateTraits{}
}
