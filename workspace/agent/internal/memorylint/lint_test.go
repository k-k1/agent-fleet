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

// A qualified snake_case name and a call are symbols even though their tail is lowercase; a
// file name (store.json, x.go) is not.
func TestQualifiedSymbolsAreChecked(t *testing.T) {
	body := "`pkg.missing_func` `pkg.removed()` `store.json` `agent_memory.go` `pkg.Exists`"
	r := testRepo
	r.symbols = map[string]bool{"Exists": true}
	rep := Lint([]Memory{{"m", body}}, Options{}, r)
	if got := subjects(rep, "m", CheckMissingSym); !sameSet(got, []string{"missing_func", "removed"}) {
		t.Fatalf("missing-symbol = %v", got)
	}
}

// fileRepo is a fakeRepo that also serves file contents, as a revision reader does.
type fileRepo struct {
	fakeRepo
	files map[string]string
	err   error
}

func (r fileRepo) ReadFile(p string) ([]byte, error) {
	if r.err != nil {
		return nil, r.err
	}
	return []byte(r.files[p]), nil
}

var shortRefRepo = fakeRepo{paths: []string{
	"docs/log/99-lcpp-agent-kind.md",
	"docs/log/007-old.md",
	"docs/build/92-worktree.md",
	"docs/build/10-development.md",
	"guide/admin/03-setup.md",
	"guide/admin/03-setup.ja.md",
	"docs/README.md",
}}

// A numbered short reference finds its "<number>-*" entry; a number with no entry, a wrong
// directory and a non-numeric tail are still reported.
func TestNumberedShortReferences(t *testing.T) {
	body := "docs/log/99 docs/log/099 docs/log/7 docs/build/92 guide/admin/03 guide/admin/NN docs/51x docs/log/NN docs/99\n" +
		"negative: docs/log/9999 docs/build/93 guide/admin/04 docs/log/99x docs/log/9-9 guide/nowhere/03 docs/build/1 docs/log/9"
	rep := Lint([]Memory{{"m", body}}, Options{}, shortRefRepo)
	want := []string{"docs/51x", "docs/log/9999", "docs/build/93", "guide/admin/04", "docs/log/99x", "docs/log/9-9", "guide/nowhere/03", "docs/build/1", "docs/log/9"}
	if got := subjects(rep, "m", CheckMissingPath); !sameSet(got, want) {
		t.Fatalf("missing-path = %v, want %v", got, want)
	}
}

// "docs/<number>" is a journal number without "log/": it resolves in docs/log, and a number with
// no journal is reported.
func TestBareDocsNumberResolvesToJournal(t *testing.T) {
	rep := Lint([]Memory{{"m", "docs/99 docs/7 docs/51"}}, Options{}, shortRefRepo)
	if got := subjects(rep, "m", CheckMissingPath); !sameSet(got, []string{"docs/51"}) {
		t.Fatalf("missing-path = %v", got)
	}
}

const testIgnore = "node_modules\n/dist/\n*.log\n!keep.log\nbuild/\ndocs/tmp\n"

var ignoreRepo = fileRepo{
	fakeRepo: fakeRepo{paths: []string{
		".gitignore",
		"console/.gitignore",
		"console/src/app.tsx",
		"console/src/dist/tracked.go",
		"keep/keep.log",
		"docs/README.md",
		"tools/x.go",
	}},
	files: map[string]string{
		".gitignore":         testIgnore,
		"console/.gitignore": "dist\n!src/dist\n",
	},
}

// Paths the revision's .gitignore files ignore are skipped; everything else is still reported.
func TestGitIgnoredPathsAreSkipped(t *testing.T) {
	body := "console/node_modules/react/index.js console/dist console/dist/assets/a.js console/build/out docs/tmp docs/x.log docs/keep.log " +
		"/ negative: console/src/gone.tsx tools/gone.go docs/gone.md console/src/dist/gone.go tools/dist"
	rep := Lint([]Memory{{"m", body}}, Options{}, ignoreRepo)
	// docs/keep.log is re-included by the negation; tools/dist is not ignored by "/dist/" (root only).
	want := []string{"docs/keep.log", "console/src/gone.tsx", "tools/gone.go", "docs/gone.md", "console/src/dist/gone.go", "tools/dist"}
	if got := subjects(rep, "m", CheckMissingPath); !sameSet(got, want) {
		t.Fatalf("missing-path = %v, want %v", got, want)
	}
}

// The rules come from the repository reader only: without FileReader nothing is skipped, and an
// unreadable .gitignore is said so in Skipped and skips nothing.
func TestGitIgnoreNeedsReadableRevisionRules(t *testing.T) {
	rep := Lint([]Memory{{"m", "console/node_modules/x"}}, Options{}, ignoreRepo.fakeRepo)
	if got := subjects(rep, "m", CheckMissingPath); len(got) != 1 {
		t.Fatalf("plain Repo: %v", got)
	}
	bad := ignoreRepo
	bad.err = errors.New("boom")
	rep = Lint([]Memory{{"m", "console/node_modules/x"}}, Options{}, bad)
	if got := subjects(rep, "m", CheckMissingPath); len(got) != 1 {
		t.Fatalf("unreadable rules: %v", got)
	}
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0], "boom") {
		t.Fatalf("skipped = %v", rep.Skipped)
	}
}

