// wiremap_golden_test.go — `map[string]any` を直に返している JSON 書き出し地点の
// **キー集合**をゴールデン化する（CONTRACT-MAP / 脚③）。`wire.golden` の map 版。
//
// なぜ要るか。`wire.golden` は reflect で**名前付き型**の json タグを撮るので、
// **map 直返しは定義上ゼロ被覆**——`wire_golden_test.go` 冒頭のコメント自身が
// 「ここに無い経路が 2 つある。どちらも struct を持たないので撮れない」と書いている。
// 実測（develop 91faa224・go/ast）: **Agent の JSON 書き出し 285 地点のうち 226（79%）が map 直返し**で、
// **そのワイヤを変えても鳴る検査が 1 つも無い。**
// struct 化はその 368 を触る作業なので、**変換より先に「今の形」を固定する**。
//
// 撮り方は reflect ではなくソースの構文解析。map には型が無く、reflect で辿れる実体が
// そもそも存在しないため（型が有るものは `wire.golden` の担当）。
//
// 更新の仕方（ワイヤを意図して変えたとき / 変換で map が struct になったとき）:
//
//	cd workspace/agent && go test -run TestWireMapGolden -update-wiremap-golden ./...
//
// 🔴 撮り直した PR では**撮り直した理由を必ず書く**（routes.golden / wire.golden と同じ運用）。
// 「差分 0」より「この差分は意図」のほうが情報が多い。
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
	"testdata/wiremap.golden を実際の map 書き出し地点で書き換える（意図して変えたときだけ）")

const wireMapGoldenPath = "testdata/wiremap.golden"

// wireMapMinSites — 走査が壊れて「0 件だから緑」になるのを防ぐ下限。
// 🔴 README §4:「0 件」は道具が動いていることを確かめてから採用する。
// 実測 226（develop 91faa224・Agent）。struct 化が進めば**減る**のが正常なので下限は緩めに置き、
// 「全部消えた／半分消えた」級の壊れ方だけを捕まえる。
const wireMapMinSites = 100

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

// assertWireMapGoldenLines — routes/wire 用の assertGoldenLines と同じ突き合わせだが、
// **直し方の案内をこのゴールデンのものにする**。
// 共有ヘルパは `-update-routes-golden` を案内するので、そのまま使うと
// **赤を見た人に間違ったフラグを教える**（しかもそのフラグは何も直さない）。
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

// TestWireMapGoldenActuallyOpensMaps は「撮れているつもりで空」を防ぐ。
// 🔴 走査が黙って何も拾わなくなると、ゴールデンは緑のまま何も守らなくなる
// （`TestWireShapeGoldenCoversSession` と同じ趣旨）。
//
// 選んだ標本は**取りこぼしうる性質**を持つものだけにしてある——
// リテラル直書きの map は簡単に拾えるので対照にならない。
func TestWireMapGoldenActuallyOpensMaps(t *testing.T) {
	sites := scanWireMapSites(t, ".")
	byFunc := map[string][]wireMapSite{}
	for _, s := range sites {
		byFunc[s.Func] = append(byFunc[s.Func], s)
	}

	t.Run("同一形状が複数サイトあることを数えている", func(t *testing.T) {
		// 🔴 件数を数えていないと**同じ形状の N サイトのうち 1 つを消しても差分が出ない**
		//（assertGoldenLines の突き合わせは集合演算なので）。
		//
		// ⚠️ **標本に「変換対象のサイト」を選んではいけない。**
		// 最初は handleFSLineMarks を名指ししていたが、**この track がそれを struct 化した
		// 瞬間にこの保証ごと落ちた**（実測）。**自分の仕事で消える標本は保証にならない。**
		// 名指しは dyn 印つき（＝構造的に struct 化できない＝消えない）ものから採り、
		// 併せて「複数サイトを持つ関数が 1 つ以上ある」ことを性質として見る。
		multi := 0
		for _, ss := range byFunc {
			if len(ss) >= 2 {
				multi++
			}
		}
		if multi == 0 {
			t.Fatal("同一関数から複数サイトを書き出している関数が 1 つも無い＝件数の数え上げが効いていない")
		}
		t.Logf("複数サイトを持つ関数: %d 個", multi)

		// handleAgentRTKGain は 4 サイトのうち 1 つが dyn（rtk の出力をそのまま中継）で、
		// **構造的に struct 化できないので変換で消えない。**
		if got := byFunc["handleAgentRTKGain"]; len(got) < 2 {
			t.Errorf("handleAgentRTKGain の書き出し地点が %d 件しか取れていない（複数あるはず）", len(got))
		}
	})

	t.Run("形状関数の戻り値を開けている", func(t *testing.T) {
		// handlePutSlackConn は writeJSON(w, 200, slackStatus(...)) で、
		// キーは呼び出し先の本体にしか無い。キー数ではなく**キー名**を要求する
		//（数だけ見ていると、浅く止まったのに緑になる——CP 側で実際に踏んだ）。
		got := byFunc["handlePutSlackConn"]
		if len(got) == 0 {
			t.Fatal("handlePutSlackConn を拾えていない（形状関数の戻り値を開けていない）")
		}
		want := []string{"connected", "mode", "notify", "receive", "operator"}
		if !wireMapHasAll(got[0].Keys, want) {
			t.Errorf("slackStatus のキー集合が痩せている: %v\n  %v を含むべき", got[0].Keys, want)
		}
	})

	t.Run("条件付きキーを記録している", func(t *testing.T) {
		// omitempty の要否はここからしか読めない。1 件も記録できていないなら、
		// ゴールデンは「struct 化してよいか」の判断材料にならない。
		n := 0
		for _, s := range sites {
			if len(s.CondKeys) > 0 {
				n++
			}
		}
		if n == 0 {
			t.Error("条件付きキーを持つサイトが 1 件も無い——" +
				"条件分岐の追跡が壊れている（omitempty の要否が読めなくなる）")
		}
		t.Logf("条件付きキーを持つサイト: %d 件", n)
	})
}

