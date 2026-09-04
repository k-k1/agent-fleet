// wiremap_golden_test.go — goldens the KEY SET of every JSON write site that returns a
// bare `map[string]any` (CONTRACT-MAP / leg 3). The map counterpart of `wire.golden`.
//
// `wire.golden` snapshots json tags by reflecting over NAMED types, so a bare map return
// has zero coverage by construction — the header of `wire_golden_test.go` says as much
// itself. Measured (go/ast): 368 of the 468 JSON write sites (79%) return a map directly,
// and not one check fails when their wire changes. Converting them to structs is work on
// those same 368 sites, so pin the current shape before converting anything.
//
// The scan parses source rather than reflecting: a map carries no type, so there is no
// entity for reflect to walk (typed payloads are `wire.golden`'s job).
//
// To update (a deliberate wire change, or a map that became a struct):
//
//	cd control-plane && go test -run TestWireMapGolden -update-wiremap-golden ./...
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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var updateWireMapGolden = flag.Bool("update-wiremap-golden", false,
	"testdata/wiremap.golden を実際の map 書き出し地点で書き換える（意図して変えたときだけ）")

const wireMapGoldenPath = "testdata/wiremap.golden"

// wireMapMinSites is the floor that keeps a broken scan from passing as "green because
// zero sites". README §4: a count of zero is only usable once the tool is known to work.
// Measured 368; the number is expected to FALL as structs replace maps, so the floor is
// deliberately loose and only catches "everything / half of it vanished" breakage.
const wireMapMinSites = 60

func TestWireMapGolden(t *testing.T) {
	sites := scanWireMapSites(t, ".")
	if len(sites) < wireMapMinSites {
		t.Fatalf("map 書き出し地点が %d 件しか取れなかった（下限 %d）。"+
			"走査が壊れている疑いが濃い——ゴールデンを撮り直す前に道具を疑うこと。",
			len(sites), wireMapMinSites)
	}
	got := wireMapLines(sites)

	if *updateWireMapGolden {
		writeWireMapGolden(t, wireMapGoldenPath, got)
		t.Logf("wrote %s (%d 行 / %d サイト)", wireMapGoldenPath, len(got), len(sites))
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
		t.Errorf("%s と一致しない:\n%s\n"+
			"意図した増減なら -update-wiremap-golden で撮り直し、**撮り直した理由を PR に書く**。\n"+
			"  行が消えた = その map サイトが struct になった（変換したなら意図どおり）か、消えた。\n"+
			"  キーが増減した = ワイヤが変わった可能性そのもの。等価テストで示せていないなら止まること。",
			path, diff)
	}
}

