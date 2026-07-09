package main

import (
	"os"
	"testing"

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
