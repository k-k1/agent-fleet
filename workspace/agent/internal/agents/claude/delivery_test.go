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

// TestPromptStateSince covers the second shape a delivered prompt takes: typed while
// the previous turn is still running, claude QUEUES it (a queue-operation line carrying
// the prompt) and no user line follows for minutes. Treating that as unconfirmed made the
// self-heal retype the whole prompt — a double injection.
func TestPromptStateSince(t *testing.T) {
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
		if got := PromptStateSince(sid, snap, prompt); got != PromptAbsent {
			t.Fatalf("nothing appended yet, got %v", got)
		}
		line, _ := json.Marshal(map[string]any{
			"type": "queue-operation", "operation": "enqueue", "content": prompt,
		})
		appendJSONL(t, p, string(line)+"\n")
		if got := PromptStateSince(sid, snap, prompt); got != PromptQueued {
			t.Fatalf("an enqueue record alone is queued (received, no turn), got %v", got)
		}
	})

	t.Run("queued prompt whose turn starts is started", func(t *testing.T) {
		p := setup(t)
		snap := TranscriptSnapshot(sid)
		enq, _ := json.Marshal(map[string]any{
			"type": "queue-operation", "operation": "enqueue", "content": prompt,
		})
		appendJSONL(t, p, string(enq)+"\n")
		// The running turn's own tool result is a user line too: it must not start ours.
		appendJSONL(t, p, `{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`+"\n")
		if got := PromptStateSince(sid, snap, prompt); got != PromptQueued {
			t.Fatalf("a tool_result user line must not start the queued prompt, got %v", got)
		}
		echo, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user",
			"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "Search result: " + prompt}}}})
		appendJSONL(t, p, string(echo)+"\n")
		if got := PromptStateSince(sid, snap, prompt); got != PromptQueued {
			t.Fatalf("a tool_result echoing the prompt must not start it, got %v", got)
		}
		usr, _ := json.Marshal(map[string]any{
			"type": "user", "message": map[string]any{"role": "user", "content": prompt},
		})
		appendJSONL(t, p, string(usr)+"\n")
		if got := PromptStateSince(sid, snap, prompt); got != PromptStarted {
			t.Fatalf("the replayed user line starts the queued prompt, got %v", got)
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
		if got := PromptStateSince(sid, snap, prompt); got != PromptAbsent {
			t.Fatalf("claude's internal enqueue must not pass for the typed prompt, got %v", got)
		}
	})

	t.Run("user line still counts", func(t *testing.T) {
		p := setup(t)
		snap := TranscriptSnapshot(sid)
		appendJSONL(t, p, `{"type":"user","message":{"content":"whatever"}}`+"\n")
		if got := PromptStateSince(sid, snap, prompt); got != PromptStarted {
			t.Fatalf("a user turn is the primary evidence and must still count, got %v", got)
		}
	})

	t.Run("a slash command stored rewritten counts", func(t *testing.T) {
		p := setup(t)
		snap := TranscriptSnapshot(sid)
		usr, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user",
			"content": "<command-message>scout</command-message>\n<command-name>/scout</command-name>\n<command-args>today</command-args>"}})
		appendJSONL(t, p, string(usr)+"\n")
		if got := PromptStateSince(sid, snap, "/scout today"); got != PromptStarted {
			t.Fatalf("a slash command with arguments must still count (a miss retypes it), got %v", got)
		}
	})

	// #2741: a resumed session replays the notification of a background task that ended while
	// it was stopped. That turn's user lines are claude's own, and reading them as the typed
	// prompt reported a peer message delivered that never arrived.
	t.Run("lines claude writes itself do not count", func(t *testing.T) {
		for name, line := range map[string]map[string]any{
			"task notification": {"type": "user", "message": map[string]any{"role": "user",
				"content": "<task-notification>\n<task-id>b1</task-id>\n<status>killed</status>\n</task-notification>"}},
			"meta line": {"type": "user", "isMeta": true, "message": map[string]any{"role": "user",
				"content": "Caveat: the messages below were generated by the user while running local commands."}},
			"tool result": {"type": "user", "message": map[string]any{"role": "user",
				"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "ok"}}}},
		} {
			t.Run(name, func(t *testing.T) {
				p := setup(t)
				snap := TranscriptSnapshot(sid)
				b, _ := json.Marshal(line)
				appendJSONL(t, p, string(b)+"\n")
				if got := PromptStateSince(sid, snap, prompt); got != PromptAbsent {
					t.Fatalf("a line claude wrote itself must not pass for the typed prompt, got %v", got)
				}
			})
		}
	})
}

// The review of #2741: claude's own lines are set aside BEFORE any text match. A notification
// that quotes an earlier peer envelope carries the same head as the new message (the envelope
// opens every peer message from one sender alike), and so does an isMeta line echoing it; read
// by their text they passed for the typed prompt, as started or as queued.
func TestPromptStateSinceIgnoresClaudeLinesQuotingThePrompt(t *testing.T) {
	const (
		sid    = "88888888-8888-4888-8888-888888888888"
		prompt = "[agent-fleet:peer from=sparent intent=question reply=required] did you push?"
		quoted = "[agent-fleet:peer from=sparent intent=request reply=only-if-blocked] push it"
	)
	setup := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", dir)
		proj := filepath.Join(dir, "projects", "-q")
		if err := os.MkdirAll(proj, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(proj, sid+".jsonl")
		writeJSONL(t, p, `{"type":"assistant","message":{"content":[]}}`+"\n")
		return p
	}
	notification := "<task-notification>\n<task-id>m1</task-id>\n<output>" + quoted + "</output>\n</task-notification>"
	for name, lines := range map[string][]map[string]any{
		"notification user line": {{"type": "user", "message": map[string]any{"role": "user", "content": notification}}},
		"notification enqueue":   {{"type": "queue-operation", "operation": "enqueue", "content": notification}},
		"notification enqueue then its user line": {
			{"type": "queue-operation", "operation": "enqueue", "content": notification},
			{"type": "user", "message": map[string]any{"role": "user", "content": notification}},
		},
		"notification in a record of another type": {{"type": "attachment", "attachment": map[string]any{"type": "queued_command"}, "content": notification}},
		"meta line": {{"type": "user", "isMeta": true, "message": map[string]any{"role": "user", "content": quoted}}},
	} {
		t.Run(name, func(t *testing.T) {
			p := setup(t)
			snap := TranscriptSnapshot(sid)
			for _, l := range lines {
				b, _ := json.Marshal(l)
				appendJSONL(t, p, string(b)+"\n")
			}
			if got := PromptStateSince(sid, snap, prompt); got != PromptAbsent {
				t.Fatalf("claude's own line quoting the prompt's head: got %v, want absent", got)
			}
		})
	}

	t.Run("a typed prompt that mentions the tag still counts", func(t *testing.T) {
		p := setup(t)
		snap := TranscriptSnapshot(sid)
		typed := prompt + " (the <task-notification> line came first)"
		b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": typed}})
		appendJSONL(t, p, string(b)+"\n")
		if got := PromptStateSince(sid, snap, typed); got != PromptStarted {
			t.Fatalf("got %v, want started", got)
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
