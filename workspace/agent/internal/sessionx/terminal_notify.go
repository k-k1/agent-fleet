package sessionx

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/oscnotify"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TerminalNotificationKind is the notice kind for a desktop notification the
// session's program emitted as an OSC 9 / 99 / 777 sequence.
const TerminalNotificationKind = "terminal-notification"

const (
	// terminalNotifyRepeatWindow drops a notification identical to the previous one
	// within this window: a TUI that redraws can emit the same sequence again.
	terminalNotifyRepeatWindow = 30 * time.Second
	// terminalNotifyBurst notifications per terminalNotifyBurstWindow at most, so a
	// program printing sequences in a loop cannot flood the notification center.
	terminalNotifyBurst       = 5
	terminalNotifyBurstWindow = time.Minute
)

// terminalNotifyHasHooks lists the kinds whose attention events (answer-ready,
// question, permission) already reach the outbox through structured hooks
// (RunSessionStatusHook). An OSC notification from one of them reports the same
// moment a second time, so it is dropped — the same rule cmux applies to panes
// with a hook-integrated agent.
func terminalNotifyHasHooks(kind string) bool {
	switch kind {
	case session.KindClaude, session.KindCodex, session.KindOpencode:
		return true
	}
	return false
}

// TerminalNotifier turns the notifications found in one session's pane output
// into outbox events. One lives in each record-terminal process, which is the
// only reader of that pane's raw bytes, so its throttle state needs no lock.
type TerminalNotifier struct {
	Name string
	// Now and Put are seams for tests; nil means time.Now and notice.Put.
	Now func() time.Time
	Put func(notice.Event) error

	last     oscnotify.Notification
	lastAt   time.Time
	recentAt []time.Time
}

// Notify records n for the session unless the kind is hook-driven, it repeats the
// previous notification, or the burst budget is spent.
func (t *TerminalNotifier) Notify(n oscnotify.Notification) {
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	at := now()
	if n == t.last && at.Sub(t.lastAt) < terminalNotifyRepeatWindow {
		return
	}
	kept := t.recentAt[:0]
	for _, r := range t.recentAt {
		if at.Sub(r) < terminalNotifyBurstWindow {
			kept = append(kept, r)
		}
	}
	t.recentAt = kept
	if len(t.recentAt) >= terminalNotifyBurst {
		return
	}
	// Read per notification rather than once at start: the meta is the only place
	// the display name lives, and a rename must show up on the next notification.
	m, ok := session.ReadMeta(t.Name)
	if !ok || terminalNotifyHasHooks(m.Kind) {
		return
	}
	t.last, t.lastAt = n, at
	t.recentAt = append(t.recentAt, at)
	ev := notice.New(TerminalNotificationKind, m.Name, m.Kind, session.Display(m))
	ev.Payload["proto"] = n.Proto
	if n.Title != "" {
		ev.Payload["title"] = n.Title
	}
	if n.Body != "" {
		ev.Payload["body"] = n.Body
	}
	put := notice.Put
	if t.Put != nil {
		put = t.Put
	}
	_ = put(ev)
}
