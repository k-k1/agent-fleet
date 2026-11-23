package secretpat

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secretpat/secretpattest"
)

func TestGitHubTokenMatchesWholeToken(t *testing.T) {
	frames := []string{"%s", "token: %s. Next sentence", "https://x-access-token:%s@github.com/o/r.git", `"%s"`, "GH_TOKEN=%s\n", "(%s)", "%s.example"}
	for _, min := range []int{16, 20, GitHubTokenMinBody} {
		re := regexp.MustCompile(GitHubTokenPattern(min))
		for name, tok := range secretpattest.All() {
			fs := append([]string(nil), frames...)
			if strings.HasPrefix(name, "stateless") {
				// The stateless branch has no leading boundary, so a token glued to a word is
				// still one. A `_suffix` is not tested: a JWT signature may hold `_`, so a
				// stateless match runs on through it, which over-redacts on the safe side.
				fs = append(fs, "cache_%s", "x%s")
			} else {
				fs = append(fs, "%s_suffix")
			}
			for _, f := range fs {
				in := strings.Replace(f, "%s", tok, 1)
				if got := re.FindString(in); got != tok {
					t.Errorf("min %d, %s in %q: matched %d of %d chars", min, name, f, len(got), len(tok))
				}
				if out := re.ReplaceAllString(in, "***"); out != strings.Replace(f, "%s", "***", 1) {
					t.Errorf("min %d, %s in %q: redacted to %.80q", min, name, f, out)
				}
			}
		}
	}
	if got := GitHubToken.String(); got != GitHubTokenPattern(GitHubTokenMinBody) {
		t.Errorf("GitHubToken = %s, want the pattern at GitHubTokenMinBody", got)
	}
	for name, tok := range secretpattest.All() {
		stateless := strings.HasPrefix(name, "stateless")
		if got := GitHubStateless.FindString(tok) == tok; got != stateless {
			t.Errorf("%s: GitHubStateless whole match = %v", name, got)
		}
		if !stateless && GitHubClassic.FindString(tok) != tok {
			t.Errorf("%s: GitHubClassic does not match it whole", name)
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
	for _, tok := range []string{secretpattest.Stateless(), secretpattest.StatelessEarlyPunct()} {
		if m := old.FindString(tok); m != "" {
			t.Errorf("the old pattern finds %.12s…, so the fixture does not cover the miss", tok)
		}
	}
	if !strings.Contains(secretpattest.StatelessZeros(), "_100000_") {
		t.Error("the zeros fixture lost its APPID")
	}
}

func TestGitHubTokenBoundaries(t *testing.T) {
	re := regexp.MustCompile(GitHubTokenPattern(16))
	for _, s := range secretpattest.Benign {
		if m := re.FindString(s); m != "" {
			t.Errorf("benign %q matched %q", s, m)
		}
	}
	if m := re.FindString("a" + secretpattest.Classic()); m != "" {
		t.Errorf("a classic token glued to a word matched %q", m)
	}
}

// TestCopiesAreIdentical keeps control-plane's and workspace/agent's copies of this package
// byte-identical apart from the module path in the import above.
func TestCopiesAreIdentical(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := wd
	for {
		if _, err := os.Stat(filepath.Join(root, "control-plane", "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(root, "workspace", "agent", "go.mod")); err == nil {
				break
			}
		}
		up := filepath.Dir(root)
		if up == root {
			t.Fatalf("no repository root above %s", wd)
		}
		root = up
	}
	norm := func(b []byte) []byte {
		b = bytes.ReplaceAll(b, []byte("agent-fleet/control-plane/"), []byte("agent-fleet/MODULE/"))
		return bytes.ReplaceAll(b, []byte("agent-fleet/workspace/agent/"), []byte("agent-fleet/MODULE/"))
	}
	for _, f := range []string{"github.go", "secretpat_test.go", filepath.Join("secretpattest", "secretpattest.go")} {
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
