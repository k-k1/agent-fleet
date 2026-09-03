// wiremap_convert_test.go — 「map → struct へ変換した 1 サイトが、ワイヤを 1 バイトも
// 変えていない」ことの証明（CONTRACT-MAP / 脚③・Agent 側）。
//
// 🔴 **旧 map リテラルはここに写して残す。** 変換したあと production には
// 「元の形」がどこにも無い。**基準が消えるので、消さずにテストへ移す。**
// 写しは production から機械的にコピーしたもので、**書き換えない。**
//
// ハーネス本体と罠の対照は wiremap_equiv_test.go。
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// --- ① handleFSLineMarks（Console: LineMarks）---

type lineMarksIn struct {
	Added    []int
	Modified []int
	Deleted  []int
}

func TestWireEquivLineMarks(t *testing.T) {
	inputs := []lineMarksIn{
		{Added: []int{1, 2}, Modified: []int{5}, Deleted: []int{9}},
		// 🔴 production の 3 経路（emptyMarks / 未追跡 / diff）はいずれも
		// make か []int{} で初期化するので **nil にならない**。
		// nil は `null`・空は `[]` で別物なので、空スライスの形を明示的に測る。
		{Added: []int{}, Modified: []int{}, Deleted: []int{}},
	}
	got := assertWireEquiv(t, "handleFSLineMarks", inputs,
		func(in lineMarksIn) any { // 旧（fs_git.go の map リテラルの写し）
			return map[string]any{"added": in.Added, "modified": in.Modified, "deleted": in.Deleted}
		},
		func(in lineMarksIn) any {
			return lineMarksWire{Added: in.Added, Modified: in.Modified, Deleted: in.Deleted}
		})
	t.Logf("突き合わせ方式: %s", got)
}

// --- ② instrState（Console: Payload）---
//
// 形状関数なので、これ 1 つで 2 サイト（GET と PUT）が型を得る。

type instrStateIn struct {
	Text       string
	MaxBytes   int
	Enabled    bool
	Path       string
	Targets    []instrTarget
	FleetBytes int
}

func TestWireEquivInstrState(t *testing.T) {
	inputs := []instrStateIn{
		{Text: "hello", MaxBytes: 8192, Enabled: true, Path: "/home/dev/notes.md",
			Targets: []instrTarget{{Kind: "claude", Supported: true, On: true}}, FleetBytes: 12},
		// 未記入＝空文字。**キーは出続けなければならない**（omitempty を付けると消える）。
		// Targets は production では make(…, 0, n) 済みで nil にならないが、
		// nil（`null`）と空（`[]`）は別物なので両方の形を測る。
		{Text: "", MaxBytes: 8192, Enabled: false, Path: "/x", Targets: []instrTarget{}, FleetBytes: 0},
	}
	got := assertWireEquiv(t, "instrState", inputs,
		func(in instrStateIn) any { // 旧（agent_instructions.go の map リテラルの写し）
			return map[string]any{
				"text":        in.Text,
				"bytes":       len(in.Text),
				"max_bytes":   in.MaxBytes,
				"enabled":     in.Enabled,
				"path":        in.Path,
				"targets":     in.Targets,
				"fleet_bytes": in.FleetBytes,
			}
		},
		func(in instrStateIn) any {
			return instrStateWire{
				Text: in.Text, Bytes: len(in.Text), MaxBytes: in.MaxBytes,
				Enabled: in.Enabled, Path: in.Path, Targets: in.Targets, FleetBytes: in.FleetBytes,
			}
		})
	t.Logf("突き合わせ方式: %s", got)
}

// TestWireEquivConvertedSitesAreAllCovered — この PR で変換した形状に
// 等価テストが 1 本ずつ在ることを機械で見る。
//
// 🔴 **変換が進むほどこの 1 本の重みが増す**（#345 のレビュワーが実測）:
// 変換済みサイトはもう map ではないので **wiremap.golden は守らない**。
// json タグを改名しても**ゴールデンも旧駆動テストも PASS**で、
// **赤くなるのは等価テストだけ**。だから「等価テストが在ること」自体を検査する。
func TestWireEquivConvertedSitesAreAllCovered(t *testing.T) {
	covered := map[string]string{
		"lineMarksWire":  "TestWireEquivLineMarks",
		"instrStateWire": "TestWireEquivInstrState",
	}
	declared := wiremapConvertedWireTypes(t, ".")
	for _, name := range declared {
		if _, ok := covered[name]; !ok {
			t.Errorf("%s は CONTRACT-MAP が足した wire 型だが、等価テストが登録されていない。"+
				"変換だけして証明を書き忘れると全ゲート緑のまま通るので、ここで止める。", name)
		}
	}
	for name := range covered {
		found := false
		for _, d := range declared {
			if d == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s の等価テストが登録されているが、型がソースに無い（消したなら表からも消すこと）", name)
		}
	}
	if len(declared) == 0 {
		t.Fatal("wire 型を 1 つも見つけられなかった（走査が壊れている）")
	}
	t.Logf("変換済みの wire 型: %d 個", len(declared))
}

// wiremapConvertedMarker — 変換で生まれた型の doc コメントに必ず書く印。
//
// 🔴 型名の接尾辞（`…Wire`）では判別できない。**`mcpServerWire` のように
// CONTRACT-MAP より前から在る型が同じ綴りを持つ**ので、名前で拾うと
// 「証明が要る型」と「元から struct だった型」が混ざる。
// **「その型が map を置き換えたものか」は名前ではなく由来の情報**なので、
// 由来をコメントに書き、それを機械が読む。
const wiremapConvertedMarker = "旧: map[string]any"

func wiremapConvertedWireTypes(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, perr := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if perr != nil {
			return perr
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE || gd.Doc == nil {
				continue
			}
			if !strings.Contains(gd.Doc.Text(), wiremapConvertedMarker) {
				continue
			}
			for _, sp := range gd.Specs {
				ts, ok := sp.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if _, ok := ts.Type.(*ast.StructType); ok {
					out = append(out, ts.Name.Name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("走査に失敗: %v", err)
	}
	sort.Strings(out)
	return out
}
