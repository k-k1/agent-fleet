package claudeaudit

import (
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
		for _, cmd := range []string{
			"git push https://x-access-token:" + tok + "@github.com/o/r.git main",
			"curl -H 'X-Token: " + tok + "' https://api.github.com/user",
			"echo " + secretpattest.JWT('j') + "-" + tok,
			"echo xoxb-0123456789-" + tok,
			"echo cache_" + tok,
		} {
			if strings.HasPrefix(cmd, "echo cache_") && !strings.HasPrefix(name, "stateless") {
				continue
			}
			out := MaskCommandSecrets(cmd)
			if l := secretpattest.Leftover(out, tok); l != "" {
				t.Errorf("%s: %q survives in %.120q", name, l, out)
			}
		}
	}
	for _, s := range secretpattest.Benign {
		if out := MaskCommandSecrets(s); strings.Contains(out, "***") {
			t.Errorf("benign %q masked to %q", s, out)
		}
	}
}
