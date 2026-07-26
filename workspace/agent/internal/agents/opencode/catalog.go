package opencode

// Launch-model catalog shaping for the two opencode.ai billing routes.
//
// One key (OPENCODE_API_KEY) unlocks TWO provider ids, and `opencode models` lists
// both side by side（実測 2026-07-26）:
//
//	opencode/…      opencode Zen — pay-per-request from a prepaid balance（59 件）
//	opencode-go/…   OpenCode Go — the subscription plan（16 件）
//
// 10 of the 16 Go models exist under BOTH prefixes with the SAME suffix
// (deepseek-v4-pro, glm-5.2, kimi-k2.7-code, …). The ids were the only label, so the
// twins were indistinguishable in the launch picker and in the MCP list_models an
// assistant picks from — choosing the Zen twin spends balance (and fails outright with
// 401 Insufficient balance when there is none) while the Go twin is covered by the
// subscription. That is exactly how two comparison sessions were launched on Zen ids
// and produced nothing.
//
// The preference only reorders/filters the MENU — an explicitly requested model id is
// never rewritten (handleCreateSession keeps validating against the unshaped list),
// because silently moving a turn to a different billing route is worse than showing the
// wrong one. The id itself is self-describing (the prefix names the route); the Console
// decorates it further, in the user's language, from the same prefix.

import (
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// Catalog preferences (ui-prefs opencodeCatalog). Absent/unknown ⇒ CatalogGoFirst.
const (
	// CatalogGoFirst lists the Go models first, then everything else. A no-op for an
	// account without the Go plan (no opencode-go/… ids in its catalog), so it is safe
	// as the default.
	CatalogGoFirst = "go-first"
	// CatalogHideZen drops the pay-per-request opencode/… ids. Go plus any DIRECT
	// provider the user connected (anthropic/…, openrouter/…) stay — the point is to
	// hide the metered twins, not to cut the user off from their own keys.
	CatalogHideZen = "hide-zen"
	// CatalogAll keeps the catalog exactly as opencode reports it.
	CatalogAll = "all"
)

const (
	goPrefix  = "opencode-go/"
	zenPrefix = "opencode/"
)

// Catalog shapes the live `opencode models` ids into launch choices by applying the
// user's preference. ids is the raw catalog (Models()); pref is one of the Catalog*
// constants. The label stays the id: the Console localizes the Go/Zen marker itself
// (agentModels.ts) and the MCP list_models an assistant reads wants the raw id anyway.
func Catalog(ids []string, pref string) []agents.ModelChoice {
	out := make([]agents.ModelChoice, 0, len(ids))
	for _, id := range ids {
		if pref == CatalogHideZen && strings.HasPrefix(id, zenPrefix) {
			continue
		}
		out = append(out, agents.ModelChoice{ID: id, Label: id})
	}
	// Emptying the picker would be worse than ignoring the preference: an account
	// without the Go plan that flips 隠す must still be able to launch. Guard on the
	// INPUT being non-empty — an already-empty catalog (CLI absent / offline) is not a
	// preference problem and must not bounce back into this function.
	if len(out) == 0 && len(ids) > 0 {
		return Catalog(ids, CatalogAll)
	}
	// Both preferences hoist Go (hiding the metered twins implies preferring the
	// covered ones); only CatalogAll leaves the catalog exactly as reported. The sort
	// is STABLE so the catalog's own order still reads through inside each group.
	if pref != CatalogAll {
		sort.SliceStable(out, func(i, j int) bool {
			return strings.HasPrefix(out[i].ID, goPrefix) && !strings.HasPrefix(out[j].ID, goPrefix)
		})
	}
	return out
}

// CatalogPref normalizes a stored preference value; anything unknown or absent falls
// back to CatalogGoFirst (a no-op without the Go plan).
func CatalogPref(v string) string {
	switch v {
	case CatalogHideZen, CatalogAll:
		return v
	}
	return CatalogGoFirst
}
