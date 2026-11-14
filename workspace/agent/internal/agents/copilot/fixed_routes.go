package copilot

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

var _ agents.FixedRouter = agentImpl{}

// FixedRoutes implements agents.FixedRouter (ADR 0138 D2). Copilot runs on the member's GitHub connection, whose model plan is GitHub's.
func (agentImpl) FixedRoutes() []string {
	return []string{routes.RouteCopilotLogin}
}
