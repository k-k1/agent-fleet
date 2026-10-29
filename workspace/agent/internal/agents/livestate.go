package agents

import "github.com/k-k1/agent-fleet/workspace/agent/internal/session"

// LiveStater is an optional Agent capability: the live-state verdicts the kind's own store or
// runtime gives beyond the status store its hooks write. sessionx's DriveState (the mirror and
// chat chip) and the sessions list's turn-end record read it. A nil func or a false field means
// the kind has no such verdict, which is what every kind without the capability gets. It is
// resolved by exact registry key: no kind with it is reached through the claude fallback.
type LiveStater interface {
	LiveStateTraits() LiveStateTraits
}

// LiveStateTraits is one kind's live-state verdicts. Each field names what breaks without it.
type LiveStateTraits struct {
	// State is the kind's own verdict for a live session, "" for no opinion; a non-empty one is
	// the state, ahead of the status store. Without it a kind whose hooks are missing or
	// unreliable keeps the optimistic "working" /input stored, or never shows its question.
	State func(m session.Meta) string
	// PolledTurnEnd means the kind has no Stop hook, so reading State is the only place a turn's
	// end can be observed: a working→idle transition read there fires the turn-end notification
	// and is recorded by the sessions list. Without it the completion report never arrives.
	PolledTurnEnd bool
	// StaleWorkingIdle means that while State has no opinion, a stored "working" past the
	// warm-up reads idle, since nothing of the kind's ever clears it. Nothing is persisted, so a
	// turn that really ends later still reports.
	StaleWorkingIdle bool
	// RateLimited reports that an idle session's last turn hit a usage limit, which waiting
	// lifts: the session reads StateLimited instead of idle, because re-sending hits the same
	// limit. RateLimitedManagedOnly restricts it to Managed sessions.
	RateLimited            func(name string) bool
	RateLimitedManagedOnly bool
}
