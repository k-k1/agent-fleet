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

// Usage preferences (ui-prefs opencodeCatalog) — WHICH opencode.ai billing route this
// workspace means to use. It shapes the launch menu, and 無料枠 additionally decides
// that opencode is usable at all without any credential（auth.go の env・Status）。
// 直接つないだ他プロバイダ（anthropic/…, openrouter/… — 利用者自身の課金）はどの値でも
// 落とさない。opencode.ai の枠を選ぶ設定であって、他社の鍵を取り上げる設定ではない。
const (
	// UsageFree keeps only the zero-auth free models. 認証ゼロで動く枠（実測 8 件・
	// cost.input が 0）で、混雑（503）と無料枠上限に左右される。
	UsageFree = "free"
	// UsageGo keeps the subscription route only: opencode-go/… 。Go は API キーに
	// 紐づく（実測: アカウントログインだけでは生えない）。
	UsageGo = "go"
	// UsageZen keeps the pay-per-request route (opencode/…) and, when the account also
	// has the Go plan, its ids too — 実測どおり両方使える状態をそのまま見せる。並びは
	// Go を先にする（サブスクで賄える方を上に）。
	UsageZen = "zen"
)

const (
	goPrefix  = "opencode-go/"
	zenPrefix = "opencode/"
)

// Catalog shapes the live catalog ids into launch choices by applying the user's
// usage preference. ids is the raw catalog (Models()); pref is one of the Usage*
// constants. The label stays the id: the Console localizes the Go/Zen marker itself
// (agentModels.ts) and the MCP list_models an assistant reads wants the raw id anyway.
func Catalog(ids []string, pref string) []agents.ModelChoice {
	out := make([]agents.ModelChoice, 0, len(ids))
	for _, id := range ids {
		if !keepForUsage(id, pref) {
			continue
		}
		out = append(out, agents.ModelChoice{ID: id, Label: id})
	}
	// Emptying the picker would be worse than ignoring the preference: an account
	// without the Go plan that picks Go のみ must still be able to launch. Guard on the
	// INPUT being non-empty — an already-empty catalog (CLI absent / offline) is not a
	// preference problem and must not bounce back into this function.
	if len(out) == 0 && len(ids) > 0 {
		return Catalog(ids, UsageZen)
	}
	// Go first everywhere: whichever route is selected, a subscription-covered id is
	// the one to reach for first. STABLE so the catalog's own order reads through
	// inside each group.
	sort.SliceStable(out, func(i, j int) bool {
		return strings.HasPrefix(out[i].ID, goPrefix) && !strings.HasPrefix(out[j].ID, goPrefix)
	})
	return out
}

// keepForUsage decides whether one id belongs in the menu under pref. opencode.ai の
// 2 経路だけを判定し、他プロバイダ（利用者自身の鍵）は素通しする。
func keepForUsage(id, pref string) bool {
	isGo := strings.HasPrefix(id, goPrefix)
	isZen := strings.HasPrefix(id, zenPrefix) && !isGo
	if !isGo && !isZen {
		return true // anthropic/…, openrouter/… — 別課金なので枠の選択とは無関係
	}
	switch pref {
	case UsageFree:
		return isFreeModel(id)
	case UsageGo:
		return isGo
	default: // UsageZen: 課金経路を絞らない（Go を併用していれば両方出る）
		return true
	}
}

// CatalogPref normalizes a stored preference value, including the values this setting
// used to hold（"hide-zen" は Go だけを見たいという意思なので UsageGo、"go-first"/"all"
// は両方見たいので UsageZen）。未設定/不明は UsageZen ＝ 従来の見え方。
func CatalogPref(v string) string {
	switch v {
	case UsageFree, UsageGo, UsageZen:
		return v
	case "hide-zen":
		return UsageGo
	}
	return UsageZen
}
