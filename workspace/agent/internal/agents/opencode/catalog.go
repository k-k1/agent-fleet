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
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// Usage preferences (ui-prefs opencodeCatalog) — WHICH opencode.ai billing route this
// workspace means to use. It shapes the launch menu, and 無料枠 additionally decides
// that opencode is usable at all without any credential（auth.go の env・Status）。
// 直接つないだ他プロバイダ（anthropic/…, openrouter/… — 利用者自身の課金）はどの値でも
// 落とさない。opencode.ai の枠を選ぶ設定であって、他社の鍵を取り上げる設定ではない。
const (
	// UsageOff hard-disables opencode regardless of anything else configured: stored
	// provider keys, an account OAuth login — none of it is honored while this is
	// selected (auth.go の connected()/env())。「鍵を消し忘れているだけで無料枠/他社課金
	// に乗ってしまう」を admin が明示的に断てるようにする選択肢。UsageFree（無料枠を
	// 使いたい）とは別物 — off は「一切使わない」の宣言で、free の対極にある。
	UsageOff = "off"
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
// usage preference, in a normalized order (Go route first, id ascending inside each
// group — see the sort below). ids is the raw catalog (Models()); pref is one of the
// Usage* constants. The label stays the id: the Console localizes the Go/Zen marker itself
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
	// preference problem and must not bounce back into this function. UsageOff is
	// exempt from this rescue: an empty picker IS the intended result of "off".
	if pref != UsageOff && len(out) == 0 && len(ids) > 0 {
		return Catalog(ids, UsageZen)
	}
	// Go first everywhere: whichever route is selected, a subscription-covered id is
	// the one to reach for first. Inside a group the order is normalized by id
	// (=label), NOT inherited from the catalog: opencode is the one kind read through
	// two sources, and they disagree — the daemon's /api/model hands back the upstream
	// catalog's own (meaningless) order while `opencode models` prints it sorted. The
	// picker's order therefore flipped depending on whether a serve happened to be
	// running when the modal was opened（実測 2026-08-31・見た目は「時々並びが乱れる」）。
	// docs/log/54 の取得元切り替えは維持したまま、見え方だけを揃える。
	return agents.SortGrouped(out, func(m agents.ModelChoice) int {
		if strings.HasPrefix(m.ID, goPrefix) {
			return 0
		}
		return 1
	})
}

// keepForUsage decides whether one id belongs in the menu under pref. opencode.ai の
// 2 経路だけを判定し、他プロバイダ（利用者自身の鍵）は素通しする。
func keepForUsage(id, pref string) bool {
	if pref == UsageOff {
		return false // 一切使わない宣言 — 他社プロバイダの id も含め、何も出さない。
	}
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
// は両方見たいので UsageZen）。未設定/不明は UsageOff ＝ 明示的に選ぶまで無効。
func CatalogPref(v string) string {
	switch v {
	case UsageOff, UsageFree, UsageGo, UsageZen:
		return v
	case "hide-zen":
		return UsageGo
	case "go-first", "all":
		return UsageZen
	}
	return UsageOff
}
