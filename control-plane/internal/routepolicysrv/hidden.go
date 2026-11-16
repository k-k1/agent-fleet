package routepolicysrv

import (
	"slices"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/routepolicy"
)

// What the effective policy hides from a tenant's members, for its administrators (ADR 0138
// D10a, D16): the harnesses, assistant chat backends and image providers none of whose routes the
// layers allow, each with the layers that forbid its routes. This is where "why can't my members
// see X" is answered; the members' own screens never name a layer or a hidden item.
//
// It covers the administered layers only. A member's own switches (the member layer) live in
// their Workspace and are not visible here.

// hiddenWire is the tenant view's hidden section.
type hiddenWire struct {
	Kinds    []hiddenItemWire `json:"kinds"`
	Chat     []hiddenItemWire `json:"chat"`
	Imagegen []hiddenItemWire `json:"imagegen"`
}

// hiddenItemWire is one hidden harness, chat backend or image provider: its routes, and the
// layers that forbid them, in the layers' own order (ceiling, deployment, tenant_cap, tenant).
type hiddenItemWire struct {
	ID          string   `json:"id"`
	Routes      []string `json:"routes"`
	ForbiddenBy []string `json:"forbiddenBy"`
}

// layerOrder is the order forbiddenBy lists layers in.
var layerOrder = []string{routepolicy.LayerCeiling, routepolicy.LayerDeployment, routepolicy.LayerTenantCap, routepolicy.LayerTenant}

// hiddenOf folds the route decisions into what they hide. A per-connection route the layers allow
// on an approved endpoint (endpointRoutes, with at least one approved host) counts as allowed: a
// member whose endpoint is approved can use it, so its harness is not hidden from the tenant.
func hiddenOf(layers routepolicy.Layers, endpointRoutes []string, approvedHosts int) hiddenWire {
	allowed := func(r string) bool {
		return len(layers.ForbiddenBy(r)) == 0 || approvedHosts > 0 && slices.Contains(endpointRoutes, r)
	}
	fold := func(ids []string, routesOf func(string) []string) []hiddenItemWire {
		out := []hiddenItemWire{}
		for _, id := range ids {
			rs := routesOf(id)
			if len(rs) == 0 || slices.ContainsFunc(rs, allowed) {
				continue
			}
			by := map[string]bool{}
			for _, r := range rs {
				for _, l := range layers.ForbiddenBy(r) {
					by[l] = true
				}
			}
			item := hiddenItemWire{ID: id, Routes: rs, ForbiddenBy: []string{}}
			for _, l := range layerOrder {
				if by[l] {
					item.ForbiddenBy = append(item.ForbiddenBy, l)
				}
			}
			out = append(out, item)
		}
		return out
	}
	kindRoutes := func(k string) []string {
		rs := append(agentroutes.RoutesOf(k), agentroutes.FixedRoutesOf(k)...)
		slices.Sort(rs)
		return slices.Compact(rs)
	}
	return hiddenWire{
		Kinds:    fold(agentroutes.ModelKinds(), kindRoutes),
		Chat:     fold(agentroutes.ChatBackends(), agentroutes.ChatRoutesOf),
		Imagegen: fold(agentroutes.ImagegenProviders(), agentroutes.ImagegenRoutesOf),
	}
}

// HiddenKinds is the set of harnesses the tenant's effective policy hides altogether, for the
// hosted MCP tool texts (#4241). Empty when nothing is restricted.
func (v tenantPolicyWire) HiddenKinds() map[string]bool {
	out := map[string]bool{}
	for _, k := range v.Hidden.Kinds {
		out[k.ID] = true
	}
	return out
}
