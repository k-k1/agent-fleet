package agents

// Ordering policy for the launch model list (GET /agents/{kind}/models, where the Console
// picker and MCP list_models meet).
//
// The rule is to show upstream's recommended order as-is: codex's priority order and the
// enumeration order of cursor / kiro / copilot / agy are meaningful (newest first, grouped by
// family), and flattening them by name buries the model you should pick first. A kind with only
// one source is already deterministic, so doing nothing is correct.
//
// The exception is a kind with two sources (today, opencode). With the same account and the same
// settings the daemon and the CLI return different orders, which looks to the user like the sort
// order occasionally scrambling (measured 2026-08-31):
//
//	daemon GET /api/model -> the raw upstream catalogue order (meaningless)
//	CLI    opencode models -> ascending by id
//
// So opencode alone normalises with SortByLabel, making both routes agree. Judge a new kind the
// same way: one source, keep upstream order; several, normalise.

import (
	"sort"
	"strings"
)

// SortByLabel orders choices by their displayed label — what the picker shows — so
// the list reads the same regardless of which source produced it. Case-insensitive
// (labels mix case across kinds), with the id as the tiebreak so twins that share a
// label (opencode's identically named Go/Zen models do split, since their label is the id) never
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
