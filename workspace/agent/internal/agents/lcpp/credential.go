package lcpp

import (
	"context"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var (
	_ agents.CredentialResolver = agentImpl{}
	_ agents.OneRouteResolver   = agentImpl{}
	_ agents.PerCallRouter      = agentImpl{}
)

// RoutesPerCall implements agents.PerCallRouter: every call resolves its connection anew
// (harness.EngineToken), so the member's endpoint or key can change between two calls of one
// session, and each call records what served it.
func (agentImpl) RoutesPerCall() {}

// memberConn is the member's own llama.cpp connection, ok=false without one. A seam for tests.
var memberConn = func() (secrets.LcppConn, bool) {
	s, err := secrets.Load()
	if err != nil || s.Lcpp == nil || strings.TrimSpace(s.Lcpp.URL) == "" {
		return secrets.LcppConn{}, false
	}
	return *s.Lcpp, true
}

// Connection is what an lcpp call runs on now (ADR 0138 D2, D5b), by the rule the engine seam
// applies (package main's harnessEngineToken): the member's own connection wins over the
// deployment's engine. The member route binds to the endpoint and key together, so repointing
// the URL or replacing the key is another connection; neither is recoverable from the binding.
func Connection() routes.Launch {
	if c, ok := memberConn(); ok {
		return routes.Launch{Route: routes.RouteMemberLlamacpp, Binding: credbind.Endpoint(routes.RouteMemberLlamacpp, c.URL, c.APIKey)}
	}
	// The engine's token is minted per session; the catalogue key is the stable part.
	return routes.Launch{Route: routes.RouteAFEngineLLM, Binding: credbind.Named(routes.RouteAFEngineLLM, engineKey)}
}

// ResolveLaunch implements agents.CredentialResolver.
func (agentImpl) ResolveLaunch(session.Meta) routes.Launch { return Connection() }

// ResolvesOneRoute implements agents.OneRouteResolver.
func (agentImpl) ResolvesOneRoute() {}

// UsableRoutes implements agents.CredentialResolver: the member's endpoint while it is set, else
// the deployment's chat engine while the engine catalogue (cached) has one.
func (agentImpl) UsableRoutes() []string {
	if _, ok := memberConn(); ok {
		return []string{routes.RouteMemberLlamacpp}
	}
	if harness.EngineAvailable != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if harness.EngineAvailable(ctx, engineKey) {
			return []string{routes.RouteAFEngineLLM}
		}
	}
	return nil
}
