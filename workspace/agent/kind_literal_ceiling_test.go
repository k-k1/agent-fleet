package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// kindLiteralCeiling is the most kind-literal branches (a `case` or an
// ==/!= on an agent kind) allowed in non-test Go outside internal/agents.
// A PR that removes branches lowers it; nothing may raise it, because a new
// kind branch belongs behind the kind registry in internal/agents
// (ADR 0124 Phase 3).
const kindLiteralCeiling = 219

// nonAgentKinds are registered kinds that are not agents (a plain shell, an
// SSM host); ADR 0124 does not count branches on them. Every other registered
// kind is counted, so a new kind is counted unless someone lists it here.
var nonAgentKinds = []string{"shell", "ssm"}

// agentKinds is the registry minus nonAgentKinds.
func agentKinds(t *testing.T) []string {
	t.Helper()
	reg := sessionx.RegisteredKinds()
	for _, k := range nonAgentKinds {
		if !slices.Contains(reg, k) {
			t.Fatalf("nonAgentKinds lists %q, which is not registered", k)
		}
	}
	var out []string
	for _, k := range reg {
		if !slices.Contains(nonAgentKinds, k) {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		t.Fatal("the kind registry holds no agent kind")
	}
	return out
}

// kindLiteralBranch builds the expression of ADR 0124's "Measuring" section
// (counted per line, as grep -E does) for the given kinds. The kinds come
// from the registry rather than a typed list, so a new kind's branches are
// counted from the PR that registers it. Identifiers match Kind<kind> in any
// case, which covers KindOpencode/KindOpenCode and KindLcpp/KindLCPP.
func kindLiteralBranch(kinds []string) *regexp.Regexp {
	q := make([]string, len(kinds))
	for i, k := range kinds {
		q[i] = regexp.QuoteMeta(k)
	}
	k := strings.Join(q, "|")
	lit := `"(` + k + `)"`
	id := `Kind(?i:` + k + `)\b`
	return regexp.MustCompile(`(case .*(` + lit + `|` + id + `))` +
		`|((==|!=) *(` + lit + `|session\.` + id + `|` + id + `))` +
		`|((` + lit + `|` + id + `) *(==|!=))`)
}

// countKindLiteralBranches counts matching lines in the non-test .go files
// under root, skipping internal/agents and every testdata directory.
func countKindLiteralBranches(t *testing.T, root string, re *regexp.Regexp) (int, map[string]int) {
	t.Helper()
	total, perFile := 0, map[string]int{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == "testdata" || rel == "internal/agents" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if re.MatchString(line) {
				total++
				perFile[rel]++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total, perFile
}

// sessionKindConsts returns the values of the Kind* string constants in
// internal/session, the persisted kind list.
func sessionKindConsts(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join("internal", "session", "session.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Kind") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					out = append(out, v)
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no Kind* constants found in internal/session/session.go")
	}
	return out
}

func TestKindLiteralCeiling(t *testing.T) {
	kinds := agentKinds(t)
	// A kind declared as a session constant but not registered would escape the count.
	reg := sessionx.RegisteredKinds()
	for _, k := range sessionKindConsts(t) {
		if !slices.Contains(reg, k) {
			t.Errorf("session.Kind constant %q is not registered; its branches would not be counted", k)
		}
	}

	// The test runs in the package directory, which is the module root.
	n, perFile := countKindLiteralBranches(t, ".", kindLiteralBranch(kinds))
	if n == 0 {
		t.Fatal("no kind-literal branch found at all; the scan is not reading the module")
	}
	switch {
	case n > kindLiteralCeiling:
		t.Errorf("kind-literal branches outside internal/agents: %d, ceiling %d.\n"+
			"Do not branch on an agent kind here: put the behaviour behind the kind registry "+
			"(internal/agents) and look it up from there (ADR 0124 Phase 3).\nPer file: %v",
			n, kindLiteralCeiling, perFile)
	case n < kindLiteralCeiling:
		t.Logf("kind-literal branches: %d, below the ceiling %d; lower kindLiteralCeiling to %d in this PR.",
			n, kindLiteralCeiling, n)
	}
}

// TestKindLiteralBranchPattern pins what the scan counts, so a pattern edit
// that silently matches nothing cannot pass the ceiling.
func TestKindLiteralBranchPattern(t *testing.T) {
	re := kindLiteralBranch(agentKinds(t))
	for _, line := range []string{
		`	case "codex":`,
		`	case session.KindCodex, session.KindKiro:`,
		`	if kind == "claude" {`,
		`	if s.Kind != session.KindMuse {`,
		`	if "lcpp" == kind {`,
		`	if KindOpenCode == k {`,
	} {
		if !re.MatchString(line) {
			t.Errorf("not counted: %q", line)
		}
	}
	for _, line := range []string{
		`	name := "codex"`,
		`	kinds := []string{"claude", "codex"}`,
		`	case "bash":`,
		`	if kind == "shell" {`,
	} {
		if re.MatchString(line) {
			t.Errorf("counted, but is no agent-kind branch: %q", line)
		}
	}
}

// TestKindLiteralNewKindCounted simulates a kind that does not exist yet: once
// it is in the kind set, its string and identifier branches are counted.
func TestKindLiteralNewKindCounted(t *testing.T) {
	dir := t.TempDir()
	src := "package x\n\nfunc f(k string) {\n\tswitch k {\n\tcase \"pi\":\n\t}\n" +
		"\tif k == session.KindPi {\n\t}\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	kinds := agentKinds(t)
	if n, _ := countKindLiteralBranches(t, dir, kindLiteralBranch(kinds)); n != 0 {
		t.Fatalf("today's kinds counted %d branches on pi", n)
	}
	if n, _ := countKindLiteralBranches(t, dir, kindLiteralBranch(append(kinds, "pi"))); n != 2 {
		t.Fatalf("with pi in the kind set: %d branches, want 2", n)
	}
}
