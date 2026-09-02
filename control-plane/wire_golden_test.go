// wire_golden_test.go — Console が読む代表的なレスポンス DTO の**キー集合**をゴールデン化する。
//
// なぜ要るか（ADR 0067 決定 6）。ルート表（routes_golden_test.go）が守るのは「窓口が
// 在るか」だけで、窓口から出てくる JSON の形は守らない。移送で struct を internal/ へ
// 動かすとき、json タグの打ち直し・field の載せ忘れ・型の取り違えは **Go のコンパイラを
// 一切鳴らさずに** Console 側だけを壊す。sessionWire は現にこれを 3 回踏んでいる
// （Title / driver / color・context・exit 系。session_wire_test.go の冒頭に経緯）。
//
// ★ 目的は「json タグが変わったら赤くなる」ことであって、値の網羅ではない。値は
// 既存の session_wire_test.go のような往復テストの担当。ここはキー・JSON 上の型・
// omitempty だけを撮る。
//
// ★ Go の型名は書かない。移送で型が main から internal/x へ移ると型名は必ず変わるが、
// **ワイヤは何も変わっていない**——型名を撮ると全移送が偽の赤になる。
//
// 更新の仕方（ワイヤを意図して変えたとき）:
//
//	cd control-plane && go test -run TestWireShapeGolden -update-wire-golden ./...
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var updateWireGolden = flag.Bool("update-wire-golden", false,
	"testdata/wire.golden を実際の DTO 形状で書き換える（ワイヤを意図して変えたときだけ）")

const wireGoldenPath = "testdata/wire.golden"

// wireGoldenTypes は「Console が実際に読む」もののうち代表を選んだもの。全 DTO を
// 並べるのが目的ではない（それは維持されない）——**壊れると画面が壊れる**ものを置く。
//
// ⚠️ ここに無い経路が 2 つある。どちらも struct を持たないので撮れない:
//   - GET /api/workspace と /api/workspace/stats は map[string]any を組み立てて返す
//     （workspace_handlers.go workspacePayload / metrics.go workspaceStats）。
//   - /api/repos 系は Agent への素通しプロキシで、DTO は workspace/agent 側にある
//     （そちらの wire_golden_test.go が撮っている）。
func wireGoldenTypes() []struct {
	name string
	typ  reflect.Type
} {
	return []struct {
		name string
		typ  reflect.Type
	}{
		// セッション一覧 / SSE。Agent 応答を decode→再 emit する中継なので、
		// ここに無い field は silently drop される（前科 3 回）。
		{"sessionWire", reflect.TypeOf(sessionWire{})},
		{"adminSessionRow", reflect.TypeOf(adminSessionRow{})},
		// 使用量（管理画面の集計とヒートマップ）。
		{"usageTotal", reflect.TypeOf(usageTotal{})},
		{"usageHourlyResponse", reflect.TypeOf(usageHourlyResponse{})},
		// 通知センター。
		{"notificationDTO", reflect.TypeOf(notificationDTO{})},
		// 作業アイテム受信箱。
		{"workItemDTO", reflect.TypeOf(workItemDTO{})},
		{"workItemQueryDTO", reflect.TypeOf(workItemQueryDTO{})},
		{"workItemSessionDTO", reflect.TypeOf(workItemSessionDTO{})},
		// メモキュー / 定時実行。
		{"memoDTO", reflect.TypeOf(memoDTO{})},
		{"scheduleDTO", reflect.TypeOf(scheduleDTO{})},
		// 版情報（更新トーストと再起動バッジ）。
		{"imageInfo", reflect.TypeOf(imageInfo{})},
	}
}

func TestWireShapeGolden(t *testing.T) {
	var got []string
	for _, e := range wireGoldenTypes() {
		got = append(got, wireShape(t, e.name, e.typ)...)
	}
	sort.Strings(got)

	if *updateWireGolden {
		writeWireGolden(t, wireGoldenPath, got)
		t.Logf("wrote %s (%d keys)", wireGoldenPath, len(got))
		return
	}
	assertGoldenLines(t, wireGoldenPath, got)
}

