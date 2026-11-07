package main

// Parity between the Agent's member-origin registrations and the checked-in list of signed
// routes (internal/memberorigin/signed_routes.txt). The Control Plane module checks the same
// file against its relay rows, so the two sides cannot drift apart without one test failing.
//
// The Agent side is read from routes.go's syntax tree rather than probed over HTTP: probing an
// unmarked POST would run the real handler of every route that is not behind the mark.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const signedRoutesPath = "internal/memberorigin/signed_routes.txt"

// conditionalMarkFiles maps a route that asks for the mark only for part of a request to the
// file that must call memberorigin.RequireBody for it.
var conditionalMarkFiles = map[string]string{"PUT /env/ui-prefs": "ui_prefs.go"}

type signedEntry struct {
	route, cp   string
	conditional bool
}

func readSignedRoutes(t *testing.T) []signedEntry {
	t.Helper()
	raw, err := os.ReadFile(signedRoutesPath)
	if err != nil {
		t.Fatal(err)
	}
	var out []signedEntry
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 || (f[2] != "relay" && f[2] != "direct") {
			t.Fatalf("%s: malformed line %q", signedRoutesPath, line)
		}
		out = append(out, signedEntry{f[0] + " " + f[1], f[2], len(f) > 3 && f[3] == "conditional"})
	}
	if len(out) == 0 {
		t.Fatalf("%s lists no routes", signedRoutesPath)
	}
	return out
}

// requireRegistrations returns the "METHOD path" literals routes.go registers behind a
// member-origin wrapper: a mux.Handle call whose handler expression mentions
// memberorigin.Require or memoryx.MemberImportHandler (which wraps RequireUpload), and the keys
// of a route map ranged over by a loop that calls memberorigin.Require.
func requireRegistrations(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "routes.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	wraps := func(n ast.Node) bool {
		found := false
		ast.Inspect(n, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok {
					name := id.Name + "." + sel.Sel.Name
					if name == "memberorigin.Require" || name == "memberorigin.RequireUpload" || name == "memoryx.MemberImportHandler" {
						found = true
					}
				}
			}
			return !found
		})
		return found
	}
	routeLit := func(n ast.Node) (string, bool) {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(bl.Value)
		if err != nil || !strings.Contains(s, " /") {
			return "", false
		}
		return s, true
	}
	got := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Handle" || len(n.Args) != 2 || !wraps(n.Args[1]) {
				return true
			}
			if s, ok := routeLit(n.Args[0]); ok {
				got[s] = true
			}
		case *ast.RangeStmt:
			if !wraps(n.Body) {
				return true
			}
			ast.Inspect(n.X, func(n ast.Node) bool {
				if kv, ok := n.(*ast.KeyValueExpr); ok {
					if s, ok := routeLit(kv.Key); ok {
						got[s] = true
					}
				}
				return true
			})
		}
		return true
	})
	if len(got) == 0 {
		t.Fatal("no memberorigin.Require registration found in routes.go: the scan is broken")
	}
	return got
}

func TestSignedRoutesMatchRequireRegistrations(t *testing.T) {
	entries := readSignedRoutes(t)
	registered := map[string]bool{}
	for _, r := range muxRoutes(t, buildMux()) {
		registered[r] = true
	}
	want := map[string]bool{}
	for _, e := range entries {
		if !registered[e.route] {
			t.Errorf("%s lists %q, which buildMux does not register", signedRoutesPath, e.route)
		}
		if e.conditional {
			file, ok := conditionalMarkFiles[e.route]
			if !ok {
				t.Errorf("%q is marked conditional but conditionalMarkFiles does not name its file", e.route)
				continue
			}
			src, err := os.ReadFile(file)
			if err != nil || !strings.Contains(string(src), "memberorigin.RequireBody(") {
				t.Errorf("%q is conditional but %s does not call memberorigin.RequireBody", e.route, file)
			}
			continue
		}
		want[e.route] = true
	}
	got := requireRegistrations(t)
	var problems []string
	for r := range got {
		if !want[r] {
			problems = append(problems, "routes.go wraps "+r+" in memberorigin.Require but "+signedRoutesPath+" does not list it (and the CP must sign it)")
		}
	}
	for r := range want {
		if !got[r] {
			problems = append(problems, signedRoutesPath+" lists "+r+" but routes.go does not wrap it in memberorigin.Require")
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
