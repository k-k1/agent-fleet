package main

import (
	"log"
	"net/url"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cpurl"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// internalGitRewriteWant is the global git config entry that points this workspace's git at
// the CP's workspace listener (ADR 0106 decision 8): key url.<internal>/git/.insteadOf,
// value <public>/git/. The CP keeps handing out public clone URLs because people clone
// them from outside the cluster; from inside it the public base may not be reachable, so
// the rewrite happens here, below every clone, fetch, push and LFS transfer alike —
// including remotes cloned before the internal URL existed. ("", "") when there is
// nothing to rewrite: internal git off, or no internal URL.
func internalGitRewriteWant() (key, from string) {
	pub, in := cpurl.Public(), cpurl.Internal()
	if internalGitHost() == "" || pub == "" || in == "" || in == pub {
		return "", ""
	}
	return "url." + in + "/git/.insteadOf", pub + "/git/"
}

// internalGitRewriteHost is the authority git sends as `host=` once the rewrite applies,
// which the internal git credential has to be stored under as well. "" without a rewrite.
func internalGitRewriteHost() string {
	if key, _ := internalGitRewriteWant(); key == "" {
		return ""
	}
	u, err := url.Parse(cpurl.Internal())
	if err != nil {
		return ""
	}
	return u.Host
}

// syncInternalGitRewrite makes the global git config match internalGitRewriteWant, and
// removes a rewrite of the public git base left by an earlier start whose internal URL
// has since changed or gone — otherwise a workspace would keep sending its git to an
// address the CP no longer serves.
func syncInternalGitRewrite() {
	want, from := internalGitRewriteWant()
	pub := cpurl.Public()
	if pub == "" {
		return
	}
	stale := pub + "/git/"
	have := false
	out, _ := gitx.Run("", "config", "--global", "--get-regexp", `^url\..*\.insteadof$`)
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(line, " ")
		if !ok || val != stale {
			continue
		}
		if want != "" && strings.EqualFold(key, want) {
			have = true
			continue
		}
		if o, err := gitx.Combined("", "config", "--global", "--fixed-value", "--unset-all", key, stale); err != nil {
			log.Printf("internal git: removing stale rewrite %s: %v: %s", key, err, o)
		}
	}
	if want == "" || have {
		return
	}
	if o, err := gitx.Combined("", "config", "--global", "--add", want, from); err != nil {
		log.Printf("internal git: setting %s: %v: %s", want, err, o)
	}
}
