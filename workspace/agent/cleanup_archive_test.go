package main

import (
	"os"
	"path/filepath"
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
	if err := writeCleanupArchive(man, map[string][]byte{"sessions/slot42/00.jsonl": want}); err != nil {
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
	if err := purgeCleanupArchive(man.ID); err != nil {
		t.Fatal(err)
	}
	if len(listCleanupArchives()) != 0 {
		t.Fatal("archive still listed after purge")
	}
}

func TestCleanupArchiveIDTraversalGuard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, _, err := readCleanupArchive("../etc/passwd"); err == nil {
		t.Fatal("expected traversal id to be rejected")
	}
	if err := purgeCleanupArchive("../../x"); err == nil {
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
