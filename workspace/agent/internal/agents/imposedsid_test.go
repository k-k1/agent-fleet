package agents

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// isolateSlots points the sid store and MetaDir at temp dirs, so the real fleet's sessions are
// never read.
func isolateSlots(t *testing.T) SidStore {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	return NewSidStore("test-sid")
}

const (
	imposedID = "11111111-1111-4111-8111-111111111111" // the id we imposed
	driftedID = "22222222-2222-4222-8222-222222222222" // the id the CLI actually started using
	otherID   = "33333333-3333-4333-8333-333333333333"
)

func slotMeta(t *testing.T, name, dir, created string) session.Meta {
	t.Helper()
	m := session.Meta{Name: name, Dir: dir, Kind: session.KindCopilot, CreatedAt: created}
	session.WriteMeta(m)
	return m
}

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The normal path: as long as the CLI writes under the id we imposed, nothing moves. This is
// the vast majority of cases, and leaving no room to steal a healthy slot's conversation is the
// precondition for this recovery to be safe at all.
func TestResolveImposedKeepsHonoredID(t *testing.T) {
	store := isolateSlots(t)
	m := slotMeta(t, "s1", "/tmp/repo", "2026-08-22T10:00:00+09:00")
	slot := session.UUID(m.Dir, m.Name)
	store.Write(slot, imposedID)

	list := func(string) []CLISession {
		return []CLISession{
			{ID: imposedID, Created: ts(t, "2026-08-22T10:00:05+09:00")},
			{ID: driftedID, Created: ts(t, "2026-08-22T10:01:00+09:00")}, // another slot's newer conversation
		}
	}
	if got := ResolveImposedSID(store, m, list); got != imposedID {
		t.Fatalf("= %q, want the honored id %q", got, imposedID)
	}
	if got := store.Read(slot); got != imposedID {
		t.Fatalf("ledger = %q, a healthy slot's ledger entry was overwritten", got)
	}
}

// Drift: the imposed id does not exist on the CLI side, i.e. it was never used. When exactly
// one unclaimed conversation exists in this dir, it is picked up instead (the breakage actually
// seen with claude).
func TestResolveImposedAdoptsDrift(t *testing.T) {
	store := isolateSlots(t)
	m := slotMeta(t, "s1", "/tmp/repo", "2026-08-22T10:00:00+09:00")
	slot := session.UUID(m.Dir, m.Name)
	store.Write(slot, imposedID)

	list := func(string) []CLISession {
		return []CLISession{{ID: driftedID, Created: ts(t, "2026-08-22T10:00:30+09:00")}}
	}
	if got := ResolveImposedSID(store, m, list); got != driftedID {
		t.Fatalf("= %q, want the drifted id %q", got, driftedID)
	}
	if got := store.Read(slot); got != driftedID {
		t.Fatalf("ledger = %q, want it repointed to %q", got, driftedID)
	}
}

// No discovery for a slot that never launched (an empty ledger). If a fresh slot grabbed
// someone else's conversation in the same dir, a mirror that has not even started would show a
// conversation nobody here had.
func TestResolveImposedNeverAdoptsForFreshSlot(t *testing.T) {
	store := isolateSlots(t)
	m := slotMeta(t, "s1", "/tmp/repo", "2026-08-22T10:00:00+09:00")

	list := func(string) []CLISession {
		return []CLISession{{ID: driftedID, Created: ts(t, "2026-08-22T10:00:30+09:00")}}
	}
	if got := ResolveImposedSID(store, m, list); got != "" {
		t.Fatalf("= %q, want \"\" - a slot that never launched must not discover anything", got)
	}
}

// Nothing moves when there are several candidates: adopting the wrong one (showing someone
// else's conversation in the mirror) is worse than staying stuck.
func TestResolveImposedRefusesAmbiguity(t *testing.T) {
	store := isolateSlots(t)
	m := slotMeta(t, "s1", "/tmp/repo", "2026-08-22T10:00:00+09:00")
	store.Write(session.UUID(m.Dir, m.Name), imposedID)

	list := func(string) []CLISession {
		return []CLISession{
			{ID: driftedID, Created: ts(t, "2026-08-22T10:00:30+09:00")},
			{ID: otherID, Created: ts(t, "2026-08-22T10:00:40+09:00")},
		}
	}
	if got := ResolveImposedSID(store, m, list); got != imposedID {
		t.Fatalf("= %q, want the cached id kept when ambiguous", got)
	}
}

// A conversation another slot already holds is not a candidate. copilot/cursor write to the
// ledger in BuildLaunch, so every conversation AF launched counts as claimed.
func TestResolveImposedSkipsClaimedByOtherSlot(t *testing.T) {
	store := isolateSlots(t)
	m := slotMeta(t, "s1", "/tmp/repo", "2026-08-22T10:00:00+09:00")
	other := slotMeta(t, "s2", "/tmp/repo", "2026-08-22T09:00:00+09:00")
	store.Write(session.UUID(m.Dir, m.Name), imposedID)
	store.Write(session.UUID(other.Dir, other.Name), driftedID) // belongs to s2

	list := func(string) []CLISession {
		return []CLISession{{ID: driftedID, Created: ts(t, "2026-08-22T10:00:30+09:00")}}
	}
	if got := ResolveImposedSID(store, m, list); got != imposedID {
		t.Fatalf("= %q, another slot's conversation was stolen", got)
	}
}

// A conversation older than the slot itself is never picked up. A recreate cuts a new slug
// into the same dir, so the predecessor slot's conversation is always still sitting there (the
// same fence as kiro's discoverSid).
func TestResolveImposedFencesBySlotCreation(t *testing.T) {
	store := isolateSlots(t)
	m := slotMeta(t, "s1", "/tmp/repo", "2026-08-22T10:00:00+09:00")
	store.Write(session.UUID(m.Dir, m.Name), imposedID)

	list := func(string) []CLISession {
		return []CLISession{{ID: driftedID, Created: ts(t, "2026-08-22T09:59:00+09:00")}} // the predecessor
	}
	if got := ResolveImposedSID(store, m, list); got != imposedID {
		t.Fatalf("= %q, the predecessor slot's conversation was adopted", got)
	}
}

// If the imposed id EXISTS on the CLI side it is kept, even when it predates the slot's
// creation. Concretely: a session materialized by a fork inherits the source conversation's
// created_at (copilot's MaterializeForkAt only swaps the sid), so it is a healthy conversation
// older than its slot. Unless "exists, so do not touch" is decided first, the slot switches to
// a newer conversation in the same dir and the whole branched conversation disappears.
func TestResolveImposedKeepsHonoredIDOlderThanSlot(t *testing.T) {
	store := isolateSlots(t)
	m := slotMeta(t, "s1", "/tmp/repo", "2026-08-22T10:00:00+09:00")
	slot := session.UUID(m.Dir, m.Name)
	store.Write(slot, imposedID)

	list := func(string) []CLISession {
		return []CLISession{
			{ID: imposedID, Created: ts(t, "2026-08-01T09:00:00+09:00")}, // the fork source's creation time
			{ID: driftedID, Created: ts(t, "2026-08-22T10:00:30+09:00")},
		}
	}
	if got := ResolveImposedSID(store, m, list); got != imposedID {
		t.Fatalf("= %q, want the honored id %q kept", got, imposedID)
	}
	if got := store.Read(slot); got != imposedID {
		t.Fatalf("ledger = %q, a healthy (branched) conversation was let go", got)
	}
}
