package agents

import (
	"strings"
	"testing"
)

func labels(list []ModelChoice) string {
	var out []string
	for _, m := range list {
		out = append(out, m.ID)
	}
	return strings.Join(out, ",")
}

// 表示名で並べる（利用者が目にしている文字列）。大小混在でも辞書順に読める並びになり、
// 同名のときだけ id で決着する — 呼ぶたびに入れ替わらないため。
func TestSortByLabelIsCaseInsensitiveAndTotal(t *testing.T) {
	got := labels(SortByLabel([]ModelChoice{
		{ID: "b", Label: "GPT-5.6-Sol"},
		{ID: "a2", Label: "claude"},
		{ID: "a1", Label: "claude"},
		{ID: "c", Label: "Claude Opus"},
	}))
	// claude(a1) < claude(a2) < "Claude Opus"(c) < "GPT-5.6-Sol"(b) — id で表示。
	if want := "a1,a2,c,b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// グループ優先（opencode の Go 先頭）。同ランク内は入力順ではなく表示名順 — 入力順に
// 落とすと、取得元ごとに違う並びがそのまま透けてしまう。
func TestSortGroupedRanksThenLabels(t *testing.T) {
	rank := func(m ModelChoice) int {
		if strings.HasPrefix(m.ID, "go/") {
			return 0
		}
		return 1
	}
	in := []ModelChoice{
		{ID: "zen/kimi", Label: "zen/kimi"},
		{ID: "go/qwen", Label: "go/qwen"},
		{ID: "zen/aya", Label: "zen/aya"},
		{ID: "go/glm", Label: "go/glm"},
	}
	if got, want := labels(SortGrouped(in, rank)), "go/glm,go/qwen,zen/aya,zen/kimi"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// 入力順を変えても結果は同じ（並びが取得元に依存しないことの核心）。
	shuffled := []ModelChoice{in[3], in[0], in[2], in[1]}
	if got, want := labels(SortGrouped(shuffled, rank)), "go/glm,go/qwen,zen/aya,zen/kimi"; got != want {
		t.Errorf("shuffled: got %q, want %q", got, want)
	}
}