// TestWireShapeGoldenCoversSessionWire は「撮れているつもりで 0 件」を防ぐ。
// wireShape が黙って空を返す壊れ方をすると、ゴールデンは緑のまま何も守らなくなる。
func TestWireShapeGoldenCoversSessionWire(t *testing.T) {
	lines := wireShape(t, "sessionWire", reflect.TypeOf(sessionWire{}))
	for _, want := range []string{
		"sessionWire.exitCode number,omitempty",      // docs/log/26 の exit chip
		"sessionWire.color string,omitempty",         // SSM の背景色
		"sessionWire.driver string,omitempty",        // managed 判定
		"sessionWire.context raw,omitempty",          // ContextBar
		"sessionWire.backgroundBusy bool",            // BG 実行中バッジ
		"sessionWire.locked bool,omitempty",          // 削除ロック
		"sessionWire.workingCopyId string,omitempty", // 作業コピーの世代
	} {
		if !containsLine(lines, want) {
			t.Errorf("wireShape が %q を返さない（過去に drop 事故を起こした field）", want)
		}
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// --- 形状の抽出 ---

var jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

// wireShape は 1 つの型を "<prefix>.<jsonキー> <JSON上の型>[,omitempty]" の行へ畳む。
// 入れ子は "a.b.c"、struct の配列は "a[].b" と書く。
func wireShape(t *testing.T, name string, typ reflect.Type) []string {
	t.Helper()
	var out []string
	shapeInto(t, name, typ, map[reflect.Type]bool{}, &out)
	if len(out) == 0 {
		t.Fatalf("%s: キーが 1 つも取れなかった（wireShape が壊れている）", name)
	}
	sort.Strings(out)
	return out
}

func shapeInto(t *testing.T, prefix string, typ reflect.Type, seen map[reflect.Type]bool, out *[]string) {
	t.Helper()
	typ = deref(typ)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("%s: struct でない型は撮れない: %s", prefix, typ.Kind())
	}
	if seen[typ] {
		*out = append(*out, prefix+" <recursive>")
		return
	}
	seen[typ] = true
	defer delete(seen, typ)

	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue // json は出さない
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		key, opts, _ := strings.Cut(tag, ",")
		if key == "" {
			key = f.Name
		}
		// 埋め込み（匿名）で json タグが無いものは field が親へ持ち上がる。
		if f.Anonymous && f.Tag.Get("json") == "" && deref(f.Type).Kind() == reflect.Struct {
			shapeInto(t, prefix, f.Type, seen, out)
			continue
		}
		suffix := ""
		if strings.Contains(","+opts+",", ",omitempty,") {
			suffix = ",omitempty"
		}
		emitField(t, prefix+"."+key, f.Type, suffix, seen, out)
	}
}

func emitField(t *testing.T, path string, typ reflect.Type, suffix string, seen map[reflect.Type]bool, out *[]string) {
	t.Helper()
	typ = deref(typ)
	// 自前 MarshalJSON を持つ型は field 構成と出力が無関係なので、そこで止める
	// （time.Time / json.RawMessage / 独自エンコーダ）。
	if typ != reflect.TypeOf(json.RawMessage{}) &&
		(typ.Implements(jsonMarshalerType) || reflect.PointerTo(typ).Implements(jsonMarshalerType)) {
		*out = append(*out, path+" custom"+suffix)
		return
	}
	switch typ.Kind() {
	case reflect.Struct:
		shapeInto(t, path, typ, seen, out)
	case reflect.Slice, reflect.Array:
		elem := deref(typ.Elem())
		switch {
		case elem.Kind() == reflect.Uint8:
			// []byte は base64、json.RawMessage は生 JSON。どちらも「中身は撮らない」。
			if typ == reflect.TypeOf(json.RawMessage{}) {
				*out = append(*out, path+" raw"+suffix)
			} else {
				*out = append(*out, path+" base64"+suffix)
			}
		case elem.Kind() == reflect.Struct:
			shapeInto(t, path+"[]", elem, seen, out)
		default:
			*out = append(*out, path+" ["+jsonKind(elem)+"]"+suffix)
		}
	case reflect.Map:
		*out = append(*out, path+" object"+suffix)
	default:
		*out = append(*out, path+" "+jsonKind(typ)+suffix)
	}
}

// jsonKind は Go の型名ではなく **JSON 上の型**を返す。型名を書くと移送
// （main → internal/x）で必ず変わり、ワイヤが同じでも赤くなる。
func jsonKind(typ reflect.Type) string {
	switch typ.Kind() {
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.String:
		return "string"
	case reflect.Interface:
		return "any"
	case reflect.Map:
		return "object"
	default:
		return typ.Kind().String()
	}
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func writeWireGolden(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	var b strings.Builder
	b.WriteString("# Console が読む代表的な DTO の JSON キー集合。生成物 —— 手で編集しない。\n")
	b.WriteString("# 更新: cd control-plane && go test -run TestWireShapeGolden -update-wire-golden ./...\n")
	b.WriteString("# 形式: <型>.<キーパス> <JSON上の型>[,omitempty]（[]=配列 / raw=素通し JSON）\n")
	fmt.Fprintf(&b, "# count: %d\n", len(lines))
	for _, ln := range lines {
		b.WriteString(ln)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