// TestWireMapScanExclusionsAreJustified — 走査の除外に付けた理由を機械で検査する。
//
// 🔴 除外の理由は「製品バイナリの依存グラフに入らない」。**主張ではなく `go list -deps` で示す**
// （`git grep` では「入らない」は言えない）。**main からも ./... のどこからも辿れないこと**を見る。
// 誰かが製品コードから import した瞬間にここが赤くなり、除外は取り消される。
//
// これは免除の寿命の逆検査（§4）と同じ形——**除外が要らなくなる／許されなくなる道を機械が見張る。**
func TestWireMapScanExclusionsAreJustified(t *testing.T) {
	if len(wireMapScanExcluded) == 0 {
		t.Skip("除外なし")
	}
	// 🔴 go が無ければ Skip ではなく Fatal。Skip に落ちると
	// 「理由が検査されていない」ことに誰も気付かないまま緑になる。
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("go が無いので除外の理由を検査できない: %v", err)
	}
	mod, err := exec.Command("go", "list", "-m").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	modPath := strings.TrimSpace(string(mod))

	// 検査 1: 製品バイナリ（main）の依存グラフに入らない。
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps .: %v", err)
	}
	deps := strings.Fields(string(out))
	for pkg, why := range wireMapScanExcluded {
		full := modPath + "/" + pkg
		for _, d := range deps {
			if d == full {
				t.Errorf("走査から外している %s が `go list -deps .` に出る。\n"+
					"  外してよい理由は %q だったが、**製品バイナリから辿れるようになっている**。\n"+
					"  除外を取り消して走査対象に戻すか、import を外すこと。", pkg, why)
			}
		}
	}
	t.Logf("go list -deps .: %d パッケージ（除外対象は不在）", len(deps))

	// 検査 2: 非テストの import 元が自分以外に居ない。
	// 🔴 `go list -deps ./...` は**そのパッケージ自身を必ず含む**ので、
	// 「出るか出ないか」では判定できない（最初これで書いて偽の赤を出した）。
	// .Imports は**テストファイルの import を含まない**ので、
	// 「非テストの誰かが import したか」はここで見るのが正しい。
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
				continue // 自分自身は数えない
			}
			for _, im := range imports {
				if im == full {
					t.Errorf("%s が非テストコードから %s を import している。\n"+
						"  外してよい理由は %q だった。**テスト専用ではなくなっている。**",
						importer, pkg, why)
				}
			}
		}
	}
	if nchecked == 0 {
		t.Fatal("go list が 1 パッケージも返さなかった（検査が空回りしている）")
	}
	t.Logf("非テスト import を検査したパッケージ: %d", nchecked)
}

// TestWireMapScanExclusionsActuallySkip — 除外が**実際に効いている**ことを見る。
// 🔴 除外表に書いただけで走査側に繋がっていなければ、上の理由検査は
// 「使われていない表」を検査しているだけになる（#345 で踏んだ「写しを検査していた」と同族）。
func TestWireMapScanExclusionsActuallySkip(t *testing.T) {
	for _, s := range scanWireMapSites(t, ".") {
		dir := filepath.ToSlash(filepath.Dir(s.File))
		if why, skip := wireMapScanExcluded[dir]; skip {
			t.Errorf("%s は除外対象（%s）なのに走査結果に出ている＝除外が効いていない", s.File, why)
		}
	}
}

