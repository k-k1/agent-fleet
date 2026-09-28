// Package branchrule resolves a branch name and a base branch from four layers of rules:
// the repository's own declaration, the user, the tenant and the built-in default
// (ADR 0103). The layers merge field by field, never rule by rule, because a git-flow
// declaration knows prefixes and bases but nothing about what follows the prefix.
//
// Rules are advisory: every problem becomes a Warning, and nothing here refuses a name.
package branchrule

import (
	"sort"
	"strings"
)

// Kinds is the closed kind vocabulary (ADR 0103 decision 1). The first five are git-flow's
// and Bitbucket's own kinds, so their declarations map one to one; the last three are the
// conventional-commit prefixes the rename chips offered.
var Kinds = []string{"feature", "bugfix", "hotfix", "release", "support", "docs", "chore", "refactor"}

// ValidKind reports whether k is in the vocabulary. The comparison is exact: a
// `[type "Feature"]` section is not the feature kind.
func ValidKind(k string) bool {
	for _, v := range Kinds {
		if v == k {
			return true
		}
	}
	return false
}

// KindRule holds one kind's overrides. A nil pointer is "not set", which differs from an
// empty prefix: git-flow may declare a kind with no prefix at all.
type KindRule struct {
	Prefix *string  `json:"prefix,omitempty"`
	Base   *string  `json:"base,omitempty"`
	From   []string `json:"from,omitempty"`
}

// Rule is `{match, name, base, types}`. Every field is optional.
type Rule struct {
	Match string              `json:"match,omitempty"`
	Name  *string             `json:"name,omitempty"`
	Base  *string             `json:"base,omitempty"`
	Types map[string]KindRule `json:"types,omitempty"`

	// Source labels where the rule came from ("user *", ".gitflow"); Keys names the
	// config key behind a single field ("base" → "gitflow.branch.develop") when a source
	// can say so. Both only feed the response's sources map.
	Source string            `json:"-"`
	Keys   map[string]string `json:"-"`
	// Declares lists the kinds a repository source names. Their union is the kind set that
	// blocks weaker layers (decision 3); it is not derived from Types because the avh form
	// sets a base for kinds it has no prefix for.
	Declares []string `json:"-"`
}

func (r Rule) label(field string) string {
	if k, ok := r.Keys[field]; ok {
		return r.Source + " " + k
	}
	return r.Source
}

// Layer is one of the four, strongest first in Input.Layers.
type Layer struct {
	Name  string
	Rules []Rule
	// Repository marks the layer whose Declares form the kind set.
	Repository bool
}

func str(s string) *string { return &s }

// Builtin is decision 6: `{prefix}{ref}-{slug}` off the parent clone's HEAD.
func Builtin() Layer {
	p := map[string]string{
		"feature": "feature/", "bugfix": "fix/", "hotfix": "hotfix/", "release": "release/",
		"support": "support/", "docs": "docs/", "chore": "chore/", "refactor": "refactor/",
	}
	types := map[string]KindRule{}
	for k, v := range p {
		types[k] = KindRule{Prefix: str(v)}
	}
	return Layer{Name: "builtin", Rules: []Rule{{
		Match: "*", Name: str("{prefix}{ref}-{slug}"), Base: str("head"), Types: types, Source: "builtin",
	}}}
}

// builtinFrom is the map tried after every layer's `from` (decision 4). It is separate from
// the built-in layer on purpose: a user `types.bugfix.from = [Defect]` supplies the field
// and would otherwise switch off the `bug` label.
var builtinFrom = []struct{ value, kind string }{
	{"bug", "bugfix"}, {"defect", "bugfix"}, {"hotfix", "hotfix"}, {"documentation", "docs"}, {"docs", "docs"},
}

// RepoID turns origin's URL into the lower-cased `host/owner/repo` that `match` is compared
// with. "" when the URL has no host and path (a local path remote).
func RepoID(origin string) string {
	u := strings.TrimSpace(origin)
	if u == "" {
		return ""
	}
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		if j := strings.Index(u, "/"); j >= 0 {
			host := u[:j]
			if k := strings.LastIndex(host, "@"); k >= 0 {
				host = host[k+1:]
			}
			if k := strings.Index(host, ":"); k >= 0 {
				host = host[:k]
			}
			u = host + "/" + u[j+1:]
		} else {
			return ""
		}
	} else if i := strings.Index(u, ":"); i > 0 && !strings.Contains(u[:i], "/") {
		// scp form: git@host:owner/repo.git
		host := u[:i]
		if k := strings.LastIndex(host, "@"); k >= 0 {
			host = host[k+1:]
		}
		u = host + "/" + strings.TrimLeft(u[i+1:], "/")
	} else {
		return ""
	}
	u = strings.TrimSuffix(strings.TrimRight(u, "/"), ".git")
	segs := []string{}
	for _, s := range strings.Split(u, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	if len(segs) < 2 {
		return ""
	}
	return strings.ToLower(strings.Join(segs, "/"))
}

// ValidMatch reports whether pattern is a bare `*` or `/`-separated non-empty segments,
// each literal or `*`.
func ValidMatch(pattern string) bool {
	if pattern == "*" {
		return true
	}
	if pattern == "" {
		return false
	}
	for _, s := range strings.Split(pattern, "/") {
		if s == "" || (strings.Contains(s, "*") && s != "*") {
			return false
		}
	}
	return true
}

// specificity is the number of literal segments when pattern matches id, or -1. A bare `*`
// matches every repository with specificity 0; otherwise each `*` stands for exactly one
// segment, so the segment counts must agree.
func specificity(pattern, id string) int {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "*" {
		return 0
	}
	if id == "" || !ValidMatch(pattern) {
		return -1
	}
	ps, is := strings.Split(pattern, "/"), strings.Split(id, "/")
	if len(ps) != len(is) {
		return -1
	}
	n := 0
	for i, p := range ps {
		if p == "*" {
			continue
		}
		if p != is[i] {
			return -1
		}
		n++
	}
	return n
}

// matching returns the layer's rules that match id, most specific first; a tie keeps the
// order the rules were listed in. The repository layer has no match: it is the repository.
func matching(l Layer, id string) []Rule {
	if l.Repository {
		return l.Rules
	}
	type ranked struct {
		r Rule
		s int
	}
	var rs []ranked
	for _, r := range l.Rules {
		if s := specificity(r.Match, id); s >= 0 {
			rs = append(rs, ranked{r, s})
		}
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].s > rs[j].s })
	out := make([]Rule, len(rs))
	for i, x := range rs {
		out[i] = x.r
	}
	return out
}
