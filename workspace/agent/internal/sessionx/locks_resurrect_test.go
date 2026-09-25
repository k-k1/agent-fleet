package sessionx

import (
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A snapshot written back after the session was deleted must not bring it back (ADR 0101):
// the list handler, halt and the driver switch all read a meta, work for a while, then write
// it through stampHalted / ArchiveSession — and a delete can land in between,
// after which the row would reappear with its transcript already in the trash.
func TestSnapshotWriteDoesNotResurrectADeletedSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))

	m := session.Meta{Name: "gone001", Dir: home, Kind: session.KindClaude}
	session.WriteMeta(m)
	snapshot, _ := session.ReadMeta(m.Name)
	session.RemoveMeta(m.Name) // the delete lands between the read and the write

	stampHalted(snapshot)
	if _, ok := session.ReadMeta(m.Name); ok {
		t.Fatal("stampHalted wrote a deleted session back")
	}
	ArchiveSession(snapshot)
	if _, ok := session.ReadMeta(m.Name); ok {
		t.Fatal("ArchiveSession wrote a deleted session back")
	}

	// And a lock set after the snapshot survives the archive's write.
	session.WriteMeta(m)
	snapshot, _ = session.ReadMeta(m.Name)
	locked := m
	locked.Locked = true
	session.WriteMeta(locked)
	ArchiveSession(snapshot)
	if got, ok := session.ReadMeta(m.Name); !ok || !got.Locked || !got.Archived {
		t.Fatalf("after archiving a snapshot: meta=%+v ok=%v, want archived and still locked", got, ok)
	}
}
