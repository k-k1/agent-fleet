package main

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// modelListPrice is what chatx ranks a kind's live catalog by when it picks the recommended
// model for short one-shots — titles, branch names, reply chips (Issue #972): the cheapest
// model the account lists, rather than an id written into the source that goes stale the day a
// cheaper generation ships (gpt-6-luna undercut the hardcoded gpt-5.6-luna by half on input).
//
// The number is a list price in $/1M tokens for a TYPICAL short call, not a bill: most of these
// are subscription-billed, where the list price is a proxy for how fast the plan's quota goes.
// A short call is input-dominated (measured on the title prompt: ~4.3k in / 13 out, plus the
// reasoning a low effort still spends), so output is weighted at a tenth.
//
// ok=false — no catalog, no price for this id, or upstream marks it deprecated — takes the id
// out of the running; when nothing is left, the caller keeps its fixed rule.
func modelListPrice(kind, model string) (float64, bool) {
	p, _, deprecated, ok := usageCatalogLookupStatus(kind, recommendPriceID(kind, model))
	if !ok || deprecated {
		return 0, false
	}
	return p.In + p.Out/10, true
}

// agyEffortSuffixes are the reasoning-effort / thinking variants agy lists one model under
// ("gemini-3.5-flash-low", "Gemini 3.5 Flash (Medium)", "claude-sonnet-4-6-thinking"). They
// share the base model's per-token price, which is keyed without them on models.dev.
var agyEffortSuffixes = []string{"minimal", "low", "medium", "high", "thinking"}

// recommendPriceID maps a catalog id to the name models.dev prices it under. Only agy needs
// this: its catalog carries an effort variant per row, and older agy builds list display names
// instead of ids (memory: agy-models-two-column). Everyone else's ids are already the
// provider's own, and usageNormalizeModel does the rest.
func recommendPriceID(kind, model string) string {
	if kind != session.KindAgy {
		return model
	}
	s := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(s, " ("); i > 0 && strings.HasSuffix(s, ")") {
		s = s[:i] + "-" + s[i+2:len(s)-1] // "gemini 3.5 flash (medium)" → "gemini 3.5 flash-medium"
	}
	s = strings.ReplaceAll(s, " ", "-")
	for _, suf := range agyEffortSuffixes {
		if base, ok := strings.CutSuffix(s, "-"+suf); ok {
			s = base
			break
		}
	}
	// Anthropic writes its versions with hyphens ("claude-sonnet-4-6"); agy's display names use
	// a dot ("Claude Sonnet 4.6"). Gemini keeps the dot on both sides ("gemini-3.8-flash").
	if strings.HasPrefix(s, "claude-") {
		s = strings.ReplaceAll(s, ".", "-")
	}
	return s
}
