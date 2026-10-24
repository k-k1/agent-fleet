package main

import "github.com/k-k1/agent-fleet/control-plane/internal/egressallow"

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
