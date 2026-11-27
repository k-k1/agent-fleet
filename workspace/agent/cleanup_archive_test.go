package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestCleanupArchiveRoundTrip: a session bundled to a gz archive can be listed and
// restored — its meta reappears and its jsonl is written back to the original path.
func TestCleanupArchiveRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A session with a transcript jsonl on disk.
	dir := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "slot42", Dir: dir, Kind: session.KindClaude, Title: "掃除対象"}
	jsonlPath := filepath.Join(home, "transcript-slot42.jsonl")
	want := []byte(`{"type":"user"}` + "\n" + `{"type":"assistant"}` + "\n")
	if err := os.WriteFile(jsonlPath, want, 0o600); err != nil {
		t.Fatal(err)
	}

	// Build an archive by hand (bypasses claude.TranscriptRead path resolution).
	man := cleanupManifest{
		ID: newCleanupID(time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC), "slot42"),
		At: "2026-07-20T09:00:00Z", Reason: "delete_session",
		Sessions: []cleanupArchivedSession{{
			Name: m.Name, Display: session.Display(m), Kind: m.Kind, Meta: marshalMeta(m),
			JSONLPaths: []string{jsonlPath}, JSONLNames: []string{"sessions/slot42/00.jsonl"},
		}},
	}
	if err := writeCleanupArchive(&man, map[string][]byte{"sessions/slot42/00.jsonl": want}); err != nil {
		t.Fatal(err)
	}

	// List sees it.
	got := listCleanupArchives()
	if len(got) != 1 || got[0].ID != man.ID || len(got[0].Sessions) != 1 {
		t.Fatalf("list = %+v", got)
	}

	// Simulate the delete: remove the live jsonl + meta.
	_ = os.Remove(jsonlPath)
	session.RemoveMeta(m.Name)
	if _, ok := session.ReadMeta(m.Name); ok {
		t.Fatal("meta should be gone before restore")
	}

	// Restore brings both back.
	res, err := restoreCleanupArchive(man.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ss, _ := res["sessions"].([]string); len(ss) != 1 || ss[0] != "slot42" {
		t.Fatalf("restored sessions = %v", res["sessions"])
	}
	if _, ok := session.ReadMeta(m.Name); !ok {
		t.Fatal("meta not restored")
	}
	back, err := os.ReadFile(jsonlPath)
	if err != nil || string(back) != string(want) {
		t.Fatalf("jsonl not restored: %q err=%v", back, err)
	}

	// Purge removes it for good.
	if _, err := purgeCleanupArchive(man.ID); err != nil {
		t.Fatal(err)
	}
	if len(listCleanupArchives()) != 0 {
		t.Fatal("archive still listed after purge")
	}
}

func TestCleanupArchiveIDTraversalGuard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := restoreCleanupArchive("../etc/passwd"); err == nil {
		t.Fatal("expected traversal id to be rejected")
	}
	if _, err := purgeCleanupArchive("../../x"); err == nil {
		t.Fatal("expected traversal purge to be rejected")
	}
}

func TestIDSlug(t *testing.T) {
	cases := map[string]string{
		"temp/wip-abc": "temp-wip-abc",
		"slot42":       "slot42",
		"":             "item",
		"a/../../etc":  "a-------etc",
		"very-long-name-that-exceeds-the-limit-xxxxx": "very-long-name-that-exce",
	}
	for in, want := range cases {
		if got := idSlug(in); got != want {
			t.Errorf("idSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// readCleanupArchive inspects a fixture through the same reader as restore.
func readCleanupArchive(id string) (cleanupManifest, map[string][]byte, error) {
	root, err := os.OpenRoot(cleanupStoreDir())
	if err != nil {
		return cleanupManifest{}, nil, err
	}
	defer root.Close()
	f, _, err := openArchiveFile(root, id+".tar.gz")
	if err != nil {
		return cleanupManifest{}, nil, err
	}
	defer f.Close()
	return readCleanupTar(f, id)
}

func TestReadArchiveFileRequiresBoundedRegularMetadata(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFile("valid.json", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("valid.json", "link.json"); err != nil {
		t.Fatal(err)
	}
	if err := root.Mkdir("directory.json", 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := root.Create("oversized.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxCleanupEntryBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"link.json", "directory.json", "oversized.json"} {
		if _, err := readArchiveFile(root, name); err == nil {
			t.Errorf("readArchiveFile accepted %s", name)
		}
	}
	if data, err := readArchiveFile(root, "valid.json"); err != nil || string(data) != "{}" {
		t.Errorf("readArchiveFile of regular metadata = %q, %v", data, err)
	}
}

func TestRestoreRejectsAMixedSpaceArchive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	man := cleanupManifest{
		ID:       newCleanupID(time.Now(), "mixed-space"),
		Space:    &spaceTombstone{Folder: "space"},
		Branches: []cleanupArchivedBranch{{Name: "branch"}},
	}
	if err := writeCleanupArchive(&man, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreCleanupArchive(man.ID); err == nil || !strings.Contains(err.Error(), "a space cannot share an archive") {
		t.Fatalf("restore of a mixed space archive returned %v", err)
	}
}
