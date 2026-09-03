// wiremap_equiv_test.go — 「ワイヤを変えていないこと」の証明に使う道具（CONTRACT-MAP / 脚③）。
//
// なぜ要るか。`map[string]any` を struct に置き換える変更は、**Go の型検査も go vet も
// 一切鳴らさずに JSON のバイト列を変える。** そして今この面を守る検査は 1 つも無い——
// `wire.golden` は reflect で名前付き型の json タグを撮るので、**map は定義上ゼロ被覆**
// （`wire_golden_test.go` 冒頭のコメント自身が「撮れない経路」として
// workspacePayload / workspaceStats を名指ししている）。
//
// 既知の 5 つの罠は全部「同じキー・同じ値に見えるのに違う」形をしている:
//
//	① omitempty の有無      — map はキーを入れなければ出ない。struct はゼロ値でも出る
//	② nil と空              — nil スライスは null、[]T{} は []（map / struct 両方で起きる）
//	③ 数値の型と精度        — int64 の大きな値を float64 で受けると桁が落ちる
//	④ キーの綴り            — json タグを書き忘れると Go の公開名がそのまま出る
//	⑤ キーの有無とゼロ値    — Console が `if (x.foo)` で見ていると両者が潰れる
//
// ⑥ として、README §4 が全トラックに課している「**同じ型のフィールドを 2 つ入れ替える
// 変異**」も同じ道具で捕まる（型が合うので型検査は絶対に鳴らない）。
//
// 使い方（変換する 1 サイトにつき 1 回）:
//
//	assertWireEquiv(t, "workItemsAPI.list", inputs,
//	    func(f fixture) any { /* 旧 map リテラルをそのまま写す */ },
//	    func(f fixture) any { /* 新 struct を組む */ })
//
// 🔴 **旧 map リテラルは消さずにここへ写す。** それが唯一の参照実装であり、
// 「変えていない」の基準になるものが他に無い。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// wireEquivMode は突き合わせに使った方式。**どちらを使ったかは報告に書く必要がある**ので記録する。
type wireEquivMode string

const (
	// バイト列がそのまま一致した。
	wireEquivBytes wireEquivMode = "bytes"
	// バイト列は違うがパース後の深い比較で一致した。
	// struct は宣言順・map はキー昇順で書かれるので、**キー順の差だけなら正常**。
	wireEquivParsed wireEquivMode = "parsed"
)

// wireEquivT は *testing.T のうちハーネスが使う分だけ。
// 自己診断（罠を本当に捕まえるか）で record 実装を差し込むために切ってある。
type wireEquivT interface {
	Helper()
	Errorf(format string, args ...any)
}

type wireEquivResult struct {
	Name  string
	Cases int
	Modes map[wireEquivMode]int
}

func (r wireEquivResult) String() string {
	return fmt.Sprintf("%s: %d ケース (bytes=%d parsed=%d)",
		r.Name, r.Cases, r.Modes[wireEquivBytes], r.Modes[wireEquivParsed])
}

// assertWireEquiv は同じ入力に対する旧 map と新 struct の JSON が一致することを主張する。
//
// 🔴 **ゼロ値の入力はハーネスが必ず先頭に足す。** ①③⑤ の罠はゼロ値でしか現れないので、
// 「呼ぶ側がゼロ値ケースを書き忘れる」だけで検査が無言になる。それを作法ではなく機械で塞ぐ。
func assertWireEquiv[T any](t wireEquivT, name string, inputs []T, oldFn, newFn func(T) any) wireEquivResult {
	t.Helper()
	res := wireEquivResult{Name: name, Modes: map[wireEquivMode]int{}}

	var zero T
	all := make([]T, 0, len(inputs)+1)
	all = append(all, zero) // ★ ゼロ値は必ず測る
	all = append(all, inputs...)
	res.Cases = len(all)

	for i, in := range all {
		oldB, err := json.Marshal(oldFn(in))
		if err != nil {
			t.Errorf("%s[case %d]: 旧 map の Marshal が失敗した: %v", name, i, err)
			continue
		}
		newB, err := json.Marshal(newFn(in))
		if err != nil {
			t.Errorf("%s[case %d]: 新 struct の Marshal が失敗した: %v", name, i, err)
			continue
		}
		if bytes.Equal(oldB, newB) {
			res.Modes[wireEquivBytes]++
			continue
		}
		oldV, err := decodeWireValue(oldB)
		if err != nil {
			t.Errorf("%s[case %d]: 旧 JSON を読み戻せない: %v", name, i, err)
			continue
		}
		newV, err := decodeWireValue(newB)
		if err != nil {
			t.Errorf("%s[case %d]: 新 JSON を読み戻せない: %v", name, i, err)
			continue
		}
		if diffs := wireValueDiff("", oldV, newV); len(diffs) > 0 {
			t.Errorf("%s[case %d]: ワイヤが変わった\n  旧: %s\n  新: %s\n  差:\n    %s",
				name, i, oldB, newB, strings.Join(diffs, "\n    "))
			continue
		}
		// キー順だけの差。struct は宣言順・map は昇順なので、ここは正常な経路。
		res.Modes[wireEquivParsed]++
	}
	return res
}

