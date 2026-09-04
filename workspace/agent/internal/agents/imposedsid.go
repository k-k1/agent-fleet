package agents

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Recovery for slots of the "imposed id" shape.
//
// Unlike the kinds where the CLI allocates its own conversation id and we capture it
// (codex via a hook, opencode via a plugin, agy/kiro by disk discovery), claude, copilot
// and cursor get an id WE allocated and we go on trusting it (`--session-id` /
// `--resume <uuid>`). That shape breaks silently the moment the CLI stops using the id:
//
//	measured on claude 2.1.239 — switching to the full-screen TUI makes claude relaunch
//	itself, and the relaunch argv is rebuilt from the config flags alone, so
//	--session-id structurally cannot be in it. Having lost the id, claude starts a
//	blank conversation under a random new one. We kept looking for the transcript of
//	the deterministic sid and the mirror sat at "no conversation yet" (6 slots out of
//	386). For claude the hook announces session_id, so that side could be pulled back
//	using AF_SESSION_NAME (internal/agents/claude/sid.go).
//
// copilot and cursor have no status hook (their state comes from events.jsonl and from
// the tail of the transcript), so there is no channel to hear the id announced on. Disk
// is the only clue left, which is what this does.
//
// The point of the recovery is that it only acts when the imposed id does NOT EXIST at
// all on the CLI side. That line keeps it from ever hijacking a healthy slot's
// conversation, and it matches exactly the breakage that was observed: the CLI never
// wrote anything under the id we imposed.

// CLISession is one conversation the CLI itself keeps on disk, as seen by a kind's
// enumerator. Created may be zero when the CLI records no creation time (cursor — the
// transcript directory's mtime stands in; measured: appending to a file does not move it,
// so it stays at creation time).
type CLISession struct {
	ID      string
	Created time.Time
}

// ResolveImposedSID returns the conversation id the slot should use, adopting a
// replacement when the id we imposed was never taken up by the CLI.
//
// sessions enumerates the CLI's own conversations for dir. Returns "" for a slot that
// has never launched (no id allocated yet) — a fresh slot must never adopt a stranger's
// conversation, so discovery is deliberately not attempted there.
//
// A replacement is adopted only when EXACTLY ONE candidate is in this dir, dates from at
// or after the slot's creation time, and is not claimed by another slot. Ambiguity means
// no move: adopting the wrong one shows someone else's conversation in the mirror, which
// is worse than staying stuck. The known edge is two live slots on the same dir; giving
// each a separate dir via a worktree is the fleet's isolation mechanism (the same call as
// kiro's discoverSid).
func ResolveImposedSID(store SidStore, m session.Meta, sessions func(dir string) []CLISession) string {
	slot := session.UUID(m.Dir, m.Name)
	cached := store.Read(slot)
	if cached == "" {
		return "" // slot has never launched; do not go looking
	}
	all := sessions(m.Dir)
	for _, s := range all {
		if s.ID == cached {
			return cached // the CLI writes under the id we gave it: healthy, and the common case
		}
	}
	// Past here only on drift, so the cost of walking ListMetas is confined to this rare path.
	notBefore := metaCreated(m)
	claimed := claimedByOtherSlots(store, slot)
	var found string
	for _, s := range all {
		if claimed[s.ID] {
			continue
		}
		if !notBefore.IsZero() && !s.Created.IsZero() && s.Created.Before(notBefore) {
			continue // predates this slot, so it may belong to a previous one
		}
		if found != "" {
			return cached // more than one candidate; do not guess
		}
		found = s.ID
	}
	if found == "" {
		return cached
	}
	store.Write(slot, found)
	return found
}

// claimedByOtherSlots collects the conversation ids other slots have already been
// given, so a replacement is never stolen from a healthy session. copilot/cursor
// record theirs at BuildLaunch (before the CLI starts), so every AF slot's id is
// claimed from launch — an unclaimed conversation is one no slot imposed.
func claimedByOtherSlots(store SidStore, slot string) map[string]bool {
	out := map[string]bool{}
	for _, other := range session.ListMetas() {
		s := session.UUID(other.Dir, other.Name)
		if s == slot {
			continue
		}
		if id := store.Read(s); id != "" {
			out[id] = true
		}
	}
	return out
}

// metaCreated parses the slot's creation time. Zero (absent/unparsable) = no fence,
// degrading to the permissive behavior rather than never resolving — the same call as
// kiro's slotCreatedAt.
func metaCreated(m session.Meta) time.Time {
	if m.CreatedAt == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}
