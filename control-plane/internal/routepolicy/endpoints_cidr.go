package routepolicy

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// CIDR ranges in the endpoint patterns of the layers above the tenant (ADR 0138 §2, 2026-11-20
// note). A range names IP literals and nothing else: a host name never matches one, even when it
// resolves into the range, because approval compares the connection's host as written and nothing
// resolves it. The tenant rule still approves exact hosts only, so the Agent, which receives that
// list, compares text and needs no range logic.

// privateSpace is the only address space a range may lie inside. A blanket approval of anything
// else (a public range, loopback, link-local, multicast, "this network") is refused: one
// mistyped prefix there would approve hosts of unrelated owners or the node's own services.
var privateSpace = mustPrefixes("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7")

// metadataAddrs are instance-metadata endpoints inside privateSpace. 169.254.169.254 and the ECS
// task endpoint are link-local and already outside it.
var metadataAddrs = []netip.Addr{
	netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud
	netip.MustParseAddr("fd00:ec2::254"),   // AWS, IPv6
	netip.MustParseAddr("fd00:ec2::23"),    // AWS EKS Pod Identity Agent, IPv6
	netip.MustParseAddr("fd20:ce::254"),    // GCP Compute Engine metadata server, IPv6
}

// refusedAddr says why an exact IP can never be an approved endpoint, "" when it can: an
// instance-metadata address, or a link-local one (169.254.0.0/16 holds the common metadata and
// task-credential endpoints, fe80::/10 the IPv6 equivalent). Loopback is deliberately not here: a
// member's own llama.cpp is what an approval exists for, and it is approvable only where no layer
// above narrows endpoints without listing it (passes). An IPv4-mapped address is judged as its
// IPv4 address.
func refusedAddr(a netip.Addr) string {
	a = a.Unmap()
	if a.Is4() && a.As4()[0] == 169 && a.As4()[1] == 254 || a.Is6() && a.IsLinkLocalUnicast() {
		return "a link-local address"
	}
	if slices.Contains(metadataAddrs, a) {
		return "an instance-metadata address"
	}
	return ""
}

// numericLastLabel reports whether a host name ends in an all-digit or 0x-hex label. No top-level
// domain looks like that, and resolvers read such a name ("2852039166", "0xa9.254.169.254") as an
// IPv4 address in a spelling the canonical-IP check does not see.
func numericLastLabel(h string) bool {
	l := h[strings.LastIndex(h, ".")+1:]
	if rest, ok := strings.CutPrefix(l, "0x"); ok {
		l = rest
		return l == "" || strings.Trim(l, "0123456789abcdef") == ""
	}
	return l != "" && strings.Trim(l, "0123456789") == ""
}

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, v := range s {
		out[i] = netip.MustParsePrefix(v)
	}
	return out
}

// cidrOf parses a canonical range; ok is false for anything else, a range with host bits set
// included, so a malformed pattern matches nothing.
func cidrOf(s string) (netip.Prefix, bool) {
	if !strings.Contains(s, "/") {
		return netip.Prefix{}, false
	}
	p, err := netip.ParsePrefix(s)
	if err != nil || p.Addr().Is4In6() || p != p.Masked() || p.String() != s {
		return netip.Prefix{}, false
	}
	return p, true
}

// normalizeCIDR returns the canonical spelling of a range, or an error saying why it is refused.
func normalizeCIDR(p netip.Prefix) (string, error) {
	a := p.Addr()
	switch {
	case a.Zone() != "":
		return "", fmt.Errorf("an IPv6 zone is not a host")
	case a.Is4In6():
		return "", fmt.Errorf("an IPv4-mapped IPv6 range: write the IPv4 range")
	case p.Bits() == a.BitLen():
		return "", fmt.Errorf("a range of one address: write the address itself")
	case p != p.Masked():
		return "", fmt.Errorf("host bits are set: write %s", p.Masked())
	}
	inside := false
	for _, s := range privateSpace {
		inside = inside || (s.Bits() <= p.Bits() && s.Contains(a))
	}
	if !inside {
		return "", fmt.Errorf("a range must lie inside private address space (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, fc00::/7)")
	}
	for _, m := range metadataAddrs {
		if p.Contains(m) {
			return "", fmt.Errorf("the range contains the instance-metadata address %s", m)
		}
	}
	return p.String(), nil
}
