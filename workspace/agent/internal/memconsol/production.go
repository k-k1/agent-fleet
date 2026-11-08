package memconsol

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// Production is the Agent's own wiring: the member's switch (ui-prefs, with agent memory on) and
// the session-activity signal. An unwired consolidator never runs, so main must Configure it.
func Production() Deps {
	return Deps{Enabled: uiprefs.MemoryConsolidationEnabled, Activity: sessionActivity, Notice: raiseNotice}
}

// NoticeKind is the Console notification kind of the daily "proposals are waiting" notice.
const NoticeKind = "memory-consolidation"

// raiseNotice puts the one notification in the outbox, the same channel as memory-sync. It is about
// the workspace, and the payload is the count only: no memory name, body or evidence ever enters a
// notification or a log line. The Console row opens the proposals view; nothing is decided from
// the notification itself.
func raiseNotice(waiting int) error {
	ev := notice.New(NoticeKind, "", "", "")
	ev.TargetType = "workspace"
	ev.Payload["pending"] = waiting
	return notice.Put(ev)
}

// sessionActivity reads the same status store the snapshot trigger and the session list read: a
// session whose state is working and whose process is alive makes the workspace busy, and the
// newest write of any session's state (a turn started or ended) is the last activity. A stopped
// session stuck on "working" does not count as busy, so a false busy cannot starve the run.
func sessionActivity() (busy bool, last time.Time) {
	for _, m := range session.ListMetas() {
		sid := session.UUID(m.Dir, m.Name)
		if at, ok := status.StateAt(sid); ok && at.After(last) {
			last = at
		}
		if !busy && status.LiveState(sid) == "working" && sessionx.SessionAlive(m) {
			busy = true
		}
	}
	return busy, last
}
