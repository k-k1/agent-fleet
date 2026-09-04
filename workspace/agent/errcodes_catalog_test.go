package main

// Cross-checks errcodes.go's values against the Console's `err.<code>` catalogue.
//
// Nothing in the repository did this check. Both errcodes.go's header and egress_member.go say
// "change both sides together", but only the note said so. Change one letter of a spelling on
// the Go side and the Console cannot look the key up and silently falls back to the
// developer-facing English message, with HTTP still 200/4xx and both the Go and the Console
// tests green.
//
// Only the codes actually EMITTED are examined: a code that is declared but emitted by nobody
// cannot reach the screen, so demanding a catalogue entry for it would be pure noise. As
// measured, that filter drops nothing today (all 71 constants in errcodes.go are referenced
// from somewhere in package main) — it is insurance for the future, not a response to unused
// constants. The two codes without a catalogue entry are not unreferenced; they are
// deliberately exempted with a reason in catalogExempt below.
//
// ja is the source of truth (`../../console/src/lib/i18n/locales/ja`). A missing en entry is
// the i18n-side check's job.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// emittedErrCodes returns the string values of errcodes.go's constants that some
// other file in package main actually references. The map is code -> const name.
func emittedErrCodes(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "errcodes.go", nil, 0)
	if err != nil {
		t.Fatalf("cannot read errcodes.go: %v", err)
	}
	codeOf := map[string]string{} // const name -> code value
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
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				codeOf[n.Name] = strings.Trim(lit.Value, `"`)
			}
		}
	}
	if len(codeOf) == 0 {
		t.Fatal("read no constants at all from errcodes.go = this check has gone silent")
	}

	// Count which constants are actually referenced, across package main except errcodes.go.
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
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
			id, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			if code, ok := codeOf[id.Name]; ok {
				out[code] = id.Name
			}
			return true
		})
	}
	if scanned < 50 {
		t.Fatalf("only %d .go files were read = this check has gone silent", scanned)
	}
	if len(out) == 0 {
		t.Fatal("found no emitted error codes at all (the scan is broken)")
	}
	return out
}

// catalogExempt are the codes that are INTENDED to have no catalogue entry. Always write the
// reason when adding one; this is not a place to hide "not fixed yet".
var catalogExempt = map[string]string{
	// The reason is written right above the declaration in errcodes.go: it is only returned
	// after the client already gave up (a timeout or disconnect detected while taking the
	// mutex), so no live screen ever draws it.
	"write_cancelled": "never reaches a live client (stated at the declaration in errcodes.go)",

	// Exempted on purpose because adding a catalogue entry would REDUCE the information shown.
	// `errText` is `localized ?? error.message` (client.ts:159), so once `err.<code>` exists
	// the server's message is discarded in favour of the catalogue wording.
	// `conn_jira_rejected` carries Jira's own raw reason (`err.Error()`) in message, and a
	// generalized localized string would erase the specific reason currently displayed.
	// The harm today is only that an English message shows instead of a localized one; nothing
	// breaks. The permanent fix is to move to `errDetail` (catalogue wording plus the raw
	// message), which needs call-site changes and therefore a separate PR (the first case of
	// the same shape, `runtime_failed`, is in progress).
	"conn_jira_rejected": "message carries Jira's own raw reason and a catalogue entry would make errText hide it; the permanent fix is errDetail, which needs call-site changes and a separate PR",
}

func TestEmittedErrCodesHaveConsoleCatalogEntry(t *testing.T) {
	catalog := consoleCatalog(t, "ja")
	for code, constName := range emittedErrCodes(t) {
		if _, ok := catalogExempt[code]; ok {
			continue
		}
		if !consoleCatalogHasKey(catalog, "err."+code) {
			t.Errorf("%s = %q is emitted but the Console catalogue has no \"err.%s\". "+
				"Add it to console/src/lib/i18n/locales/{ja,en}/errors.ts at the same time "+
				"(without it the screen silently falls back to the developer-facing message)",
				constName, code, code)
		}
	}
	// Keep the exemption table from rotting: once a catalogue entry is added, force the
	// exemption out. Left alone, "exempt, so don't look" piles up and this check becomes a
	// name only.
	for code, why := range catalogExempt {
		if consoleCatalogHasKey(catalog, "err."+code) {
			t.Errorf("err.%s is in the catalogue yet still listed as exempt (reason: %s). Remove it from the exemption table", code, why)
		}
	}
}
