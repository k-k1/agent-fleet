package memorylint

import (
	"errors"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestImportsNoAFPackage fails when a non-test file of this package imports any Agent Fleet
// package: the engine has to be movable to its own module (ADR 0130 decision 8).
func TestImportsNoAFPackage(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files found: %v", err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(p, "github.com/k-k1/") {
				t.Errorf("%s imports %s: the lint engine must import no AF package", f, p)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no non-test file was examined")
	}
}

type fakeRepo struct {
	paths   []string
	symbols map[string]bool
	symErr  error
	pathErr error
}

func (r fakeRepo) Paths() ([]string, error) { return r.paths, r.pathErr }
func (r fakeRepo) FindSymbols(syms []string) (map[string]bool, error) {
	if r.symErr != nil {
		return nil, r.symErr
	}
	out := map[string]bool{}
	for _, s := range syms {
		out[s] = r.symbols[s]
	}
	return out, nil
}

func subjects(rep Report, memory, check string) []string {
	var out []string
	for _, f := range rep.Findings {
		if f.Memory == memory && f.Check == check {
			out = append(out, f.Subject)
		}
	}
	return out
}

var testRepo = fakeRepo{
	paths: []string{
		"docs/decisions/0130-cultivated-memory.md",
		"workspace/agent/internal/memoryx/agent_memory.go",
		"console/src/app.tsx",
		"README.md",
	},
	symbols: map[string]bool{"agentMemSave": true, "TestAgentMemorySaves": true},
}

// One memory built to fail each check is reported by it; the same memory with the defect fixed
// is not.
func TestEachCheckHasAPositiveControl(t *testing.T) {
	mems := []Memory{
		{"target", "ok"},
		{"links", "see [[target]] and [[ghost]] and [[ghost|alias]]\n[[ -f x ]] is shell"},
		{"paths", "edit `workspace/agent/internal/memoryx/agent_memory.go` and workspace/agent/internal/memoryx/gone.go:12.\nsee https://example.com/docs/nowhere.md and /usr/docs/x.go and and/or"},
		{"syms", "calls `agentMemSave` and `agentMemVanished` and `plain` and `store.json`; run TestAgentMemorySaves and TestGoneAway"},
		{"adrs", "per ADR 0130 and ADR 9999, also ADR-0007"},
		{"terms", "the Muse import\nuses retired-thing here; a museum is fine; 旧設定 too"},
		{"big", strings.Repeat("x", DefaultMaxBytes+1)},
		{"edge", strings.Repeat("x", DefaultMaxBytes)},
	}
	rep := Lint(mems, Options{RetiredTerms: []string{"muse", "retired-thing", "旧設定", "  "}}, testRepo)
	if rep.Checked != len(mems) {
		t.Fatalf("checked = %d", rep.Checked)
	}
	cases := []struct {
		mem, check string
		want       []string
	}{
		{"links", CheckBrokenLink, []string{"ghost"}},
		{"target", CheckBrokenLink, nil},
		{"paths", CheckMissingPath, []string{"workspace/agent/internal/memoryx/gone.go"}},
		{"syms", CheckMissingSym, []string{"TestGoneAway", "agentMemVanished"}},
		{"adrs", CheckMissingADR, []string{"ADR 7", "ADR 9999"}},
		{"terms", CheckRetiredTerm, []string{"muse", "retired-thing", "旧設定"}},
		{"big", CheckOversizeBody, []string{strconv.Itoa(DefaultMaxBytes + 1)}},
		{"edge", CheckOversizeBody, nil},
	}
	for _, c := range cases {
		got := subjects(rep, c.mem, c.check)
		if !sameSet(got, c.want) {
			t.Errorf("%s/%s = %v, want %v", c.mem, c.check, got, c.want)
		}
	}
	if got := rep.Counts[CheckBrokenLink]; got != 1 {
		t.Errorf("broken-link count = %d, want 1", got)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

// A path-shaped token whose first segment is not a top-level directory of the revision is never
// reported: the prototype's false positives were mostly this.
func TestPathPrecision(t *testing.T) {
	body := "internal/memoryx/nowhere.go, foo/bar/baz, ./README.md, console/src, console/src/app.tsx#L3, console/dist/*.js, `docs/decisions/`"
	rep := Lint([]Memory{{"m", body}}, Options{}, testRepo)
	if got := subjects(rep, "m", CheckMissingPath); len(got) != 0 {
		t.Fatalf("false positives: %v", got)
	}
	rep = Lint([]Memory{{"m", "console/src/missing.tsx and workspace/gone/"}}, Options{}, testRepo)
	if got := subjects(rep, "m", CheckMissingPath); !sameSet(got, []string{"console/src/missing.tsx", "workspace/gone"}) {
		t.Fatalf("got %v", got)
	}
}

// Without a repository only the link, term and size checks run; with no ADR directory the ADR
// check does not guess.
func TestNoRepoRunsOnlyTheThreeChecks(t *testing.T) {
	body := "[[ghost]] muse console/src/gone.go `agentMemVanished` ADR 9999 " + strings.Repeat("x", DefaultMaxBytes)
	rep := Lint([]Memory{{"m", body}}, Options{RetiredTerms: []string{"muse"}}, nil)
	for _, c := range []string{CheckMissingPath, CheckMissingSym, CheckMissingADR} {
		if rep.Counts[c] != 0 {
			t.Errorf("%s ran without a repo", c)
		}
	}
	for _, c := range []string{CheckBrokenLink, CheckRetiredTerm, CheckOversizeBody} {
		if rep.Counts[c] != 1 {
			t.Errorf("%s = %d, want 1", c, rep.Counts[c])
		}
	}
	noADR := fakeRepo{paths: []string{"console/a.go"}}
	rep = Lint([]Memory{{"m", "ADR 12"}}, Options{}, noADR)
	if rep.Counts[CheckMissingADR] != 0 {
		t.Error("ADR check ran with no ADR directory")
	}
}

// A repository that cannot be read is said so in Skipped, never reported as clean.
func TestRepoFailureIsSaid(t *testing.T) {
	rep := Lint([]Memory{{"m", "console/x.go"}}, Options{}, fakeRepo{pathErr: errors.New("boom")})
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0], "boom") {
		t.Fatalf("skipped = %v", rep.Skipped)
	}
	r := testRepo
	r.symErr = errors.New("timeout")
	rep = Lint([]Memory{{"m", "`agentMemVanished`"}}, Options{}, r)
	if len(rep.Skipped) != 1 || rep.Counts[CheckMissingSym] != 0 {
		t.Fatalf("skipped = %v counts = %v", rep.Skipped, rep.Counts)
	}
}

func TestLinesAndOrder(t *testing.T) {
	rep := Lint([]Memory{{"b", "x\n[[ghost]]"}, {"a", "y\n\nmuse"}}, Options{RetiredTerms: []string{"muse"}}, nil)
	if len(rep.Findings) != 2 || rep.Findings[0].Memory != "a" || rep.Findings[0].Line != 3 || rep.Findings[1].Line != 2 {
		t.Fatalf("%+v", rep.Findings)
	}
}