// decodeWireValue は UseNumber で読む。
// 🔴 既定の float64 で読むと **1 と 1.0 が同じになり、int64 の桁落ちも潰れる**（罠③が無言になる）。
func decodeWireValue(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// wireValueDiff は読み戻した 2 つの JSON 値の差を「どのキーがどう違うか」で返す。
// キーの有無・null と空・数値の綴りを**別々の差として**出す（潰さない）。
func wireValueDiff(path string, oldV, newV any) []string {
	at := func() string {
		if path == "" {
			return "(root)"
		}
		return path
	}
	switch o := oldV.(type) {
	case map[string]any:
		n, ok := newV.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: オブジェクトが %T になった", at(), newV)}
		}
		var keys []string
		seen := map[string]bool{}
		for k := range o {
			keys = append(keys, k)
			seen[k] = true
		}
		for k := range n {
			if !seen[k] {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		var out []string
		for _, k := range keys {
			ov, oOK := o[k]
			nv, nOK := n[k]
			p := k
			if path != "" {
				p = path + "." + k
			}
			switch {
			case oOK && !nOK:
				out = append(out, fmt.Sprintf("%s: キーが消えた（旧 %v）", p, ov))
			case !oOK && nOK:
				out = append(out, fmt.Sprintf("%s: キーが増えた（新 %v）", p, nv))
			default:
				out = append(out, wireValueDiff(p, ov, nv)...)
			}
		}
		return out
	case []any:
		n, ok := newV.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: 配列が %T になった（null と [] の取り違えを疑う）", at(), newV)}
		}
		if len(o) != len(n) {
			return []string{fmt.Sprintf("%s: 要素数 %d → %d", at(), len(o), len(n))}
		}
		var out []string
		for i := range o {
			out = append(out, wireValueDiff(fmt.Sprintf("%s[%d]", path, i), o[i], n[i])...)
		}
		return out
	default:
		if !reflect.DeepEqual(oldV, newV) {
			return []string{fmt.Sprintf("%s: %s → %s", at(), wireLit(oldV), wireLit(newV))}
		}
		return nil
	}
}

func wireLit(v any) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%v (%T)", v, v)
}

// --- ここから下はハーネス自身の自己診断 ---
//
// 🔴 README §4:「0 件」「緑」は道具を検証してから採用する。
// このハーネスは**罠を捕まえられなければ意味が無い**ので、
// **わざと壊した変換を当てて赤くなること**を 1 件ずつ確かめる。

// recordT は wireEquivT の記録実装。Errorf を数えるだけ。
type recordT struct{ errs []string }

func (r *recordT) Helper() {}
func (r *recordT) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

// equivFixture は自己診断用の入力。罠が現れる性質を持つ値だけを置く。
type equivFixture struct {
	Name string
	Note string // Name と**同じ型**。⑥ 入れ替え変異のために要る
	N    int64  // float64 で受けると桁が落ちる大きさを流す
	Tags []string
	Flag bool
}

// equivInputs — ゼロ値はハーネスが足すので、ここには「ゼロでない形」だけ置く。
func equivInputs() []equivFixture {
	return []equivFixture{
		{Name: "a", Note: "b", N: 1 << 62, Tags: []string{"x"}, Flag: true},
		{Name: "", Note: "b", N: 0, Tags: []string{}, Flag: false}, // 空スライス（nil ではない）
	}
}

