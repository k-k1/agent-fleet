package session

import (
	"path/filepath"
	"testing"
)

// A deleted meta is never written back by a stale snapshot, and a restore from the trash
// (CreateMetaIfAbsent) is what brings the name back (ADR 0101; review 115 🟡5).
func TestWriteMetaRefusesADeletedName(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	m := Meta{Name: "dead001", Kind: KindShell}
	WriteMeta(m)
	RemoveMetaAndLineage(m.Name)
	WriteMeta(m)
	if _, ok := ReadMeta(m.Name); ok {
		t.Fatal("WriteMeta brought a deleted session back")
	}
	if created, err := CreateMetaIfAbsent(m); err != nil || !created {
		t.Fatalf("restore: created=%v err=%v", created, err)
	}
	m.Title = "after restore"
	WriteMeta(m)
	if got, _ := ReadMeta(m.Name); got.Title != "after restore" {
		t.Fatal("a restored session can no longer be written")
	}
}
