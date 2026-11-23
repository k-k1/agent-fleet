package claudeaudit

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/secretpat/secretpattest"
)

// No part of a stateless GitHub token survives into the audit log, whatever word it sits in.
func TestMaskCommandSecretsRemovesWholeStatelessGitHubTokens(t *testing.T) {
	for _, tok := range []string{secretpattest.Classic(), secretpattest.Stateless(), secretpattest.StatelessEarlyPunct()} {
		for _, cmd := range []string{
			"echo " + tok + " | gh auth login --with-token",
			"git push https://x-access-token:" + tok + "@github.com/o/r.git main",
			"curl -H 'X-Token: " + tok + "' https://api.github.com/user",
		} {
			out := MaskCommandSecrets(cmd)
			if l := secretpattest.Leftover(out, tok); l != "" {
				t.Errorf("%q survives in %q", l, out)
			}
		}
		if out := MaskCommandSecrets("echo " + tok + " | gh auth login --with-token"); out != "echo *** | gh auth login --with-token" {
			t.Errorf("masked to %q", out)
		}
	}
	for _, s := range secretpattest.Benign {
		if out := MaskCommandSecrets(s); strings.Contains(out, "***") {
			t.Errorf("benign %q masked to %q", s, out)
		}
	}
}
