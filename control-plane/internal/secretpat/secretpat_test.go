package secretpat

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/secretpat/secretpattest"
)

// redact is what a redactor does with the detectors' shapes: every span, merged.
func redact(s string) string {
	return ReplaceSpans(s, "***", append(Spans(s, githubClassic), GitHubStatelessSpans(s)...))
}

func TestGitHubTokensMatchWholeToken(t *testing.T) {
	frames := []string{"%s", "token: %s. Next sentence", "https://x-access-token:%s@github.com/o/r.git", `"%s"`, "GH_TOKEN=%s\n", "(%s)", "%s.example", "see %s.md"}
	for name, tok := range secretpattest.All() {
		stateless := strings.HasPrefix(name, "stateless")
		fs := append([]string(nil), frames...)
		if stateless {
			// No leading boundary: a token glued to a word is still one.
			fs = append(fs, "cache_%s", "x%s")
		}
		for _, f := range fs {
			in := strings.Replace(f, "%s", tok, 1)
			if got := GitHubTokens.FindString(in); got != tok {
				t.Errorf("%s in %q: matched %d of %d chars", name, f, len(got), len(tok))
			}
			if got := GitHubTokens.FindAllStringSubmatch(in, -1); len(got) != 1 || got[0][0] != tok {
				t.Errorf("%s in %q: FindAllStringSubmatch found %d", name, f, len(got))
			}
			if out := redact(in); out != strings.Replace(f, "%s", "***", 1) {
				t.Errorf("%s in %q: redacted to %.80q", name, f, out)
			}
		}
		if IsGitHubStateless(tok) != stateless {
			t.Errorf("%s: IsGitHubStateless = %v", name, !stateless)
		}
	}
}

// Every gh?_ is a start of its own, so tokens written back to back are all found.
func TestGitHubStatelessSpansFindJoinedTokens(t *testing.T) {
	for name, tok := range secretpattest.All() {
		if !strings.HasPrefix(name, "stateless") {
			continue
		}
		for _, joined := range []string{tok + tok, tok + "-" + secretpattest.StatelessSpacedHeader()} {
			if got := redact("echo " + joined + " done"); got != "echo *** done" {
				t.Errorf("%s joined: %.120q", name, got)
			}
		}
	}
}

// TestFixturesBeatTheOldPattern proves the fixtures exercise the change: the alphanumeric-only
// pattern every site used before either misses them or leaves part of them in clear.
func TestFixturesBeatTheOldPattern(t *testing.T) {
	old := regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{16,}\b`)
	for name, tok := range secretpattest.All() {
		if !strings.HasPrefix(name, "stateless") {
			continue
		}
		if l := secretpattest.Leftover(old.ReplaceAllString(tok, "***"), tok); l == "" {
			t.Errorf("%s: the old pattern redacts it whole, so it proves nothing", name)
		}
		if len(tok) < 400 || strings.Count(tok, ".") != 2 {
			t.Errorf("%s is not JWT-shaped: %d chars", name, len(tok))
		}
	}
	if m := old.FindString(secretpattest.StatelessEarlyPunct()); m != "" {
		t.Errorf("the old pattern finds %.20q… in the early-punctuation fixture", m)
	}
	if !strings.Contains(secretpattest.StatelessZeros(), "_100000_") {
		t.Error("the zeros fixture lost its APPID")
	}
	if strings.Contains(secretpattest.StatelessSpacedHeader(), "_eyJ") {
		t.Error("the spaced-header fixture starts its JWT with eyJ")
	}
}

func TestGitHubTokenBoundaries(t *testing.T) {
	for _, s := range secretpattest.Benign {
		if m := GitHubTokens.FindString(s); m != "" {
			t.Errorf("benign %q matched %q", s, m)
		}
	}
	if m := GitHubTokens.FindString("a" + secretpattest.Classic()); m != "" {
		t.Errorf("a classic token glued to a word matched %q", m)
	}
	// A JWT signature may hold `_`, so a stateless match runs on through a glued `_suffix`:
	// over-redaction, on the safe side.
	if tok := secretpattest.Stateless(); GitHubTokens.FindString(tok+"_suffix") != tok+"_suffix" {
		t.Error("a stateless token's glued _suffix is not taken with it")
	}
	// The pattern's opening alone is not enough: the header must decode to a JSON object.
	forged := ""
	for _, s := range secretpattest.Benign {
		if strings.Contains(s, "eyJunk") {
			forged = s
		}
	}
	if !githubStateless.MatchString(forged[strings.Index(forged, "ghs_"):]) {
		t.Error("the eyJunk entry does not pass the pattern, so the decode check is untested")
	}
}

func TestRedactAllMergesOverlaps(t *testing.T) {
	a, b, c := regexp.MustCompile(`a+`), regexp.MustCompile(`ab+`), regexp.MustCompile(`c`)
	for in, want := range map[string]string{
		"":          "",
		"xyz":       "xyz",
		"xaabbbx":   "x*x",
		"aac":       "*",
		"a-c-ab":    "*-*-*",
		"xab ca bx": "x* * bx", // touching matches merge too
	} {
		if got := RedactAll(in, "*", a, b, c); got != want {
			t.Errorf("RedactAll(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGitleaksRuleIsThePattern keeps the repository's gitleaks rule for the stateless shape on
// the same pattern as the detectors.
func TestGitleaksRuleIsThePattern(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".gitleaks.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("regex = '''"+GitHubStatelessPattern+"'''\n")) {
		t.Error(".gitleaks.toml has no rule whose regex is GitHubStatelessPattern")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for root := wd; ; {
		if _, err := os.Stat(filepath.Join(root, "control-plane", "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(root, "workspace", "agent", "go.mod")); err == nil {
				return root
			}
		}
		up := filepath.Dir(root)
		if up == root {
			t.Fatalf("no repository root above %s", wd)
		}
		root = up
	}
}

// TestCopiesAreIdentical keeps control-plane's and workspace/agent's copies of this package
// byte-identical apart from the module path in the import above.
func TestCopiesAreIdentical(t *testing.T) {
	root := repoRoot(t)
	norm := func(b []byte) []byte {
		b = bytes.ReplaceAll(b, []byte("agent-fleet/control-plane/"), []byte("agent-fleet/MODULE/"))
		return bytes.ReplaceAll(b, []byte("agent-fleet/workspace/agent/"), []byte("agent-fleet/MODULE/"))
	}
	for _, f := range []string{"github.go", "secretpat_test.go", "secretpat_perf_test.go", filepath.Join("secretpattest", "secretpattest.go")} {
		cp, err1 := os.ReadFile(filepath.Join(root, "control-plane", "internal", "secretpat", f))
		ag, err2 := os.ReadFile(filepath.Join(root, "workspace", "agent", "internal", "secretpat", f))
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: %v / %v", f, err1, err2)
		}
		if !bytes.Equal(norm(cp), norm(ag)) {
			t.Errorf("%s differs between control-plane and workspace/agent", f)
		}
	}
}
