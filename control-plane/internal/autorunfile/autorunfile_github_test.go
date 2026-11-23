package autorunfile

import (
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/secretpat/secretpattest"
)

// A settings file carrying a GitHub token, classic or stateless, is refused as a credential;
// prose with underscores and dots is not.
func TestLooksLikeCredentialStatelessGitHubTokens(t *testing.T) {
	for _, tok := range []string{secretpattest.Classic(), secretpattest.Stateless(), secretpattest.StatelessEarlyPunct()} {
		if got := credentialRule("push with " + tok + " please"); got != "github-token" {
			t.Errorf("%.12s… (%d chars): rule %q, want github-token", tok, len(tok), got)
		}
	}
	for _, s := range secretpattest.Benign {
		if LooksLikeCredential(s) {
			t.Errorf("benign %q refused as %s", s, credentialRule(s))
		}
	}
}
