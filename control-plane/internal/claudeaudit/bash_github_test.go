package claudeaudit

import (
	"regexp"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/secretpat/secretpattest"
)

// No part of a GitHub token of any shape survives into the audit log, whatever word it sits in
// and whatever other secret shape it is glued to.
func TestMaskCommandSecretsRemovesWholeGitHubTokens(t *testing.T) {
	for name, tok := range secretpattest.All() {
		if got, want := MaskCommandSecrets("echo "+tok+" | gh auth login --with-token"), "echo *** | gh auth login --with-token"; got != want {
			t.Errorf("%s: masked to %.120q, want %q", name, got, want)
		}
		cmds := []string{
			"git push https://x-access-token:" + tok + "@github.com/o/r.git main",
			"curl -H 'X-Token: " + tok + "' https://api.github.com/user",
			"echo " + secretpattest.JWT('j') + "-" + tok,
			"echo xoxb-0123456789-" + tok,
		}
		if strings.HasPrefix(name, "stateless") {
			// Two classic tokens run together cannot be told apart, on main or here.
			cmds = append(cmds, "echo cache_"+tok, "echo "+secretpattest.Classic()+tok)
		}
		for _, cmd := range cmds {
			out := MaskCommandSecrets(cmd)
			if l := secretpattest.Leftover(out, tok); l != "" {
				t.Errorf("%s: %q survives in %.120q", name, l, out)
			}
		}
	}
}

// A replacement never cuts another rule's match short: a JWT whose signature holds a
// GitHub-token-shaped run goes whole, as on main.
func TestMaskCommandSecretsKeepsEnclosingMatchesWhole(t *testing.T) {
	letters := secretpattest.ClassicLetters()
	for in, want := range map[string]string{
		"echo " + secretpattest.JWTHolding(letters) + " done":         "echo *** done",
		"curl -H 'Authorization: Bearer " + letters + "_private' x":   "curl -H 'Authorization: Bearer ***' x",
		"echo " + secretpattest.Classic() + secretpattest.Stateless(): "echo ***",
	} {
		if got := MaskCommandSecrets(in); got != want {
			t.Errorf("MaskCommandSecrets(%.60q…) = %.120q, want %q", in, got, want)
		}
	}
}

// The audit records a Bash command's first line cut at 80 runes with "…" (the Agent's toolInfo),
// which cuts a stateless token short of its signature; the part that is left still goes, and so
// do stateless tokens run together.
func TestBashTargetMasksCutAndJoinedStatelessTokens(t *testing.T) {
	cut := func(s string) string {
		if r := []rune(s); len(r) > 80 {
			return string(r[:80]) + "…"
		}
		return s
	}
	for name, tok := range secretpattest.All() {
		if !strings.HasPrefix(name, "stateless") {
			continue
		}
		for _, cmd := range []string{"echo " + tok, "gh api -H 'Authorization: token " + tok + "' /x", "curl -sS --data-binary " + tok} {
			info := cut(cmd)
			out := BashTarget(info, true)
			if l := secretpattest.Leftover(out, tok); l != "" {
				t.Errorf("%s: %q survives in %q", name, l, out)
			}
		}
		for _, joined := range []string{tok + tok, tok + "-" + tok} {
			if got := MaskCommandSecrets("echo " + joined + " done"); got != "echo *** done" {
				t.Errorf("%s joined: %.120q", name, got)
			}
		}
	}
}

func TestMaskCommandSecretsLeavesBenignTextAlone(t *testing.T) {
	// The audit's classic rule has had no trailing boundary since before the stateless shape,
	// so it masks a long gh?_ name; that is not this change's to narrow.
	mainClassic := regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{16,}`)
	for _, s := range secretpattest.Benign {
		if mainClassic.MatchString(s) {
			continue
		}
		if out := MaskCommandSecrets(s); strings.Contains(out, "***") {
			t.Errorf("benign %q masked to %q", s, out)
		}
	}
}
