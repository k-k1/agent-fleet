package status

import (
	"os"
	"reflect"
	"strconv"
	"sync"
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
	if raw, _ := lastTools.Read(sid); raw != joinCall("Bash · echo q", "echo q", "toolu_q") {
		t.Errorf("record changed by the publish: %q", raw)
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
	if _, ok := lastTools.Read(sid); !ok {
		t.Error("record lost")
	}
	if err := os.Remove(pendingPerms.Path(sid)); err != nil {
		t.Fatal(err)
	}
	PublishPermissionPrompt(sid, "")
	if id, _ := ReadPendingPermissionToolUseID(sid); id != "toolu_q" {
		t.Errorf("retry: %q", id)
	}
}

// InFlightCalls lists the started calls that have not ended, tells a missing log from an
// unreadable one, and keeps an unknown id as one that never ends.
func TestInFlightCalls(t *testing.T) {
	isolate(t)
	sid := "s-calls"
	if got, err := InFlightCalls(sid); got != nil || err != nil {
		t.Fatalf("no log: %v %v", got, err)
	}
	NoteCallStart(sid, "toolu_x")
	NoteCallStart(sid, "toolu_p")
	NoteCallEnd(sid, "toolu_x")
	NoteCallEnd(sid, "toolu_read") // an untracked call's end
	NoteCallStart(sid, "")
	NoteCallEnd(sid, "?")
	if got, err := InFlightCalls(sid); err != nil || !reflect.DeepEqual(got, []string{"toolu_p", "?"}) {
		t.Fatalf("in flight: %v %v", got, err)
	}
	ResetCalls(sid)
	if got, err := InFlightCalls(sid); got != nil || err != nil {
		t.Fatalf("after reset: %v %v", got, err)
	}
	if err := os.MkdirAll(callLogs.Path("s-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := InFlightCalls("s-dir"); err == nil {
		t.Error("EISDIR read as no calls")
	}
}

// Hooks run as separate processes and may fire together (parallel siblings); no start is lost.
func TestNoteCallStartConcurrent(t *testing.T) {
	isolate(t)
	sid := "s-par"
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			NoteCallStart(sid, "toolu_"+strconv.Itoa(i))
		}(i)
	}
	wg.Wait()
	if got, err := InFlightCalls(sid); err != nil || len(got) != 50 {
		t.Fatalf("%d of 50 starts kept, err %v", len(got), err)
	}
}
