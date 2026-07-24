package claude

// 配達検証（docs/38）の一次記録チェック: 「snapshot 以降に user ターンが追記された」
// だけを真とし、既存の user 行・assistant 行の追記・別 sid では偽のままであること。

import (
	"os"
	"path/filepath"
	"testing"
)

func writeJSONL(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendJSONL(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

func TestUserTurnAppendedSince(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	proj := filepath.Join(dir, "projects", "-home-dev-repos-x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const sid = "44444444-4444-4444-8444-444444444444"
	p := filepath.Join(proj, sid+".jsonl")
	writeJSONL(t, p, `{"type":"user","message":{"content":"old turn"}}`+"\n")

	snap := TranscriptSnapshot(sid)
	if UserTurnAppendedSince(sid, snap) {
		t.Fatal("nothing appended yet — the pre-existing user line must not count")
	}

	// An assistant append is not submit evidence.
	appendJSONL(t, p, `{"type":"assistant","message":{"content":[]}}`+"\n")
	if UserTurnAppendedSince(sid, snap) {
		t.Fatal("assistant append must not count as a delivered prompt")
	}

	// A user append after the snapshot IS the evidence.
	appendJSONL(t, p, `{"type":"user","message":{"content":"/scout"}}`+"\n")
	if !UserTurnAppendedSince(sid, snap) {
		t.Fatal("user append after the snapshot must count")
	}
}

// The create path: the log does not exist when the snapshot is taken and materializes
// with the first turn — a user line in the newborn file counts from byte 0.
func TestUserTurnAppendedSinceNewFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	proj := filepath.Join(dir, "projects", "-home-dev-repos-y")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const sid = "55555555-5555-4555-8555-555555555555"

	snap := TranscriptSnapshot(sid) // no file yet → empty (but non-nil) baseline
	if snap == nil {
		t.Fatal("TranscriptSnapshot must never return nil (nil means 検証手段なし)")
	}
	if UserTurnAppendedSince(sid, snap) {
		t.Fatal("no log at all — no evidence")
	}

	p := filepath.Join(proj, sid+".jsonl")
	writeJSONL(t, p, `{"type":"summary","summary":"stub"}`+"\n")
	if UserTurnAppendedSince(sid, snap) {
		t.Fatal("bookkeeping-only newborn log must not count")
	}
	appendJSONL(t, p, `{"type":"user","message":{"content":"task"}}`+"\n")
	if !UserTurnAppendedSince(sid, snap) {
		t.Fatal("first user turn in a newborn log must count")
	}
}