// Nothing below an ignored directory is re-included, while a negation under a non-ignored parent
// works.
func TestIgnoreCannotReincludeUnderIgnoredDir(t *testing.T) {
	set := &ignoreSet{files: []ignoreFile{{rules: parseIgnore("build/\n!build/keep\n*.log\n!keep.log\n")}}}
	for _, c := range []struct {
		p   string
		ign bool
	}{{"build/keep", true}, {"build/keep/x", true}, {"sub/keep.log", false}, {"sub/other.log", true}} {
		if got := set.ignored(c.p, false); got != c.ign {
			t.Errorf("ignored(%q) = %v, want %v", c.p, got, c.ign)
		}
	}
}

func TestParseIgnorePatterns(t *testing.T) {
	set := &ignoreSet{files: []ignoreFile{{rules: parseIgnore("# c\n\n/a/**/z\n**/deep\nx[0-9]?.o\n\\#hash\ntrail \n")}}}
	cases := []struct {
		p   string
		dir bool
		ign bool
	}{
		{"a/b/c/z", false, true}, {"a/z", false, true}, {"b/a/z", false, false},
		{"q/r/deep", true, true}, {"deep", false, true},
		{"x1a.o", false, true}, {"x1.o", false, false},
		{"#hash", false, true}, {"trail", false, true},
	}
	for _, c := range cases {
		if got := set.ignored(c.p, c.dir); got != c.ign {
			t.Errorf("ignored(%q) = %v, want %v", c.p, got, c.ign)
		}
	}
}

type proposalRepo struct {
	fileRepo
	keys    []RemovedKey
	keysErr error
}

func (r proposalRepo) RemovedKeys() ([]RemovedKey, error) { return r.keys, r.keysErr }

func (r proposalRepo) FindSymbols(syms []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, s := range syms {
		out[s] = r.symbols[s]
	}
	return out, nil
}

var proposalFixture = proposalRepo{
	fileRepo: fileRepo{
		fakeRepo: fakeRepo{
			paths: []string{
				"docs/decisions/0027-operator-interaction-graph.md",
				"docs/decisions/0027-operator-interaction-graph.ja.md",
				"docs/decisions/0117-background-memory-review.md",
				"docs/decisions/0130-cultivated-memory.md",
				"docs/decisions/0140-unused-old-thing.md",
				"docs/decisions/0150-single.md",
			},
			symbols: map[string]bool{"AF_STILL_HERE": true},
		},
		files: map[string]string{
			"docs/decisions/0027-operator-interaction-graph.md": "# 0027. x\n\n- Status: **superseded (2026-09-20, by [0096](0096-fleet-session-graph.md))**. Only Phase 0.\n",
			"docs/decisions/0117-background-memory-review.md":   "# 0117\n\n- Status: **accepted, mostly superseded by [0130](x.md)**\n",
			"docs/decisions/0130-cultivated-memory.md":          "# 0130\n\n- Status: accepted\n",
			"docs/decisions/0140-unused-old-thing.md":           "# 0140\n\n- Status: superseded by 0141\n",
			"docs/decisions/0150-single.md":                     "# 0150\n\n- Status: superseded by 0151\n",
		},
	},
	keys: []RemovedKey{
		{"AF_OLD_SWITCH", "abc1234", "remove the old switch"},
		{"AF_STILL_HERE", "def5678", "touch it"},
		{"AF_UNUSED_KEY", "0123abc", "drop"},
	},
}

// Proposals carry evidence and are only the candidates a memory uses; a partly superseded ADR,
// a key still in the tree, an unused candidate, a one-word name, a listed or a rejected term
// are not proposed.
func TestRetiredTermProposals(t *testing.T) {
	mems := []Memory{
		{"a", "the Operator Interaction Graph view; set AF_OLD_SWITCH=1 and AF_STILL_HERE"},
		{"b", "background memory review is mostly alive; AF_OLD_SWITCH_V2 is another key; single"},
	}
	rep := Lint(mems, Options{}, proposalFixture)
	if len(rep.Proposals) != 2 {
		t.Fatalf("proposals = %+v", rep.Proposals)
	}
	p0, p1 := rep.Proposals[0], rep.Proposals[1]
	if p0.Term != "AF_OLD_SWITCH" || p0.Kind != ProposalRemovedKey || !strings.Contains(p0.Evidence, "abc1234") || len(p0.Memories) != 1 || p0.Memories[0] != "a" {
		t.Errorf("key proposal = %+v", p0)
	}
	if p1.Term != "operator interaction graph" || p1.Kind != ProposalSupersededADR ||
		!strings.Contains(p1.Evidence, "ADR 27") || !strings.Contains(p1.Evidence, "superseded (2026-09-20") {
		t.Errorf("adr proposal = %+v", p1)
	}
	// Listed and rejected terms drop out, case-insensitively; a proposal never edits the list.
	rep = Lint(mems, Options{RetiredTerms: []string{"af_old_switch"}, RejectedTerms: []string{"Operator Interaction Graph"}}, proposalFixture)
	if len(rep.Proposals) != 0 {
		t.Errorf("listed/rejected still proposed: %+v", rep.Proposals)
	}
	// A repository that cannot name its history or read files proposes nothing and says why.
	bad := proposalFixture
	bad.keysErr = errors.New("git log timed out")
	bad.fileRepo.err = errors.New("boom")
	rep = Lint(mems, Options{}, bad)
	if len(rep.Proposals) != 0 || len(rep.Skipped) < 2 {
		t.Errorf("proposals = %+v skipped = %v", rep.Proposals, rep.Skipped)
	}
	// Without the optional interfaces nothing is proposed and nothing is skipped.
	rep = Lint(mems, Options{}, proposalFixture.fakeRepo)
	if len(rep.Proposals) != 0 || len(rep.Skipped) != 0 {
		t.Errorf("plain repo: %+v %v", rep.Proposals, rep.Skipped)
	}
}
