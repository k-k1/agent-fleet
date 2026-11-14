package cursor

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

var _ agents.FixedRouter = agentImpl{}

// FixedRoutes implements agents.FixedRouter (ADR 0138 D2). The Cursor CLI keeps its login by itself.
func (agentImpl) FixedRoutes() []string {
	return []string{routes.RouteCursorLogin}
}
