// wiremap_convert_test.go — 「map → struct へ変換した 1 サイトが、ワイヤを 1 バイトも
// 変えていない」ことの証明（CONTRACT-MAP / 脚③）。
//
// 🔴 **旧 map リテラルはここに写して残す。** 変換したあと、production 側にはもう
// 「元の形」がどこにも無い。**基準が消えるので、消さずにテストへ移す。**
// 写しは production から機械的にコピーしたもので、**書き換えない**
// （書き換えた瞬間、基準ではなく「新しい実装の別表現」になる）。
//
// ハーネス本体と罠の対照は wiremap_equiv_test.go。
// ここは「どのサイトを変換し、その等価をどう示したか」だけを持つ。
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

// wiremapConvertedMarker — 変換で生まれた型の doc コメントに必ず書く印。
//
// 🔴 型名の接尾辞（`…Wire`）では判別できない。**`sessionWire` のように
// CONTRACT-MAP より前から在る型が同じ綴りを持つ**ので、名前で拾うと
// 「証明が要る型」と「元から struct だった型」が混ざる。
// **「その型が map を置き換えたものか」は名前ではなく由来の情報**なので、
// 由来をコメントに書き、それを機械が読む。
const wiremapConvertedMarker = "旧: map[string]any"

// wiremapConvertedWireTypes は「map を置き換えた」と doc コメントで宣言している
// struct 型の名前を返す。
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

// --- ① egressAPI.checkHosts（Console: EgressCheck）---

type egressCheckIn struct {
	Configured bool
	Mode       string
	Enforce    bool
	Hosts      map[string]egressHostVerdict
}

func TestWireEquivEgressCheck(t *testing.T) {
	inputs := []egressCheckIn{
		{Configured: true, Mode: "enforce", Enforce: true,
			Hosts: map[string]egressHostVerdict{"a.example": {Host: "a.example", Allowed: true, Proposed: false}}},
		// 呼び出し側は make() 済みなので nil にはならないが、**空 map と nil map は
		// `{}` と `null` で別物**なので両方測る。
		{Configured: false, Mode: "log-only", Enforce: false, Hosts: map[string]egressHostVerdict{}},
	}
	got := assertWireEquiv(t, "egressAPI.checkHosts", inputs,
		func(in egressCheckIn) any { // 旧（egress_member.go の map リテラルの写し）
			return map[string]any{
				"configured": in.Configured, "mode": in.Mode, "enforce": in.Enforce, "hosts": in.Hosts,
			}
		},
		func(in egressCheckIn) any {
			return egressCheckWire{
				Configured: in.Configured, Mode: in.Mode, Enforce: in.Enforce, Hosts: in.Hosts,
			}
		})
	t.Logf("突き合わせ方式: %s", got)
}

// --- ② adminAPI.hostStats（Console: HostStats）---

type hostStatsIn struct {
	Load1    float64
	Ncpu     int
	MemUsed  uint64
	MemTotal uint64
}

func TestWireEquivHostStats(t *testing.T) {
	inputs := []hostStatsIn{
		{Load1: 1.25, Ncpu: 8, MemUsed: 3 << 30, MemTotal: 10 << 30},
		// 🔴 uint64 を float64 で受け直していないことを実際に測る標本。
		// 2^53 を超える値は float64 では正確に表せない。
		{Load1: 0, Ncpu: 1, MemUsed: 1<<53 + 1, MemTotal: 1<<62 + 3},
	}
	got := assertWireEquiv(t, "adminAPI.hostStats", inputs,
		func(in hostStatsIn) any { // 旧（metrics.go の map リテラルの写し）
			return map[string]any{
				"load1": in.Load1, "ncpu": in.Ncpu, "mem_used": in.MemUsed, "mem_total": in.MemTotal,
			}
		},
		func(in hostStatsIn) any {
			return hostStatsWire{
				Load1: in.Load1, Ncpu: in.Ncpu, MemUsed: in.MemUsed, MemTotal: in.MemTotal,
			}
		})
	t.Logf("突き合わせ方式: %s", got)
}

