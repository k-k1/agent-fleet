package main

// Pins usagex's `feature` enumeration against the two places that have to list the same values:
// the Console catalogue that names them on screen, and ADR 0029 §2, which declares the
// enumeration frozen.
//
// Why a test and not the rule that already exists: ADR 0029 says in as many words that "a new
// constant in ledger.go and a new value in this table land in the same commit" — and the
// enumeration has drifted from that table THREE times since (plan.update, engine.llm, and
// translate.mirror, the last one caught only while merging). A prose rule in a document nobody
// re-reads while adding a constant does not hold; this does.
//
// What each side costs when it drifts:
//   catalogue … the usage view falls back to the raw key ("translate.mirror") as a column label,
//               in both languages, with every test green.
//   ADR       … the frozen list stops being the list. It is the only place that says what the
//               axis MEANS, so a reader deciding whether a new feature needs its own value is
//               reading a stale set.
//
// ja is the source of truth for the catalogue (the en side is the i18n lint's job), and the ja
// ADR likewise; both files are checked for the value all the same, because an enumeration split
// across languages is the same defect one level down.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// usageFeatureValues returns the string values of internal/usagex/ledger.go's Feature* constants.
func usageFeatureValues(t *testing.T) map[string]string {
	t.Helper()
	path := filepath.Join("internal", "usagex", "ledger.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	out := map[string]string{} // value -> const name
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
				if i >= len(vs.Values) || !strings.HasPrefix(n.Name, "Feature") {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				out[strings.Trim(lit.Value, `"`)] = n.Name
			}
		}
	}
	// A count of zero and a broken scan look identical, and the whole check would pass as
	// "nothing to verify". 10 is far below the 17 values that exist and far above zero.
	if len(out) < 10 {
		t.Fatalf("only %d Feature* constants read from %s = this check has gone silent", len(out), path)
	}
	return out
}

// adrFeatureRow returns ADR 0029 §2's `feature` row, or "" when the document is not available
// (a checkout of the Agent alone).
func adrFeatureRow(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "decisions", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("ADR not available (%v)", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "| `feature` |") {
			return line
		}
	}
	t.Fatalf("no `feature` row in %s (did ADR 0029 §2's table move?)", path)
	return ""
}

func TestUsageFeaturesHaveConsoleCatalogEntry(t *testing.T) {
	catalog := consoleCatalog(t, "ja")
	for value, constName := range usageFeatureValues(t) {
		if !consoleCatalogHasKey(catalog, "usage.val.feature."+value) {
			t.Errorf("%s = %q has no \"usage.val.feature.%s\" in the Console catalogue. "+
				"Add it to console/src/lib/i18n/locales/{ja,en}/usage.ts in the same commit "+
				"(without it the usage view labels the series with the raw key)",
				constName, value, value)
		}
	}
}

func TestUsageFeaturesAreListedInADR0029(t *testing.T) {
	for _, doc := range []string{"0029-usage-accounting.ja.md", "0029-usage-accounting.md"} {
		row := adrFeatureRow(t, doc)
		for value, constName := range usageFeatureValues(t) {
			if !strings.Contains(row, "`"+value+"`") {
				t.Errorf("%s: %s = %q is missing from ADR 0029 §2's frozen `feature` row. "+
					"The ADR's own rule is that the constant and the row land in the same commit",
					doc, constName, value)
			}
		}
		// The other direction: a value listed in the ADR but gone from the code means the
		// frozen list now promises a dimension nothing writes. Checked only for values that
		// look like a feature (dotted or a bare word), so the row's prose cannot trip it.
		for _, cell := range strings.Split(row, "/") {
			value := strings.Trim(strings.TrimSpace(cell), "|` ")
			if value == "" || value == "feature" || strings.ContainsAny(value, " 　") {
				continue
			}
			if _, ok := usageFeatureValues(t)[value]; !ok {
				t.Errorf("%s: ADR 0029 §2 lists %q but no Feature* constant has that value",
					doc, value)
			}
		}
	}
}
