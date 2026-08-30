package projcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddIgnorePatternExclude(t *testing.T) {
	dir := newGitRepo(t)

	if err := AddIgnorePattern(dir, IgnoreExclude, ".mcp.json"); err != nil {
		t.Fatal(err)
	}
	common, err := GitCommonDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(common, "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != ".mcp.json\n" && !containsLine(string(b), ".mcp.json") {
		t.Fatalf("pattern not written: %q", b)
	}

	// Idempotent: adding again must not duplicate.
	if err := AddIgnorePattern(dir, IgnoreExclude, ".mcp.json"); err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(filepath.Join(common, "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if countLines(string(b2), ".mcp.json") != 1 {
		t.Fatalf("pattern duplicated: %q", b2)
	}
}

func TestAddIgnorePatternExcludeIsCommonDirNotWorktree(t *testing.T) {
	parent := newGitRepo(t)
	if err := os.WriteFile(filepath.Join(parent, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, parent, "add", "a.txt")
	runGit(t, parent, "commit", "-q", "-m", "x")
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, parent, "worktree", "add", "-q", "-b", "side", wt)

	if err := AddIgnorePattern(wt, IgnoreExclude, ".mcp.json"); err != nil {
		t.Fatal(err)
	}
	// docs/log/56 §2.4: .git/info/exclude is the COMMON dir — the pattern must land
	// under the PARENT's .git, not any per-worktree location.
	b, err := os.ReadFile(filepath.Join(parent, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("expected pattern in parent's common dir: %v", err)
	}
	if !containsLine(string(b), ".mcp.json") {
		t.Fatalf("pattern missing: %q", b)
	}
}

func TestAddIgnorePatternGitignoreCreatesAndPreservesTrailingNewline(t *testing.T) {
	dir := newGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules"), 0o644); err != nil { // no trailing newline
		t.Fatal(err)
	}
	if err := AddIgnorePattern(dir, IgnoreGitignore, ".mcp.json"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	want := "node_modules\n.mcp.json\n"
	if string(b) != want {
		t.Fatalf("got %q want %q", b, want)
	}
}

func TestAddIgnorePatternNoMarkerComment(t *testing.T) {
	dir := newGitRepo(t)
	if err := AddIgnorePattern(dir, IgnoreGitignore, ".mcp.json"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if containsLine(string(b), "#") {
		t.Fatalf("expected no marker comment: %q", b)
	}
}

func containsLine(body, line string) bool {
	for _, l := range strings.Split(body, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

func countLines(body, line string) int {
	n := 0
	for _, l := range strings.Split(body, "\n") {
		if l == line {
			n++
		}
	}
	return n
}
