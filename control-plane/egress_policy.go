package main

import "strings"

// egressPolicy is the allowlist the egress proxy checks each destination against
// (docs/log/20 M2). Matching is case-insensitive. An entry starting with "." (or "*.",
// normalised to a leading ".") is a suffix match covering that domain and all its
// subdomains; any other entry is an exact host match.
type egressPolicy struct {
	exact  map[string]bool
	suffix []string // e.g. ".githubusercontent.com" (leading dot)
}

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
func newEgressPolicy(entries []string) *egressPolicy {
	p := &egressPolicy{exact: map[string]bool{}}
	for _, e := range entries {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" || strings.HasPrefix(e, "#") {
			continue
		}
		e = strings.TrimPrefix(e, "*") // "*.x" -> ".x"
		if strings.HasPrefix(e, ".") {
			p.suffix = append(p.suffix, e)
		} else {
			p.exact[e] = true
		}
	}
	return p
}

// allows reports whether host is permitted. The caller strips any port first.
func (p *egressPolicy) allows(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, ".")) // drop a trailing FQDN dot
	if p.exact[host] {
		return true
	}
	for _, s := range p.suffix {
		// ".github.com" matches both "github.com" and "x.github.com".
		if host == strings.TrimPrefix(s, ".") || strings.HasSuffix(host, s) {
			return true
		}
	}
	return false
}
