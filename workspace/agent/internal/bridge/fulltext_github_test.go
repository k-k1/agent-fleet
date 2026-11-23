package bridge

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secretpat/secretpattest"
)

// A stateless GitHub token leaves nothing behind in the text sent to a chat, and the GitHub
// pattern alone takes it whole: the entropy fallback is not what saves it.
func TestScrubSecretsRemovesWholeStatelessGitHubTokens(t *testing.T) {
	for _, tok := range []string{secretpattest.Classic(), secretpattest.Stateless(), secretpattest.StatelessEarlyPunct()} {
		in := "pushed with " + tok + ". Done"
		out := ScrubSecrets(in)
		if l := secretpattest.Leftover(out, tok); l != "" {
			t.Errorf("%.12s…: %q survives in %q", tok, l, out)
		}
		if !strings.HasPrefix(out, "pushed with "+redactedMark) || !strings.HasSuffix(out, ". Done") {
			t.Errorf("%.12s…: scrubbed to %q", tok, out)
		}
		gh := knownSecretPatterns[1]
		if got := gh.FindString(in); got != tok {
			t.Errorf("%.12s…: the GitHub pattern matched %d of %d chars", tok, len(got), len(tok))
		}
	}
	// The pattern alone, not ScrubSecrets: its entropy fallback takes long mixed runs on
	// purpose, benign or not.
	for _, s := range secretpattest.Benign {
		if m := knownSecretPatterns[1].FindString(s); m != "" {
			t.Errorf("benign %q: the GitHub pattern matched %q", s, m)
		}
	}
}
