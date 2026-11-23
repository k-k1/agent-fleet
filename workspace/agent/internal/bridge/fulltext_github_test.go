package bridge

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secretpat/secretpattest"
)

// A GitHub token of any shape leaves nothing behind in the text sent to a chat, also when it is
// glued to a Slack token or a JWT whose own patterns would otherwise take its head first.
func TestScrubSecretsRemovesWholeGitHubTokens(t *testing.T) {
	for name, tok := range secretpattest.All() {
		if got, want := ScrubSecrets("pushed with "+tok+". Done"), "pushed with "+redactedMark+". Done"; got != want {
			t.Errorf("%s: scrubbed to %.120q, want %q", name, got, want)
		}
		ins := []string{"xoxb-0123456789-" + tok, secretpattest.JWT('j') + "-" + tok}
		if strings.HasPrefix(name, "stateless") {
			// A classic token keeps its leading word boundary, as on main.
			ins = append(ins, "echo cache_"+tok)
		}
		for _, in := range ins {
			out := ScrubSecrets(in)
			if l := secretpattest.Leftover(out, tok); l != "" {
				t.Errorf("%s: %q survives in %.120q", name, l, out)
			}
		}
	}
	// The pattern alone, not ScrubSecrets: its entropy fallback takes long mixed runs on
	// purpose, benign or not.
	for _, s := range secretpattest.Benign {
		if m := knownSecretPatterns[0].FindString(s); m != "" {
			t.Errorf("benign %q: the GitHub pattern matched %q", s, m)
		}
	}
}
