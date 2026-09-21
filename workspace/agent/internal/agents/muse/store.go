package muse

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// museSession is what AF remembers about a slot's conversation on the muse side.
//
// Two fields, and neither can be derived. The id cannot, because MSP refuses a retained or
// reserved one (`session_id_conflict`) — so AF's usual deterministic UUIDv5 of (dir, name)
// would work exactly once and fail on every resume. The path cannot, because the store is
// partitioned by date (`sessions/YYYY/MM/DD/<sid>/session.jsonl`): computing it means guessing
// which day the session was created, which is the trap kiro hit with cwd+mtime (ADR 0026
// decision 6). `session/start` returns the path, so it is recorded instead (decision 4).
type museSession struct {
	// ID is the UUIDv7 AF minted and the host took verbatim.
	ID string `json:"id"`
	// Path is the session.jsonl `session/start` reported. Subagent records are interleaved
	// into this same file under their own stream id; there is no per-child directory.
	Path string `json:"path"`
}

// sessions maps a slot sid to its muse session. Keyed by the slot sid rather than the session
// name so it shares the recreate/fork semantics every other kind's SidStore already has.
var sessions = fstore.JSON[museSession](paths.AgentStateDir, "muse-sessions", ".json")

// slotSid is the slot's identity: the working copy and the session name, never the subdir —
// the same rule kiro's slotSid follows, so a session launched into a subfolder is still the
// same slot.
func slotSid(m session.Meta) string { return session.UUID(m.Dir, m.Name) }

func readSession(sid string) (museSession, bool) { return sessions.Read(sid) }

func writeSession(sid string, s museSession) { _ = sessions.Write(sid, s) }
