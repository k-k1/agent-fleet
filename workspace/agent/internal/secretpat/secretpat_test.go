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
	toks := map[string]string{
		"classic":               secretpattest.Classic(),
		"stateless":             secretpattest.Stateless(),
		"stateless-early-punct": secretpattest.StatelessEarlyPunct(),
	}
	frames := []string{"%s", "token: %s. Next sentence", "https://x-access-token:%s@github.com/o/r.git", `"%s"`, "GH_TOKEN=%s\n", "(%s)"}
	for _, min := range []int{16, 20, GitHubTokenMinBody} {
		re := regexp.MustCompile(GitHubTokenPattern(min))
		for name, tok := range toks {
			for _, f := range frames {
				in := strings.Replace(f, "%s", tok, 1)
				if got := re.FindString(in); got != tok {
					t.Errorf("min %d, %s in %q: matched %q, want the whole token (%d chars)", min, name, f, got, len(tok))
				}
				if out := re.ReplaceAllString(in, "***"); secretpattest.Leftover(out, tok) != "" {
					t.Errorf("min %d, %s in %q: %q survives redaction", min, name, f, secretpattest.Leftover(out, tok))
				}
			}
		}
	}
	if got := GitHubToken.String(); got != GitHubTokenPattern(GitHubTokenMinBody) {
		t.Errorf("GitHubToken = %s, want the pattern at GitHubTokenMinBody", got)
	}
}

// TestFixturesBeatTheOldPattern proves the fixtures exercise the change: the alphanumeric-only
// pattern every site used before either misses them or leaves part of them in clear.
func TestFixturesBeatTheOldPattern(t *testing.T) {
	old := regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{16,}\b`)
	for name, tok := range map[string]string{"stateless": secretpattest.Stateless(), "stateless-early-punct": secretpattest.StatelessEarlyPunct()} {
		if secretpattest.Leftover(old.ReplaceAllString(tok, "***"), tok) == "" {
			t.Errorf("%s: the old pattern redacts it whole, so it proves nothing", name)
		}
	}
	if old.FindString(secretpattest.StatelessEarlyPunct()) != "" {
		t.Error("stateless-early-punct: the old pattern finds it, so it does not cover the miss")
	}
	if !strings.Contains(secretpattest.Stateless(), ".") || len(secretpattest.Stateless()) < 400 {
		t.Errorf("stateless fixture is not JWT-shaped: %d chars", len(secretpattest.Stateless()))
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
		t.Errorf("a token glued to a word matched %q", m)
	}
	tok := secretpattest.Classic()
	for in, want := range map[string]string{
		tok + "...":     tok,
		tok + "-":       tok + "-",
		tok + "_suffix": tok + "_suffix",
	} {
		if got := GitHubToken.FindString(in); got != want {
			t.Errorf("FindString(%q) = %q, want %q", in, got, want)
		}
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
