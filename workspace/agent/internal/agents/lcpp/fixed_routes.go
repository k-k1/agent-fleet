package lcpp

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

var _ agents.FixedRouter = agentImpl{}

// FixedRoutes implements agents.FixedRouter (ADR 0138 D2). The member's own llama.cpp connection, when set, wins over the deployment's engine.
func (agentImpl) FixedRoutes() []string {
	return []string{routes.RouteMemberLlamacpp, routes.RouteAFEngineLLM}
}
