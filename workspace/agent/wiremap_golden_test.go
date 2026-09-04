// wiremap_golden_test.go — goldens the KEY SET of every JSON write site that returns a
// bare `map[string]any` (CONTRACT-MAP / leg 3). The map counterpart of `wire.golden`.
//
// `wire.golden` snapshots json tags by reflecting over NAMED types, so a bare map return
// has zero coverage by construction — the header of `wire_golden_test.go` says as much
// itself. Measured (go/ast): 226 of the Agent's 285 JSON write sites (79%) return a map
// directly, and not one check fails when their wire changes. Converting them to structs is
// work on those same 226 sites, so pin the current shape before converting anything.
//
// The scan parses source rather than reflecting: a map carries no type, so there is no
// entity for reflect to walk (typed payloads are `wire.golden`'s job).
//
// To update (a deliberate wire change, or a map that became a struct):
//
//	cd workspace/agent && go test -run TestWireMapGolden -update-wiremap-golden ./...
//
// A PR that re-snapshots must state why (same rule as routes.golden / wire.golden):
// "this diff is intended" carries more information than "no diff".
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var updateWireMapGolden = flag.Bool("update-wiremap-golden", false,
	"rewrite testdata/wiremap.golden from the actual map write sites (only when the change was intended)")

const wireMapGoldenPath = "testdata/wiremap.golden"

// wireMapMinSites is the floor that keeps a broken scan from passing as "green because
// zero sites". README §4: a count of zero is only usable once the tool is known to work.
// Measured 226; the number is expected to FALL as structs replace maps, so the floor is
// deliberately loose and only catches "everything / half of it vanished" breakage.
const wireMapMinSites = 100

func TestWireMapGolden(t *testing.T) {
	sites := scanWireMapSites(t, ".")
	if len(sites) < wireMapMinSites {
		t.Fatalf("only %d map write sites were picked up (floor %d). "+
			"the scan is very likely broken - suspect the tool before retaking the golden.",
			len(sites), wireMapMinSites)
	}
	got := wireMapLines(sites)

	if *updateWireMapGolden {
		writeWireMapGolden(t, wireMapGoldenPath, got)
		t.Logf("wrote %s (%d lines / %d sites)", wireMapGoldenPath, len(got), len(sites))
		return
	}
	assertWireMapGoldenLines(t, wireMapGoldenPath, got)
}

// assertWireMapGoldenLines does the same comparison as the routes/wire assertGoldenLines,
// but points the reader at THIS golden's fix. The shared helper names
// `-update-routes-golden`, so reusing it as-is would hand whoever sees the failure a flag
// that repairs nothing here.
func assertWireMapGoldenLines(t *testing.T, path string, got []string) {
	t.Helper()
	if diff := lineDiff(readGoldenLines(t, path), got); diff != "" {
		t.Errorf("does not match %s:\n%s\n"+
			"if the gain/loss was intended, retake with -update-wiremap-golden and **state in the PR why it was retaken**.\n"+
			"  a line disappeared = that map site became a struct (intended, if you converted it), or it is gone.\n"+
			"  a key was gained or lost = that IS the wire possibly changing. stop unless an equivalence test shows otherwise.",
			path, diff)
	}
}

