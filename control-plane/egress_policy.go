package main

import (
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/egressallow"
)

// egressPolicy is the allowlist the egress proxy checks each destination against
// (docs/log/20 M2). The matcher lives in internal/egressallow because the gateway agent
// reports its verdicts with the same one.
type egressPolicy = egressallow.Policy

// defaultEgressAllowlist is what the product itself needs (docs/log/20 §B.5): Anthropic/
// Claude (product-critical), the git hosts, and the common package registries —
// registries are included by decision (docs/log/20 §E.1). Deployments tune it via
// AF_EGRESS_ALLOWLIST (a file, one host per line, "#" comments).
var defaultEgressAllowlist = []string{
	// Anthropic / Claude — product-critical, always allowed.
	"api.anthropic.com", ".anthropic.com", "claude.ai", ".claude.ai",
	// git hosts.
	"github.com", ".github.com", ".githubusercontent.com",
	"bitbucket.org", ".bitbucket.org", ".atlassian.com",
	// package registries (decision: included).
	"registry.npmjs.org", ".npmjs.org",
	"pypi.org", "files.pythonhosted.org", ".pythonhosted.org",
	"proxy.golang.org", "sum.golang.org", ".golang.org", "go.dev",
	".debian.org", ".ubuntu.com",
	// aws cli / tooling. `.api.aws` is a SEPARATE apex from `.amazonaws.com` and is
	// where the AWS MCP Server lives (aws-mcp.<region>.api.aws — docs/log/25 §AWS MCP);
	// without it that builtin integration dies the day a deployment flips to enforce.
	".amazonaws.com", ".api.aws",
}

// newEgressPolicy builds a policy from a list of entries (blank lines and "#"
// comments ignored). Passing nil yields an empty (deny-all) policy.
func newEgressPolicy(entries []string) *egressPolicy { return egressallow.New(entries) }

// cpEgressHosts are the hosts a workspace reaches this Control Plane by: those of
// PUBLIC_BASE_URL (AF_CP_BASE_URL, also the internal git host) and AF_CP_INTERNAL_URL.
// effectivePolicy always admits them, outside the admin allowlist, so no edit there can
// lock workspaces out of their own CP. A workspace's requests to the public base pass the
// egress gateway, whose intercept sees port 443 whatever NO_PROXY says.
//
// The allowlist is by host: every port and path of these hosts is admitted, so the CP
// should have a hostname of its own rather than share one with another service.
func (m *manager) cpEgressHosts() []string {
	hosts, _ := cpEgressHostsOf(m.publicBaseURL, m.internalBaseURL)
	return hosts
}

// cpHostRe is one exact host name or IPv4 address. A leading "." or "*" would make
// egressallow and Squid read the entry as a suffix and open every sibling for good.
var cpHostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// cpEgressHostsOf is cpEgressHosts for the given URLs, plus each set URL it refused: not
// http(s), or a host that is not one exact name. An IPv6 literal is refused too, because
// the gateway agent's allow file cannot carry one (#2754) and listing it as admitted
// would be false. When in doubt, refuse: a refused host is only as unreachable as it was
// before the built-in entries existed.
func cpEgressHostsOf(urls ...string) (hosts, refused []string) {
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		h := ""
		if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			h = strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
		}
		if !cpHostRe.MatchString(h) {
			refused = append(refused, raw)
			continue
		}
		if !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	return hosts, refused
}

// egressBuiltin is every entry the admin allowlist cannot remove: the product defaults and
// the CP's own hosts.
func (m *manager) egressBuiltin() []string {
	return append(slices.Clip(defaultEgressAllowlist), m.cpEgressHosts()...)
}
