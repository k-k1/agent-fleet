package sessionx

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// ArchivedAt is what the af-work prune counts its seven days from: archiving stamps it,
// archiving again keeps it, and a restore clears it so a later archive starts a new week.
func TestArchiveStampsAndRestoreClearsArchivedAt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	session.WriteMeta(session.Meta{Name: "arch001", Dir: home, Kind: session.KindClaude})
	m, _ := session.ReadMeta("arch001")

	ArchiveSession(m)
	got, _ := session.ReadMeta("arch001")
	at, err := time.Parse(time.RFC3339, got.ArchivedAt)
	if !got.Archived || err != nil || time.Since(at) > time.Minute {
		t.Fatalf("after archive: archived=%v archivedAt=%q", got.Archived, got.ArchivedAt)
	}

	// Archiving a row that is already on the shelf must not push its date back.
	old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	got.ArchivedAt = old
	session.WriteMeta(got)
	ArchiveSession(got)
	if again, _ := session.ReadMeta("arch001"); again.ArchivedAt != old {
		t.Fatalf("re-archive moved archivedAt: %q, want %q", again.ArchivedAt, old)
	}

	restored, ok := RestoreSession("arch001")
	if !ok || restored.Archived || restored.ArchivedAt != "" {
		t.Fatalf("restore: %+v ok=%v", restored, ok)
	}
	if disk, _ := session.ReadMeta("arch001"); disk.ArchivedAt != "" {
		t.Fatalf("restore left archivedAt on disk: %q", disk.ArchivedAt)
	}

	// Un-archiving after a failed recreate clears it the same way.
	ArchiveSession(restored)
	unarchiveAfterFailedRecreate("arch001")
	if disk, _ := session.ReadMeta("arch001"); disk.Archived || disk.ArchivedAt != "" {
		t.Fatalf("unarchiveAfterFailedRecreate: %+v", disk)
	}
}