// TestWireMapGoldenActuallyOpensMaps guards against "snapshotted, but empty": once the scan
// silently stops picking anything up, the golden stays green while protecting nothing (same
// intent as `TestWireShapeGoldenCoversSessionWire`).
//
// The samples are chosen only for properties the scan could plausibly miss:
//   - a map held in a variable plus conditional keys (a plain literal is easy to pick up,
//     so it would not be a control)
//   - the return value of a shape function (only reachable by opening the callee's body)
//   - foreign keys merged in (without a dyn mark the key set would be over-trusted)
func TestWireMapGoldenActuallyOpensMaps(t *testing.T) {
	sites := scanWireMapSites(t, ".")
	byFunc := map[string][]wireMapSite{}
	for _, s := range sites {
		byFunc[s.Func] = append(byFunc[s.Func], s)
	}

	t.Run("条件付きキーを持つ変数 map を開けている", func(t *testing.T) {
		// Do not name the sample. This used to point at gitServerAPI.blob, and the
		// guarantee died the moment this track turned that site into a struct (#347 was
		// the same shape). What your own work deletes cannot guarantee anything.
		//
		// The reason it was picked is the property — "opens a variable map whose keys are
		// added in a conditional" — so assert the property instead: some site has it, with
		// both its keys and its conditions recorded. Nothing is named, so the next
		// conversion cannot take the check with it.
		var withCond []wireMapSite
		for _, s := range sites {
			if len(s.CondKeys) > 0 && len(s.Keys) > len(s.CondKeys) {
				// Having BOTH conditional and unconditional keys is the evidence
				// that the "base map literal + keys added in an if" shape was opened.
				withCond = append(withCond, s)
			}
		}
		if len(withCond) == 0 {
			t.Fatal("「基底のキー ＋ 条件付きキー」を両方持つサイトが 1 つも無い＝" +
				"変数 map の追跡か条件分岐の判定が壊れている（omitempty の要否が読めなくなる）")
		}
		t.Logf("基底＋条件付きの両方を開けたサイト: %d 件（例: %s の %v / cond=%v）",
			len(withCond), withCond[0].Func, withCond[0].Keys, withCond[0].CondKeys)
	})

	t.Run("形状関数の戻り値を2段たどれている", func(t *testing.T) {
		// workspaceAPI.stats is writeJSON(w, 200, workspaceStats(...)), and workspaceStats
		// returns out := containerStats(...): the keys live in the callee OF the callee.
		//
		// Asserting "green if at least one key" let a real miss through: the recursion
		// depth limit was too shallow to descend into containerStats and stopped at 3
		// keys, still passing. Demand the specific key NAMES, not a key count.
		//
		// Take the sample from the exemption table — an exemption means "structurally
		// cannot become a struct", so it is machine-stated that this track's conversion
		// will not delete it. Where naming a site is unavoidable (the second-level key
		// names are the point), at least pick from a set that does not disappear.
		const sample = "workspaceAPI.stats"
		exempt := false
		for _, ex := range wiremapExemptions() {
			if ex.Func == sample {
				exempt = true
			}
		}
		if !exempt {
			t.Fatalf("%s が免除表に無い。標本は「変換で消えない」ものから採る約束なので、"+
				"免除でなくなったなら標本も選び直すこと。", sample)
		}
		got := byFunc[sample]
		if len(got) == 0 {
			t.Fatal(sample + " を拾えていない（形状関数の戻り値を開けていない）")
		}
		// running/starting/oom_recent come from workspaceStats itself (level 1);
		// exit_code/oom_killed/cpu_pct/mem_max/oom_kill_total from containerStats (level 2).
		want := []string{"running", "starting", "oom_recent", "exit_code", "oom_killed", "cpu_pct", "mem_max"}
		if !wireMapHasAll(got[0].Keys, want) {
			t.Errorf("2 段目（containerStats）まで降りていない: %v\n  %v を含むべき", got[0].Keys, want)
		}
		// Even so the key set is not complete: containerStats assigns `out` three separate
		// times, and mem_used in the third literal `{"running": true, "mem_used": memUsed}`
		// is unreachable for this scan. Unless that unreachability itself shows up in the
		// golden, the reader takes the key set for complete.
		if wireMapHasAll(got[0].Keys, []string{"mem_used"}) {
			t.Log("mem_used まで開けるようになった。partial 印の要否を見直すこと")
		} else if !got[0].Partial {
			t.Error("キー集合が不完全（mem_used が取れていない）のに partial 印が付いていない——" +
				"ゴールデンの読者がキー集合を完全だと誤解する")
		}
	})

	t.Run("外来キーの流し込みに dyn 印が付く", func(t *testing.T) {
		// workspaceStats merges a map coming from the Agent with `for k, v := range`, so
		// the key set is not statically determined and must not be snapshotted as if it were.
		got := byFunc["workspaceAPI.stats"]
		if len(got) == 0 || !got[0].DynKey {
			t.Error("workspaceStats の dyn 印が落ちている——" +
				"キー集合が静的に確定しないことがゴールデンから読めなくなる")
		}
	})
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
	b.WriteString("# map[string]any を直に返している JSON 書き出し地点のキー集合。生成物 —— 手で編集しない。\n")
	b.WriteString("# 更新: cd control-plane && go test -run TestWireMapGolden -update-wiremap-golden ./...\n")
	b.WriteString("# 形式: <ファイル> <関数> x<同一形状の件数> {キー集合} [cond{条件付きキー}] [dyn|partial]\n")
	b.WriteString("#   cond    = 条件文の中で足されるキー（struct 化するなら omitempty の要否を判断する対象）\n")
	b.WriteString("#   dyn     = 非リテラルのキーがある（キー集合は静的に確定しない＝struct 化できない）\n")
	b.WriteString("#   partial = 変数が複数回代入されており、キー集合を開けきれていない\n")
	b.WriteString("# 🔴 キーが減った/増えた差分は、ワイヤが変わった可能性そのもの。撮り直す理由を PR に書くこと。\n")
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
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", p, perr)
		}
		files = append(files, f)
		names = append(names, filepath.ToSlash(p))
		return nil
	})
	if err != nil {
		t.Fatalf("走査に失敗: %v", err)
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
					// entirely — the very sites being converted would vanish from
					// the golden.
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
