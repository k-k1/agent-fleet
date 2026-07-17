package main

import (
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// feedStatusHook drives runSessionStatusHook once with the given claude hook JSON on
// stdin (as the real claude hook does), restoring os.Stdin afterward.
func feedStatusHook(t *testing.T, state, stdinJSON string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(stdinJSON); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = w.Close()
	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig }()
	runSessionStatusHook([]string{state})
	_ = r.Close()
}

func TestWorkingToIdleQueuesDurableNotification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s1", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)
	status.Persist(sid, "working")
	runSessionStatusHook([]string{"idle", sid})
	events := notice.List()
	if len(events) != 1 || events[0].Kind != "answer-ready" || events[0].SessionName != "s1" || events[0].DisplayName != "Project" {
		t.Fatalf("events=%+v", events)
	}
}

// A permission prompt for AskUserQuestion fires between that tool's PreToolUse and
// PostToolUse. It must NOT destroy the captured question — otherwise the Console loses
// the options and shows only the bare permission block.
func TestPermissionPromptKeepsPendingQuestion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "sess-abc"

	// 1) PreToolUse(AskUserQuestion) → question, with the questions payload on stdin.
	feedStatusHook(t, "question", `{"session_id":"`+sid+`","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Pick"}]}}`)
	if _, ok := status.ReadPendingQuestion(sid); !ok {
		t.Fatal("pending question not written on the question hook")
	}

	// 2) Notification(permission_prompt) → permission. Must retain the question.
	feedStatusHook(t, "permission", `{"session_id":"`+sid+`","notification_type":"permission_prompt","message":"needs permission"}`)

	st, ok := status.Read(sid)
	if !ok || st.State != "permission" {
		t.Fatalf("status = %v (ok=%v), want permission", st.State, ok)
	}
	if _, ok := status.ReadPendingQuestion(sid); !ok {
		t.Fatal("permission prompt destroyed the pending question (regression)")
	}
	if _, ok := status.ReadPendingPermission(sid); !ok {
		t.Fatal("pending permission not written")
	}

	// 3) PostToolUse(AskUserQuestion) → working clears the question via its own lifecycle.
	feedStatusHook(t, "working", `{"session_id":"`+sid+`"}`)
	if _, ok := status.ReadPendingQuestion(sid); ok {
		t.Fatal("pending question not cleared when the question was answered")
	}
}

// A non-permission state (idle/working) still clears a stale pending question, so the
// retention above is scoped to the permission overlay only.
func TestIdleClearsPendingQuestion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "sess-idle"
	feedStatusHook(t, "question", `{"session_id":"`+sid+`","tool_input":{"questions":[{"question":"x"}]}}`)
	if _, ok := status.ReadPendingQuestion(sid); !ok {
		t.Fatal("precondition: question should be pending")
	}
	feedStatusHook(t, "idle", `{"session_id":"`+sid+`"}`)
	if _, ok := status.ReadPendingQuestion(sid); ok {
		t.Fatal("idle should clear the pending question")
	}
}

// boot (the SessionStart hook) resets a session to idle, so a session killed mid-turn and
// then resumed — where no Stop ever fired — doesn't badge 進行中 forever off a stale
// "working" status file.
func TestBootResetsStaleWorkingToIdle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, source := range []string{"startup", "resume", "clear"} {
		t.Run(source, func(t *testing.T) {
			sid := "sess-boot-" + source
			status.Persist(sid, "working") // stale: killed mid-turn, never got a Stop
			feedStatusHook(t, "boot", `{"session_id":"`+sid+`","source":"`+source+`"}`)
			if got := status.LiveState(sid); got != "idle" {
				t.Errorf("after boot(source=%s): state = %q, want idle", source, got)
			}
		})
	}
}

// ...but source=="compact" must NOT reset, because auto-compaction resumes the SAME
// in-flight turn: SessionStart fires mid-turn there, and idling it would false-idle live
// work — the very bug the reverse-heal exists to undo. This guard has no other test and
// its loss is silent (the session merely reads idle while claude is still working), so it
// is pinned here explicitly.
func TestBootSkipsAutoCompactToAvoidFalseIdle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "sess-boot-compact"
	status.Persist(sid, "working") // a real, live turn
	feedStatusHook(t, "boot", `{"session_id":"`+sid+`","source":"compact"}`)
	if got := status.LiveState(sid); got != "working" {
		t.Errorf("boot(source=compact) reset a live turn to %q — auto-compaction resumes the same turn, so it must stay working", got)
	}
}

// A boot reset is not an answer — it must not queue the 応答あり notification. Otherwise
// resuming a session killed mid-turn would ping the user as though claude had replied.
func TestBootDoesNotQueueAnswerReadyNotification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s-boot", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)
	status.Persist(sid, "working")
	feedStatusHook(t, "boot", `{"session_id":"`+sid+`","source":"resume"}`)
	if events := notice.List(); len(events) != 0 {
		t.Errorf("boot queued %d notification(s), want 0: %+v", len(events), events)
	}
}