// TestWireMapGoldenActuallyOpensMaps guards against "snapshotted, but empty": once the scan
// silently stops picking anything up, the golden stays green while protecting nothing (same
// intent as `TestWireShapeGoldenCoversSession`).
//
// The samples are chosen only for properties the scan could plausibly miss — a map written
// out as a plain literal is easy to pick up, so it would not be a control.
func TestWireMapGoldenActuallyOpensMaps(t *testing.T) {
	sites := scanWireMapSites(t, ".")
	byFunc := map[string][]wireMapSite{}
	for _, s := range sites {
		byFunc[s.Func] = append(byFunc[s.Func], s)
	}

	t.Run("counts that one shape occurs at several sites", func(t *testing.T) {
		// Without the count, deleting one of N identical-shape sites produces no diff at
		// all (assertGoldenLines diffs as a set operation).
		//
		// Do not take the sample from the sites being converted. This used to name
		// handleFSLineMarks, and the guarantee died the moment this track turned it into a
		// struct (measured): what your own work deletes cannot guarantee anything. Names are
		// taken from sites carrying a dyn mark (structurally unable to become a struct, so
		// they do not disappear), plus the property "at least one function has several
		// sites".
		multi := 0
		for _, ss := range byFunc {
			if len(ss) >= 2 {
				multi++
			}
		}
		if multi == 0 {
			t.Fatal("not a single function writes out more than one site = the count of identical shapes is doing nothing")
		}
		t.Logf("functions with several sites: %d", multi)

		// One of handleAgentRTKGain's four sites is dyn (it relays rtk's output verbatim),
		// so it structurally cannot become a struct and the conversion will not delete it.
		if got := byFunc["handleAgentRTKGain"]; len(got) < 2 {
			t.Errorf("only %d write sites were picked up for handleAgentRTKGain (there should be several)", len(got))
		}
	})

	t.Run("opens a shape function's return value", func(t *testing.T) {
		// handlePutSlackConn is writeJSON(w, 200, slackStatus(...)): the keys live only in
		// the callee's body. Demand the specific key NAMES, not a key count — counting
		// alone lets a recursion that stopped short still pass green (hit for real on the
		// control-plane side).
		got := byFunc["handlePutSlackConn"]
		if len(got) == 0 {
			t.Fatal("handlePutSlackConn was not picked up (the shape function's return value is not being opened)")
		}
		want := []string{"connected", "mode", "notify", "receive", "operator"}
		if !wireMapHasAll(got[0].Keys, want) {
			t.Errorf("slackStatus's key set has thinned out: %v\n  should contain %v", got[0].Keys, want)
		}
	})

	t.Run("records conditional keys", func(t *testing.T) {
		// Whether omitempty is needed is readable only from here. With not one recorded,
		// the golden cannot inform the decision of whether a site may become a struct.
		n := 0
		for _, s := range sites {
			if len(s.CondKeys) > 0 {
				n++
			}
		}
		if n == 0 {
			t.Error("not a single site has conditional keys - " +
				"the conditional tracking is broken (whether omitempty is needed becomes unreadable)")
		}
		t.Logf("sites with conditional keys: %d", n)
	})
}

// TestWireMapScanExclusionsAreJustified checks by machine the reason given for each scan
// exclusion.
//
// That reason is "it is not in the product binary's dependency graph", and it is shown with
// `go list -deps` rather than asserted (`git grep` cannot establish "is not in"): what is
// checked is that the package is reachable neither from main nor from anywhere under ./....
// The moment someone imports it from product code this goes red and the exclusion is
// withdrawn.
//
// Same shape as the reverse check on an exemption's lifetime (§4): a machine watches the road
// on which the exclusion stops being needed, or stops being allowed.
func TestWireMapScanExclusionsAreJustified(t *testing.T) {
	if len(wireMapScanExcluded) == 0 {
		t.Skip("no exclusions")
	}
	// Fatal, not Skip, when go is missing: a Skip here goes green with nobody noticing that
	// the reason was never checked.
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("cannot check the exclusion reasons because go is missing: %v", err)
	}
	mod, err := exec.Command("go", "list", "-m").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	modPath := strings.TrimSpace(string(mod))

	// Check 1: not in the product binary's (main) dependency graph.
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps .: %v", err)
	}
	deps := strings.Fields(string(out))
	for pkg, why := range wireMapScanExcluded {
		full := modPath + "/" + pkg
		for _, d := range deps {
			if d == full {
				t.Errorf("%s is excluded from the scan yet shows up in `go list -deps .`.\n"+
					"  the stated reason was %q, but it IS now reachable from the product binary.\n"+
					"  withdraw the exclusion and scan it again, or drop the import.", pkg, why)
			}
		}
	}
	t.Logf("go list -deps .: %d packages (no excluded package among them)", len(deps))

	// Check 2: nobody but the package itself imports it from non-test code.
	// `go list -deps ./...` always contains the package itself, so presence or absence there
	// cannot decide this (written that way first, and it produced a false red). .Imports
	// leaves out imports made by test files, which is what makes it the right place to ask
	// whether a non-test importer exists.
	lst, err := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list -f ./...: %v", err)
	}
	nchecked := 0
	for _, line := range strings.Split(string(lst), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		importer, imports := fields[0], fields[1:]
		nchecked++
		for pkg, why := range wireMapScanExcluded {
			full := modPath + "/" + pkg
			if importer == full {
				continue // the package itself does not count
			}
			for _, im := range imports {
				if im == full {
					t.Errorf("%s imports %s from non-test code.\n"+
						"  the stated reason was %q. it is no longer test-only.",
						importer, pkg, why)
				}
			}
		}
	}
	if nchecked == 0 {
		t.Fatal("go list returned no packages at all (the check is spinning without measuring anything)")
	}
	t.Logf("packages checked for non-test imports: %d", nchecked)
}

