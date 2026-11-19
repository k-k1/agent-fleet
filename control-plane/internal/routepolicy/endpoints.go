package routepolicy

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentroutes"
)

// Approved self-hosted endpoints (ADR 0138 §2). A per-connection route (a member's llama.cpp, a
// custom provider) reads as unclassified, refused under any restriction, unless its connection's
// host is approved: it is then class self-hosted at destination self-hosted, judged by the layers'
// class and destination sets like any route, and its binding pins the host.
//
// Approval is an intersection like the policy (D7). Every layer's Endpoints is a list of host
// patterns: an exact host (an IPv4 or IPv6 literal counts as one), "*.suffix" for every host
// under it, or, above the tenant only, a CIDR range of IP literals (endpoints_cidr.go). The ceiling, the deployment rule and a tenant's cap name the patterns that may be
// approved (nil: this layer does not narrow; the ceiling's nil is "none" when it restricts
// destinations); the tenant rule names the exact hosts the tenant_admin approves. A host is
// approved when the tenant rule names it and every layer above lets it through. A host under a
// vendor destination's catalogued hosts is never approvable, at any layer.

// SelfHosted is the destination of an approved endpoint, and ClassSelfHosted its class.
const (
	SelfHosted      = "self-hosted"
	ClassSelfHosted = "self-hosted"
)

// NormalizePattern checks one host pattern and returns its canonical spelling: lowercase, without
// a trailing dot, an internationalised name in its ASCII (punycode) form, an IP address in its
// canonical text, a CIDR range masked. exact refuses a wildcard or a range (the tenant rule
// approves hosts, not patterns).
// Refused: a scheme, port or path; a bare "*" or a "*" anywhere but a leading "*."; a wildcard
// over an IP address; and a pattern on a public suffix ("*.com", "co.uk", "*.github.io"), which
// would approve hosts of unrelated owners.
func NormalizePattern(s string, exact bool) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", errors.New("an empty host")
	case s == "*" || s == "*.":
		return "", fmt.Errorf("%q would approve every host", s)
	}
	rest, wild := strings.CutPrefix(s, "*.")
	if wild && exact {
		return "", fmt.Errorf("%q is a pattern; approve exact hosts", s)
	}
	if strings.Contains(rest, "*") {
		return "", fmt.Errorf("%q: only a leading \"*.\" is a wildcard", s)
	}
	if strings.Contains(rest, "/") {
		if p, err := netip.ParsePrefix(rest); err == nil {
			switch {
			case wild:
				return "", fmt.Errorf("%q: a wildcard over an IP range", s)
			case exact:
				return "", fmt.Errorf("%q is a range; approve exact hosts", s)
			}
			n, err := normalizeCIDR(p)
			if err != nil {
				return "", fmt.Errorf("%q: %w", s, err)
			}
			return n, nil
		}
	}
	h, ip, err := normalizeHost(rest)
	if err != nil {
		return "", fmt.Errorf("%q: %w", s, err)
	}
	if ip {
		if wild {
			return "", fmt.Errorf("%q: a wildcard over an IP address", s)
		}
		return h, nil
	}
	if publicSuffix(h) {
		return "", fmt.Errorf("%q is on the public suffix %q, whose names belong to unrelated owners", s, h)
	}
	if wild {
		return "*." + h, nil
	}
	return h, nil
}

// normalizeHost is one host's canonical spelling, and whether it is an IP address.
func normalizeHost(s string) (string, bool, error) {
	if b, ok := strings.CutPrefix(s, "["); ok {
		s, _ = strings.CutSuffix(b, "]")
	}
	if a, err := netip.ParseAddr(s); err == nil {
		if a.Zone() != "" {
			return "", false, errors.New("an IPv6 zone is not a host")
		}
		return a.Unmap().String(), true, nil
	}
	if strings.ContainsAny(s, "/:@?#[] ") {
		return "", false, errors.New("a host only: no scheme, port or path")
	}
	s = strings.TrimSuffix(strings.ToLower(s), ".")
	a, err := idna.Lookup.ToASCII(s)
	if err != nil || a == "" {
		return "", false, fmt.Errorf("not a valid host name: %v", err)
	}
	return a, false, nil
}

// publicSuffix reports whether h is itself a public suffix: one the list names (an ICANN one such
// as "com" or "co.uk", or a private one such as "github.io"). A single label the list does not
// know ("internal", "lan") is a private name, not a suffix.
func publicSuffix(h string) bool {
	ps, icann := publicsuffix.PublicSuffix(h)
	return ps == h && (icann || strings.Contains(ps, "."))
}

// matches reports whether a host is under a pattern. Both are canonical. A range holds IP
// literals only.
func matches(pattern, host string) bool {
	if p, ok := cidrOf(pattern); ok {
		a, err := netip.ParseAddr(host)
		return err == nil && p.Contains(a.Unmap())
	}
	if sfx, ok := strings.CutPrefix(pattern, "*."); ok {
		return strings.HasSuffix(host, "."+sfx)
	}
	return pattern == host
}

