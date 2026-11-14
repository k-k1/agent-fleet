package status

import (
	"os"
	"testing"
)

// The record of the call a prompt stands for is retired only after that prompt's payload is
// published: an answer path that finds no record must find the new payload (and its new token).
// Retiring first left a window with the old payload and no record, where an answer for the old
// prompt passed every check (#4125 review).
func TestPublishPermissionPromptPublishesBeforeRetiring(t *testing.T) {
	isolate(t)
	sid := "s-publish"
	WriteLastToolCall(sid, "Bash · echo q", "echo q", "toolu_q")
	seen := false
	beforeRetireLastTool = func() {
		seen = true
		if d, c, _ := ReadPendingPermissionCommand(sid); d != "Bash · echo q" || c != "echo q" {
			t.Errorf("payload not yet published: %q %q", d, c)
		}
		if _, _, present, err := LastToolRecord(sid); !present || err != nil {
			t.Errorf("record gone before the publish finished: present=%v err=%v", present, err)
		}
	}
	t.Cleanup(func() { beforeRetireLastTool = func() {} })
	PublishPermissionPrompt(sid, "fallback")
	if !seen {
		t.Fatal("seam not reached")
	}
	if _, _, present, _ := LastToolRecord(sid); present {
		t.Error("published record not retired")
	}
	if id, _ := ReadPendingPermissionToolUseID(sid); id != "toolu_q" {
		t.Errorf("tool_use id: %q", id)
	}
}

// A newer call's record that lands while a prompt is being published is not retired with it.
func TestPublishPermissionPromptKeepsANewerRecord(t *testing.T) {
	isolate(t)
	sid := "s-newer"
	WriteLastToolCall(sid, "Bash · echo q", "echo q", "toolu_q")
	beforeRetireLastTool = func() { WriteLastToolCall(sid, "Bash · echo r", "echo r", "toolu_r") }
	t.Cleanup(func() { beforeRetireLastTool = func() {} })
	PublishPermissionPrompt(sid, "")
	d, c, present, err := LastToolRecord(sid)
	if !present || err != nil || d != "Bash · echo r" || c != "echo r" {
		t.Fatalf("newer record lost: %q %q present=%v err=%v", d, c, present, err)
	}
	// No record at all: the fallback message is published and nothing is retired.
	sid2 := "s-none"
	PublishPermissionPrompt(sid2, "")
	if d, _, _ := ReadPendingPermissionCommand(sid2); d != "Claude needs your permission" {
		t.Errorf("fallback: %q", d)
	}
}

// LastToolRecord tells an absent record from one it could not read.
func TestLastToolRecordReportsReadFailures(t *testing.T) {
	isolate(t)
	if _, _, present, err := LastToolRecord("s-absent"); present || err != nil {
		t.Errorf("absent: present=%v err=%v", present, err)
	}
	if err := os.MkdirAll(lastTools.Path("s-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, present, err := LastToolRecord("s-dir"); !present || err == nil {
		t.Errorf("EISDIR: present=%v err=%v", present, err)
	}
	if err := os.WriteFile(lastTools.Path("s-empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, present, err := LastToolRecord("s-empty"); !present || err == nil {
		t.Errorf("empty: present=%v err=%v", present, err)
	}
	WriteLastToolCall("s-ok", "Bash · ls", "ls", "toolu_1")
	if d, c, present, err := LastToolRecord("s-ok"); !present || err != nil || d != "Bash · ls" || c != "ls" {
		t.Errorf("ok: %q %q %v %v", d, c, present, err)
	}
}
