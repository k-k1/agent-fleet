package main

// Cross-checks the error codes the agent DRIVERS emit against the Console's `err.<code>`
// catalogue.
//
// errcodes_catalog_test.go covers package main, where the codes are named constants in
// errcodes.go. The driver sign-in handlers under internal/agents/* pass their codes as bare
// string literals in another package, so nothing checked them: a driver could add a code, the
// Console would fail the lookup, and `errText` would quietly show the developer-facing English
// message instead. Both sides stay green while a Japanese Console shows English.
//
// The scan reads the literals rather than a hand-kept list, so a new driver or a new code is
// covered the day it lands instead of the day somebody remembers this file.
//
// ja is the source of truth; a missing en entry is caught by the Console's own type check
// (`en` is `Record<keyof typeof ja, string>`).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// driverErrCodes returns every code passed as a string literal to httpx.WriteErr under
// internal/agents, mapped to the file that emits it.
func driverErrCodes(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	scanned := 0
	root := filepath.Join("internal", "agents")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "WriteErr" {
				return true
			}
			// WriteErr(w, status, code, message): the code is the third argument, and only a
			// literal can be checked — a computed code is invisible here by construction.
			if len(call.Args) < 3 {
				return true
			}
			lit, ok := call.Args[2].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			out[strings.Trim(lit.Value, `"`)] = path
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	// A rename or a move that empties the walk would turn this check into a no-op that
	// reports success. Both counts are far below today's values (measured: 100+ files,
	// 27 codes), so they catch "went silent" without tracking every addition.
	if scanned < 30 {
		t.Fatalf("only %d driver .go files were read = this check has gone silent", scanned)
	}
	if len(out) < 10 {
		t.Fatalf("found only %d driver error codes = the scan is broken", len(out))
	}
	return out
}

func TestDriverErrCodesHaveConsoleCatalogEntry(t *testing.T) {
	catalog := consoleCatalog(t, "ja")
	for code, file := range driverErrCodes(t) {
		if !consoleCatalogHasKey(catalog, "err."+code) {
			t.Errorf("%s emits %q but the Console catalogue has no \"err.%s\". "+
				"Add it to console/src/lib/i18n/locales/{ja,en}/errors.ts at the same time "+
				"(without it the screen silently falls back to the developer-facing message)",
				file, code, code)
		}
	}
}

// The catalogue only helps if the call site actually asks for it. These handlers are rendered
// by the agent cards, which used to interpolate `error.message` directly and so bypassed the
// code lookup entirely — adding catalogue entries alone changed nothing on screen. errDetail
// is what puts the two together (catalogue wording, then the server's raw cause).
func TestAgentCardsRenderErrorsThroughErrDetail(t *testing.T) {
	dir := filepath.Join("..", "..", "console", "src", "features", "settings", "agents")
	ents, err := filepath.Glob(filepath.Join(dir, "*.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) == 0 {
		t.Skipf("console sources not available at %s", dir)
	}
	checked := 0
	for _, p := range ents {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		checked++
		// `error.message` reaching a toast means that call site skips the catalogue.
		for _, bad := range []string{"error.message", "error?.message"} {
			if strings.Contains(string(raw), bad) {
				t.Errorf("%s still uses %q; render server errors with errDetail(...) so the "+
					"`err.<code>` catalogue entry is used and the raw cause is appended",
					filepath.Base(p), bad)
			}
		}
	}
	if checked < 5 {
		t.Fatalf("only %d agent card files were read = this check has gone silent", checked)
	}
}
