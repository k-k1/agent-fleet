package sessionx

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/oscnotify"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func newTestNotifier(t *testing.T, kind string) (*TerminalNotifier, *time.Time, *[]notice.Event) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s" + kind, Kind: kind, Title: "ビルド係"}
	session.WriteMeta(m)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	var got []notice.Event
	n := &TerminalNotifier{Name: m.Name, Now: func() time.Time { return now },
		Put: func(e notice.Event) error { got = append(got, e); return nil }}
	return n, &now, &got
}

func TestTerminalNotifierRecordsForKindsWithoutHooks(t *testing.T) {
	for _, kind := range []string{session.KindShell, session.KindKiro, session.KindAgy, session.KindCopilot, session.KindCursor} {
		n, _, got := newTestNotifier(t, kind)
		n.Notify(oscnotify.Notification{Proto: "osc777", Title: "Build", Body: "done"})
		if len(*got) != 1 {
			t.Fatalf("%s: %d events, want 1", kind, len(*got))
		}
		e := (*got)[0]
		if e.Kind != TerminalNotificationKind || e.SessionKind != kind || e.DisplayName != "ビルド係" ||
			e.Payload["title"] != "Build" || e.Payload["body"] != "done" || e.Payload["proto"] != "osc777" {
			t.Fatalf("%s: event = %+v", kind, e)
		}
	}
}

// claude / codex / opencode already report the same moments through their hooks.
func TestTerminalNotifierSkipsHookDrivenKinds(t *testing.T) {
	for _, kind := range []string{session.KindClaude, session.KindCodex, session.KindOpencode} {
		n, _, got := newTestNotifier(t, kind)
		n.Notify(oscnotify.Notification{Proto: "osc9", Body: "x"})
		if len(*got) != 0 {
			t.Fatalf("%s: %d events, want 0", kind, len(*got))
		}
	}
}

func TestTerminalNotifierUnknownSessionIsDropped(t *testing.T) {
	n, _, got := newTestNotifier(t, session.KindShell)
	n.Name = "missing"
	n.Notify(oscnotify.Notification{Proto: "osc9", Body: "x"})
	if len(*got) != 0 {
		t.Fatalf("%d events for a session without meta", len(*got))
	}
}

func TestTerminalNotifierThrottles(t *testing.T) {
	n, now, got := newTestNotifier(t, session.KindShell)
	same := oscnotify.Notification{Proto: "osc9", Body: "same"}
	n.Notify(same)
	*now = now.Add(terminalNotifyRepeatWindow - time.Second)
	n.Notify(same)
	if len(*got) != 1 {
		t.Fatalf("repeat inside the window: %d events, want 1", len(*got))
	}
	*now = now.Add(2 * time.Second)
	n.Notify(same)
	if len(*got) != 2 {
		t.Fatalf("repeat after the window: %d events, want 2", len(*got))
	}

	// A burst of distinct messages stops at the budget, and the budget refills.
	*now = now.Add(terminalNotifyBurstWindow)
	for i := 0; i < terminalNotifyBurst+3; i++ {
		n.Notify(oscnotify.Notification{Proto: "osc9", Body: string(rune('a' + i))})
	}
	if len(*got) != 2+terminalNotifyBurst {
		t.Fatalf("burst: %d events, want %d", len(*got), 2+terminalNotifyBurst)
	}
	*now = now.Add(terminalNotifyBurstWindow)
	n.Notify(oscnotify.Notification{Proto: "osc9", Body: "later"})
	if len(*got) != 3+terminalNotifyBurst {
		t.Fatalf("after the window: %d events, want %d", len(*got), 3+terminalNotifyBurst)
	}
}
