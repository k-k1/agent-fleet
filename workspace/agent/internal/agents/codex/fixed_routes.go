package codex

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

var _ agents.FixedRouter = agentImpl{}

// FixedRoutes implements agents.FixedRouter (ADR 0138 D2). codex's auth.json holds either the ChatGPT login or an API-key login, told apart by auth_mode.
func (agentImpl) FixedRoutes() []string {
	return []string{routes.RouteChatGPTSubscription, routes.RouteOpenAIAPI}
}
