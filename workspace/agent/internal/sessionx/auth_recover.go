package sessionx

// The kind-neutral side of the login recovery: which kinds judge "the last turn died on the
// login" from their own records (agents.AuthRecoverer) instead of claude's credentials expiry.
// claude does not implement it and keeps its own path, so nothing here changes what it does.

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// authRecovererOf is the kind's capability by exact registry key; ok=false for claude and for
// every kind without it.
func authRecovererOf(kind string) (agents.AuthTraits, bool) {
	a, _ := RegisteredAgent(kind)
	r, ok := a.(agents.AuthRecoverer)
	if !ok {
		return agents.AuthTraits{}, false
	}
	return r.AuthTraits(), true
}

// authBlockedFor reports that a session of a recoverer kind sits on a failed login. A session
// of any other kind (claude included) is never blocked here.
func authBlockedFor(m session.Meta) bool {
	t, ok := authRecovererOf(m.Kind)
	return ok && t.Blocked(m)
}

// authExpiredFor is the full "refuse on the login" verdict for a session: claude's credentials
// expiry, or a recoverer kind's failed turn.
func authExpiredFor(m session.Meta) bool {
	return callTrue0(normalizedTerminalTraits(m.Kind).AuthExpired) || authBlockedFor(m)
}
