package codex

// A turn that died on codex's login, and when the credentials were last written (agents.
// AuthRecoverer). codex's own "login status" cannot be the judge: it reads only local files, so
// it says OK for a refresh token revoked server side, which is the very failure this recovers
// from. What cannot be faked is that ~/.codex/auth.json was rewritten AFTER the turn that died.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// AuthTraits is agents.AuthRecoverer.
func (agentImpl) AuthTraits() agents.AuthTraits {
	return agents.AuthTraits{LastFailure: LastAuthFailure, CredentialsWrittenAt: AuthOKAt}
}

// AuthOKAt is when auth.json was last written, zero when it is missing. codex rewrites it on a
// sign-in and on a token refresh that succeeded, never on a refresh the server refused.
func AuthOKAt() time.Time {
	fi, err := os.Stat(filepath.Join(paths.HomeDir(), ".codex", "auth.json"))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// LastAuthFailure reports that the session's newest turn ended on the login. A Managed turn
// rejected at turn/start leaves nothing in the rollout, so the handle's own record is read
// first; otherwise the rollout's last turn-ending record answers, which covers a Terminal
// session and a Managed one after an Agent restart. A turn that started since, or ended any
// other way, is not a failure (the newest record decides, never an older one).
func LastAuthFailure(m session.Meta) (agents.AuthFailure, bool) {
	if m.DriverKind() == session.DriverManaged {
		if h := handleFor(m.Name); h != nil {
			if ce := h.turnError(); ce != nil {
				return agents.AuthFailure{Msg: ce.summary(), At: ce.at}, ce.isAuth() && !ce.at.IsZero()
			}
		}
	}
	path := rolloutPath(sids.Read(session.UUID(m.Dir, m.Name)))
	if path == "" {
		return agents.AuthFailure{}, false
	}
	return rolloutAuthFailure(rolloutTailLines(path))
}

// rolloutAuthFailure reads the newest lifecycle record of the tail: only a task_complete that
// carries an auth error counts.
func rolloutAuthFailure(lines [][]byte) (agents.AuthFailure, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		var ev struct {
			TS      string `json:"timestamp"`
			Type    string `json:"type"`
			Payload struct {
				Type  string          `json:"type"`
				Error json.RawMessage `json:"error"`
			} `json:"payload"`
		}
		if json.Unmarshal(lines[i], &ev) != nil || ev.Type != "event_msg" {
			continue
		}
		switch ev.Payload.Type {
		case "task_started", "turn_aborted":
			return agents.AuthFailure{}, false
		case "task_complete":
			ce, ok := decodeRolloutError(ev.Payload.Error)
			at, err := time.Parse(time.RFC3339Nano, ev.TS)
			if !ok || !ce.isAuth() || err != nil {
				return agents.AuthFailure{}, false
			}
			return agents.AuthFailure{Msg: ce.summary(), At: at}, true
		}
	}
	return agents.AuthFailure{}, false
}
