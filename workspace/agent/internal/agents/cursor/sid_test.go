package cursor

import (
	"os"
	"path/filepath"
	"testing"
)

// writeChat lays out one cursor chat the way the CLI does:
// projects/<cwdSlug>/agent-transcripts/<chatID>/<chatID>.jsonl (the real on-disk shape).
func writeChat(t *testing.T, home, dir, chatID string, withTranscript bool) string {
	t.Helper()
	d := filepath.Join(home, ".cursor", "projects", cwdSlug(dir), "agent-transcripts", chatID)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if withTranscript {
		if err := os.WriteFile(filepath.Join(d, chatID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// The cwd is part of the path, so attribution needs nothing but reading the directory.
// Chats from another cwd must not leak in — once that breaks, another project's
// conversation gets picked up.
func TestCliSessionsScopedToCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeChat(t, home, "/tmp/repo", "aaaaaaaa-0000-4000-8000-000000000001", true)
	writeChat(t, home, "/tmp/other", "bbbbbbbb-0000-4000-8000-000000000002", true)

	got := cliSessions("/tmp/repo")
	if len(got) != 1 || got[0].ID != "aaaaaaaa-0000-4000-8000-000000000001" {
		t.Fatalf("cliSessions = %+v, want only the /tmp/repo chat", got)
	}
	if got[0].Created.IsZero() {
		t.Fatal("Created is empty - cannot be matched against the slot creation time")
	}
}

// A directory with no transcript file is not a conversation. cursor creates the empty
// container at launch, so accepting one as a candidate grabs a chat that has said nothing
// yet and loses the real conversation.
func TestCliSessionsIgnoresEmptyChatDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeChat(t, home, "/tmp/repo", "cccccccc-0000-4000-8000-000000000003", false)

	if got := cliSessions("/tmp/repo"); len(got) != 0 {
		t.Fatalf("cliSessions = %+v, want none", got)
	}
}

// The cwd slug rule (strip leading/trailing "/" and turn the rest into "-") is the same
// mapping transcriptPath uses; changing only one side empties the candidate list, so pin
// them together.
func TestCliSessionsUsesTranscriptPathSlug(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const chat = "dddddddd-0000-4000-8000-000000000004"
	writeChat(t, home, "/home/dev/repos/proj", chat, true)

	got := cliSessions("/home/dev/repos/proj")
	if len(got) != 1 {
		t.Fatalf("cliSessions = %+v, want 1", got)
	}
	// The same id must resolve through transcriptPath too (read path and discovery path
	// share one mapping).
	if p := transcriptPath("/home/dev/repos/proj", chat); filepath.Base(p) != chat+".jsonl" {
		t.Fatalf("transcriptPath = %q", p)
	} else if _, err := os.Stat(p); err != nil {
		t.Fatalf("transcriptPath does not point at a real file: %v", err)
	}
}
