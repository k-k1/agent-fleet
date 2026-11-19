package status

// The latest turn start of every session (ADR 0137, addendum 2026-11-19, part B). A queue run's
// session may hand its message back (defer_queue_item); the Control Plane drops that request when
// a turn of the run's session tree started after the call, because a run that went on to work
// must not be handed back and done twice. The status record cannot answer: a stop deletes it, and
// a turn that started after the call and ended before a stop would then read as none. So the
// start is kept in a file of its own, written under the per-sid lock by the same rule the
// first-turn ledger counts starts by, and removed only when its session is gone.

import (
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// TurnStart is when a session's latest turn started, by this host's clock.
type TurnStart struct {
	At string `json:"at"`
}

var turnStarts = fstore.JSON[TurnStart](paths.AgentStateDir, "session-turn-start", ".json")

// noteTurnStart is called by persistLocked, with the per-sid lock held and before the new record
// replaces the old one. A working write is a start when a driver marked it as one, when no record
// was there (a stop deleted it), or when the record was a plain idle: a heartbeat and the answer
// to a question or a permission are the same turn.
func noteTurnStart(sid string, s SessionStatus) {
	if s.State != "working" {
		return
	}
	if !s.turnStart {
		if prev, had := Read(sid); had && !(prev.State == "idle" && !prev.InputNotDelivered) {
			return
		}
	}
	_ = turnStarts.Write(sid, TurnStart{At: time.Now().Format(time.RFC3339Nano)})
}

// LastTurnStart is when sid's latest turn started (RFC 3339), "" when none was seen.
func LastTurnStart(sid string) string {
	t, ok := turnStarts.Read(sid)
	if !ok {
		return ""
	}
	return t.At
}

// SweepOrphanTurnStarts removes the turn starts no session claims any more: a sid not in live (the
// sids of every session meta on disk) whose file is older than minAge, the bound that keeps a
// create in flight safe (see SweepOrphanFirstTurns). It returns how many it removed.
func SweepOrphanTurnStarts(live map[string]bool, minAge time.Duration, now time.Time) int {
	entries, err := os.ReadDir(turnStarts.Dir())
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sid := strings.TrimSuffix(e.Name(), ".json")
		if live[sid] {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= minAge {
			continue
		}
		turnStarts.Remove(sid)
		n++
	}
	return n
}
