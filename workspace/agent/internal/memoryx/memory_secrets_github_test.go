package memoryx

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secretpat/secretpattest"
)

// A stateless installation token (JWT-shaped, with `.`, `-` and `_`) is a github-token
// finding like a classic one, its hint keeps only the prefix, and prose with underscores and
// dots is not one.
func TestMemoryScanContentFindsStatelessGitHubTokens(t *testing.T) {
	toks := []string{secretpattest.Classic(), secretpattest.Stateless(), secretpattest.StatelessEarlyPunct()}
	for _, tok := range toks {
		got := memoryScanContent("a.md", []byte("# note\nuse "+tok+" for the push.\n"))
		if len(got) != 1 || got[0].Rule != "github-token" || got[0].Line != 2 {
			t.Errorf("%.12s… (%d chars): findings %+v, want one github-token on line 2", tok, len(tok), got)
			continue
		}
		if want := tok[:memorySecretHintPrefixLen] + "…("; !strings.HasPrefix(got[0].Hint, want) {
			t.Errorf("hint %q, want it to start with %q", got[0].Hint, want)
		}
		if l := secretpattest.Leftover(got[0].Hint, tok); l != "" {
			t.Errorf("hint %q carries %q of the token", got[0].Hint, l)
		}
	}
	if got := memoryScanContent("b.md", []byte(strings.Join(secretpattest.Benign, "\n"))); len(got) != 0 {
		t.Errorf("benign text flagged: %+v", got)
	}
}
