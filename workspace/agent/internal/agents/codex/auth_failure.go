package codex

// A turn that died on codex's login, and when the credentials were last written (agents.
// AuthRecoverer). codex's own "login status" cannot be the judge: it reads only local files, so
// it says OK for a refresh token revoked server side, which is the very failure this recovers
// from. What cannot be faked is that ~/.codex/auth.json was rewritten AFTER the turn that died.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
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
				if !ce.isAuth() || ce.at.IsZero() {
					return agents.AuthFailure{}, false
				}
				at := ce.at
				if !ce.started.IsZero() {
					at = ce.started
				}
				return agents.AuthFailure{Msg: ce.summary(), At: at, NoTurn: ce.turnID == ""}, true
			}
		}
	}
	path := rolloutPath(sids.Read(session.UUID(m.Dir, m.Name)))
	if path == "" {
		return agents.AuthFailure{}, false
	}
	return rolloutAuthFailureMemo(path)
}

// authMemo remembers the last answer per rollout, keyed by (size, mtime): the callers poll
// (list, chip, send guard, the 30s sweep) and the rollout lives on EFS, so an unchanged file
// must cost one stat, not a 256 KB read.
var authMemo sync.Map // path -> authMemoEntry

type authMemoEntry struct {
	size int64
	mod  time.Time
	f    agents.AuthFailure
	ok   bool
}

func rolloutAuthFailureMemo(path string) (agents.AuthFailure, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return agents.AuthFailure{}, false
	}
	if v, hit := authMemo.Load(path); hit {
		e := v.(authMemoEntry)
		if e.size == fi.Size() && e.mod.Equal(fi.ModTime()) {
			return e.f, e.ok
		}
	}
	f, ok := rolloutAuthFailure(rolloutTailLines(path))
	authMemo.Store(path, authMemoEntry{size: fi.Size(), mod: fi.ModTime(), f: f, ok: ok})
	return f, ok
}

// rolloutAuthFailure reads the newest lifecycle record of the tail: only a task_complete that
// carries an auth error counts. Its At is the matching task_started's time when the tail holds
// it (see agents.AuthFailure.At), else the failure's own.
func rolloutAuthFailure(lines [][]byte) (agents.AuthFailure, bool) {
	type rec struct {
		TS      string `json:"timestamp"`
		Type    string `json:"type"`
		Payload struct {
			Type   string          `json:"type"`
			TurnID string          `json:"turn_id"`
			Error  json.RawMessage `json:"error"`
		} `json:"payload"`
	}
	var fail agents.AuthFailure
	var failTurn string
	found := false
	for i := len(lines) - 1; i >= 0; i-- {
		var ev rec
		if json.Unmarshal(lines[i], &ev) != nil || ev.Type != "event_msg" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, ev.TS)
		switch ev.Payload.Type {
		case "task_started":
			if !found {
				return agents.AuthFailure{}, false
			}
			if err == nil && ev.Payload.TurnID == failTurn && at.Before(fail.At) {
				fail.At = at
			}
			return fail, true
		case "turn_aborted":
			if !found {
				return agents.AuthFailure{}, false
			}
		case "task_complete":
			if found {
				return fail, true // an older turn's end: the failed turn's start is not in the tail
			}
			ce, ok := decodeRolloutError(ev.Payload.Error)
			if !ok || !ce.isAuth() || err != nil {
				return agents.AuthFailure{}, false
			}
			fail, failTurn, found = agents.AuthFailure{Msg: ce.summary(), At: at}, ev.Payload.TurnID, true
		}
	}
	return fail, found
}

// usageLimitMemo is authMemo's twin for the usage-limit answer: the same (size, mtime) key, so
// the per-poll caller costs one stat while the rollout is unchanged.
var usageLimitMemo sync.Map // path -> usageLimitMemoEntry

type usageLimitMemoEntry struct {
	size  int64
	mod   time.Time
	limit bool
}

// rolloutUsageLimited reports whether the rollout's newest turn-ending record is a task_complete
// that failed on the usage limit. It is the evidence a session keeps after the managed handle is
// gone (a stop, an Agent restart): the handle's in-memory turn error is the primary source, this
// is the fallback. A later task_started / turn_aborted, or a task_complete without the error,
// means the limit is no longer the story. The rollout spells the label snake_case, the
// app-server camelCase; both count.
func rolloutUsageLimited(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	if v, hit := usageLimitMemo.Load(path); hit {
		e := v.(usageLimitMemoEntry)
		if e.size == fi.Size() && e.mod.Equal(fi.ModTime()) {
			return e.limit
		}
	}
	limit := rolloutTailUsageLimited(rolloutTailLines(path))
	usageLimitMemo.Store(path, usageLimitMemoEntry{size: fi.Size(), mod: fi.ModTime(), limit: limit})
	return limit
}

func rolloutTailUsageLimited(lines [][]byte) bool {
	for i := len(lines) - 1; i >= 0; i-- {
		var ev struct {
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
			return false
		case "task_complete":
			ce, ok := decodeRolloutError(ev.Payload.Error)
			return ok && (ce.label == "usage_limit_exceeded" || ce.label == "usageLimitExceeded")
		}
	}
	return false
}

// RanTurnSince reports that the session's newest turn-ending rollout record is a task_complete
// WITHOUT an error, written after at: the session moved on by hand. A newer task_started (a turn
// still running) or any failure answers false; the next sweep sees the completion. A tail read
// per call, made only for sessions with an open episode.
func RanTurnSince(m session.Meta, at time.Time) bool {
	path := rolloutPath(sids.Read(session.UUID(m.Dir, m.Name)))
	return path != "" && rolloutCleanTurnSince(rolloutTailLines(path), at)
}

func rolloutCleanTurnSince(lines [][]byte, at time.Time) bool {
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
			return false
		case "task_complete":
			if _, failed := decodeRolloutError(ev.Payload.Error); failed {
				return false
			}
			ts, err := time.Parse(time.RFC3339Nano, ev.TS)
			return err == nil && ts.After(at)
		}
	}
	return false
}
