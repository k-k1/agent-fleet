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

	ents, err := os.ReadDir(".")
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
		af, err := parser.ParseFile(token.NewFileSet(), n, nil, 0)
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
	if scanned < 50 {
		t.Fatalf("only %d .go files were read = this check has gone silent", scanned)
	}
	if len(emitted) == 0 {
		t.Fatal("found no emitted error code at all (the scan is broken)")
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
