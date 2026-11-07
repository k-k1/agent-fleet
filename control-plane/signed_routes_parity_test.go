package main

// Parity between the CP's signing and the checked-in list of member-origin signed routes. The
// list lives with the Agent (workspace/agent/internal/memberorigin/signed_routes.txt) because
// the two Go modules cannot import each other; the Agent module checks the same file against
// its memberorigin.Require registrations. A route signed here without a line there, or a line
// without a relay row, fails this test.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const signedRoutesList = "../workspace/agent/internal/memberorigin/signed_routes.txt"

func TestSignedRoutesMatchRelayRows(t *testing.T) {
	raw, err := os.ReadFile(signedRoutesList)
	if err != nil {
		t.Fatal(err)
	}
	listedRelay := map[string]bool{}
	type directRoute struct{ route, fn string }
	var direct []directRoute
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			t.Fatalf("malformed line %q", line)
		}
		switch f[2] {
		case "relay":
			listedRelay[f[0]+" /api"+f[1]] = true
		case "direct":
			if len(f) < 4 {
				t.Fatalf("direct line %q must name the signing function in a fourth column", line)
			}
			direct = append(direct, directRoute{f[0] + " " + f[1], f[3]})
		default:
			t.Fatalf("unknown CP-SIDE %q in %q", f[2], line)
		}
	}

	signed, _ := signedRelayRoutes()
	if len(signed) == 0 {
		t.Fatal("no relay row has sign: the derivation is broken")
	}
	var problems []string
	for r := range signed {
		if !listedRelay[r] {
			problems = append(problems, "relay row "+r+" has sign but the list does not carry it as relay")
		}
	}
	for r := range listedRelay {
		if !signed[r] {
			problems = append(problems, "the list carries "+r+" as relay but no relay row signs it")
		}
	}

	// Direct signing: the functions that call memberOriginSign, other than the relay path's own
	// (manager.memberOriginMarkBuffered, which signs by relay row), are the CP handlers that sign
	// for themselves. Each listed route must name its function, that function must be the one
	// signing, and its body must carry the route's path literals; moving or deleting the signing
	// call fails here whatever strings remain elsewhere.
	signers := directSigners(t)
	listed := map[string]bool{}
	for _, d := range direct {
		listed[d.fn] = true
		lits, ok := signers[d.fn]
		if !ok {
			problems = append(problems, "direct route "+d.route+": "+d.fn+" does not call memberOriginSign")
			continue
		}
		for _, piece := range strings.FieldsFunc(d.route[strings.Index(d.route, " ")+1:], func(r rune) bool { return r == '{' || r == '}' }) {
			if strings.Contains(d.route, "{"+piece+"}") {
				continue // a placeholder name, not a literal piece
			}
			if !lits[piece] {
				problems = append(problems, "direct route "+d.route+": "+d.fn+" has no string literal "+strconv.Quote(piece))
			}
		}
	}
	for fn := range signers {
		if !listed[fn] {
			problems = append(problems, fn+" calls memberOriginSign but no direct route in the list names it")
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// directSigners maps every non-test function that calls memberOriginSign (other than the relay
// path's own) to the string literals in its body.
func directSigners(t *testing.T) map[string]map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			name := fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) == 1 {
				rt := fd.Recv.List[0].Type
				if st, ok := rt.(*ast.StarExpr); ok {
					rt = st.X
				}
				if id, ok := rt.(*ast.Ident); ok {
					name = id.Name + "." + name
				}
			}
			signs := false
			lits := map[string]bool{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "memberOriginSign" {
						signs = true
					}
				case *ast.BasicLit:
					if n.Kind == token.STRING {
						if v, err := strconv.Unquote(n.Value); err == nil {
							lits[v] = true
						}
					}
				}
				return true
			})
			if signs && name != "manager.memberOriginMarkBuffered" {
				out[name] = lits
			}
		}
	}
	return out
}