// TestWireMapScanExclusionsActuallySkip checks that an exclusion is actually IN EFFECT. If
// the table is written but never wired into the scan, the reason check above is only
// checking a table nobody uses.
func TestWireMapScanExclusionsActuallySkip(t *testing.T) {
	for _, s := range scanWireMapSites(t, ".") {
		dir := filepath.ToSlash(filepath.Dir(s.File))
		if why, skip := wireMapScanExcluded[dir]; skip {
			t.Errorf("%s is excluded (%s) yet shows up in the scan result = the exclusion is not in effect", s.File, why)
		}
	}
}

// --- Golden lines ---

// wireMapLines renders one line per group of sites sharing a function and a key set.
//
// No line numbers: an unrelated edit would move every line. The count of identical shapes
// (xN) goes on the line instead — `assertGoldenLines` diffs as a set operation, so without
// it deleting one of five identical sites produces no diff at all.
func wireMapLines(sites []wireMapSite) []string {
	type key struct{ file, fn, keys, cond, flags string }
	count := map[key]int{}
	for _, s := range sites {
		var flags []string
		if s.DynKey {
			flags = append(flags, "dyn")
		}
		if s.Partial {
			flags = append(flags, "partial")
		}
		count[key{s.File, s.Func, strings.Join(s.Keys, ","), strings.Join(s.CondKeys, ","), strings.Join(flags, "+")}]++
	}
	var out []string
	for k, n := range count {
		line := fmt.Sprintf("%s %s x%d {%s}", k.file, k.fn, n, k.keys)
		if k.cond != "" {
			line += " cond{" + k.cond + "}"
		}
		if k.flags != "" {
			line += " " + k.flags
		}
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

func writeWireMapGolden(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	var b strings.Builder
	b.WriteString("# Key sets of the JSON write sites that still return a map[string]any directly.\n# Generated - do not edit by hand.\n")
	b.WriteString("# Update: cd workspace/agent && go test -run TestWireMapGolden -update-wiremap-golden ./...\n")
	b.WriteString("# Format: <file> <func> x<count of identical shapes> {key set} [cond{conditional keys}] [dyn|partial]\n")
	b.WriteString("#   cond    = keys added inside a conditional (decide omitempty for these when converting)\n")
	b.WriteString("#   partial = the variable is assigned more than once, so the key set is not fully opened\n")
	b.WriteString("#   dyn     = a non-literal key is present, so the key set is not statically known and it cannot become a struct\n")
	b.WriteString("# A key gained or lost in this diff IS the wire possibly changing. State in the PR why it was retaken.\n")
	fmt.Fprintf(&b, "# count: %d\n", len(lines))
	for _, ln := range lines {
		b.WriteString(ln)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func wireMapHasAll(got, want []string) bool {
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}

// --- Scan (go/ast only; no type checking) ---

type wireMapSite struct {
	File     string
	Func     string
	Keys     []string
	CondKeys []string
	DynKey   bool
	Partial  bool
}

// wireMapScanExcluded maps a package excluded from the scan to the reason it may be excluded.
//
// "It is for tests" written in prose alone lets the next non-test thing someone drops under
// internal/ slip through with it, so the reason itself is checked by machine
// (TestWireMapScanExclusionsAreJustified) and goes red once it stops holding.
//
// Matching is on the FULL package path: a prefix match would drag in unrelated packages such
// as `internal/wiretestfoo`.
var wireMapScanExcluded = map[string]string{
	"internal/wiretest": "a shared harness imported only from tests, not in the product binary's dependency graph",
}

// wireMapHelpers lists the write helpers themselves; writes inside their bodies are not
// counted (they are the envelope, not the DTO).
var wireMapHelpers = map[string]bool{
	"writeJSON": true, "WriteJSON": true, "writeAPIErr": true, "WriteErr": true, "writeErr": true,
}

func scanWireMapSites(t *testing.T, root string) []wireMapSite {
	t.Helper()
	var out []wireMapSite
	funcs := map[string]*ast.FuncDecl{} // index for looking up shape functions

	var files []*ast.File
	var names []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		if _, skip := wireMapScanExcluded[filepath.ToSlash(filepath.Dir(strings.TrimPrefix(p, "./")))]; skip {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", p, perr)
		}
		files = append(files, f)
		names = append(names, filepath.ToSlash(p))
		return nil
	})
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}

	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if fd.Recv != nil {
				funcs[wireMapRecv(fd.Recv)+"."+fd.Name.Name] = fd
				funcs["."+fd.Name.Name] = fd
			} else {
				funcs[fd.Name.Name] = fd
				// Also index under the package qualifier. With the bare name
				// alone, a same-named function in another package overwrites the
				// entry and a foreign shape function such as `uiprefs.Read()`
				// silently stops resolving.
				funcs[f.Name.Name+"."+fd.Name.Name] = fd
			}
		}
	}

	for i, f := range files {
		rel := strings.TrimPrefix(names[i], "./")
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || wireMapHelpers[fd.Name.Name] {
				continue
			}
			fn := fd.Name.Name
			if fd.Recv != nil {
				fn = wireMapRecv(fd.Recv) + "." + fn
			}
			env := wireMapBuildEnv(fd.Body)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				payload, ok := wireMapPayload(n, env)
				if !ok {
					return true
				}
				s := wireMapSite{File: rel, Func: fn}
				if wireMapClassify(&s, payload, env, funcs, map[*ast.FuncDecl]bool{}, 0) {
					out = append(out, s)
				}
				return true
			})
		}
	}
	return out
}

