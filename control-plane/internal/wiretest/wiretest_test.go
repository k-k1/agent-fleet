// wiretest_test.go — ハーネス自身の自己診断。
//
// 🔴 運用キット §4:「0 件」「緑」は道具を検証してから採用する。
// このハーネスは**罠を捕まえられなければ意味が無い**ので、
// **わざと壊した変換を当てて赤くなること**を 1 件ずつ確かめる。
// 加えて**忠実な変換は通ること**も見る（全部赤くする道具は何も守らない）。
package wiretest

import (
	"strings"
	"testing"
)

// ===== 共有区間ここから（control-plane と workspace/agent で byte 一致）=====
// 🔴 この行より下は 2 つのモジュールで **1 バイトも違ってはいけない**
// （wiretest_dup_test.go が検査する）。番兵より上は package 宣言と import だけで、
// モジュールパスが違うため一致しない。

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
	rec := &Recorder{}
	got := AssertEquiv(rec, "faithful", equivInputs(), oldFaithful, newFaithful)
	if len(rec.errs) > 0 {
		t.Fatalf("忠実な変換を赤にした（ハーネスが厳しすぎる）:\n%s", strings.Join(rec.errs, "\n"))
	}
	if got.Modes[ModeParsed] != got.Cases {
		t.Errorf("宣言順が map の昇順と違うので全ケース parsed のはず: %s", got)
	}

	rec2 := &Recorder{}
	got2 := AssertEquiv(rec2, "faithful-sorted", equivInputs(), oldFaithful, newSorted)
	if len(rec2.errs) > 0 {
		t.Fatalf("キー昇順に宣言した忠実な変換を赤にした:\n%s", strings.Join(rec2.errs, "\n"))
	}
	if got2.Modes[ModeBytes] != got2.Cases {
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
			// ①⑤ 裏返し。要らない omitempty を足すと、ゼロ値でキーが消える。
			// これは⑤（キーの有無とゼロ値の区別）の対照でもある——Console が
			// `if (x.foo)` で見ていると「キーが無い」と `""` は潰れて同じに見えるが、
			// ワイヤは違う。ハーネスは両者を別の差として出す。
			trap: "①⑤ 余計な omitempty（キーの有無とゼロ値）",
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
			rec := &Recorder{}
			AssertEquiv(rec, tc.trap, equivInputs(), oldFaithful, tc.newF)
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
	rec := &Recorder{}
	res := AssertEquiv(rec, "zero-only", nil, oldFaithful, noOmitEmpty)
	if res.Cases != 1 {
		t.Fatalf("入力 nil でもゼロ値 1 ケースは測るはず: %d", res.Cases)
	}
	if len(rec.errs) == 0 {
		t.Fatal("ゼロ値ケースが足されていない＝呼ぶ側の書き忘れで検査が無言になる")
	}
}
