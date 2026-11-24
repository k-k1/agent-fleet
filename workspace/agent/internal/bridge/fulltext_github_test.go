package bridge

import (
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

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

// Text glued after a known token in the same value goes with it, for every shape the known
// pass matches: the mark a known token leaves must not cut the assignment or entropy pass
// short. A label before the token and a sentence's final full stop stay.
func TestScrubSecretsTakesTextGluedAfterAKnownToken(t *testing.T) {
	toks := secretpattest.All()
	toks["slack"] = "xoxb-123456789012-AbCdEfGhIjKlMn"
	toks["aws"] = "AKIAIOSFODNN7EXAMPLE"
	toks["jwt"] = secretpattest.JWT('g')
	for name, tok := range toks {
		for in, want := range map[string]string{
			"API_TOKEN=" + tok + ":private next": "API_TOKEN=" + redactedMark + " next",
			"API_TOKEN=\"" + tok + "\" next":     "API_TOKEN=" + redactedMark + " next",
			"GITHUB_TOKEN=" + tok:                "GITHUB_TOKEN=" + redactedMark,
			"echo " + tok + "+private next":      "echo " + redactedMark + " next",
			"echo " + tok + "/private.":          "echo " + redactedMark + ".",
			"--token=" + tok + "-private next":   "--token=" + redactedMark + " next",
			"pushed with " + tok + ". Done":      "pushed with " + redactedMark + ". Done",
			"API_TOKEN=" + tok + ". Done":        "API_TOKEN=" + redactedMark + ". Done",
			"API_TOKEN='" + tok + ":x.y' next":   "API_TOKEN=" + redactedMark + " next",
			"x " + tok + ".md y":                 "x " + redactedMark + " y",
			"echo " + tok + "+" + tok + " next":  "echo " + redactedMark + " next",
			tok + ".NEXT_API_TOKEN=hunter2":      redactedMark + ".NEXT_API_TOKEN=" + redactedMark,
		} {
			if got := ScrubSecrets(in); got != want {
				t.Errorf("%s: ScrubSecrets(%.60q…) = %.120q, want %.120q", name, in, got, want)
			}
		}
	}
}

// The assignment pass runs where the known tokens are masked, never on the raw input: a mark's
// bracket is a word boundary before a name glued after a token, and a raw match could start
// inside a token and take the text after it.
func TestScrubSecretsMatchesAssignmentsAroundMarks(t *testing.T) {
	slack, cl := "xoxb-123456789012-AbCdEfGhIjKlMn", secretpattest.Classic()
	for in, want := range map[string]string{
		slack + "_PASSWORD: hunter2":                             redactedMark + "_PASSWORD: " + redactedMark,
		"Authorization: Bearer abcdefghij0123-PASSWORD= proceed": "Authorization: " + redactedMark + " proceed",
		"Authorization: Bearer abcdefghij0123-PASSWORD=":         "Authorization: " + redactedMark,
		// A classic token ends at a word boundary, so the next assignment survives the mark, and
		// so does the separator in front of it, whatever the value; what is glued before the
		// separator goes with the token.
		"API_TOKEN=" + cl + "-PASSWORD='hunter2'":         "API_TOKEN=" + redactedMark + "-PASSWORD=" + redactedMark,
		"API_TOKEN=" + cl + ":NEXT_PASSWORD=hunter2":      "API_TOKEN=" + redactedMark + ":NEXT_PASSWORD=" + redactedMark,
		"API_TOKEN=" + cl + ";PASSWORD=hunter2":           "API_TOKEN=" + redactedMark + ";PASSWORD=" + redactedMark,
		"API_TOKEN=" + cl + ",PASSWORD=hunter2":           "API_TOKEN=" + redactedMark + ",PASSWORD=" + redactedMark,
		"API_TOKEN=" + cl + ":private;PASSWORD=hunter2 x": "API_TOKEN=" + redactedMark + ";PASSWORD=" + redactedMark + " x",
		"API_TOKEN=" + cl + ":private,COUNT=42":           "API_TOKEN=" + redactedMark + ",COUNT=42",
		cl + ".NEXT_API_TOKEN=short":                      redactedMark + ".NEXT_API_TOKEN=short",
		cl + ".COUNT=42":                                  redactedMark + ".COUNT=42",
		cl + ".COUNT =42":                                 redactedMark + ".COUNT =42",
		"--token=" + cl + "-count=42 x":                   "--token=" + redactedMark + "-count=42 x",
		// The separator steps back over punctuation only, never into the value.
		"API_TOKEN=" + cl + ":secret-PASSWORD=hunter2": "API_TOKEN=" + redactedMark + "-PASSWORD=" + redactedMark,
		"API_TOKEN=" + cl + ":秘密-PASSWORD=hunter2":     "API_TOKEN=" + redactedMark + "-PASSWORD=" + redactedMark,
		"API_TOKEN=" + cl + ":秘密;PASSWORD=hunter2":     "API_TOKEN=" + redactedMark + ";PASSWORD=" + redactedMark,
		// A shell metacharacter starts another command, which the value does not take.
		"API_TOKEN=" + cl + ";printf done":   "API_TOKEN=" + redactedMark + ";printf done",
		"API_TOKEN=" + cl + ":x&&echo ok":    "API_TOKEN=" + redactedMark + "&&echo ok",
		"API_TOKEN=" + cl + ":x|tee out.log": "API_TOKEN=" + redactedMark + "|tee out.log",
		// Inside quotes a metacharacter or a comma is the value's own; outside, a comma starts
		// another field.
		"API_TOKEN='" + cl + ":part&tail' next":      "API_TOKEN=" + redactedMark + " next",
		"API_TOKEN=\"" + cl + ":a,b\" next":          "API_TOKEN=" + redactedMark + " next",
		"API_TOKEN=" + cl + ",OK,42":                 "API_TOKEN=" + redactedMark + ",OK,42",
		"API_TOKEN=" + cl + ":a,OK,42":               "API_TOKEN=" + redactedMark + ",OK,42",
		"API_TOKEN=\"" + cl + ":a,mode=debug\" next": "API_TOKEN=" + redactedMark + " next",
		"API_TOKEN=" + cl + ":a,mode=debug next":     "API_TOKEN=" + redactedMark + ",mode=debug next",
		"API_TOKEN=" + cl + ":a-mode=debug next":     "API_TOKEN=" + redactedMark + "-mode=debug next",
		// What the entropy scan takes between marks stays taken where a tail stops early: here
		// the run after the token is one high-entropy run on main, `-code=9a3Z` included.
		"API_TOKEN=" + cl + ":aB3xK9mP2qR7sT1vW5yZ8nQ4-code=9a3Z": "API_TOKEN=" + redactedMark + redactedMark,
		cl + "+aB3xK9mP2qR7sT1vW5yZ8nQ4-code=9a3Z":                redactedMark + redactedMark,
		"echo " + cl + "+private-count=42":                        "echo " + redactedMark + "-count=42",
	} {
		if got := ScrubSecrets(in); got != want {
			t.Errorf("ScrubSecrets(%q) = %q, want %q", in, got, want)
		}
	}
}

// A run of joined tokens is scanned once, not once per token. Measured on 840 KB: 0.3 s with
// one scan, 99 s with a scan per token; the bound leaves a busy host room.
func TestScrubSecretsJoinedTokensAreLinear(t *testing.T) {
	in := strings.Repeat("AKIAIOSFODNN7EXAMPLE.", 40000)
	start := time.Now()
	if got := ScrubSecrets(in); got != redactedMark+"." {
		t.Errorf("ScrubSecrets(joined) = %.80q", got)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("ScrubSecrets(joined) took %s", d)
	}
}

// The buffers are sized for what they hold: a body that is one key or one entropy run costs
// about its mark, and the mark returned holds no buffer the size of the body. Measured on
// 1 MiB: 17 KB allocated, 2.1 MB with buffers of the input's size.
func TestScrubSecretsAllocatesForTheOutput(t *testing.T) {
	// Built at run time so the literal does not trip the secret scan.
	pk := "PRIVATE " + "KEY-----"
	for name, in := range map[string]string{
		"key":     "-----BEGIN RSA " + pk + "\n" + strings.Repeat("MIIEowIBAAKCAQEA0Z3VS5JJcds3xfn/ygWyF8PbnGy0AHB7MaW\n", 20000) + "-----END RSA " + pk,
		"entropy": strings.Repeat("aB3xK9mP2qR7sT1vW5yZ8nQ4", 40000),
	} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got := ScrubSecrets(in)
		runtime.ReadMemStats(&after)
		if got != redactedMark {
			t.Fatalf("%s: ScrubSecrets = %.80q", name, got)
		}
		if n := after.TotalAlloc - before.TotalAlloc; n > uint64(len(in))/4 {
			t.Errorf("%s: ScrubSecrets(%d bytes) allocated %d bytes", name, len(in), n)
		}
	}
}

