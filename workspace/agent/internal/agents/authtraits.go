package agents

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// AuthRecoverer is an optional Agent capability: the kind can tell from its own records that
// a session's last turn died on the login, and when its credentials were last written. It is
// what lets sessionx hold a send, report StateAuth and resume the session after a
// re-authentication without a `kind == "claude"` branch. claude keeps its own path
// (TerminalTraits.AuthExpired, claude.Abort); a kind with this capability is not asked that
// way. Resolved by exact registry key.
type AuthRecoverer interface {
	AuthTraits() AuthTraits
}

// AuthFailure is a turn that ended on the login: the error text and when the turn ended.
type AuthFailure struct {
	Msg string
	At  time.Time
}

// AuthTraits is one kind's login-failure evidence.
type AuthTraits struct {
	// LastFailure reports that the session's newest turn ended on the login and no turn has
	// started since. At must be set: the whole recovery compares it with CredentialsWrittenAt.
	LastFailure func(m session.Meta) (AuthFailure, bool)
	// CredentialsWrittenAt is when the credentials in force were last written, zero when that
	// cannot be told. It is a file's mtime, never the CLI's own "login status": that only reads
	// local files and says OK for a token revoked server side, which is the failure this exists
	// for.
	CredentialsWrittenAt func() time.Time
}

// Blocked reports that the session's last turn died on the login and nothing has rewritten the
// credentials since: re-sending reproduces the same failure. Once they are rewritten it is
// false at once, the same moment the Console's error block stops offering the fix.
func (t AuthTraits) Blocked(m session.Meta) bool {
	if t.LastFailure == nil || t.CredentialsWrittenAt == nil {
		return false
	}
	f, ok := t.LastFailure(m)
	if !ok {
		return false
	}
	return !t.CredentialsWrittenAt().After(f.At)
}
