// Package routepolicy is the credential policy of ADR 0138: which model routes a member may use,
// written by the operator (the ceiling, D9), the super_admin (the deployment rule and each
// tenant's cap) and the tenant_admin (the tenant's own rule), and evaluated on route ids through
// each id's class and destination (D1c).
//
// A layer is a Rule. Each layer's allowed set is (the routes whose class and destination it
// allows, plus its Except) minus its Deny, and the effective set is the intersection of the
// layers' sets (D1d, D7a): a layer can only remove, and an exception reopens only what the same
// layer closed. A Rule with no restriction allows every route, which is the default at every
// layer (D8).
package routepolicy

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentroutes"
)

// Rule is one layer's rule. Classes and Destinations nil mean every class and every destination;
// an empty list allows none. Except and Deny are route ids.
type Rule struct {
	Classes      *[]string `json:"classes,omitempty"`
	Destinations *[]string `json:"destinations,omitempty"`
	Except       []string  `json:"except,omitempty"`
	Deny         []string  `json:"deny,omitempty"`
}

// Restricted reports that the rule forbids anything at all. Except alone restricts nothing.
func (r Rule) Restricted() bool {
	return r.Classes != nil || r.Destinations != nil || len(r.Deny) > 0
}

// Allows reports whether the rule allows the route id. A route id the catalogue does not know
// is allowed only by a rule that restricts nothing: under any restriction it is unclassified and
// refused (fail closed, D1c).
func (r Rule) Allows(routeID string) bool {
	if !r.Restricted() {
		return true
	}
	if slices.Contains(r.Deny, routeID) {
		return false
	}
	if slices.Contains(r.Except, routeID) {
		return true
	}
	rt, ok := agentroutes.Lookup(routeID)
	if !ok {
		return false
	}
	return (r.Classes == nil || slices.Contains(*r.Classes, rt.Class)) &&
		(r.Destinations == nil || slices.Contains(*r.Destinations, rt.Destination))
}

// Classes and Destinations are the vocabulary, taken from the route catalogue the CP embeds.
func Classes() []string      { return vocab(func(r agentroutes.Route) string { return r.Class }) }
func Destinations() []string { return vocab(func(r agentroutes.Route) string { return r.Destination }) }

func vocab(f func(agentroutes.Route) string) []string {
	var out []string
	for _, r := range agentroutes.Routes() {
		if v := f(r); v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// Validate checks a rule's vocabulary: known classes and destinations, catalogued route ids in
// Except and Deny, no per-connection route in Except (its class and destination come from its
// endpoint, which endpoint approval governs, D1d), and no id in both lists of one layer is
// allowed (deny wins, so it is accepted as written).
func (r Rule) Validate() error {
	check := func(what string, list *[]string, known []string) error {
		if list == nil {
			return nil
		}
		for _, v := range *list {
			if !slices.Contains(known, v) {
				return fmt.Errorf("unknown %s %q; known: %s", what, v, strings.Join(known, ", "))
			}
		}
		return nil
	}
	if err := check("class", r.Classes, Classes()); err != nil {
		return err
	}
	if err := check("destination", r.Destinations, Destinations()); err != nil {
		return err
	}
	for _, id := range append(slices.Clone(r.Except), r.Deny...) {
		if _, ok := agentroutes.Lookup(id); !ok {
			return fmt.Errorf("unknown route id %q", id)
		}
	}
	for _, id := range r.Except {
		if rt, _ := agentroutes.Lookup(id); rt.PerConnection {
			return fmt.Errorf("route %q is per-connection and cannot be excepted: its destination comes from its endpoint", id)
		}
	}
	return nil
}

// Normalize sorts and de-duplicates every list, so equal rules serialise equally.
func (r Rule) Normalize() Rule {
	norm := func(l []string) []string {
		if len(l) == 0 {
			return nil
		}
		out := slices.Clone(l)
		sort.Strings(out)
		return slices.Compact(out)
	}
	out := Rule{Except: norm(r.Except), Deny: norm(r.Deny)}
	if r.Classes != nil {
		c := norm(*r.Classes)
		if c == nil {
			c = []string{}
		}
		out.Classes = &c
	}
	if r.Destinations != nil {
		d := norm(*r.Destinations)
		if d == nil {
			d = []string{}
		}
		out.Destinations = &d
	}
	return out
}

// Exceeds lists the catalogued route ids r allows that over does not: a write that would widen
// the layer above it (a super_admin's rule under the operator ceiling, §8 M1).
func (r Rule) Exceeds(over Rule) []string {
	var out []string
	for _, rt := range agentroutes.Routes() {
		if r.Allows(rt.ID) && !over.Allows(rt.ID) {
			out = append(out, rt.ID)
		}
	}
	return out
}

// Layer names, in order from the top.
const (
	LayerCeiling    = "ceiling"
	LayerDeployment = "deployment"
	LayerTenantCap  = "tenant_cap"
	LayerTenant     = "tenant"
)

// Layers is the set of rules one tenant's members are judged by.
type Layers struct {
	Ceiling, Deployment, TenantCap, Tenant Rule
}

func (l Layers) each() [][2]any {
	return [][2]any{{LayerCeiling, l.Ceiling}, {LayerDeployment, l.Deployment}, {LayerTenantCap, l.TenantCap}, {LayerTenant, l.Tenant}}
}

// Restricted reports whether any layer restricts anything.
func (l Layers) Restricted() bool {
	for _, e := range l.each() {
		if e[1].(Rule).Restricted() {
			return true
		}
	}
	return false
}

// Allows is the effective decision for a route id: every layer must allow it.
func (l Layers) Allows(routeID string) bool { return len(l.ForbiddenBy(routeID)) == 0 }

// ForbiddenBy names the layers that forbid the route id, top first; none when it is allowed.
func (l Layers) ForbiddenBy(routeID string) []string {
	var out []string
	for _, e := range l.each() {
		if !e[1].(Rule).Allows(routeID) {
			out = append(out, e[0].(string))
		}
	}
	return out
}
