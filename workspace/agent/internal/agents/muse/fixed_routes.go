package muse

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

var _ agents.FixedRouter = agentImpl{}

// FixedRoutes implements agents.FixedRouter (ADR 0138 D2). Muse's auth.json holds either the account login or a Meta API key, told apart by its mechanism; a META_API_KEY in the Agent's environment overrides the login.
func (agentImpl) FixedRoutes() []string {
	return []string{routes.RouteMuseLogin, routes.RouteMetaAPI}
}
