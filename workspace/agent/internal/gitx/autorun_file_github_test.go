package gitx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secretpat/secretpattest"
)

// A remote whose path carries a GitHub token, classic or stateless, is not shown.
func TestDisplayRemoteHidesStatelessGitHubTokens(t *testing.T) {
	for _, tok := range []string{secretpattest.Classic(), secretpattest.Stateless(), secretpattest.StatelessEarlyPunct()} {
		for _, r := range []string{"https://git.invalid/o/" + tok + "/r.git", "/srv/" + tok + "/r.git"} {
			if got := displayRemote(r); got != remoteHidden {
				t.Errorf("%.40s…: shown as %.60q", r, got)
			}
		}
		whole := false
		for _, re := range remoteSecretRules {
			whole = whole || re.FindString("x "+tok+" y") == tok
		}
		if !whole {
			t.Errorf("%.12s…: no remoteSecretRules entry matches the whole token", tok)
		}
	}
	if got := displayRemote("https://git.invalid/ghost_writer/high_score.v2-final.git"); got == remoteHidden {
		t.Errorf("a plain remote is hidden")
	}
}
