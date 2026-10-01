package sessionx

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/oscnotify"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// pushNotificationProto marks a terminal-notification that came from claude's
// PushNotification tool through its PostToolUse hook rather than from an OSC sequence.
const pushNotificationProto = "claude-push"

// pushNotSent reports whether claude declined to raise the notification at all. Read
// from the 2.1.286 binary: "config_off" (Remote Control is up but "Push when Claude
// decides" is off) and "user_present" (the user is active in that terminal) return
// before anything is sent. "no_transport" only means no mobile push — the local
// notification still went out, which is the one we stand in for — so it is forwarded.
func pushNotSent(reason string) bool {
	return reason == "config_off" || reason == "user_present"
}

// recordPushNotification puts claude's PushNotification message in the outbox as a
// terminal-notification. It cannot double up with the OSC route: the same tool's OSC
// sequence (when claude's preferredNotifChannel emits one at all) is dropped for claude
// by terminalNotifyHasHooks. A hook that fires twice for one call is absorbed by keying
// on tool_use_id. Missing fields keep it inert: no message, no event.
func recordPushNotification(sid string, h hookInput) {
	if pushNotSent(h.pushDisabledReason) {
		return
	}
	body := oscnotify.CleanBody(h.pushMessage)
	if body == "" {
		return
	}
	for _, m := range session.ListMetas() {
		if session.UUID(m.Dir, m.Name) != sid {
			continue
		}
		ev := notice.New(TerminalNotificationKind, m.Name, m.Kind, session.Display(m))
		ev.Payload["proto"] = pushNotificationProto
		ev.Payload["body"] = body
		if h.toolUseID != "" {
			_ = notice.PutOnce("push-notification:"+m.Name+":"+h.toolUseID, ev)
		} else {
			_ = notice.Put(ev)
		}
		return
	}
}
