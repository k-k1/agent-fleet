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

// The audit records a Bash command's first line cut at 80 runes with "…" appended (the Agent's
// toolInfo), which can cut a token anywhere, even inside its header; whatever part is left still
// goes. Stateless tokens run together go too, and what follows a token is kept.
func TestBashTargetMasksCutAndJoinedStatelessTokens(t *testing.T) {
	cut := func(s string) string {
		if r := []rune(s); len(r) > 80 {
			return string(r[:80]) + "…"
		}
		return s
	}
	for name, tok := range secretpattest.All() {
		for pad := 0; pad < 80; pad++ {
			info := cut("echo " + strings.Repeat("a", pad) + " " + tok + " done")
			out := BashTarget(info, true)
			if l := secretpattest.Leftover(out, tok); l != "" {
				t.Fatalf("%s, cut %d runes into the command: %q survives in %q", name, 80-pad, l, out)
			}
			if strings.Contains(out, "gh"+tok[2:4]) {
				t.Fatalf("%s: the prefix survives in %q", name, out)
			}
		}
		if !strings.HasPrefix(name, "stateless") {
			continue
		}
		for in, want := range map[string]string{
			"echo " + tok + tok + " done":       "echo *** done",
			"echo " + tok + "-" + tok + " done": "echo *** done",
			"cp " + tok + ".md /tmp":            "cp ***.md /tmp",
		} {
			if got := MaskCommandSecrets(in); got != want {
				t.Errorf("%s: %.120q, want %q", name, got, want)
			}
		}
	}
	// Only a line of the cut's own shape (80 runes and "…") is taken for a cut one.
	for _, in := range []string{"cat docs/ghs_eyewash_station.md", "cat docs/ghs_guide.md…", "echo ghs_wip…"} {
		if got := BashTarget(in, true); got != in {
			t.Errorf("%q masked to %q", in, got)
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
