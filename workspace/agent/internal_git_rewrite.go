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

// internalGitRewriteOwner is the global git config key that records the rewrite entries
// the Agent itself added, one "<key> <value>" per value. Only those are ever changed or
// removed: a url.*.insteadOf the person wrote — even one identical to the Agent's — is
// theirs, and survives every start, with or without an internal URL.
const internalGitRewriteOwner = "agent-fleet.internalGitRewrite"

// syncInternalGitRewrite makes the global git config match internalGitRewriteWant. It
// removes the entries it added on an earlier start whose internal or public URL has since
// changed or gone — otherwise a workspace would keep sending its git to an address the CP
// no longer serves — and leaves every other entry alone.
func syncInternalGitRewrite() {
	want, from := internalGitRewriteWant()
	wantRec := ""
	if want != "" {
		wantRec = want + " " + from
	}
	owned, _ := gitx.Run("", "config", "--global", "--get-all", internalGitRewriteOwner)
	have := false
	for _, rec := range strings.Split(owned, "\n") {
		if rec == "" {
			continue
		}
		if rec == wantRec {
			key, val, _ := strings.Cut(rec, " ")
			if gitx.Cmd("", "config", "--global", "--fixed-value", "--get", key, val).Run() == nil {
				have = true
				continue
			}
			// The entry was deleted by hand: forget it below and add it afresh.
		}
		key, val, ok := strings.Cut(rec, " ")
		if ok {
			_ = gitx.Cmd("", "config", "--global", "--fixed-value", "--unset", key, val).Run()
		}
		if o, err := gitx.Combined("", "config", "--global", "--fixed-value", "--unset-all", internalGitRewriteOwner, rec); err != nil {
			log.Printf("internal git: forgetting rewrite %q: %v: %s", rec, err, o)
		}
	}
	if want == "" || have {
		return
	}
	// The same entry already there was put there by somebody else: use it, never own it.
	if gitx.Cmd("", "config", "--global", "--fixed-value", "--get", want, from).Run() == nil {
		return
	}
	if o, err := gitx.Combined("", "config", "--global", "--add", want, from); err != nil {
		log.Printf("internal git: setting %s: %v: %s", want, err, o)
		return
	}
	if o, err := gitx.Combined("", "config", "--global", "--add", internalGitRewriteOwner, wantRec); err != nil {
		log.Printf("internal git: recording %s: %v: %s", want, err, o)
	}
}