// oldFaithful は「移送前の map リテラル」の役。参照実装。
func oldFaithful(f equivFixture) any {
	m := map[string]any{
		"name": f.Name,
		"note": f.Note,
		"n":    f.N,
		"tags": f.Tags,
	}
	if f.Flag { // 条件付きキー＝omitempty 相当
		m["flag"] = true
	}
	return m
}

// 忠実な変換。宣言順は map の昇順と違うので、突き合わせは parsed 経路になる。
type equivNewFaithful struct {
	Name string   `json:"name"`
	Note string   `json:"note"`
	N    int64    `json:"n"`
	Tags []string `json:"tags"`
	Flag bool     `json:"flag,omitempty"`
}

// 忠実な変換その 2。**キー昇順で宣言**したので bytes 経路で一致するはず。
type equivNewSorted struct {
	Flag bool     `json:"flag,omitempty"`
	N    int64    `json:"n"`
	Name string   `json:"name"`
	Note string   `json:"note"`
	Tags []string `json:"tags"`
}

func newFaithful(f equivFixture) any {
	return equivNewFaithful{Name: f.Name, Note: f.Note, N: f.N, Tags: f.Tags, Flag: f.Flag}
}

func newSorted(f equivFixture) any {
	return equivNewSorted{Name: f.Name, Note: f.Note, N: f.N, Tags: f.Tags, Flag: f.Flag}
}

// TestWireEquivAcceptsFaithfulConversion — **陽性側**。
// 忠実な変換は通らなければならない（通らない道具は「常に赤」なだけで何も守らない）。
// あわせて bytes / parsed の**両方の経路が実際に走る**ことを見る。
func TestWireEquivAcceptsFaithfulConversion(t *testing.T) {
	rec := &recordT{}
	got := assertWireEquiv(rec, "faithful", equivInputs(), oldFaithful, newFaithful)
	if len(rec.errs) > 0 {
		t.Fatalf("忠実な変換を赤にした（ハーネスが厳しすぎる）:\n%s", strings.Join(rec.errs, "\n"))
	}
	if got.Modes[wireEquivParsed] != got.Cases {
		t.Errorf("宣言順が map の昇順と違うので全ケース parsed のはず: %s", got)
	}

	rec2 := &recordT{}
	got2 := assertWireEquiv(rec2, "faithful-sorted", equivInputs(), oldFaithful, newSorted)
	if len(rec2.errs) > 0 {
		t.Fatalf("キー昇順に宣言した忠実な変換を赤にした:\n%s", strings.Join(rec2.errs, "\n"))
	}
	if got2.Modes[wireEquivBytes] != got2.Cases {
		t.Errorf("キー昇順の宣言なので全ケース bytes 一致のはず: %s", got2)
	}
	t.Logf("突き合わせ方式の実測: %s / %s", got, got2)
}

