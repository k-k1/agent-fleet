package gitx

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secretpat/secretpattest"
)

// A remote whose path carries a GitHub token of any shape, glued to a word or not, is not
// shown; remotes whose names merely contain dots after a token-like prefix still are.
func TestDisplayRemoteHidesGitHubTokens(t *testing.T) {
	for name, tok := range secretpattest.All() {
		rs := []string{"https://git.invalid/o/" + tok + "/r.git", "/srv/" + tok + "/r.git"}
		if strings.HasPrefix(name, "stateless") {
			rs = append(rs, "https://git.invalid/o/cache_"+tok+"/r.git")
		}
		for _, r := range rs {
			if got := displayRemote(r); got != remoteHidden {
				t.Errorf("%s: %.40s… shown as %.60q", name, r, got)
			}
		}
	}
	for _, r := range []string{
		"https://git.invalid/ghost_writer/high_score.v2-final.git",
		"https://git.invalid/team/ghs_v2.api.git",
		"https://git.invalid/team/ATBBdev.tools.git",
	} {
		if got := displayRemote(r); got != r {
			t.Errorf("%s shown as %q", r, got)
		}
	}
}
