package sessionx

import (
	"fmt"
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/oscnotify"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// feedPushHook runs the PushNotification hook subcommand once with stdinJSON on stdin.
func feedPushHook(t *testing.T, stdinJSON string) {
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
	RunPushNotificationHook(nil)
	_ = r.Close()
}

// newPushSession writes a claude session's meta and returns it with its hook sid.
func newPushSession(t *testing.T) (session.Meta, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s-push", Dir: t.TempDir(), Kind: session.KindClaude, Title: "通知係"}
	session.WriteMeta(m)
	return m, session.UUID(m.Dir, m.Name)
}

// pushHookJSON is the PostToolUse(PushNotification) stdin shape read from the claude
// 2.1.286 binary: tool_input is the call's input, tool_response the tool's output.
func pushHookJSON(sid, toolUseID, message, disabledReason string) string {
	resp := `{"message":` + fmt.Sprintf("%q", message) + `,"pushSent":false,"localSent":true`
	if disabledReason != "" {
		resp += `,"disabledReason":"` + disabledReason + `"`
	}
	resp += `,"sentAt":"2026-10-01T00:00:00.000Z"}`
	return `{"session_id":"` + sid + `","hook_event_name":"PostToolUse","tool_name":"PushNotification",` +
		`"tool_use_id":"` + toolUseID + `","tool_input":{"message":` + fmt.Sprintf("%q", message) + `,"status":"proactive"},` +
		`"tool_response":` + resp + `}`
}

func TestPushNotificationHookPutsTerminalNotification(t *testing.T) {
	m, sid := newPushSession(t)
	status.Persist(sid, "working")

	feedPushHook(t, pushHookJSON(sid, "toolu_1", "Build finished:\n42 tests green", "no_transport"))

	events := notice.List()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	e := events[0]
	if e.Kind != TerminalNotificationKind || e.SessionName != m.Name || e.SessionKind != session.KindClaude ||
		e.DisplayName != "通知係" || e.Payload["body"] != "Build finished: 42 tests green" || e.Payload["proto"] != pushNotificationProto {
		t.Fatalf("event = %+v", e)
	}
	if _, has := e.Payload["title"]; has {
		t.Fatalf("PushNotification has no title, event carries one: %+v", e)
	}
	// A notification, not a status: the session stays working.
	if cur, _ := status.Read(sid); cur.State != "working" {
		t.Fatalf("status = %q after the push hook, want working", cur.State)
	}
}

// claude returns before raising anything for these two reasons, so there is nothing to
// stand in for.
func TestPushNotificationHookSkipsWhenClaudeDidNotSend(t *testing.T) {
	for _, reason := range []string{"user_present", "config_off"} {
		t.Run(reason, func(t *testing.T) {
			_, sid := newPushSession(t)
			feedPushHook(t, pushHookJSON(sid, "toolu_1", "hello", reason))
			if events := notice.List(); len(events) != 0 {
				t.Fatalf("%s: got %d events, want 0: %+v", reason, len(events), events)
			}
		})
	}
}

// Fields missing from the payload keep the hook inert instead of putting an empty event.
func TestPushNotificationHookInertWithoutMessage(t *testing.T) {
	_, sid := newPushSession(t)
	for _, in := range []string{
		`{"session_id":"` + sid + `","tool_name":"PushNotification"}`,
		`{"session_id":"` + sid + `","tool_name":"PushNotification","tool_input":{"message":"  \u001b "}}`,
		`not json`,
	} {
		feedPushHook(t, in)
	}
	if events := notice.List(); len(events) != 0 {
		t.Fatalf("got %d events, want 0: %+v", len(events), events)
	}
	// An unknown tool_response shape (no disabledReason) still forwards the message.
	feedPushHook(t, `{"session_id":"`+sid+`","tool_use_id":"toolu_2","tool_input":{"message":"hi"}}`)
	if events := notice.List(); len(events) != 1 {
		t.Fatalf("got %d events without tool_response, want 1", len(events))
	}
}

// One tool call is one notification: the hook firing twice for the same tool_use_id,
// and the same text arriving over OSC (claude's own local notification, when its
// preferredNotifChannel emits one), add nothing.
func TestPushNotificationNotDeliveredTwice(t *testing.T) {
	m, sid := newPushSession(t)
	in := pushHookJSON(sid, "toolu_1", "Deploy done", "no_transport")
	feedPushHook(t, in)
	feedPushHook(t, in)
	(&TerminalNotifier{Name: m.Name}).Notify(oscnotify.Notification{Proto: "osc9", Body: "Deploy done"})

	if events := notice.List(); len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	// A second call with the same text is a second notification.
	feedPushHook(t, pushHookJSON(sid, "toolu_2", "Deploy done", "no_transport"))
	if events := notice.List(); len(events) != 2 {
		t.Fatalf("got %d events after a second call, want 2", len(events))
	}
}