// TestWireEquivCatchesEachTrap — **陰性側（負の対照）**。
// 罠ごとに 1 つずつ壊した変換を当て、**その罠でだけ赤くなること**を見る。
// 🔴 1 つでも捕まえられないなら、その罠に対してこのハーネスは無力であり、
// 「ワイヤを変えていない」という主張はその分だけ嘘になる。
func TestWireEquivCatchesEachTrap(t *testing.T) {
	traps := []struct {
		trap string
		newF func(equivFixture) any
		want string // 差の説明に必ず含まれるべき語（罠を取り違えて赤くなっていないか）
	}{
		{
			// ① omitempty の付け忘れ。ゼロ値ケースで "flag": false が増える。
			trap: "① omitempty 付け忘れ",
			newF: func(f equivFixture) any {
				return struct {
					Name string   `json:"name"`
					Note string   `json:"note"`
					N    int64    `json:"n"`
					Tags []string `json:"tags"`
					Flag bool     `json:"flag"`
				}{f.Name, f.Note, f.N, f.Tags, f.Flag}
			},
			want: "flag: キーが増えた",
		},
		{
			// ① 裏返し。要らない omitempty を足すと、ゼロ値でキーが消える。
			trap: "① 余計な omitempty",
			newF: func(f equivFixture) any {
				return struct {
					Name string   `json:"name,omitempty"`
					Note string   `json:"note"`
					N    int64    `json:"n"`
					Tags []string `json:"tags"`
					Flag bool     `json:"flag,omitempty"`
				}{f.Name, f.Note, f.N, f.Tags, f.Flag}
			},
			want: "name: キーが消えた",
		},
		{
			// ② nil と空。nil スライスを [] に正規化すると null が [] になる。
			trap: "② nil を空スライスへ正規化",
			newF: func(f equivFixture) any {
				tags := f.Tags
				if tags == nil {
					tags = []string{}
				}
				return equivNewFaithful{f.Name, f.Note, f.N, tags, f.Flag}
			},
			want: "tags",
		},
		{
			// ③ 数値の型。int64 を float64 で受けると 1<<62 の桁が落ちる。
			trap: "③ int64 を float64 で受ける",
			newF: func(f equivFixture) any {
				return struct {
					Name string   `json:"name"`
					Note string   `json:"note"`
					N    float64  `json:"n"`
					Tags []string `json:"tags"`
					Flag bool     `json:"flag,omitempty"`
				}{f.Name, f.Note, float64(f.N), f.Tags, f.Flag}
			},
			want: "n:",
		},
		{
			// ④ json タグの書き忘れ。Go の公開名がそのまま出る。
			trap: "④ json タグ書き忘れ",
			newF: func(f equivFixture) any {
				return struct {
					Name string
					Note string   `json:"note"`
					N    int64    `json:"n"`
					Tags []string `json:"tags"`
					Flag bool     `json:"flag,omitempty"`
				}{f.Name, f.Note, f.N, f.Tags, f.Flag}
			},
			want: "Name: キーが増えた",
		},
		{
			// ⑥ 同じ型のフィールドを 2 つ入れ替える（README §4 の必須変異）。
			// 型が合うのでコンパイラは絶対に鳴らない。
			trap: "⑥ 同型フィールドの入れ替え",
			newF: func(f equivFixture) any {
				return equivNewFaithful{Name: f.Note, Note: f.Name, N: f.N, Tags: f.Tags, Flag: f.Flag}
			},
			want: "name:",
		},
	}

	for _, tc := range traps {
		t.Run(tc.trap, func(t *testing.T) {
			rec := &recordT{}
			assertWireEquiv(rec, tc.trap, equivInputs(), oldFaithful, tc.newF)
			if len(rec.errs) == 0 {
				t.Fatalf("罠 %q を素通しした＝この罠に対してハーネスは無力", tc.trap)
			}
			joined := strings.Join(rec.errs, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("罠 %q で赤くはなったが、理由が違う（別の罠を踏んで赤い可能性）\n"+
					"  %q を含むべき\n  実際:\n%s", tc.trap, tc.want, joined)
			}
		})
	}
}

// TestWireEquivAlwaysMeasuresZeroValue — ハーネスの中核的な保証を明示的に見る。
// 🔴 ①③⑤ の罠は**ゼロ値の入力でしか現れない**。呼ぶ側がゼロ値ケースを書き忘れても
// 検査が無言にならないことを、「入力を 1 つも渡さなくても罠を捕まえる」ことで示す。
func TestWireEquivAlwaysMeasuresZeroValue(t *testing.T) {
	noOmitEmpty := func(f equivFixture) any {
		return struct {
			Name string   `json:"name"`
			Note string   `json:"note"`
			N    int64    `json:"n"`
			Tags []string `json:"tags"`
			Flag bool     `json:"flag"`
		}{f.Name, f.Note, f.N, f.Tags, f.Flag}
	}
	rec := &recordT{}
	res := assertWireEquiv(rec, "zero-only", nil, oldFaithful, noOmitEmpty)
	if res.Cases != 1 {
		t.Fatalf("入力 nil でもゼロ値 1 ケースは測るはず: %d", res.Cases)
	}
	if len(rec.errs) == 0 {
		t.Fatal("ゼロ値ケースが足されていない＝呼ぶ側の書き忘れで検査が無言になる")
	}
}
