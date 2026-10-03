package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testguardUnguarded lists the test binaries that do not run under testguard, each with the
// reason. Anything else that has tests and no testguard.Run is a red: a package that cannot
// reach tmux today can after one import, and the guard costs a temp dir per run.
var testguardUnguarded = map[string]string{
	// Kept byte-for-byte with control-plane's copy, file-name set included
	// (wiretest_dup_test.go), and that module cannot import this one. Its tests compare
	// encodings in memory and start no process.
	"internal/wiretest": "shared with control-plane, file-name set pinned; pure in-memory checks",
}

// TestEveryTestBinaryIsGuarded checks by machine that every package with tests runs them
// through testguard.Run (so no test, or goroutine a test left behind, can type into the
// workspace's live tmux panes or write into its real HOME), and that testguard itself never
// reaches the product binary.
func TestEveryTestBinaryIsGuarded(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("go is missing, so the guard's coverage cannot be checked: %v", err)
	}
	mod, err := exec.Command("go", "list", "-m").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	modPath := strings.TrimSpace(string(mod))
	guard := modPath + "/internal/testguard"

	// Not in any main package's dependency graph, and imported by no non-test file.
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".", "./internal/msp/gen").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 10 {
		t.Fatalf("go list -deps returned %d packages: the check is not measuring anything", len(deps))
	}
	for _, d := range deps {
		if d == guard {
			t.Errorf("%s is in a product binary's dependency graph: it must be imported from _test.go files only", guard)
		}
	}

	lst, err := exec.Command("go", "list", "-f",
		"{{.ImportPath}}|{{.Dir}}|{{join .Imports \" \"}}|{{join .TestGoFiles \" \"}} {{join .XTestGoFiles \" \"}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list ./...: %v", err)
	}
	guarded := 0
	for _, line := range strings.Split(strings.TrimSpace(string(lst)), "\n") {
		f := strings.SplitN(line, "|", 4)
		if len(f) != 4 {
			t.Fatalf("unexpected go list line %q", line)
		}
		pkg, dir, imports, tests := f[0], f[1], strings.Fields(f[2]), strings.Fields(f[3])
		for _, im := range imports {
			if im == guard {
				t.Errorf("%s imports %s from non-test code", pkg, guard)
			}
		}
		if len(tests) == 0 {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(pkg, modPath), "/")
		if _, ok := testguardUnguarded[rel]; ok {
			continue
		}
		call := "testguard.Run("
		if pkg == guard {
			call = "Run("
		}
		found := false
		for _, name := range tests {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			src := string(b)
			if i := strings.Index(src, "\nfunc TestMain(m *testing.M) {"); i >= 0 && strings.Contains(src[i:], call) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s has tests but no TestMain calling %s: add one (see internal/testguard), or list it in testguardUnguarded with the reason", pkg, call)
			continue
		}
		guarded++
	}
	if guarded < 10 {
		t.Fatalf("only %d guarded packages found: the check is not measuring anything", guarded)
	}
	for rel := range testguardUnguarded {
		if _, err := os.Stat(rel); err != nil {
			t.Errorf("testguardUnguarded names %s, which no longer exists: %v", rel, err)
		}
	}
	t.Logf("%d test binaries run under testguard", guarded)
}
