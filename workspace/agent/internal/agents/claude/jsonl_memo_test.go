package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// The memo exists to stop the projects/* sweep (jsonl_memo.go: 821 directory reads per second
// on a production workspace, which exhausted the file system's burst credits). These pin both
// halves of that bargain — that the sweep really stops, and that it never buys speed with a
// wrong answer.

func TestPathMemoSearchesOnceWhileTheAnswerHolds(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "found.jsonl")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var m pathMemo
	calls := 0
	search := func() []string { calls++; return []string{p} }

	for range 5 {
		if got := m.lookup("k", search); len(got) != 1 || got[0] != p {
			t.Fatalf("lookup = %v, want [%s]", got, p)
		}
	}
	if calls != 1 {
		t.Fatalf("searched %d times, want 1 — the sweep is what this exists to avoid", calls)
	}
}

func TestPathMemoResearchesWhenTheRememberedPathIsGone(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "moved.jsonl")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var m pathMemo
	calls := 0
	search := func() []string {
		calls++
		if _, err := os.Lstat(p); err != nil {
			return nil
		}
		return []string{p}
	}

	m.lookup("k", search)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if got := m.lookup("k", search); len(got) != 0 {
		t.Fatalf("lookup = %v, want empty once the file is gone", got)
	}
	if calls != 2 {
		t.Fatalf("searched %d times, want 2 — a remembered hit must be re-checked on disk", calls)
	}
}

func TestPathMemoNeverRemembersAMiss(t *testing.T) {
	var m pathMemo
	calls := 0
	search := func() []string { calls++; return nil }

	m.lookup("k", search)
	m.lookup("k", search)
	if calls != 2 {
		t.Fatalf("searched %d times, want 2 — remembering an absence is how a transcript that "+
			"appeared later would stay invisible", calls)
	}
}

// The hazard the invariant above protects, at the real call site: SessionJSONLExists decides
// --resume vs --session-id, and claiming "no transcript" when one exists makes claude exit
// with "Session ID is already in use".
func TestRawJSONLPathsSeesATranscriptCreatedAfterAMiss(t *testing.T) {
	cfg := isolateSlot(t)
	const id = "11111111-2222-3333-4444-555555555555"

	if got := rawJSONLPaths(id); len(got) != 0 {
		t.Fatalf("before the transcript exists: %v, want empty", got)
	}
	want := writeSlotJSONL(t, cfg, "-home-dev-repos-x", id)
	got := rawJSONLPaths(id)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("after the transcript is written: %v, want [%s]", got, want)
	}
	if !SessionJSONLExists(id) {
		t.Fatal("SessionJSONLExists = false with the transcript on disk — this is the " +
			"\"Session ID is already in use\" crash")
	}
}

func TestRawJSONLPathsStopsReportingADeletedTranscript(t *testing.T) {
	cfg := isolateSlot(t)
	const id = "66666666-7777-8888-9999-000000000000"

	p := writeSlotJSONL(t, cfg, "-home-dev-repos-y", id)
	if got := rawJSONLPaths(id); len(got) != 1 {
		t.Fatalf("seed: %v, want one path", got)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if got := rawJSONLPaths(id); len(got) != 0 {
		t.Fatalf("after deletion: %v, want empty — a remembered hit is re-checked on disk", got)
	}
}

// SubagentLogs lost its projects/* sweep the same way; it must still find the logs.
func TestSubagentLogsStillFindsLogsAfterTheSweepWasRemoved(t *testing.T) {
	cfg := isolateSlot(t)
	const sid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	base := filepath.Join(cfg, "projects", "-home-dev-repos-z", sid, "subagents")
	if err := os.MkdirAll(filepath.Join(base, "workflows", "wf_abc"), 0o700); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(base, "agent-1.jsonl")
	nested := filepath.Join(base, "workflows", "wf_abc", "agent-2.jsonl")
	for _, p := range []string{plain, nested} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got := SubagentLogs(sid)
	if len(got) != 2 {
		t.Fatalf("SubagentLogs = %v, want both the plain and the workflow log", got)
	}
	seen := map[string]bool{got[0]: true, got[1]: true}
	if !seen[plain] || !seen[nested] {
		t.Fatalf("SubagentLogs = %v, want %s and %s", got, plain, nested)
	}
}
