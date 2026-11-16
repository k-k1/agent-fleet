package routepolicy

import (
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// Approved self-hosted endpoints (ADR 0138 §2). A per-connection route (a member's llama.cpp)
// reads as unclassified, so the CP never lists it in Allowed; under a restriction it is allowed
// only while its connection points at a host the policy approves (Policy.Endpoints) and the route
// is one the layers allow there (Policy.EndpointRoutes). The connection's binding covers its URL,
// so a session launched on one endpoint and resumed after the URL changed is a replacement (D4),
// refused like any other.

var (
	endpointMu    sync.RWMutex
	endpointHosts = map[string]func() string{}
)

// RegisterEndpoint names how to read the host a per-connection route's connection points at now:
// "" when it has none. Register at init. A route with none registered is never on an approved
// endpoint.
func RegisterEndpoint(route string, host func() string) {
	endpointMu.Lock()
	defer endpointMu.Unlock()
	endpointHosts[route] = host
}

// AllowsAt reports whether the policy allows route on a connection to host: the check for a
// connection about to be stored, before it is the current one.
func (p Policy) AllowsAt(route, host string) bool {
	return !p.Restricted || slices.Contains(p.Allowed, route) || p.approvedAt(route, host)
}

// onApprovedEndpoint reports whether route's connection points at an approved host now. The
// connection is read only for a route the policy allows on an approved endpoint.
func (p Policy) onApprovedEndpoint(route string) bool {
	if len(p.Endpoints) == 0 || !slices.Contains(p.EndpointRoutes, route) {
		return false
	}
	endpointMu.RLock()
	f := endpointHosts[route]
	endpointMu.RUnlock()
	return f != nil && p.approvedAt(route, f())
}

// AddableOnEndpoint reports whether a connection for route could be added on some host the policy
// approves: the route is one the layers allow on an approved endpoint, and there is one. It names
// no host, so a member's screen can offer the entry without learning which hosts are approved.
func (p Policy) AddableOnEndpoint(route string) bool {
	return len(p.Endpoints) > 0 && slices.Contains(p.EndpointRoutes, route)
}

func (p Policy) approvedAt(route, host string) bool {
	h := CanonicalHost(host)
	return h != "" && slices.Contains(p.EndpointRoutes, route) && slices.Contains(p.Endpoints, h)
}

// CanonicalHost is a host as the CP spells an approved one: lowercase, without a trailing dot, an
// IP address in its canonical text. The CP sends internationalised names in their punycode form,
// and a name entered in Unicode is not converted here, so it matches nothing (refused, never
// approved by mistake); the member enters the punycode form.
func CanonicalHost(h string) string {
	h = strings.TrimSpace(h)
	if b, ok := strings.CutPrefix(h, "["); ok {
		h, _ = strings.CutSuffix(b, "]")
	}
	if a, err := netip.ParseAddr(h); err == nil {
		if a.Zone() != "" {
			return ""
		}
		return a.Unmap().String()
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// HostOf is the host of a connection URL, "" when it has none.
func HostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}
