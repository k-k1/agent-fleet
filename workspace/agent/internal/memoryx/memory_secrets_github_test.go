package memoryx

import (
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secretpat/secretpattest"
)

// A stateless GitHub token (ghs_APPID_JWT) is a github-token finding like a classic one, even
// when its APPID or body holds a placeholder-looking run; its hint keeps only the prefix; a
// classic token followed by ".example" is still found; prose with underscores and dots is not.
func TestMemoryScanContentFindsStatelessGitHubTokens(t *testing.T) {
	for name, tok := range secretpattest.All() {
		for _, line := range []string{"use " + tok + " for the push.", "cache_" + tok, tok + ".example"} {
			if strings.HasPrefix(line, "cache_") && !strings.HasPrefix(name, "stateless") {
				continue
			}
			got := memoryScanContent("a.md", []byte("# note\n"+line+"\n"))
			if len(got) != 1 || got[0].Rule != "github-token" || got[0].Line != 2 {
				t.Errorf("%s in %.20q…: findings %+v, want one github-token on line 2", name, line, got)
				continue
			}
			if want := tok[:memorySecretHintPrefixLen] + "…(" + strconv.Itoa(len(tok)) + ")"; got[0].Hint != want {
				t.Errorf("%s: hint %q, want %q", name, got[0].Hint, want)
			}
		}
	}
	// Tokens written back to back reach the placeholder filter as one merged candidate; the
	// zeros in one APPID must not drop both.
	z, st := secretpattest.StatelessZeros(), secretpattest.Stateless()
	for _, line := range []string{z + st, z + "-" + st, st + z} {
		if got := memoryScanContent("c.md", []byte(line+"\n")); len(got) != 1 || got[0].Rule != "github-token" {
			t.Errorf("joined tokens: findings %+v", got)
		}
	}
	if got := memoryScanContent("b.md", []byte(strings.Join(secretpattest.Benign, "\n"))); len(got) != 0 {
		t.Errorf("benign text flagged: %+v", got)
	}
}