// --- ③ updateStatus（Console: HostUpdateStatus）---

type updateStatusIn struct {
	Current   string
	Installed string
	Systemd   bool
}

func TestWireEquivUpdateStatus(t *testing.T) {
	inputs := []updateStatusIn{
		{Current: "v1", Installed: "v2", Systemd: true},
		// installed="" が「staged 無し」の表現。**キーは出続けなければならない。**
		{Current: "v1", Installed: "", Systemd: false},
		{Current: "v1", Installed: "v1", Systemd: false}, // 同版＝restartRequired false
	}
	got := assertWireEquiv(t, "updateStatus", inputs,
		func(in updateStatusIn) any { // 旧（update.go の map リテラルの写し）
			return map[string]any{
				"current":         in.Current,
				"installed":       in.Installed,
				"restartRequired": in.Installed != "" && in.Installed != in.Current,
				"systemd":         in.Systemd,
			}
		},
		func(in updateStatusIn) any {
			return hostUpdateStatusWire{
				Current:         in.Current,
				Installed:       in.Installed,
				RestartRequired: in.Installed != "" && in.Installed != in.Current,
				Systemd:         in.Systemd,
			}
		})
	t.Logf("突き合わせ方式: %s", got)
}

// --- ④ workItemsAPI.workItemsPayload（Console: WorkItemPayload）---
//
// 形状関数なので、これ 1 つで 3 サイト（list ×1 / refresh ×2）が型を得る。

type workItemsPayloadIn struct {
	Items     []workItemDTO
	Queries   []workItemQueryDTO
	Sessions  []workItemSessionDTO
	FetchedAt string
	Running   bool
}

func TestWireEquivWorkItemsPayload(t *testing.T) {
	inputs := []workItemsPayloadIn{
		{
			Items:     []workItemDTO{{ID: "i1", Labels: []string{"bug"}}},
			Queries:   []workItemQueryDTO{{ID: "q1", Enabled: true}},
			Sessions:  []workItemSessionDTO{{ID: "s1"}},
			FetchedAt: "2026-09-03T00:00:00Z", Running: true,
		},
		// 🔴 production は make(…, 0, n) 済みなので**空スライス**が出る。
		// nil スライスは `null`・空スライスは `[]` で**別物**なので、両方を測る
		//（ゼロ値ケース＝全部 nil はハーネスが自動で足す）。
		{
			Items:    []workItemDTO{},
			Queries:  []workItemQueryDTO{},
			Sessions: []workItemSessionDTO{},
		},
	}
	got := assertWireEquiv(t, "workItemsAPI.workItemsPayload", inputs,
		func(in workItemsPayloadIn) any { // 旧（workitems.go の map リテラルの写し）
			return map[string]any{
				"items": in.Items, "queries": in.Queries, "sessions": in.Sessions,
				"fetchedAt": in.FetchedAt, "running": in.Running,
			}
		},
		func(in workItemsPayloadIn) any {
			return workItemsPayloadWire{
				Items: in.Items, Queries: in.Queries, Sessions: in.Sessions,
				FetchedAt: in.FetchedAt, Running: in.Running,
			}
		})
	t.Logf("突き合わせ方式: %s", got)
}

// TestWireEquivConvertedSitesAreAllCovered — この PR で変換した形状に
// 等価テストが 1 本ずつ在ることを機械で見る。
//
// 🔴 **なぜ要るか**: 変換だけして等価テストを書き忘れても、**全ゲートは緑のまま通る**
// （型検査は通り、ゴールデンからはそのサイトが消えるだけ）。
// 「証明が付いていない変換」を捕まえる網が他に無い。
func TestWireEquivConvertedSitesAreAllCovered(t *testing.T) {
	// 変換した wire 型 → 等価テストの名前。**型を足したらここも足す。**
	covered := map[string]string{
		"egressCheckWire":      "TestWireEquivEgressCheck",
		"hostStatsWire":        "TestWireEquivHostStats",
		"hostUpdateStatusWire": "TestWireEquivUpdateStatus",
		"workItemsPayloadWire": "TestWireEquivWorkItemsPayload",
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
