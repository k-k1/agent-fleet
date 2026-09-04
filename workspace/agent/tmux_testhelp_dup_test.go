package main

// The three tmux isolation helpers (`paneShowing` / `isolatedTmuxSocket` / `isolateAgentState`,
// 60 lines in total) exist twice with identical contents: in package main and in
// internal/sessionx.
//
// They cannot be merged. Go cannot share test helpers (the contents of `_test.go`) across
// packages, and the only way to share them would be to export test-only code from a production
// package - which undoes the point of splitting internal out. Two copies is the correct design.
//
// The danger is the copies drifting apart, and when they do, both still compile and the whole
// suite stays green. What happens then has been measured: `isolateAgentState` is the isolation
// that keeps a test from materializing into the real `~/.claude` and friends, so when one copy
// falls behind, the tests on that side read and write the developer's real configuration.
//
// So this check has exactly one job: go red when they drift. The fix is to make the same change
// in both copies; which one is the original does not matter, only byte equality is required.
//
// Relative paths resolve from where this check lives (a package main test runs with
// workspace/agent as cwd, so `internal/sessionx/...` reaches). Following README §4, "judge by
// the resolved result, not by the pattern": an unreadable file is a Fatal, not a Skip.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// The two files holding the copies. Either one may serve as the original.
const (
	tmuxHelpersMain     = "session_rate_limit_state_test.go"
	tmuxHelpersSessionx = "internal/sessionx/testhelp_test.go"
)

// The helpers that must be identical on both sides.
var tmuxSharedHelpers = []string{"paneShowing", "isolatedTmuxSocket", "isolateAgentState"}

// funcSource returns the exact source bytes of a top-level func, comments excluded
// (the declaration only), so the comparison is of the code the two copies share.
func funcSource(t *testing.T, path, name string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		// Not a Skip: if a move changes the path, the check degrades into "unreadable, so
		// nothing is checked" and disappears without a word.
		t.Fatalf("cannot read %s (if a move changed the path, fix the constants in this check too): %v", path, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Name.Name != name {
			continue
		}
		return string(src[fset.Position(fd.Pos()).Offset:fset.Position(fd.End()).Offset])
	}
	t.Fatalf("%s has no %s (if it was renamed, fix tmuxSharedHelpers too)", path, name)
	return ""
}

func TestTmuxTestHelpersStayInSync(t *testing.T) {
	if len(tmuxSharedHelpers) == 0 {
		t.Fatal("zero helpers to compare - this check has gone silent")
	}
	for _, name := range tmuxSharedHelpers {
		a := funcSource(t, tmuxHelpersMain, name)
		b := funcSource(t, tmuxHelpersSessionx, name)
		if a == b {
			continue
		}
		t.Errorf("the copies of %s have drifted: the contents differ between %s and %s.\n"+
			"They cannot be merged into one (Go cannot share test helpers across packages), so "+
			"make the same change in both and bring them back in sync.\n--- %s ---\n%s\n--- %s ---\n%s",
			name, tmuxHelpersMain, tmuxHelpersSessionx, tmuxHelpersMain, a, tmuxHelpersSessionx, b)
	}
}
