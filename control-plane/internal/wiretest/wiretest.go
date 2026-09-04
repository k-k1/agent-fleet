// Package wiretest — map → struct の変換が「ワイヤを 1 バイトも変えていない」ことを
// 示すための共有ハーネス（CONTRACT-MAP / 脚③）。
//
// 🔴 **テストからしか import されない。**製品バイナリの依存グラフには入らないことを
// wiremap_golden_test.go の TestWireMapScanExclusionsAreJustified が `go list -deps` で
// 機械的に確かめている（主張ではなく検査）。
//
// なぜ 1 つに出すか。変換の対象は internal パッケージへ広がり続けるが、wire 型は
// 非公開なので**そのパッケージの中でしか等価を測れない**。ハーネスを写すと
// モジュール内で何コピーにもなり、直したときに漂流する
// （運用キット 0.5「手書きの複製が漂流する」）。
//
// 🔴 **workspace/agent 側の同名パッケージと byte 一致で保たれている。**
// モジュール横断の共有パッケージは見送られている（ADR 0012 決定 3）ので写しは 2 つ必要だが、
// **漂流は wiretest_dup_test.go が機械で止める**（共有区間の byte 比較 ＋ ファイル名集合の一致）。
//
// 既知の 5 つの罠は全部「同じキー・同じ値に見えるのに違う」形をしている:
//
//	① omitempty の有無      — map はキーを入れなければ出ない。struct はゼロ値でも出る
//	② nil と空              — nil スライスは null、[]T{} は []
//	③ 数値の型と精度        — int64 の大きな値を float64 で受けると桁が落ちる
//	④ キーの綴り            — json タグを書き忘れると Go の公開名がそのまま出る
//	⑤ キーの有無とゼロ値    — Console が `if (x.foo)` で見ていると両者が潰れる
//
// ⑥ として、運用キット §4 が全トラックに課している「同じ型のフィールドを 2 つ
// 入れ替える変異」も同じ道具で捕まる（型が合うので型検査は絶対に鳴らない）。
//
// 使い方（変換する 1 サイトにつき 1 回）:
//
//	wiretest.AssertEquiv(t, "HandleServersGet", inputs,
//	    func(in fixture) any { /* 旧 map リテラルをそのまま写す */ },
//	    func(in fixture) any { /* 新 struct を組む */ })
//
// 🔴 **旧 map リテラルは消さずにテストへ写す。** それが唯一の参照実装であり、
// 「変えていない」の基準になるものが他に無い。
package wiretest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// ===== 共有区間ここから（control-plane と workspace/agent で byte 一致）=====
// 🔴 この行より下は 2 つのモジュールで **1 バイトも違ってはいけない**
// （wiretest_dup_test.go が検査する）。番兵より上は package 宣言と import だけで、
// モジュールパスが違うため一致しない。

// Mode は突き合わせに使った方式。**どちらを使ったかは報告に書く必要がある**ので記録する。
type Mode string

const (
	// バイト列がそのまま一致した。
	ModeBytes Mode = "bytes"
	// バイト列は違うがパース後の深い比較で一致した。
	// struct は宣言順・map はキー昇順で書かれるので、**キー順の差だけなら正常**。
	ModeParsed Mode = "parsed"
)

// T は *testing.T のうちハーネスが使う分だけ。
// 自己診断（罠を本当に捕まえるか）で record 実装を差し込むために切ってある。
type TB interface {
	Helper()
	Errorf(format string, args ...any)
}

type Result struct {
	Name  string
	Cases int
	Modes map[Mode]int
}

func (r Result) String() string {
	return fmt.Sprintf("%s: %d ケース (bytes=%d parsed=%d)",
		r.Name, r.Cases, r.Modes[ModeBytes], r.Modes[ModeParsed])
}

// AssertEquiv は同じ入力に対する旧 map と新 struct の JSON が一致することを主張する。
//
// 🔴 **ゼロ値の入力はハーネスが必ず先頭に足す。** ①③⑤ の罠はゼロ値でしか現れないので、
// 「呼ぶ側がゼロ値ケースを書き忘れる」だけで検査が無言になる。それを作法ではなく機械で塞ぐ。
func AssertEquiv[In any](t TB, name string, inputs []In, oldFn, newFn func(In) any) Result {
	t.Helper()
	res := Result{Name: name, Modes: map[Mode]int{}}

	var zero In
	all := make([]In, 0, len(inputs)+1)
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
			res.Modes[ModeBytes]++
			continue
		}
		oldV, err := decodeValue(oldB)
		if err != nil {
			t.Errorf("%s[case %d]: 旧 JSON を読み戻せない: %v", name, i, err)
			continue
		}
		newV, err := decodeValue(newB)
		if err != nil {
			t.Errorf("%s[case %d]: 新 JSON を読み戻せない: %v", name, i, err)
			continue
		}
		if diffs := valueDiff("", oldV, newV); len(diffs) > 0 {
			t.Errorf("%s[case %d]: ワイヤが変わった\n  旧: %s\n  新: %s\n  差:\n    %s",
				name, i, oldB, newB, strings.Join(diffs, "\n    "))
			continue
		}
		// キー順だけの差。struct は宣言順・map は昇順なので、ここは正常な経路。
		res.Modes[ModeParsed]++
	}
	return res
}

// decodeValue は UseNumber で読む。
// 🔴 既定の float64 で読むと **1 と 1.0 が同じになり、int64 の桁落ちも潰れる**（罠③が無言になる）。
func decodeValue(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// valueDiff は読み戻した 2 つの JSON 値の差を「どのキーがどう違うか」で返す。
// キーの有無・null と空・数値の綴りを**別々の差として**出す（潰さない）。
func valueDiff(path string, oldV, newV any) []string {
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
				out = append(out, valueDiff(p, ov, nv)...)
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
			out = append(out, valueDiff(fmt.Sprintf("%s[%d]", path, i), o[i], n[i])...)
		}
		return out
	default:
		if !reflect.DeepEqual(oldV, newV) {
			return []string{fmt.Sprintf("%s: %s → %s", at(), lit(oldV), lit(newV))}
		}
		return nil
	}
}

func lit(v any) string {
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

// Recorder は T の記録実装。Errorf を数えるだけ。
type Recorder struct{ errs []string }

func (r *Recorder) Helper() {}
func (r *Recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

// Errs は記録された Errorf の本文。パッケージ外の対照が「実際に報告されたか」を
// 見るために要る（フィールドは非公開のまま）。
func (r *Recorder) Errs() []string { return r.errs }
