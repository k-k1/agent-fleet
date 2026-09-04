package usagex

// Lower layer of usage accounting (ADR 0067 wave B). Only the types that depend on nothing
// in main, plus the context plumbing that carries a call's tag, belong here.

import (
	"context"
	"regexp"
	"strings"
)

// Tag says what a call was FOR. The provider layer never interprets it and copies it into
// the ledger as-is, so adding a feature needs no provider change.
type Tag struct {
	Feature string // usageFeature*
	Trigger string // usageTrigger*
	Ref     string // session name, or assistant conversation id
	Verb    string // sub-dimension of assistant.chat (translate|summarize)
}

type tagKeyT struct{}

var tagKey tagKeyT

// WithTag is the single line every call site has to write.
func WithTag(ctx context.Context, t Tag) context.Context {
	return context.WithValue(ctx, tagKey, t)
}

// TagOf pulls the tag out. ok=false means there is no tag (or Feature is empty); the caller
// decides the default, and TagOrUnknown fills in feature=unknown — consumption disappearing
// because someone forgot a tag is worse than unknown rows mixed in.
func TagOf(ctx context.Context) (Tag, bool) {
	if ctx != nil {
		if t, ok := ctx.Value(tagKey).(Tag); ok && t.Feature != "" {
			return t, true
		}
	}
	return Tag{}, false
}

// ContextUsage is the CURRENT context fill: the last main-chain assistant event's
// input snapshot (cache read / cache creation / fresh input), like the Console's
// ContextBar. Absent until the session's first assistant reply; after claude's
// auto-compaction it reflects the post-compaction (smaller) context.
type ContextUsage struct {
	Tokens int `json:"tokens"` // read + create + fresh
	Read   int `json:"read"`
	Create int `json:"create"`
	Fresh  int `json:"fresh"`
	Window int `json:"window,omitempty"` // context-window size the pct is against
	// windowSource: "recorded" = the agent reported its real window (codex
	// model_context_window); "estimated" = guessed from the model name.
	WindowSource string  `json:"windowSource,omitempty"`
	Pct          float64 `json:"pct,omitempty"` // 0–100, tokens/window
	Model        string  `json:"model,omitempty"`
}

// smallWindowClaudeRe enumerates only the 200k-side Claude models. Claude is natively 1M
// from Opus 4.6 / Sonnet 4.6 on and every future model is assumed to be too, so listing the
// large side (and appending to it per new model, where a miss means the model is mistaken
// for 200k) is inverted here: 1M is the default and only the small ones are exceptions.
//   - the haiku line (200k up to 4.5)
//   - Claude 3.x and earlier (claude-2 / claude-3-*)
//   - Opus 4.0/4.1/4.5 and Sonnet 4.0/4.5. Dated ids have the form opus-4-20250514, so
//     "4-2" counts as an old generation too (it cannot collide with the 1M-side 4-6/4-7/4-8).
var smallWindowClaudeRe = regexp.MustCompile(`haiku|claude-[123]|opus-4-[0125]|sonnet-4-[025]`)

// WindowGuess mirrors the Console's contextWindow() (ContextBar.tsx — keep
// the two in sync). Order: 272k for GPT-5.x (codex normally records its real
// window, so this is the fallback — e.g. the assistant chat's `codex exec`, whose
// events don't carry it) → 200k for the legacy Claude generations above → 1M for
// every other Claude → for non-Claude unknowns, 200k with a grow-to-fit fallback
// when the observed usage already exceeds it.
func WindowGuess(model string, used int) int {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "gpt-5"):
		return 272_000
	case smallWindowClaudeRe.MatchString(m):
		return 200_000
	case strings.Contains(m, "claude"):
		return 1_000_000
	}
	if used > 200_000 {
		return 1_000_000
	}
	return 200_000
}