// wireMapPayload decides whether n is a JSON write site and returns the payload expression.
// `conn.WriteJSON(v)` (one argument) sends a WebSocket frame, not an HTTP wire, and is
// excluded by requiring three arguments (measured: 11 such calls, to codex app-server /
// Discord / Slack).
func wireMapPayload(n ast.Node, env *wireMapEnv) (ast.Expr, bool) {
	ce, ok := n.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	switch fn := ce.Fun.(type) {
	case *ast.Ident:
		if (fn.Name == "writeJSON" || fn.Name == "WriteJSON") && len(ce.Args) == 3 {
			return ce.Args[2], true
		}
	case *ast.SelectorExpr:
		if fn.Sel.Name == "WriteJSON" && len(ce.Args) == 3 {
			return ce.Args[2], true
		}
		if fn.Sel.Name == "Encode" && len(ce.Args) == 1 {
			if inner, ok := fn.X.(*ast.CallExpr); ok && wireMapIsSel(inner.Fun, "json", "NewEncoder") {
				return ce.Args[0], true
			}
			if id, ok := fn.X.(*ast.Ident); ok && env.encoders[id.Name] {
				return ce.Args[0], true
			}
		}
	}
	return nil, false
}

// wireMapClassify inspects the payload and fills in the key set when it is a map.
// A false return means "not a map" (named type, slice, nil, …; not this file's business).
// depth is a runaway guard, not a semantic limit: cut it short and key sets silently thin
// out — measured, a limit of 3 stopped at the fourth level of
// `workspaceStats → out := containerStats() → out := map{...}` and turned 8 keys into 3,
// while still looking green. Recursion is spent on function nesting, so cycles are stopped
// by visited and the depth is left generous.
func wireMapClassify(s *wireMapSite, e ast.Expr, env *wireMapEnv, funcs map[string]*ast.FuncDecl, visited map[*ast.FuncDecl]bool, depth int) bool {
	return wireMapClassifyAt(s, e, env, funcs, visited, 0, depth)
}

