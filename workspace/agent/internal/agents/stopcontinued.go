package agents

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// StopContinuer is an optional Agent capability: whether the Stop behind an idle marker was
// blocked by another Stop hook, so the turn is still running (#1600). Only a kind whose idle
// comes from our Stop hook running beside the user's can be fooled that way, so a kind without
// it answers false. Resolved by exact registry key, never through the claude fallback.
type StopContinuer interface {
	// StopContinued gets the session's meta, its provider session id (sid, "" when unknown) and
	// the time of the idle marker. It may read a transcript, so callers run it only on idle.
	StopContinued(m session.Meta, sid string, marker time.Time) bool
}
