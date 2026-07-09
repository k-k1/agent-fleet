package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// A {prompt} while an AskUserQuestion is pending would be typed into the modal, which
// ignores the text and lets the Enter confirm the FIRST option (docs/dev/92). The
// input handler must therefore see the session as question-pending and reject it.
func TestQuestionPendingGatesPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "auq_guard"
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude})
	sid := session.UUID(dir, name)

	if questionPending(name) {
		t.Fatal("no status recorded yet — must read as not-pending")
	}
	status.Persist(sid, "question")
	if !questionPending(name) {
		t.Fatal("status=question must gate the {prompt} path")
	}
	// Answered → PostToolUse flips to working; the gate must lift.
	status.Persist(sid, "working")
	if questionPending(name) {
		t.Fatal("status=working must not gate prompts")
	}
	if questionPending("no_such_session") {
		t.Fatal("unknown session must read as not-pending")
	}
}
