package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The audit-coverage check for admin write routes works on the syntax tree: it finds route
// registrations (Handle / HandleFunc, any receiver, any line layout, string constants and
// concatenations), resolves each to its handler, and looks for an audit call and the table's
// marker in the code the handler reaches. Comments and string fragments in unrelated code do
// not count.

type adminRouteProblems struct{ missing, stale, unverified []string }

func (p adminRouteProblems) any() bool {
	return len(p.missing)+len(p.stale)+len(p.unverified) > 0
}

type auditScan struct {
	fset   *token.FileSet
	files  []*ast.File
	consts map[string]string
	byName map[string][]*ast.FuncDecl
}

func newAuditScan(sources map[string]string) (*auditScan, error) {
	sc := &auditScan{fset: token.NewFileSet(), consts: map[string]string{}, byName: map[string][]*ast.FuncDecl{}}
	names := make([]string, 0, len(sources))
	for n := range sources {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f, err := parser.ParseFile(sc.fset, n, sources[n], parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		sc.files = append(sc.files, f)
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				sc.byName[d.Name.Name] = append(sc.byName[d.Name.Name], d)
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					if vs, ok := sp.(*ast.ValueSpec); ok && d.Tok == token.CONST && len(vs.Names) == len(vs.Values) {
						for i, v := range vs.Values {
							if str, ok := sc.evalString(v); ok {
								sc.consts[vs.Names[i].Name] = str
							}
						}
					}
				}
			}
		}
	}
	return sc, nil
}

func (sc *auditScan) evalString(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			s, err := strconv.Unquote(e.Value)
			return s, err == nil
		}
	case *ast.Ident:
		s, ok := sc.consts[e.Name]
		return s, ok
	case *ast.ParenExpr:
		return sc.evalString(e.X)
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			l, ok1 := sc.evalString(e.X)
			r, ok2 := sc.evalString(e.Y)
			return l + r, ok1 && ok2
		}
	}
	return "", false
}

func isAdminWriteRoute(s string) bool {
	for _, m := range []string{"POST ", "PUT ", "PATCH ", "DELETE "} {
		if strings.HasPrefix(s, m+"/api/admin/") {
			return true
		}
	}
	return false
}

// recvTypeName is the receiver type of fd without a pointer star ("" for a function).
func recvTypeName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	return typeName(fd.Recv.List[0].Type)
}

func typeName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.StarExpr:
		return typeName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

// varTypes guesses the type name of the local variables and parameters of fn: `x := f(...)`
// takes f's first result, `x := T{}` / `&T{}` takes T, a parameter takes its declared type.
func (sc *auditScan) varTypes(fn *ast.FuncDecl) map[string]string {
	vt := map[string]string{}
	if fn.Type.Params != nil {
		for _, p := range fn.Type.Params.List {
			for _, n := range p.Names {
				vt[n.Name] = typeName(p.Type)
			}
		}
	}
	if fn.Body == nil {
		return vt
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) < 1 || len(as.Rhs) < 1 {
			return true
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		rhs := as.Rhs[0]
		if u, ok := rhs.(*ast.UnaryExpr); ok && u.Op == token.AND {
			rhs = u.X
		}
		switch r := rhs.(type) {
		case *ast.CompositeLit:
			vt[id.Name] = typeName(r.Type)
		case *ast.CallExpr:
			if f, ok := r.Fun.(*ast.Ident); ok {
				for _, d := range sc.byName[f.Name] {
					if d.Type.Results != nil && len(d.Type.Results.List) > 0 {
						vt[id.Name] = typeName(d.Type.Results.List[0].Type)
						break
					}
				}
			}
		}
		return true
	})
	return vt
}

type adminRoute struct {
	route    string
	handlers []*ast.FuncDecl
}

// routes returns every admin write registration, keyed by "METHOD /path".
func (sc *auditScan) routes() map[string]*adminRoute {
	out := map[string]*adminRoute{}
	for _, f := range sc.files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var vt map[string]string
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 2 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
					return true
				}
				route, ok := sc.evalString(call.Args[0])
				if !ok || !isAdminWriteRoute(route) {
					return true
				}
				if vt == nil {
					vt = sc.varTypes(fn)
				}
				r := out[route]
				if r == nil {
					r = &adminRoute{route: route}
					out[route] = r
				}
				r.handlers = append(r.handlers, sc.handlersOf(call.Args[1], vt)...)
				return true
			})
		}
	}
	return out
}

// handlersOf finds the functions named inside a handler expression (wrappers such as
// withSuperAdmin(a.put) included), preferring the declaration on the variable's own type.
func (sc *auditScan) handlersOf(e ast.Expr, vt map[string]string) []*ast.FuncDecl {
	var out []*ast.FuncDecl
	ast.Inspect(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			cands := sc.byName[n.Sel.Name]
			if x, ok := n.X.(*ast.Ident); ok && vt[x.Name] != "" {
				var typed []*ast.FuncDecl
				for _, c := range cands {
					if recvTypeName(c) == vt[x.Name] {
						typed = append(typed, c)
					}
				}
				if len(typed) > 0 {
					cands = typed
				}
			}
			out = append(out, cands...)
		case *ast.Ident:
			out = append(out, sc.byName[n.Name]...)
		}
		return true
	})
	return out
}

func isAuditCallName(name string) bool {
	l := strings.ToLower(name)
	return strings.HasPrefix(l, "audit") || l == "insertaudit" || l == "beginirreversible"
}

