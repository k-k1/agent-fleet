package agy

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

var _ agents.FixedRouter = agentImpl{}

// FixedRoutes implements agents.FixedRouter (ADR 0138 D2). The Antigravity CLI keeps its Google login by itself; nothing else can use it.
func (agentImpl) FixedRoutes() []string {
	return []string{routes.RouteAgyLogin}
}
