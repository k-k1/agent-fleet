package memengine_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The module must stay free of the host repository (ADR 0130 decision 8) so it
// can be split into its own repository with its history. A single import of an
// agent-fleet package, in any file of any package of this module including
// tests, breaks that.
const forbiddenPrefix = "github.com/k-k1/agent-fleet"

func forbiddenImports(t *testing.T, filename string, src any) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	var bad []string
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		if p == forbiddenPrefix || strings.HasPrefix(p, forbiddenPrefix+"/") {
			bad = append(bad, p)
		}
	}
	return bad
}

func TestNoAgentFleetImports(t *testing.T) {
	n := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		n++
		if bad := forbiddenImports(t, path, nil); len(bad) > 0 {
			t.Errorf("%s imports %v: this module must not depend on the agent-fleet repository", path, bad)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 5 {
		t.Fatalf("scanned only %d files: the walk is not covering the module", n)
	}
}

// A scanner that never flags anything looks the same as a clean module.
func TestIndependenceCheckCatchesAnImport(t *testing.T) {
	for _, imp := range []string{
		forbiddenPrefix + "/control-plane",
		forbiddenPrefix + "/workspace/agent/internal/memoryx",
		forbiddenPrefix,
	} {
		src := "package x\n\nimport _ \"" + imp + "\"\n"
		if bad := forbiddenImports(t, "x.go", src); len(bad) != 1 {
			t.Errorf("import %q not flagged: %v", imp, bad)
		}
	}
	if bad := forbiddenImports(t, "x.go", "package x\n\nimport _ \"github.com/k-k1/memengine\"\nimport _ \"github.com/k-k1/agent-fleet-site/x\"\n"); len(bad) != 0 {
		t.Errorf("a neighbour name was flagged: %v", bad)
	}
}

func TestGoModStandsAlone(t *testing.T) {
	b, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	mod := string(b)
	if strings.Contains(mod, forbiddenPrefix) {
		t.Errorf("go.mod mentions %s", forbiddenPrefix)
	}
	for _, kw := range []string{"require", "replace"} {
		if strings.Contains(mod, kw) {
			t.Errorf("go.mod has a %s directive: the module takes no dependency (stdlib only, no SDK)", kw)
		}
	}
}
