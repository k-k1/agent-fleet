package modelfallback

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// Every string constant in the registry must have a filled-in Entries row: the row is what
// tells the next reader who owns the id and why discovery cannot replace it.
func TestEntriesCoverEveryConstant(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "modelfallback.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	inEntries := map[string]bool{}
	for _, e := range Entries {
		if inEntries[e.ID] {
			t.Errorf("duplicate entry %q", e.ID)
		}
		inEntries[e.ID] = true
		if e.ID == "" || e.Kind == "" || e.Owner == "" || e.Source == "" || e.WhyNotDiscovered == "" {
			t.Errorf("entry %+v has an empty field", e)
		}
	}
	consts := 0
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			for _, v := range s.(*ast.ValueSpec).Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				id, _ := strconv.Unquote(lit.Value)
				consts++
				if !inEntries[id] {
					t.Errorf("constant %q has no Entries row", id)
				}
			}
		}
	}
	if consts != len(Entries) {
		t.Errorf("%d string constants, %d entries", consts, len(Entries))
	}
}

func TestIs(t *testing.T) {
	if !Is(ChatCodex) || Is("") || Is("sonnet") {
		t.Fatal("Is must match exactly the registry ids")
	}
}
