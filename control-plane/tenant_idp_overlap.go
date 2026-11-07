package main

import (
	"os"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
)

// Where a claimed domain overlaps the deployment's own sign-in surface. The values are the
// wire contract of domainOverlap.Source; the Console maps each to a sentence.
const (
	overlapLoginDomains = "login_domains" // the deployment's own login allowlists (env, file, env providers)
	overlapSuperAdmin   = "super_admin"   // the domain of a SUPER_ADMIN_EMAILS address
)

// domainOverlap says that a domain a tenant-defined sign-in method claims is also one the
// deployment itself signs people in on. It is a warning for the super_admin who approves
// the row, not a refusal and not stored: the deployment's lists change without the row
// changing, so it is recomputed on every read.
type domainOverlap struct {
	Domain string `json:"domain"`
	Source string `json:"source"`
}

// deploymentDomains is a read of the deployment's own login domains, taken live so a
// file edit or a changed environment is seen without a restart.
type deploymentDomains func() map[string]map[string]bool // domain -> set of overlap sources

// newDeploymentDomains collects, per domain, where the deployment itself admits or
// elevates addresses: AF_OAUTH_ALLOWED_DOMAINS, "@domain" lines of the allowlist file, the
// own domain lists of env-configured providers, and the SUPER_ADMIN_EMAILS addresses.
func newDeploymentDomains(cfg config) deploymentDomains {
	return func() map[string]map[string]bool {
		out := map[string]map[string]bool{}
		add := func(domain, source string) {
			domain = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(domain, "@")))
			if domain == "" {
				return
			}
			if out[domain] == nil {
				out[domain] = map[string]bool{}
			}
			out[domain][source] = true
		}
		for d := range cfg.allowDomains {
			add(d, overlapLoginDomains)
		}
		if cfg.allowEmailsFile != "" {
			if b, err := os.ReadFile(cfg.allowEmailsFile); err == nil {
				for _, line := range strings.Split(string(b), "\n") {
					if line = strings.TrimSpace(line); strings.HasPrefix(line, "@") {
						add(line, overlapLoginDomains)
					}
				}
			}
		}
		for _, p := range cfg.providers {
			switch v := p.(type) {
			case *auth.OIDCProvider:
				for d := range v.AllowDomains {
					add(d, overlapLoginDomains)
				}
			case *auth.GitHubProvider:
				for d := range v.AllowDomains {
					add(d, overlapLoginDomains)
				}
			}
		}
		if cfg.mgr != nil {
			for e := range cfg.mgr.superAdmins {
				if i := strings.LastIndexByte(e, '@'); i >= 0 {
					add(e[i+1:], overlapSuperAdmin)
				}
			}
		}
		return out
	}
}

// overlapsFor lists the claimed domains that are also deployment login domains, sorted.
// Empty when the API was built without a deployment view (tests, AUTH=dev).
func (a tenantIdPAPI) overlapsFor(allowedDomains string) []domainOverlap {
	if a.deployDomains == nil {
		return nil
	}
	have := a.deployDomains()
	var out []domainOverlap
	for _, d := range splitDomainCSV(allowedDomains) {
		var srcs []string
		for s := range have[d] {
			srcs = append(srcs, s)
		}
		sort.Strings(srcs)
		for _, s := range srcs {
			out = append(out, domainOverlap{Domain: d, Source: s})
		}
	}
	return out
}

// overlapAuditDetail renders overlaps for an audit line ("" when none).
func overlapAuditDetail(o []domainOverlap) string {
	if len(o) == 0 {
		return ""
	}
	parts := make([]string, len(o))
	for i, v := range o {
		parts[i] = v.Domain + ":" + v.Source
	}
	return " domain_overlaps=" + strings.Join(parts, ",")
}
