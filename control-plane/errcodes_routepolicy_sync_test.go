package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// stringConsts reads the string-literal constants of one file by name.
func stringConsts(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, sp := range g.Specs {
			vs := sp.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if v, err := strconv.Unquote(lit.Value); err == nil {
						out[n.Name] = v
					}
				}
			}
		}
	}
	return out
}

// routepolicysrv cannot import package main, so it declares its own copies of the codes that
// errcodes.go registers. A typo on one side would leave the catalogue test green while a code the
// Console has no entry for goes out, so each pair must exist on both sides and agree.
func TestRoutePolicyErrCodesMatchRootConstants(t *testing.T) {
	root := stringConsts(t, "errcodes.go")
	local := stringConsts(t, "internal/routepolicysrv/routepolicysrv.go")
	pairs := [][2]string{
		{"errCodeInvalidRoutePolicy", "codeInvalidRoutePolicy"},
		{"errCodeRoutePolicyExceedsCeiling", "codeRoutePolicyExceedsCeiling"},
		{"errCodeRoutePolicyEndpointNotApprovable", "codeRoutePolicyEndpointNotApprovable"},
		{"errCodeRoutePolicyStale", "codeRoutePolicyStale"},
	}
	for _, p := range pairs {
		r, rok := root[p[0]]
		l, lok := local[p[1]]
		if !rok || !lok {
			t.Errorf("%s (errcodes.go, found=%v) / %s (routepolicysrv.go, found=%v): both must exist", p[0], rok, p[1], lok)
			continue
		}
		if r != l {
			t.Errorf("%s = %q but %s = %q", p[0], r, p[1], l)
		}
	}
}
