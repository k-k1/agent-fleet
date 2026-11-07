package auth

import (
	"os"
	"strings"
)

// Where a claimed domain overlaps the deployment's own sign-in surface. The values are the
// wire contract of the overlap source field; the Console maps each to a sentence.
const (
	OverlapLoginDomains = "login_domains" // the deployment's own login allowlists (env, file, env providers)
	OverlapSuperAdmin   = "super_admin"   // the domain of a SUPER_ADMIN_EMAILS address
)

// DeploymentDomains collects, per domain, where the deployment itself admits or elevates
// addresses: the env allowlist domains, "@domain" lines of the allowlist file (read live, so a
// file edit is seen without a restart), the own domain lists of env-configured providers, and
// the SUPER_ADMIN_EMAILS addresses. The result maps domain -> set of Overlap* values.
func DeploymentDomains(allowDomains map[string]bool, allowEmailsFile string, providers []LoginProvider, superAdmins map[string]bool) map[string]map[string]bool {
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
	for d := range allowDomains {
		add(d, OverlapLoginDomains)
	}
	if allowEmailsFile != "" {
		if b, err := os.ReadFile(allowEmailsFile); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if line = strings.TrimSpace(line); strings.HasPrefix(line, "@") {
					add(line, OverlapLoginDomains)
				}
			}
		}
	}
	for _, p := range providers {
		switch v := p.(type) {
		case *OIDCProvider:
			for d := range v.AllowDomains {
				add(d, OverlapLoginDomains)
			}
		case *GitHubProvider:
			for d := range v.AllowDomains {
				add(d, OverlapLoginDomains)
			}
		}
	}
	for e := range superAdmins {
		if i := strings.LastIndexByte(e, '@'); i >= 0 {
			add(e[i+1:], OverlapSuperAdmin)
		}
	}
	return out
}
