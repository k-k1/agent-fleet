package mcpreg

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// NoticeKindElicitation is the outbox notification that a call is held for a person's
// confirmation. Adding or renaming it means updating the Console's notification tables
// (console/src/features/notifications/{prefs,wording}.ts) and McpElicitHost at the same time.
const NoticeKindElicitation = "mcp-elicitation"

// noticeHeld files the notification for a held call; a test replaces it.
var noticeHeld = putHeldNotice

// putHeldNotice says that a call waits for a person. The payload is the held call's id alone:
// the outbox is writable by every agent, so the Console shows what GET /mcp-elicitations lists
// for that id and nothing the notification says. A call with no live session it can be
// attributed to (a shared daemon's, or a name on the shelf) is a workspace notification, so it
// never points at a session the Console's list does not offer.
func putHeldNotice(p PendingElicitation) {
	ev := notice.New(NoticeKindElicitation, "", "", "")
	ev.TargetType = "workspace"
	if session.ValidName(p.Session) {
		if m, ok := session.ReadMeta(p.Session); ok && m.Name == p.Session && !m.Archived {
			ev.TargetType, ev.SessionName, ev.SessionKind = "session", m.Name, m.Kind
		}
	}
	ev.Payload["elicitationId"] = p.ID
	_ = notice.Put(ev)
}
