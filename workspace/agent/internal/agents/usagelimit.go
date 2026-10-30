package agents

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// LimitKind splits a turn that ended on a limit into the two kinds whose next move is
// opposite. Both arrive as 429 / `error:"rate_limit"`, so code alone cannot tell them apart
// (docs/log/47 §4-10).
type LimitKind string

const (
	// LimitWindow is a window of time (5-hour / weekly / per-model). Waiting clears it, so
	// it is eligible for auto-resume.
	LimitWindow LimitKind = "window"
	// LimitSpend is a spend or balance cap. Waiting never clears it — raising the cap or
	// adding credit is a billing decision — so no auto-resume is armed and it must not call
	// itself "waiting for the limit to lift".
	LimitSpend LimitKind = "spend"
)

// UsageLimiter is an optional Agent capability: the kind's usage limit can be seen for a
// session, so sessionx's rate-limit sweep may book an auto-resume at the instant it lifts. It is
// resolved by the registry key of the NORMALISED kind (an empty or legacy kind is claude); a
// kind without it is never looked at, which is what every kind but claude, codex and muse gets.
type UsageLimiter interface {
	UsageLimitTraits() UsageLimitTraits
}

// UsageLimitTraits is one kind's answers to the sweep's questions. Each field names what breaks
// without it.
type UsageLimitTraits struct {
	// ManagedOnly restricts the watch to Managed sessions: a Terminal session of the kind leaves
	// no evidence that THIS session's turn died on the limit (codex's rollout records the
	// account's percentages whether or not the session ran into the wall). Without it a Terminal
	// session would be booked on another session's limit.
	ManagedOnly bool
	// PaneMenu reports that the session's tmux pane is pinned on the kind's rate-limit menu.
	// Nil for a kind that draws none; the sweep then never spends a tmux call on it. A menu
	// means an account window, the kind of limit waiting clears.
	PaneMenu func(name string) bool
	// AtLimit reports whether the session's last turn is sitting at its usage limit now, and
	// which shape of limit it is. Without it nothing opens an episode.
	AtLimit func(m session.Meta) (LimitKind, bool)
	// ResetAt is the instant the limit lifts plus the evidence for it; ok=false means no
	// instant can be named and nothing is booked. menu says the episode came with a pane
	// menu. Without it nothing can be scheduled.
	ResetAt func(m session.Meta, menu bool, now time.Time) (time.Time, string, bool)
}
