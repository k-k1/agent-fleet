package sessionx

// #1560: a scheduled run that answers with the silent sentinel raises no answer-ready
// notification, and a scheduled send that carries its own delivery raises a ledger row for it.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

func silentScheduleFixture(t *testing.T, name string, silent bool, targets ...string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, Title: "Nightly"}
	session.WriteMeta(m)
	d := &chatx.ScheduleDelivery{ScheduleID: "sch_1", Slot: "2026-10-03T09:00:00Z", Targets: append([]string{}, targets...), Silent: silent}
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
	status.WriteLivePrompt(sid, "p")
	status.AppendLiveText(sid, status.LiveFlush{Prompt: "p", Turn: "t", Msg: "m2", Index: 0, Final: true, Delta: "[SILENT]"})
	RunSessionStatusHook([]string{"idle", sid})
	if events := notice.List(); len(events) != 0 {
		t.Fatalf("a silent scheduled turn raised %+v", events)
	}
}

// Review round 1, finding 2: the newest streamed message is a candidate only when it is whole
// and belongs to the turn now ending. A sentinel that is only the opening of a longer answer, or
// a late flush of the previous turn, must not hide this turn's answer.
func TestUncertainStreamedSentinelDoesNotSuppress(t *testing.T) {
	for name, fl := range map[string]struct {
		current string
		flush   status.LiveFlush
	}{
		"not final yet":      {"p", status.LiveFlush{Prompt: "p", Turn: "t", Msg: "m", Index: 0, Final: false, Delta: "[SILENT]"}},
		"an earlier turn":    {"new", status.LiveFlush{Prompt: "old", Turn: "t0", Msg: "m0", Index: 0, Final: true, Delta: "[SILENT]"}},
		"no prompt recorded": {"", status.LiveFlush{Prompt: "p", Turn: "t", Msg: "m", Index: 0, Final: true, Delta: "[SILENT]"}},
	} {
		t.Run(name, func(t *testing.T) {
			sid := silentScheduleFixture(t, "s-uncertain", true)
			status.Persist(sid, "working")
			if fl.current != "" {
				status.WriteLivePrompt(sid, fl.current)
			}
			status.AppendPendingText(sid, "[SILENT] is an example token. Disk failed.")
			status.AppendLiveText(sid, fl.flush)
			RunSessionStatusHook([]string{"idle", sid})
			if events := notice.List(); len(events) != 1 {
				t.Fatalf("events = %+v, want the answer notified", events)
			}
		})
	}
}

// Review round 1, finding 3: a silent run still being sent is not what another turn finished, so
// that turn's [SILENT] answer is notified as usual.
func TestUnstartedSilentRunDoesNotSuppressAnotherTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s-sending", Dir: t.TempDir(), Kind: session.KindCodex}
	session.WriteMeta(m)
	d := &chatx.ScheduleDelivery{ScheduleID: "sch_1", Slot: "2026-10-03T09:00:00Z", Silent: true}
	if chatx.AddScheduledInstruction(m.Name, "", "schedule", d, true) == "" {
		t.Fatal("no row")
	}
	RecordSessionNotification(session.UUID(m.Dir, m.Name), "working", "idle", "[SILENT]")
	if events := notice.List(); len(events) != 1 {
		t.Fatalf("events = %+v, want the other turn's answer notified", events)
	}
}

// Review round 1, finding 1, end to end: a run whose schedule named only the notification center
// puts nothing in the chat bridge's queue — not the hook's answer-ready, not the report's copy —
// and its result reaches the notification center through the sink.
func TestNamedTargetsBroadcastNothing(t *testing.T) {
	sid := silentScheduleFixture(t, "s-private", false, chatx.DeliverNotifications)
	// Installed after the fixture's HOME, so its cleanup (LIFO) stops it before HOME is restored.
	stop := chatx.InstallReconcilerForTest(20 * time.Millisecond)
	t.Cleanup(stop)
	time.Sleep(1100 * time.Millisecond) // the turn ends in a later second than the row's cursor
	status.Persist(sid, "working")
	status.AppendPendingText(sid, "private result")
	RunSessionStatusHook([]string{"idle", sid})
	deadline := time.Now().Add(10 * time.Second)
	for chatx.SessionReportPending("s-private") {
		if time.Now().After(deadline) {
			t.Fatal("the run never settled")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ents, _ := os.ReadDir(filepath.Join(paths.AgentStateDir(), "bridge-queue")); len(ents) != 0 {
		t.Fatalf("%d bridge message(s) queued for a notifications-only schedule", len(ents))
	}
	events := notice.List()
	if len(events) != 1 || events[0].Kind != chatx.NoticeKindScheduleResult || events[0].Payload["excerpt"] != "private result" {
		t.Fatalf("events = %+v, want the schedule result alone", events)
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