func wireMapClassifyAt(s *wireMapSite, e ast.Expr, env *wireMapEnv, funcs map[string]*ast.FuncDecl, visited map[*ast.FuncDecl]bool, resIdx, depth int) bool {
	if depth > 12 {
		return false
	}
	switch v := e.(type) {
	case *ast.CompositeLit:
		mt, ok := v.Type.(*ast.MapType)
		if !ok {
			return false
		}
		if k, ok := mt.Key.(*ast.Ident); !ok || k.Name != "string" {
			return false
		}
		for _, el := range v.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if bl, ok := kv.Key.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				lit, _ := strconv.Unquote(bl.Value)
				s.Keys = append(s.Keys, lit)
			} else {
				s.DynKey = true
			}
		}
		sort.Strings(s.Keys)
		return true

	case *ast.Ident:
		a, ok := env.assign[v.Name]
		if !ok {
			// The relay shape: `var body map[string]any` filled by json.Unmarshal.
			// The keys are only known at run time, so zero keys plus dyn is the
			// correct record ("not statically determined", not "no keys").
			if t, ok := env.declType[v.Name]; ok && wireMapIsStringMapType(t) {
				s.DynKey = true
				wireMapMergeAdded(s, v.Name, env)
				return true
			}
			return false
		}
		if !wireMapClassifyAt(s, a, env, funcs, visited, env.mvIdx[v.Name], depth+1) {
			return false
		}
		if env.nassign[v.Name] > 1 {
			s.Partial = true
		}
		if env.dynIdx[v.Name] {
			s.DynKey = true
		}
		wireMapMergeAdded(s, v.Name, env)
		return true

	case *ast.CallExpr:
		fd := wireMapLookup(v.Fun, funcs)
		if fd == nil || fd.Body == nil {
			return false
		}
		if !wireMapReturnsStringMapAt(fd, resIdx) {
			return false
		}
		if visited[fd] { // keeps mutual recursion from never returning
			return false
		}
		visited[fd] = true
		defer delete(visited, fd)
		inner := wireMapBuildEnv(fd.Body)
		found := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			rs, ok := n.(*ast.ReturnStmt)
			if !ok || resIdx >= len(rs.Results) {
				return true
			}
			sub := wireMapSite{}
			if wireMapClassify(&sub, rs.Results[resIdx], inner, funcs, visited, depth+1) {
				found = true
				s.Keys = wireMapUnion(s.Keys, sub.Keys)
				s.CondKeys = wireMapUnion(s.CondKeys, sub.CondKeys)
				s.DynKey = s.DynKey || sub.DynKey
				s.Partial = s.Partial || sub.Partial
			}
			return true
		})
		return found
	}
	return false
}

// wireMapReturnsStringMapAt reports whether result resIdx is a map[string]…. Named results
// (`(out map[string]any, err error)`) count the same as unnamed ones.
func wireMapReturnsStringMapAt(fd *ast.FuncDecl, resIdx int) bool {
	if fd.Type.Results == nil {
		return false
	}
	var types []ast.Expr
	for _, f := range fd.Type.Results.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			types = append(types, f.Type)
		}
	}
	if resIdx >= len(types) {
		return false
	}
	mt, ok := types[resIdx].(*ast.MapType)
	if !ok {
		return false
	}
	k, ok := mt.Key.(*ast.Ident)
	return ok && k.Name == "string"
}

func wireMapLookup(fun ast.Expr, funcs map[string]*ast.FuncDecl) *ast.FuncDecl {
	switch v := fun.(type) {
	case *ast.Ident:
		return funcs[v.Name]
	case *ast.SelectorExpr:
		// Try pkg.Fn first: more reliable than the receiver-based a.Fn.
		if id, ok := v.X.(*ast.Ident); ok {
			if fd, ok := funcs[id.Name+"."+v.Sel.Name]; ok {
				return fd
			}
		}
		if fd, ok := funcs["."+v.Sel.Name]; ok {
			return fd
		}
		return funcs[v.Sel.Name]
	}
	return nil
}

func wireMapIsStringMapType(t ast.Expr) bool {
	mt, ok := t.(*ast.MapType)
	if !ok {
		return false
	}
	k, ok := mt.Key.(*ast.Ident)
	return ok && k.Name == "string"
}

func wireMapRecv(fl *ast.FieldList) string {
	if len(fl.List) == 0 {
		return "?"
	}
	tp := fl.List[0].Type
	if st, ok := tp.(*ast.StarExpr); ok {
		tp = st.X
	}
	if id, ok := tp.(*ast.Ident); ok {
		return id.Name
	}
	return "?"
}

func wireMapIsSel(e ast.Expr, pkg, sel string) bool {
	s, ok := e.(*ast.SelectorExpr)
	if !ok || s.Sel.Name != sel {
		return false
	}
	id, ok := s.X.(*ast.Ident)
	return ok && id.Name == pkg
}

type wireMapAdded struct {
	key  string
	cond bool
}

