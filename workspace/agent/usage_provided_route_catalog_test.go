package main

import (
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// The provided ids are catalogued from Q0 and declared only from their phase: Q1 opens
// bedrock-deployment to claude (behind its own gate and the CP's check), and a create naming a
// route its harness does not declare is refused as any undeclared route is.
func TestProvidedRoutesAreDeclaredByPhase(t *testing.T) {
	for id, tier := range map[string]routes.Tier{
		usagex.RouteBedrockTenant:     routes.TierTenantKey,
		usagex.RouteBedrockDeployment: routes.TierDeployment,
	} {
		r, ok := routes.Lookup(id)
		if !ok || r.Wire != routes.WireBedrock || r.Credential != tier {
			t.Errorf("%s: %+v ok=%v, want wire bedrock and tier %s", id, r, ok, tier)
		}
		// Q1 opens bedrock-deployment to claude alone; bedrock-tenant waits for its phase.
		declared := id == usagex.RouteBedrockDeployment
		for _, k := range buildRouteCatalog().Kinds {
			if slices.Contains(k.Routes, id) != (declared && k.Kind == session.KindClaude) {
				t.Errorf("kind %q declares %s: %v (ADR 0129 D7)", k.Kind, id, slices.Contains(k.Routes, id))
			}
		}
		claude, _ := sessionxRoutesOf(session.KindClaude)
		code, err := routes.CheckStatic(session.KindClaude, &routes.Selection{Route: id}, claude)
		if declared && (code != "" || err != nil) || !declared && (code != routes.CodeRouteUnsupported || err == nil) {
			t.Errorf("claude on %s: code %q err %v", id, code, err)
		}
	}
}

// sessionxRoutesOf is the harness's declared allow-list as the snapshot carries it.
func sessionxRoutesOf(kind string) ([]string, bool) {
	for _, k := range buildRouteCatalog().Kinds {
		if k.Kind == kind {
			return k.Routes, true
		}
	}
	return nil, false
}
