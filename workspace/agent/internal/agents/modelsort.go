package agents

// 起動モデル一覧の並び順ポリシー（GET /agents/{kind}/models = Console のピッカーと
// MCP list_models の合流点）。
//
// 原則は「上流の推奨順をそのまま見せる」— codex の priority 順、cursor / kiro /
// copilot / agy の列挙順は、新しい順・系列ごとにまとまった意味のある並びで、こちらで
// 名前順に潰すと「まず選ぶべきモデル」が埋もれる。上流が 1 経路しか無い kind は
// それだけで決定的なので、何もしないのが正しい。
//
// 例外は**取得経路が 2 つある kind**（現状 opencode）。同じアカウント・同じ設定でも、
// daemon 由来と CLI 由来で並びが違うため、利用者からは「時々ソート順が乱れる」に
// 見える（実測 2026-08-31）:
//
//	daemon GET /api/model → 上流カタログの生の並び（意味を持たない）
//	CLI    opencode models → id の昇順
//
// そこで opencode だけは SortByLabel で並びを正規化し、どちらの経路でも同じ順にする。
// 新しい kind を足すときも同じ判断で: 経路が 1 本なら上流順、複数あるなら正規化。

import (
	"sort"
	"strings"
)

// SortByLabel orders choices by their displayed label — what the picker shows — so
// the list reads the same regardless of which source produced it. Case-insensitive
// (labels mix case across kinds), with the id as the tiebreak so twins that share a
// label (opencode の Go/Zen 同名モデルはラベルが id なので実際には割れる) never
// swap between calls. Sorts in place and returns the same slice for chaining.
func SortByLabel(list []ModelChoice) []ModelChoice {
	sort.SliceStable(list, func(i, j int) bool {
		return lessByLabel(list[i], list[j])
	})
	return list
}

// SortGrouped orders choices by a caller-supplied group rank first (lower rank first
// — opencode uses it to keep the subscription route above the metered one), then by
// label inside each group. Same total order on every call: rank ties fall through to
// the label/id comparison rather than to the input order.
func SortGrouped(list []ModelChoice, rank func(ModelChoice) int) []ModelChoice {
	sort.SliceStable(list, func(i, j int) bool {
		if ri, rj := rank(list[i]), rank(list[j]); ri != rj {
			return ri < rj
		}
		return lessByLabel(list[i], list[j])
	})
	return list
}

func lessByLabel(a, b ModelChoice) bool {
	ka, kb := strings.ToLower(a.Label), strings.ToLower(b.Label)
	if ka != kb {
		return ka < kb
	}
	if a.Label != b.Label {
		return a.Label < b.Label
	}
	return a.ID < b.ID
}
