// Package egressallow is the egress allowlist matcher. The control plane uses it for the
// member-facing check and the own forward proxy, and the gateway agent uses it for the
// verdict it reports, so the two cannot disagree on what an entry covers.
package egressallow

import (
	"sort"
	"strings"
)

// Policy is a compiled allowlist. Matching is case-insensitive. An entry starting with
// "." (or "*.", normalised to a leading ".") is a suffix match covering that domain and
// all its subdomains; any other entry is an exact host match. This is also how Squid's
// dstdomain ACL reads an entry, which is what the gateway decides by.
type Policy struct {
	exact  map[string]bool
	suffix []string // e.g. ".githubusercontent.com" (leading dot)
}

// New builds a policy from a list of entries (blank lines and "#" comments ignored).
// Passing nil yields an empty (deny-all) policy.
func New(entries []string) *Policy {
	p := &Policy{exact: map[string]bool{}}
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

// Allows reports whether host is permitted. The caller strips any port first.
func (p *Policy) Allows(host string) bool {
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

// Minimal returns the policy's entries with every entry another one already covers
// removed, sorted. Squid warns about such overlaps in a dstdomain file (and older
// releases refuse the file), and the built-in defaults have them, e.g.
// "api.anthropic.com" under ".anthropic.com". The result allows exactly what p allows.
func (p *Policy) Minimal() []string {
	var out []string
	for _, s := range p.suffix {
		// A suffix is redundant when a strictly broader one covers its domain.
		if !p.coveredBySuffix(strings.TrimPrefix(s, "."), s) {
			out = append(out, s)
		}
	}
	for h := range p.exact {
		if !p.coveredBySuffix(h, "") {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return dedupe(out)
}

// coveredBySuffix reports whether a suffix entry other than self matches host.
func (p *Policy) coveredBySuffix(host, self string) bool {
	for _, s := range p.suffix {
		if s == self {
			continue
		}
		if host == strings.TrimPrefix(s, ".") || strings.HasSuffix(host, s) {
			return true
		}
	}
	return false
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}