type wireMapEnv struct {
	assign   map[string]ast.Expr
	nassign  map[string]int
	added    map[string][]wireMapAdded
	dynIdx   map[string]bool
	encoders map[string]bool
	// position on the left-hand side of a multi-value assignment (out in out, err := f() is 0)
	mvIdx map[string]int
	// declared type of a `var x T`
	declType map[string]ast.Expr
}

// wireMapBuildEnv walks a function body once, collecting assignments to variables and index
// assignments into maps. It also records whether each sits inside a conditional (if / for /
// switch / select) — that is exactly what decides whether omitempty is needed.
func wireMapBuildEnv(body *ast.BlockStmt) *wireMapEnv {
	e := &wireMapEnv{
		assign: map[string]ast.Expr{}, nassign: map[string]int{},
		added: map[string][]wireMapAdded{}, dynIdx: map[string]bool{}, encoders: map[string]bool{},
		mvIdx: map[string]int{}, declType: map[string]ast.Expr{},
	}
	depth := 0
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		if n == nil {
			return
		}
		switch v := n.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt,
			*ast.TypeSwitchStmt, *ast.SelectStmt, *ast.CaseClause, *ast.CommClause:
			depth++
			for _, c := range wireMapChildren(v) {
				walk(c)
			}
			depth--
			return
		case *ast.AssignStmt:
			for i, lhs := range v.Lhs {
				var rhs ast.Expr
				if len(v.Rhs) == len(v.Lhs) {
					rhs = v.Rhs[i]
				} else if len(v.Rhs) == 1 {
					// The `out, aerr := a.payload(...)` shape. Without opening it
					// the workitems / notification / memo write sites drop out
					// entirely — the very sites being converted would vanish
					// from the golden.
					if ce, ok := v.Rhs[0].(*ast.CallExpr); ok {
						rhs = ce
						if id, ok := lhs.(*ast.Ident); ok {
							e.mvIdx[id.Name] = i
						}
					}
				}
				switch l := lhs.(type) {
				case *ast.Ident:
					if ce, ok := rhs.(*ast.CallExpr); ok && wireMapIsSel(ce.Fun, "json", "NewEncoder") {
						e.encoders[l.Name] = true
					}
					e.nassign[l.Name]++
					if _, seen := e.assign[l.Name]; !seen && rhs != nil {
						e.assign[l.Name] = rhs
					}
				case *ast.IndexExpr:
					id, ok := l.X.(*ast.Ident)
					if !ok {
						continue
					}
					if bl, ok := l.Index.(*ast.BasicLit); ok && bl.Kind == token.STRING {
						lit, _ := strconv.Unquote(bl.Value)
						e.added[id.Name] = append(e.added[id.Name], wireMapAdded{lit, depth > 0})
					} else {
						e.dynIdx[id.Name] = true
					}
				}
			}
		case *ast.ValueSpec:
			for i, nm := range v.Names {
				if v.Type != nil {
					e.declType[nm.Name] = v.Type
				}
				if i < len(v.Values) {
					e.nassign[nm.Name]++
					if _, seen := e.assign[nm.Name]; !seen {
						e.assign[nm.Name] = v.Values[i]
					}
				}
			}
		}
		for _, c := range wireMapChildren(n) {
			walk(c)
		}
	}
	walk(body)
	return e
}

func wireMapChildren(n ast.Node) []ast.Node {
	var out []ast.Node
	ast.Inspect(n, func(c ast.Node) bool {
		if c == nil || c == n {
			return c == n
		}
		out = append(out, c)
		return false
	})
	return out
}

func wireMapMergeAdded(s *wireMapSite, name string, env *wireMapEnv) {
	have := map[string]bool{}
	for _, k := range s.Keys {
		have[k] = true
	}
	for _, ak := range env.added[name] {
		if !have[ak.key] {
			s.Keys = append(s.Keys, ak.key)
			have[ak.key] = true
		}
		if ak.cond {
			s.CondKeys = append(s.CondKeys, ak.key)
		}
	}
	sort.Strings(s.Keys)
	s.CondKeys = wireMapUnion(nil, s.CondKeys)
}

func wireMapUnion(a, b []string) []string {
	m := map[string]bool{}
	for _, v := range a {
		m[v] = true
	}
	for _, v := range b {
		m[v] = true
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
