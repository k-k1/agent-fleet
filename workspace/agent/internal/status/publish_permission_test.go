package status

import (
	"os"
	"testing"
)

// Publishing a prompt never removes or rewrites the last-tool record: it is the answer path's
// evidence of a call whose dialog is drawn ahead of its payload, and any moment without it while
// the old payload is pending lets an answer for that payload through.
func TestPublishPermissionPromptLeavesTheRecord(t *testing.T) {
	isolate(t)
	sid := "s-publish"
	WriteLastToolCall(sid, "Bash · echo q", "echo q", "toolu_q")
	PublishPermissionPrompt(sid, "fallback")
	if d, c, _ := ReadPendingPermissionCommand(sid); d != "Bash · echo q" || c != "echo q" {
		t.Fatalf("not published: %q %q", d, c)
	}
	if id, _ := ReadPendingPermissionToolUseID(sid); id != "toolu_q" {
		t.Errorf("tool_use id: %q", id)
	}
	if d, _, id, present, err := LastToolRecord(sid); !present || err != nil || d != "Bash · echo q" || id != "toolu_q" {
		t.Errorf("record changed by the publish: %q %q present=%v err=%v", d, id, present, err)
	}
	// A later prompt from a tool with no permtool hook does not inherit the published call.
	PublishPermissionPrompt(sid, "Claude needs your permission to use WebFetch")
	if d, c, _ := ReadPendingPermissionCommand(sid); d != "Claude needs your permission to use WebFetch" || c != "" {
		t.Errorf("published record reused: %q %q", d, c)
	}
	// A new call's record is published again.
	WriteLastToolCall(sid, "Bash · echo q", "echo q", "toolu_q2")
	PublishPermissionPrompt(sid, "")
	if id, _ := ReadPendingPermissionToolUseID(sid); id != "toolu_q2" {
		t.Errorf("same text, new call: %q", id)
	}
	// No record at all: the fallback message.
	PublishPermissionPrompt("s-none", "")
	if d, _, _ := ReadPendingPermissionCommand("s-none"); d != "Claude needs your permission" {
		t.Errorf("fallback: %q", d)
	}
}

// A payload that could not be written does not mark its record as published, so the next
// prompt's hook publishes it again instead of falling back to a bare message.
func TestPublishPermissionPromptFailedWrite(t *testing.T) {
	isolate(t)
	sid := "s-fail"
	WriteLastToolCall(sid, "Bash · echo q", "echo q", "toolu_q")
	if err := os.MkdirAll(pendingPerms.Path(sid), 0o700); err != nil { // rename onto a directory fails
		t.Fatal(err)
	}
	PublishPermissionPrompt(sid, "")
	if _, ok := publishedTools.Read(sid); ok {
		t.Error("marked as published after a failed write")
	}
	if _, _, _, present, err := LastToolRecord(sid); !present || err != nil {
		t.Errorf("record lost: present=%v err=%v", present, err)
	}
	if err := os.Remove(pendingPerms.Path(sid)); err != nil {
		t.Fatal(err)
	}
	PublishPermissionPrompt(sid, "")
	if id, _ := ReadPendingPermissionToolUseID(sid); id != "toolu_q" {
		t.Errorf("retry: %q", id)
	}
}

// LastToolRecord tells an absent record from one it could not read.
func TestLastToolRecordReportsReadFailures(t *testing.T) {
	isolate(t)
	if _, _, _, present, err := LastToolRecord("s-absent"); present || err != nil {
		t.Errorf("absent: present=%v err=%v", present, err)
	}
	if err := os.MkdirAll(lastTools.Path("s-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, present, err := LastToolRecord("s-dir"); !present || err == nil {
		t.Errorf("EISDIR: present=%v err=%v", present, err)
	}
	if err := os.WriteFile(lastTools.Path("s-empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, present, err := LastToolRecord("s-empty"); !present || err == nil {
		t.Errorf("empty: present=%v err=%v", present, err)
	}
	WriteLastToolCall("s-ok", "Bash · ls", "ls", "toolu_1")
	if d, c, id, present, err := LastToolRecord("s-ok"); !present || err != nil || d != "Bash · ls" || c != "ls" || id != "toolu_1" {
		t.Errorf("ok: %q %q %q %v %v", d, c, id, present, err)
	}
}