// Names are looked for only where a span reaches past a mark, and only the first after each
// mark is kept. Measured on 256 KB of `a=` after one token: 0.5 MB allocated, 4.1 MB with every
// name in the body kept, 7.8 MB with every name glued to the token kept.
func TestScrubSecretsCollectsNamesOnlyNearMarks(t *testing.T) {
	cl := secretpattest.Classic()
	for name, in := range map[string]string{
		"spaced": cl + " " + strings.Repeat("a=1 ", 64000),
		"glued":  cl + "+" + strings.Repeat("a=", 128000),
	} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got := ScrubSecrets(in)
		runtime.ReadMemStats(&after)
		if want := redactedMark + in[len(cl):]; got != want {
			t.Fatalf("%s: ScrubSecrets = %.80q", name, got)
		}
		if n := after.TotalAlloc - before.TotalAlloc; n > 3*uint64(len(in)) {
			t.Errorf("%s: ScrubSecrets(%d bytes) allocated %d bytes", name, len(in), n)
		}
	}
}

// Without a known token nothing reaches past a mark, so ScrubSecrets keeps the output of the
// sequential assignment-then-entropy scan, byte for byte, two touching marks included.
func TestScrubSecretsWithoutKnownTokensKeepsSequentialOutput(t *testing.T) {
	tokenRe := regexp.MustCompile(`[A-Za-z0-9+/=_.\-]{20,}`)
	sequential := func(s string) string {
		s = envAssignRe.ReplaceAllString(s, `$1$2`+redactedMark)
		return tokenRe.ReplaceAllStringFunc(s, func(tok string) string {
			if looksSecret(tok) {
				return redactedMark
			}
			return tok
		})
	}
	opaque := "aB3xK9mP2qR7sT1vW5yZ8nQ4"
	for _, in := range []string{
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY next line",
		"AWS2_SECRET_ACCESS_KEY=wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY.",
		"PASSWORD=hunter2 " + opaque + " and DB_PASS: 'hunter22'.",
		"API_KEY=\"" + opaque + "\" " + opaque + " then MY_TOKEN = short",
		"opaque " + opaque + ". Done, and " + opaque + "-" + opaque + "/x",
		"internationalization and counterrevolutionary are long words.",
		"see docs/user_guide.md and release-notes_2026.11.txt, PASS=abcdef",
		"SECRET_KEY=" + opaque + "SECRET_KEY=" + opaque,
		"API_KEY=\"" + opaque + "\"" + opaque + " and A1B2C3D4E5F6G7H8TOKEN=abcdef",
		"x KEY=abcdef\nKEY=abcdef PASSWORD='a b' PWD=\"" + opaque + "'",
	} {
		if got, want := ScrubSecrets(in), sequential(in); got != want {
			t.Errorf("ScrubSecrets(%q) = %q, want %q", in, got, want)
		}
	}
}
