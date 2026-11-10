package main

// Cross-checks the values in control-plane/errcodes.go against the Console's `err.<code>`
// catalog. The counterpart of workspace/agent's errcodes_catalog_test.go, covering the
// same gap on the CP side.
//
// The CP has no helper that reads the Console catalog, so the whole convention was a
// comment in `egress_member.go` and `internal/mcpsrv/mcp_server.go` saying "when adding
// or renaming one, update err.<code> in errors.ts too". Change a spelling and both Go and
// the Console stay green while the screen alone falls back to the developer-facing
// message.
//
// As on the agent side, only the codes actually emitted are examined: a constant that is
// merely declared cannot reach the screen.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cpConsoleCatalog concatenates one locale's domain catalogs.
// `locales/<locale>.ts` is deliberately not read: it is a composition file holding
// nothing but imports and spreads, and reading it turns the check into one that reports a
// key as missing while it is present (ADR 0067 decision 4).
func cpConsoleCatalog(t *testing.T, locale string) string {
	t.Helper()
	dir := filepath.Join("..", "console", "src", "lib", "i18n", "locales", locale)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Allow for a build from a distribution that does not include console/,
		// as on the agent side.
		t.Skipf("catalog not available (%v)", err)
	}
	var b strings.Builder
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s/%s: %v", dir, e.Name(), err)
		}
		b.Write(raw)
		b.WriteString("\n")
		n++
	}
	if n == 0 {
		t.Fatalf("no .ts file at all under %s (did the catalog move?)", dir)
	}
	return b.String()
}

func TestCPEmittedErrCodesHaveConsoleCatalogEntry(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "errcodes.go", nil, 0)
	if err != nil {
		t.Fatalf("cannot read errcodes.go: %v", err)
	}
	codeOf := map[string]string{}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, sp := range g.Specs {
			vs, ok := sp.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					codeOf[n.Name] = strings.Trim(lit.Value, `"`)
				}
			}
		}
	}
	if len(codeOf) == 0 {
		t.Fatal("could not read a single constant from errcodes.go = this check has gone silent")
	}

	emitted, scanned := scanEmittedErrCodes(t, ".", codeOf)
	if scanned < 50 {
		t.Fatalf("only %d .go files were read = this check has gone silent", scanned)
	}
	if len(emitted) == 0 {
		t.Fatal("found no emitted error code at all (the scan is broken)")
	}

	// Packages split out of package main cannot name the errCode* constants, so they emit the
	// same strings as literals (internal/tenantsrv does). internal/engine is the planned home
	// of the engine family's ~50 codes; it is read as soon as it exists, so moving a file there
	// cannot silently drop its codes from this check.
	internalEmitted, internalScanned := scanEmittedErrLiterals(t, "internal", codeOf)
	if internalScanned < 50 {
		t.Fatalf("only %d .go files under internal/ were read = this check has gone silent", internalScanned)
	}
	for code, where := range internalEmitted {
		if _, ok := emitted[code]; !ok {
			emitted[code] = where
		}
	}

	catalog := cpConsoleCatalog(t, "ja")
	for code, constName := range emitted {
		if !consoleCatalogHasKeyCP(catalog, "err."+code) {
			t.Errorf("%s = %q is emitted, but the Console catalog has no \"err.%s\". "+
				"Add it to console/src/lib/i18n/locales/{ja,en}/errors.ts at the same time",
				constName, code, code)
		}
	}
}

func consoleCatalogHasKeyCP(catalog, key string) bool {
	return strings.Contains(catalog, `"`+key+`"`)
}

// scanEmittedErrCodes returns the codes whose errCode* constant is referenced by an
// identifier in a non-test .go file directly under dir, and the number of files read.
func scanEmittedErrCodes(t *testing.T, dir string, codeOf map[string]string) (map[string]string, int) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	emitted := map[string]string{}
	scanned := 0
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || n == "errcodes.go" {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, n), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", n, err)
		}
		scanned++
		ast.Inspect(af, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok {
				if code, ok := codeOf[id.Name]; ok {
					emitted[code] = id.Name
				}
			}
			return true
		})
	}
	return emitted, scanned
}

// scanEmittedErrLiterals walks every non-test .go file under root and returns the codes that
// appear there as string literals (value: "<file>"), and the number of files read.
func scanEmittedErrLiterals(t *testing.T, root string, codeOf map[string]string) (map[string]string, int) {
	t.Helper()
	byValue := map[string]bool{}
	for _, v := range codeOf {
		byValue[v] = true
	}
	emitted := map[string]string{}
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		af, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		scanned++
		ast.Inspect(af, func(node ast.Node) bool {
			if lit, ok := node.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if v := strings.Trim(lit.Value, "`\""); byValue[v] {
					emitted[v] = path
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return emitted, scanned
}

// TestScanEmittedErrLiteralsSeesNestedPackages pins the scanner itself: a literal in a nested
// package directory must be reported, otherwise a moved engine file loses its codes silently.
func TestScanEmittedErrLiteralsSeesNestedPackages(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "engine", "sub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package sub\n\nconst c = \"engine_probe_code\"\n"
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, n := scanEmittedErrLiterals(t, root, map[string]string{"errCodeProbe": "engine_probe_code"})
	if n != 1 || got["engine_probe_code"] == "" {
		t.Fatalf("scan missed the nested literal: files=%d got=%v", n, got)
	}
}
