package main

// tmux の隔離ヘルパ 3 本（`paneShowing` / `isolatedTmuxSocket` / `isolateAgentState`・計 60 行）は
// **package main と internal/sessionx に同じ中身で 2 本立っている。**
//
// 🔴 **1 本化はできない。** Go はテストヘルパ（`_test.go` の中身）をパッケージ跨ぎで共有できず、
// 共有するには「テスト専用のものを製品パッケージへ公開する」しかない——それは移送で
// internal を切った意味を消す。**写しが 2 本あること自体は正しい設計。**
//
// 危ないのは**写しが割れること**のほうで、割れても**両方ともコンパイルが通り全テストが緑**になる。
// そのとき何が起きるかは実測されている: `isolateAgentState` は本物の `~/.claude` などへ
// materialize してしまう事故を防ぐための隔離で、**片方だけ古くなると、そちら側のテストが
// 開発者の実設定を読み書きする**（#335 で 7 行を両方へ入れたのはこの理由）。
//
// なのでこの検査の役目は 1 つだけ: **割れたら赤くする。** 直し方は「両方に同じ変更を入れる」で、
// どちらが原本かは問わない（byte 一致だけを要求する）。
//
// 📌 相対パスは**この検査が置かれた位置から**解決する（package main の test は
// workspace/agent が cwd なので `internal/sessionx/…` で届く）。README §4 の
// 「パターンではなく解決結果で判定する」に従い、**読めなかったら Skip ではなく Fatal**。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// 写しを持っている 2 ファイル。どちらが原本でもよい。
const (
	tmuxHelpersMain     = "session_rate_limit_state_test.go"
	tmuxHelpersSessionx = "internal/sessionx/testhelp_test.go"
)

// 両側に同じ中身で在るべきヘルパ。
var tmuxSharedHelpers = []string{"paneShowing", "isolatedTmuxSocket", "isolateAgentState"}

// funcSource returns the exact source bytes of a top-level func, comments excluded
// (the declaration only), so the comparison is of the code the two copies share.
func funcSource(t *testing.T, path, name string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		// 🔥 Skip にしない。移送でパスが変われば「読めないので何も検査しない」形になり、
		// この検査は無言のまま消える。
		t.Fatalf("%s を読めない（移送でパスが変わったならこの検査の定数も直すこと）: %v", path, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Name.Name != name {
			continue
		}
		return string(src[fset.Position(fd.Pos()).Offset:fset.Position(fd.End()).Offset])
	}
	t.Fatalf("%s に %s が無い（改名したなら tmuxSharedHelpers も直すこと）", path, name)
	return ""
}

func TestTmuxTestHelpersStayInSync(t *testing.T) {
	if len(tmuxSharedHelpers) == 0 {
		t.Fatal("比べるヘルパが 0 本＝この検査が無言化している")
	}
	for _, name := range tmuxSharedHelpers {
		a := funcSource(t, tmuxHelpersMain, name)
		b := funcSource(t, tmuxHelpersSessionx, name)
		if a == b {
			continue
		}
		t.Errorf("%s の写しが割れている: %s と %s で中身が違う。\n"+
			"1 本化はできない（Go はテストヘルパをパッケージ跨ぎで共有できない）ので、"+
			"**両方に同じ変更を入れて揃えること**。\n--- %s ---\n%s\n--- %s ---\n%s",
			name, tmuxHelpersMain, tmuxHelpersSessionx, tmuxHelpersMain, a, tmuxHelpersSessionx, b)
	}
}
