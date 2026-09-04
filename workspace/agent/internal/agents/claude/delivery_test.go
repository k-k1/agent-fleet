package claude

// The primary-record check of delivery verification (docs/log/38): only "a user turn was
// appended after the snapshot" counts as true; pre-existing user lines, appended assistant
// lines and a different sid must all stay false.

import (
	"encoding/json"
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
		t.Fatal("TranscriptSnapshot must never return nil (nil means there is no way to verify)")
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

// TestPromptAcceptedSince covers the second shape a delivered prompt takes: typed while
// the previous turn is still running, claude QUEUES it (a queue-operation line carrying
// the prompt) and no user line follows for minutes. Treating that as unconfirmed made the
// self-heal retype the whole prompt — a double injection.
func TestPromptAcceptedSince(t *testing.T) {
	const (
		sid    = "66666666-6666-4666-8666-666666666666"
		prompt = "追修正の再レビューをお願いします。worktree の 5 件です"
	)
	setup := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", dir)
		proj := filepath.Join(dir, "projects", "-home-dev-repos-z")
		if err := os.MkdirAll(proj, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(proj, sid+".jsonl")
		writeJSONL(t, p, `{"type":"assistant","message":{"content":[]}}`+"\n")
		return p
	}

	t.Run("queued prompt counts", func(t *testing.T) {
		p := setup(t)
		snap := TranscriptSnapshot(sid)
		if PromptAcceptedSince(sid, snap, prompt) {
			t.Fatal("nothing appended yet")
		}
		line, _ := json.Marshal(map[string]any{
			"type": "queue-operation", "operation": "enqueue", "content": prompt,
		})
		appendJSONL(t, p, string(line)+"\n")
		if !PromptAcceptedSince(sid, snap, prompt) {
			t.Fatal("a queued prompt IS delivered — retyping it would double-inject")
		}
	})

	t.Run("claude's own enqueue does not count", func(t *testing.T) {
		p := setup(t)
		snap := TranscriptSnapshot(sid)
		// claude enqueues its own task-notifications (a background agent finished);
		// keying on the record type alone would read those as our prompt.
		line, _ := json.Marshal(map[string]any{
			"type": "queue-operation", "operation": "enqueue",
			"content": "<task-notification>\n<task-id>abc</task-id>\n</task-notification>",
		})
		appendJSONL(t, p, string(line)+"\n")
		if PromptAcceptedSince(sid, snap, prompt) {
			t.Fatal("claude's internal enqueue must not pass for the typed prompt")
		}
	})

	t.Run("user line still counts", func(t *testing.T) {
		p := setup(t)
		snap := TranscriptSnapshot(sid)
		appendJSONL(t, p, `{"type":"user","message":{"content":"whatever"}}`+"\n")
		if !PromptAcceptedSince(sid, snap, prompt) {
			t.Fatal("a user turn is the primary evidence and must still count")
		}
	})
}

// TestSubagentReceivedSince pins the misdelivery detector: the prompt landing in a
// BACKGROUND AGENT's transcript means the pane's input box was bound to that agent, so
// the self-heal must not retype (it would steer the agent a second time).
func TestSubagentReceivedSince(t *testing.T) {
	const (
		sid    = "77777777-7777-4777-8777-777777777777"
		prompt = "追修正の再レビューをお願いします。worktree の 5 件です"
	)
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	sub := filepath.Join(dir, "projects", "-p", sid, "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(sub, "agent-a1.jsonl")
	writeJSONL(t, log, `{"type":"assistant","isSidechain":true}`+"\n")

	snap := SubagentSnapshot(sid)
	if SubagentReceivedSince(sid, snap, prompt) {
		t.Fatal("nothing appended yet")
	}
	// An agent that merely kept working is NOT a misdelivery.
	appendJSONL(t, log, `{"type":"assistant","isSidechain":true,"message":{"content":[]}}`+"\n")
	if SubagentReceivedSince(sid, snap, prompt) {
		t.Fatal("ordinary agent progress must not read as a misdelivered prompt")
	}
	// The real shape claude writes when the pane steered the agent.
	line, _ := json.Marshal(map[string]any{
		"type": "user", "isSidechain": true, "isMeta": true,
		"message": map[string]any{
			"role":    "user",
			"content": "The user sent a new message while you were working:\n" + prompt,
		},
	})
	appendJSONL(t, log, string(line)+"\n")
	if !SubagentReceivedSince(sid, snap, prompt) {
		t.Fatal("the prompt landed in the agent's transcript — that IS the misdelivery")
	}
}
