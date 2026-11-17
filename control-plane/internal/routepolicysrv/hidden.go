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

// ownerItemWire is one harness, chat backend or image provider with the routes it can use and the
// subset of them the layers allow, for the "what can members use" view. Invariant: an item is in
// hidden if and only if its allowed list is empty (both are folded from the same decision).
type ownerItemWire struct {
	ID      string   `json:"id"`
	Routes  []string `json:"routes"`
	Allowed []string `json:"allowed"`
}

// ownersWire is the tenant view's owners section: every owner of routes and what is allowed of
// them. Additive to hidden, which stays what the MCP tool texts read (#4241).
type ownersWire struct {
	Kinds    []ownerItemWire `json:"kinds"`
	Chat     []ownerItemWire `json:"chat"`
	Imagegen []ownerItemWire `json:"imagegen"`
}

// foldOwners is the one decision behind hidden and owners. A per-connection route the layers allow
// on an approved endpoint (endpointRoutes, with at least one approved host) counts as allowed: a
// member whose endpoint is approved can use it, so its harness is not hidden from the tenant.
// Owners with no routes at all appear in neither.
func foldOwners(layers routepolicy.Layers, endpointRoutes []string, approvedHosts int) (owners ownersWire, hidden hiddenWire) {
	allowed := func(r string) bool {
		return len(layers.ForbiddenBy(r)) == 0 || approvedHosts > 0 && slices.Contains(endpointRoutes, r)
	}
	fold := func(ids []string, routesOf func(string) []string) ([]ownerItemWire, []hiddenItemWire) {
		os, hs := []ownerItemWire{}, []hiddenItemWire{}
		for _, id := range ids {
			rs := routesOf(id)
			if len(rs) == 0 { // defensive: no catalog entry is routeless today
				continue
			}
			ok := []string{}
			for _, r := range rs {
				if allowed(r) {
					ok = append(ok, r)
				}
			}
			os = append(os, ownerItemWire{ID: id, Routes: rs, Allowed: ok})
			if len(ok) > 0 {
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
			hs = append(hs, item)
		}
		return os, hs
	}
	kindRoutes := func(k string) []string {
		rs := append(agentroutes.RoutesOf(k), agentroutes.FixedRoutesOf(k)...)
		slices.Sort(rs)
		return slices.Compact(rs)
	}
	owners.Kinds, hidden.Kinds = fold(agentroutes.ModelKinds(), kindRoutes)
	owners.Chat, hidden.Chat = fold(agentroutes.ChatBackends(), agentroutes.ChatRoutesOf)
	owners.Imagegen, hidden.Imagegen = fold(agentroutes.ImagegenProviders(), agentroutes.ImagegenRoutesOf)
	return owners, hidden
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

// AllowedRoutes is, per harness, the routes the tenant's effective policy allows, for the hosted
// MCP create_session text (#4323). Every harness with routes has an entry, empty when none is
// allowed.
func (v tenantPolicyWire) AllowedRoutes() map[string][]string {
	out := map[string][]string{}
	for _, k := range v.Owners.Kinds {
		out[k.ID] = k.Allowed
	}
	return out
}
