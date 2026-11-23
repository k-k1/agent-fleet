package bridge

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secretpat"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secretpat/secretpattest"
)

// A GitHub token of any shape leaves nothing behind in the text sent to a chat, also when it is
// glued to another secret shape whose match overlaps its own.
func TestScrubSecretsRemovesWholeGitHubTokens(t *testing.T) {
	for name, tok := range secretpattest.All() {
		if got, want := ScrubSecrets("pushed with "+tok+". Done"), "pushed with "+redactedMark+". Done"; got != want {
			t.Errorf("%s: scrubbed to %.120q, want %q", name, got, want)
		}
		ins := []string{"xoxb-0123456789-" + tok, secretpattest.JWT('j') + "-" + tok}
		if strings.HasPrefix(name, "stateless") {
			// A classic token keeps its leading word boundary, as on main, and two classic
			// tokens run together cannot be told apart.
			ins = append(ins, "echo cache_"+tok, "x "+secretpattest.Classic()+tok)
		}
		for _, in := range ins {
			out := ScrubSecrets(in)
			if l := secretpattest.Leftover(out, tok); l != "" {
				t.Errorf("%s: %q survives in %.120q", name, l, out)
			}
		}
	}
}

// A replacement never cuts another rule's match short: a JWT whose signature holds a
// GitHub-token-shaped run, and a Bearer value that starts with one, go whole, as on main.
func TestScrubSecretsKeepsEnclosingMatchesWhole(t *testing.T) {
	letters := secretpattest.ClassicLetters()
	for in, want := range map[string]string{
		"echo " + secretpattest.JWTHolding(letters) + " done": "echo " + redactedMark + " done",
		"Authorization: Bearer " + letters + "_private then":  "Authorization: " + redactedMark + " then",
	} {
		if got := ScrubSecrets(in); got != want {
			t.Errorf("ScrubSecrets(%.60q…) = %.120q, want %q", in, got, want)
		}
	}
}

// The GitHub shapes alone, not ScrubSecrets: its entropy fallback takes long mixed runs on
// purpose, benign or not.
// Stateless tokens run together go in the known-pattern pass itself, not only by the entropy
// fallback's grace (which a letters-only body would not get).
func TestKnownPassTakesJoinedStatelessTokens(t *testing.T) {
	known := func(s string) string {
		return secretpat.ReplaceSpans(s, "*", append(secretpat.Spans(s, knownSecretPatterns...), secretpat.GitHubStatelessSpans(s)...))
	}
	for name, tok := range secretpattest.All() {
		if !strings.HasPrefix(name, "stateless") {
			continue
		}
		for in, want := range map[string]string{
			"x " + tok + tok + " y":       "x * y",
			"x " + tok + "-" + tok + " y": "x * y",
			"x " + tok + ".md y":          "x *.md y",
		} {
			if got := known(in); got != want {
				t.Errorf("%s: %.80q", name, got)
			}
		}
	}
}

func TestGitHubPatternsSkipBenignText(t *testing.T) {
	gh := 0
	for _, re := range knownSecretPatterns {
		if !strings.Contains(re.String(), "gh[pousr]_") {
			continue
		}
		gh++
		for _, s := range secretpattest.Benign {
			if m := re.FindString(s); m != "" {
				t.Errorf("benign %q: %s matched %q", s, re, m)
			}
		}
	}
	if gh != 1 {
		t.Fatalf("found %d GitHub patterns, want the classic one", gh)
	}
	for _, s := range secretpattest.Benign {
		if sp := secretpat.GitHubStatelessSpans(s); len(sp) != 0 {
			t.Errorf("benign %q: stateless spans %v", s, sp)
		}
	}
}
