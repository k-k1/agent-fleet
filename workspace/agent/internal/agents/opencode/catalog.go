package opencode

// Launch-model catalog shaping for the two opencode.ai billing routes.
//
// One key (OPENCODE_API_KEY) unlocks TWO provider ids, and `opencode models` lists
// both side by side (measured 2026-07-26):
//
//	opencode/…      opencode Zen — pay-per-request from a prepaid balance (59 of them)
//	opencode-go/…   OpenCode Go — the subscription plan (16 of them)
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
// workspace means to use. It shapes the launch menu, and the free route additionally decides
// that opencode is usable at all without any credential (auth.go's env / Status). Other
// providers connected directly (anthropic/…, openrouter/… — billed to the user themselves) are
// never dropped for any value: this setting picks an opencode.ai route, it does not take away
// another vendor's key.
const (
	// UsageOff hard-disables opencode regardless of anything else configured: stored
	// provider keys, an account OAuth login — none of it is honored while this is
	// selected (auth.go's connected()/env()). It is the choice that lets an admin explicitly
	// stop "a key nobody remembered to delete puts us on the free route or another vendor's
	// bill". Not the same thing as UsageFree (wanting the free route) — off declares "do not use
	// it at all" and is free's opposite.
	UsageOff = "off"
	// UsageFree keeps only the zero-auth free models — the route that runs with no credential at
	// all (measured: 8 of them, cost.input 0), at the mercy of congestion (503) and the free
	// route's own limit.
	UsageFree = "free"
	// UsageGo keeps the subscription route only: opencode-go/… . Go is tied to the API key
	// (measured: an account login alone does not produce it).
	UsageGo = "go"
	// UsageZen keeps the pay-per-request route (opencode/…) and, when the account also
	// has the Go plan, its ids too — showing exactly the measured state where both work. Go
	// comes first in the order (what the subscription covers goes on top).
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
	// without the Go plan that picks Go-only must still be able to launch. Guard on the
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
	// running when the modal was opened (measured 2026-08-31; what it looks like is "the order
	// is sometimes scrambled"). docs/log/54's source switching stays as it is — only the
	// appearance is made consistent.
	return agents.SortGrouped(out, func(m agents.ModelChoice) int {
		if strings.HasPrefix(m.ID, goPrefix) {
			return 0
		}
		return 1
	})
}

// keepForUsage decides whether one id belongs in the menu under pref. Only opencode.ai's two
// routes are judged; other providers (the user's own key) pass straight through.
func keepForUsage(id, pref string) bool {
	if pref == UsageOff {
		return false // the "use nothing at all" declaration — no ids, other vendors' included
	}
	isGo := strings.HasPrefix(id, goPrefix)
	isZen := strings.HasPrefix(id, zenPrefix) && !isGo
	if !isGo && !isZen {
		return true // anthropic/…, openrouter/… — a separate bill, so the route choice is moot
	}
	switch pref {
	case UsageFree:
		return isFreeModel(id)
	case UsageGo:
		return isGo
	default: // UsageZen: no billing route is excluded (with Go alongside, both appear)
		return true
	}
}

// CatalogPref normalizes a stored preference value, including the values this setting
// used to hold ("hide-zen" means wanting to see Go only, so UsageGo; "go-first"/"all" mean
// wanting both, so UsageZen). Unset or unknown is UsageOff = disabled until explicitly chosen.
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
