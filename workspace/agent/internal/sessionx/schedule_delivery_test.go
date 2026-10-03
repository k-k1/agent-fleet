package sessionx

// #1560: a scheduled run that answers with the silent sentinel raises no answer-ready
// notification, and a scheduled send that carries its own delivery raises a ledger row for it.

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

func silentScheduleFixture(t *testing.T, name string, silent bool) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, Title: "Nightly"}
	session.WriteMeta(m)
	d := &chatx.ScheduleDelivery{ScheduleID: "sch_1", Slot: "2026-10-03T09:00:00Z", Targets: []string{}, Silent: silent}
	if chatx.AddScheduledInstruction(name, "", "schedule", d, false) == "" {
		t.Fatal("no row raised for a conversation-less scheduled run")
	}
	return session.UUID(m.Dir, m.Name)
}

// The turn's prose runs every message together; the newest message alone is the sentinel. The
// hook reads it before the turn end clears it, and the notification is not raised.
func TestSilentScheduledTurnRaisesNoNotification(t *testing.T) {
	sid := silentScheduleFixture(t, "s-silent", true)
	status.Persist(sid, "working")
	status.AppendPendingText(sid, "Checking the queue…")
	status.AppendPendingText(sid, "[SILENT]")
	status.AppendLiveText(sid, status.LiveFlush{Prompt: "p", Turn: "t", Msg: "m2", Index: 0, Final: true, Delta: "[SILENT]"})
	RunSessionStatusHook([]string{"idle", sid})
	if events := notice.List(); len(events) != 0 {
		t.Fatalf("a silent scheduled turn raised %+v", events)
	}
}

// Without the sentinel enabled, the same answer is an ordinary answer-ready notification.
func TestSentinelNotEnabledStillNotifies(t *testing.T) {
	sid := silentScheduleFixture(t, "s-loud", false)
	status.Persist(sid, "working")
	status.AppendPendingText(sid, "[SILENT]")
	RunSessionStatusHook([]string{"idle", sid})
	if events := notice.List(); len(events) != 1 || events[0].Kind != chatx.ReportKindAnswerReady {
		t.Fatalf("events = %+v, want one answer-ready", events)
	}
}

// A failed turn is never silent, whatever it printed.
func TestFailedScheduledTurnStillNotifies(t *testing.T) {
	sid := silentScheduleFixture(t, "s-failed", true)
	recordSessionNotification(sid, "working", "failed", "[SILENT]", "[SILENT]")
	if events := notice.List(); len(events) != 1 {
		t.Fatalf("events = %+v, want the failure notified", events)
	}
}

// scheduleDeliveryOf completes the delivery with the run's identity, and only a schedule source
// that names a run gets one.
func TestScheduleDeliveryOf(t *testing.T) {
	d := &chatx.ScheduleDelivery{Targets: []string{"slack"}, Silent: true}
	got := scheduleDeliveryOf("schedule", "sch_1", "2026-10-03T09:00:00Z", d)
	if got == nil || got.ScheduleID != "sch_1" || got.Slot != "2026-10-03T09:00:00Z" || !got.Silent || d.ScheduleID != "" {
		t.Fatalf("got %+v (input %+v)", got, d)
	}
	if scheduleDeliveryOf("operator", "sch_1", "2026-10-03T09:00:00Z", d) != nil {
		t.Fatal("an operator send carried a schedule delivery")
	}
	if scheduleDeliveryOf("schedule", "", "", d) != nil || scheduleDeliveryOf("schedule", "sch_1", "x", nil) != nil {
		t.Fatal("a delivery without a run or without a body")
	}
}