// covers reports whether every host under inner is under outer. A range covers an IP literal or a
// range inside it, never a host name or a "*." pattern; nothing but a range covers a range.
func covers(outer, inner string) bool {
	if ip, ok := cidrOf(inner); ok {
		op, ok := cidrOf(outer)
		return ok && op.Bits() <= ip.Bits() && op.Contains(ip.Addr())
	}
	if i, ok := strings.CutPrefix(inner, "*."); ok {
		o, wild := strings.CutPrefix(outer, "*.")
		return wild && (i == o || strings.HasSuffix(i, "."+o))
	}
	return matches(outer, inner)
}

// overlap reports whether some host is under both patterns.
func overlap(a, b string) bool { return covers(a, b) || covers(b, a) }

// vendorOf names the vendor destination one of whose catalogued hosts p overlaps; "" for none.
func vendorOf(p string) string {
	for _, d := range agentroutes.Destinations() {
		for _, h := range d.Hosts {
			if overlap(p, h) {
				return d.ID
			}
		}
	}
	return ""
}

// validateEndpoints checks a layer's patterns: well-formed, and none on a vendor's hosts.
func validateEndpoints(list *[]string) error {
	if list == nil {
		return nil
	}
	for _, p := range *list {
		n, err := NormalizePattern(p, false)
		if err != nil {
			return fmt.Errorf("endpoint %w", err)
		}
		if v := vendorOf(n); v != "" {
			return fmt.Errorf("endpoint %q covers hosts of the vendor destination %s, which cannot be approved as self-hosted", p, v)
		}
	}
	return nil
}

// ExactEndpoints refuses a pattern in a rule whose endpoints are approved hosts (the tenant rule).
func (r Rule) ExactEndpoints() error {
	if r.Endpoints == nil {
		return nil
	}
	for _, p := range *r.Endpoints {
		if _, err := NormalizePattern(p, true); err != nil {
			return fmt.Errorf("endpoint %w", err)
		}
	}
	return nil
}

// endpointPatterns is what a layer above the tenant lets through: its patterns, or nil when it
// does not narrow. ceiling marks the operator's layer, whose unset list is "none" when it
// restricts destinations (ADR 0138 §2).
func (r Rule) endpointPatterns(ceiling bool) (patterns []string, narrows bool) {
	switch {
	case r.Endpoints != nil:
		return *r.Endpoints, true
	case ceiling && r.Destinations != nil:
		return nil, true
	}
	return nil, false
}

// passes reports whether a layer above the tenant lets the host through.
func (r Rule) passes(host string, ceiling bool) bool {
	ps, narrows := r.endpointPatterns(ceiling)
	return !narrows || slices.ContainsFunc(ps, func(p string) bool { return matches(p, host) })
}

// EndpointsExceed lists r's patterns that over (the operator ceiling, for a super_admin's write)
// does not let through in full: a write that would widen the layer above it.
func (r Rule) EndpointsExceed(over Rule) []string {
	ps, narrows := over.endpointPatterns(true)
	if r.Endpoints == nil || !narrows {
		return nil
	}
	var out []string
	for _, p := range *r.Endpoints {
		if !slices.ContainsFunc(ps, func(o string) bool { return covers(o, p) }) {
			out = append(out, p)
		}
	}
	return out
}

// allowsSelfHosted reports whether the rule allows a per-connection route on an approved
// endpoint: class self-hosted at destination self-hosted, unless the rule denies the route.
func (r Rule) allowsSelfHosted(routeID string) bool {
	if !r.Restricted() {
		return true
	}
	return !slices.Contains(r.Deny, routeID) &&
		(r.Classes == nil || slices.Contains(*r.Classes, ClassSelfHosted)) &&
		(r.Destinations == nil || slices.Contains(*r.Destinations, SelfHosted))
}

// EndpointExcludedBy names what keeps a host from being approved, top first: a layer above the
// tenant that does not let it through, or "vendor" when it is under a vendor's hosts. None when
// the layers above let it through.
func (l Layers) EndpointExcludedBy(host string) []string {
	var out []string
	if vendorOf(host) != "" {
		out = append(out, "vendor")
	}
	for _, e := range []struct {
		name    string
		r       Rule
		ceiling bool
	}{{LayerCeiling, l.Ceiling, true}, {LayerDeployment, l.Deployment, false}, {LayerTenantCap, l.TenantCap, false}} {
		if !e.r.passes(host, e.ceiling) {
			out = append(out, e.name)
		}
	}
	return out
}

// ApprovedEndpoints is the tenant rule's hosts that every layer above lets through.
func (l Layers) ApprovedEndpoints() []string {
	if l.Tenant.Endpoints == nil {
		return nil
	}
	var out []string
	for _, h := range *l.Tenant.Endpoints {
		if len(l.EndpointExcludedBy(h)) == 0 {
			out = append(out, h)
		}
	}
	return out
}

// EndpointRoutes is the per-connection routes every layer allows on an approved endpoint.
func (l Layers) EndpointRoutes() []string {
	var out []string
	for _, rt := range agentroutes.Routes() {
		if !rt.PerConnection {
			continue
		}
		ok := true
		for _, e := range l.each() {
			ok = ok && e[1].(Rule).allowsSelfHosted(rt.ID)
		}
		if ok {
			out = append(out, rt.ID)
		}
	}
	return out
}
