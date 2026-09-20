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
	// UsageOwn runs opencode WITHOUT opencode.ai: only the providers the user connected
	// directly (anthropic/…, openrouter/…) and the fleet's own engines stay in the menu, and
	// OPENCODE_API_KEY is not injected (auth.go's env()). It is the state a workspace was
	// unable to declare before — "use opencode, but bill nothing to opencode.ai" could only
	// be approximated by choosing Go and never storing the key, which is an accident rather
	// than a declaration. Like UsageOff it is exempt from the empty-menu rescue below: an
	// empty opencode.ai side is the intended result, not a preference that failed.
	UsageOwn = "own"
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
	list, _ := catalogFor(ids, pref)
	return list
}

// CatalogWithRoute is Catalog plus the route the menu ACTUALLY shows — the selected one,
// except where the empty-menu rescue below had to fire, which reports UsageZen.
//
// It exists because that rescue used to be invisible: an account with no Go contract that
// selected Go was shown the METERED ids while the card kept saying "Go", which is the one
// thing a billing-route setting must never do quietly (two comparison sessions were launched
// on Zen ids that way — docs/log/54 §54.4). handleAgentModels returns it alongside the list so
// the Console can say what it did.
func CatalogWithRoute(ids []string, pref string) ([]agents.ModelChoice, string) {
	return catalogFor(ids, pref)
}

// catalogFor is Catalog's body, reporting the route it ended up applying so the two exported
// entry points cannot drift on when the rescue fires.
func catalogFor(ids []string, pref string) ([]agents.ModelChoice, string) {
	// A stored value the Agent never normalized (an older Console, a hand-edited ui-prefs)
	// used to land in the default branch below, i.e. "keep every billable id" — the most
	// expensive reading of a value nobody chose. CatalogPref makes unknown mean off.
	pref = CatalogPref(pref)
	// Unusable ids come out FIRST, before any of the billing-route shaping. The rescue
	// below re-enters this function with UsageZen and relies on "UsageZen keeps everything",
	// so an id dropped inside the loop for a reason UsageZen cannot undo would recurse for
	// ever. Filtering the input keeps that invariant true.
	usable := make([]string, 0, len(ids))
	for _, id := range ids {
		if keepInMenu(id) {
			usable = append(usable, id)
		}
	}
	out := make([]agents.ModelChoice, 0, len(usable))
	for _, id := range usable {
		if !keepForUsage(id, pref) {
			continue
		}
		out = append(out, agents.ModelChoice{ID: id, Label: id})
	}
	// Emptying the picker would be worse than ignoring the preference: an account
	// without the Go plan that picks Go-only must still be able to launch. Guard on the
	// INPUT being non-empty — an already-empty catalog (CLI absent / offline) is not a
	// preference problem and must not bounce back into this function. UsageOff and UsageOwn
	// are exempt: an empty opencode.ai side IS the intended result of both, and rescuing
	// "do not bill opencode.ai" into Zen would be the opposite of what was asked.
	if pref != UsageOff && pref != UsageOwn && len(out) == 0 && len(usable) > 0 {
		return Catalog(usable, UsageZen), UsageZen
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
	}), pref
}

// bedrockPrefix is opencode's own `amazon-bedrock` provider — nothing to do with af.
const bedrockPrefix = "amazon-bedrock/"

// keepInMenu drops ids that opencode offers but this workspace cannot actually use.
//
// Only one so far, and it is a big one: opencode enables its built-in `amazon-bedrock`
// provider whenever the AWS SDK credential chain RESOLVES, and every ECS task has
// AWS_CONTAINER_CREDENTIALS_RELATIVE_URI set, so the chain always resolves. Measured with the
// real CLI (1.18.29): no credentials → 7 ids, all opencode/…; an AWS profile in reach → the
// same 7 plus **121 amazon-bedrock/…**, and nothing else changes.
//
// They cannot work. The workspace task role is `WsTaskRole`, which carries no policies at all
// on purpose (20-platform: "No policies attached on purpose"), so bedrock:InvokeModel is
// AccessDenied; and af never exports AWS_PROFILE into a session or into the serve daemon — an
// AWS connection configured in Settings reaches MCP spawns and SSM sessions only. So the menu
// was offering 121 models that fail on the first message, which is the worst kind of wrong: it
// looks available.
//
// This is a STOPGAP for the menu, not a ban. It shapes the catalogue exactly like the billing
// route above does, so an explicitly named id still launches (handleCreateSession validates
// against the unshaped list) and AF_OPENCODE_SHOW_BEDROCK=1 puts them back for a deployment
// that has wired credentials up some other way. The real answer is ADR 0069's second provider
// layer, which gives Bedrock a connection and a credential of its own.
func keepInMenu(id string) bool {
	if !strings.HasPrefix(id, bedrockPrefix) {
		return true
	}
	return envOr("AF_OPENCODE_SHOW_BEDROCK", "") == "1"
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
	case UsageOwn:
		return false // opencode.ai is not used on this route — neither of its two sides
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
	case UsageOff, UsageOwn, UsageFree, UsageGo, UsageZen:
		return v
	case "hide-zen":
		return UsageGo
	case "go-first", "all":
		return UsageZen
	}
	return UsageOff
}