// --- ゴールデンの行 ---

// wireMapLines は 1 行 = 「同じ関数・同じキー集合のサイト群」。
//
// 🔴 行番号は入れない（無関係な編集で全行が動く）。代わりに**同一形状の件数 xN を行に入れる**——
// `assertGoldenLines` の差分は集合演算なので、件数を行に入れないと
// **5 サイトのうち 1 つを消しても差分が出ない。**
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
	b.WriteString("# 更新: cd workspace/agent && go test -run TestWireMapGolden -update-wiremap-golden ./...\n")
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

// --- 走査（go/ast のみ。型検査をしない）---

type wireMapSite struct {
	File     string
	Func     string
	Keys     []string
	CondKeys []string
	DynKey   bool
	Partial  bool
}

// wireMapScanExcluded — 走査から外すパッケージと、**外してよい理由**。
//
// 🔴 「テスト用だから」を人間の言葉で書いただけでは、次に誰かが internal/ 直下に
// テスト用でないものを置いたとき一緒に素通りする。**理由そのものを機械で検査する**
// （TestWireMapScanExclusionsAreJustified）。理由が成り立たなくなったら赤くなる。
//
// ⚠️ ここは**パッケージの完全一致**で照合する。前方一致にすると
// `internal/wiretestfoo` のような別物まで巻き込む。
var wireMapScanExcluded = map[string]string{
	"internal/wiretest": "テストからしか import されない共有ハーネスで、製品バイナリの依存グラフに入らない",
}

// wireMapHelpers — ヘルパ本体の中の書き出しは数えない（封筒であって DTO ではない）。
var wireMapHelpers = map[string]bool{
	"writeJSON": true, "WriteJSON": true, "writeAPIErr": true, "WriteErr": true, "writeErr": true,
}

func scanWireMapSites(t *testing.T, root string) []wireMapSite {
	t.Helper()
	var out []wireMapSite
	funcs := map[string]*ast.FuncDecl{} // 形状関数を引くための索引

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
				// 🔴 パッケージ修飾でも引けるようにする。素の名前だけだと
				// 別パッケージの同名関数に上書きされ、`uiprefs.Read()` のような
				// 他パッケージの形状関数が**黙って解決できなくなる**。
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

// wireMapPayload — 書き出し地点かどうかを判定し、payload の式を返す。
// 🔴 `conn.WriteJSON(v)`（引数 1 個）は **WebSocket のフレーム送信**で HTTP のワイヤではない。
// 引数 3 個を要求することで落としている（実測 11 件・codex app-server / Discord / Slack 宛）。
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

// wireMapClassify は payload が map かどうかを見て、map ならキー集合を埋める。
// 戻り値 false = map ではない（名前付き型・スライス・nil など。ここの担当外）。
// 🔴 depth は「意味の上限」ではなく暴走よけ。浅く切ると**黙ってキーが痩せる**——
// 実測: 上限 3 だと `workspaceStats → out := containerStats() → out := map{...}` の
// 4 段目で打ち切られ、8 キーが 3 キーになった（それでも緑に見えた）。
// 再帰は「関数の入れ子」で消費されるので、循環は visited で止めて深さは余裕を持たせる。
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
			// `var body map[string]any` に json.Unmarshal で詰める中継の形。
			// キーは実行時にしか決まらないので**キー 0 個＋dyn** が正しい記録
			// （「キーが無い」ではなく「静的に確定しない」）。
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
		if visited[fd] { // 相互再帰で戻らなくなるのを防ぐ
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

// wireMapReturnsStringMapAt は resIdx 番目の戻り値が map[string]… かを見る。
// 戻り値は名前付き（`(out map[string]any, err error)`）でも無名でも同じに数える。
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
		// pkg.Fn を先に試す（レシーバ経由の a.Fn より確度が高い）
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
	// 多値代入における左辺の位置（out, err := f() の out は 0）
	mvIdx map[string]int
	// `var x T` の宣言型
	declType map[string]ast.Expr
}

// wireMapBuildEnv は関数本体を 1 度走って、変数への代入・map への添字代入を集める。
// **条件文（if / for / switch / select）の中かどうか**も持つ——それが omitempty の要否そのもの。
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
					// 🔴 `out, aerr := a.payload(...)` の形。これを開けないと
					// workitems / notification / memo の書き出し地点が丸ごと落ちる
					// （＝変換対象そのものがゴールデンから消える）。
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
