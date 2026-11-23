package autorunfile

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/secretpat/secretpattest"
)

// A settings file carrying a GitHub token of any shape is refused as a credential; prose and
// paths with underscores and dots are not.
func TestLooksLikeCredentialGitHubTokens(t *testing.T) {
	for name, tok := range secretpattest.All() {
		ins := []string{"push with " + tok + " please"}
		if strings.HasPrefix(name, "stateless") {
			ins = append(ins, "cache_"+tok)
		}
		for _, in := range ins {
			if got := credentialRule(in); got != "github-token" {
				t.Errorf("%s in %.20q…: rule %q, want github-token", name, in, got)
			}
		}
	}
	for _, s := range secretpattest.Benign {
		if LooksLikeCredential(s) {
			t.Errorf("benign %q refused as %s", s, credentialRule(s))
		}
	}
}