// reach walks the handlers and what they call by name (depth-limited), and reports whether an
// audit call was seen and every string literal found outside the audit helpers themselves.
func (sc *auditScan) reach(handlers []*ast.FuncDecl) (sawAudit bool, lits []string) {
	seen := map[*ast.FuncDecl]bool{}
	type item struct {
		fd    *ast.FuncDecl
		depth int
	}
	q := make([]item, 0, len(handlers))
	for _, h := range handlers {
		q = append(q, item{h, 0})
	}
	for len(q) > 0 {
		it := q[0]
		q = q[1:]
		if seen[it.fd] || it.fd.Body == nil || isAuditCallName(it.fd.Name.Name) {
			continue
		}
		seen[it.fd] = true
		ast.Inspect(it.fd.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					if s, err := strconv.Unquote(n.Value); err == nil {
						lits = append(lits, s)
						// A raw INSERT inside a store transaction is an audit write too.
						sawAudit = sawAudit || strings.Contains(s, "INSERT INTO audit_log")
					}
				}
			case *ast.CallExpr:
				var name string
				switch f := n.Fun.(type) {
				case *ast.Ident:
					name = f.Name
				case *ast.SelectorExpr:
					name = f.Sel.Name
				}
				if isAuditCallName(name) {
					sawAudit = true
				} else if it.depth < 4 {
					for _, d := range sc.byName[name] {
						q = append(q, item{d, it.depth + 1})
					}
				}
			}
			return true
		})
	}
	return sawAudit, lits
}

func classifyAdminRoutes(sources map[string]string, table map[string]adminWriteRouteAudit) (adminRouteProblems, int, error) {
	var p adminRouteProblems
	sc, err := newAuditScan(sources)
	if err != nil {
		return p, 0, err
	}
	routes := sc.routes()
	for route, r := range routes {
		a, ok := table[route]
		if !ok {
			p.missing = append(p.missing, route)
			continue
		}
		if a.marker == "" {
			if a.exempt == "" {
				p.unverified = append(p.unverified, route+" (neither marker nor exemption)")
			}
			continue
		}
		if len(r.handlers) == 0 {
			p.unverified = append(p.unverified, route+" (handler not found)")
			continue
		}
		saw, lits := sc.reach(r.handlers)
		found := false
		for _, l := range lits {
			found = found || strings.Contains(l, a.marker)
		}
		if !saw || !found {
			p.unverified = append(p.unverified, route+" (no audit call carrying "+strconv.Quote(a.marker)+" in its handler)")
		}
	}
	for route := range table {
		if routes[route] == nil {
			p.stale = append(p.stale, route)
		}
	}
	sort.Strings(p.missing)
	sort.Strings(p.stale)
	sort.Strings(p.unverified)
	return p, len(routes), nil
}

func TestAdminWriteRoutesAreClassified(t *testing.T) {
	sources := map[string]string{}
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		sources[p] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	p, n, err := classifyAdminRoutes(sources, adminWriteRouteAudits)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(adminWriteRouteAudits) {
		t.Logf("routes found: %d, table entries: %d", n, len(adminWriteRouteAudits))
	}
	if len(p.missing) > 0 {
		t.Errorf("admin write routes with no audit classification (write an audit row and list it in adminWriteRouteAudits, or add a commented exemption): %v", p.missing)
	}
	if len(p.stale) > 0 {
		t.Errorf("adminWriteRouteAudits names routes that no longer exist: %v", p.stale)
	}
	if len(p.unverified) > 0 {
		t.Errorf("entries the source does not back up: %v", p.unverified)
	}
}

// The checker itself: it must pass a handler that audits, and fail the ways a handler or a new
// route could slip past a weaker check.
func TestAdminRouteAuditCheckerCatchesWhatItShould(t *testing.T) {
	const mk = `package x
const route = "PUT /api/admin/" + "things"
type api struct{}
func (a api) other(w, r) { a.auditAdmin(r, "other.thing") }
func (a api) auditAdmin(r any, action string) { insertAudit(action) }
func (a api) put(w, r) {
	// a.auditAdmin(r, "thing.set") — removed, only this comment is left
	write(w)
}
func routes(mux M) {
	a := api{}
	mux.HandleFunc(route, a.put)
}
`
	table := map[string]adminWriteRouteAudit{"PUT /api/admin/things": {marker: "thing.set"}}
	p, n, err := classifyAdminRoutes(map[string]string{"a.go": mk}, table)
	if err != nil || n != 1 {
		t.Fatalf("scan: n=%d err=%v", n, err)
	}
	if len(p.unverified) != 1 {
		t.Errorf("audit call removed, marker left in a comment: problems %+v, want it flagged", p)
	}

	// Positive control: the same handler with the call in place passes.
	ok := strings.Replace(mk, "// a.auditAdmin", "a.auditAdmin", 1)
	if p, _, _ := classifyAdminRoutes(map[string]string{"a.go": ok}, table); p.any() {
		t.Errorf("handler with its audit call flagged: %+v", p)
	}

	// An audit call elsewhere does not stand in for this handler's.
	if p, _, _ := classifyAdminRoutes(map[string]string{"a.go": mk}, map[string]adminWriteRouteAudit{"PUT /api/admin/things": {marker: "other.thing"}}); len(p.unverified) != 1 {
		t.Errorf("another handler's audit literal satisfied this route: %+v", p)
	}

	// A route added with Handle, over several lines, through a constant, is still seen.
	added := mk + `
func more(mux M) {
	mux.Handle(
		"DELETE /api/admin/new-thing",
		http.HandlerFunc(nothing),
	)
}
`
	p, n, _ = classifyAdminRoutes(map[string]string{"a.go": added}, table)
	if n != 2 || len(p.missing) != 1 || p.missing[0] != "DELETE /api/admin/new-thing" {
		t.Errorf("new Handle route: n=%d problems %+v, want it reported missing", n, p)
	}
}
