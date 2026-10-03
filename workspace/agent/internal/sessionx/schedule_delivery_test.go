package sessionx

// #1560: what a turn that finishes a scheduled run raises, on the Terminal hook route and the
// Managed driver route, and the ledger row a scheduled send raises.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// fakeTranscripts makes chatx read the given transcripts (by session name) instead of the real
// agents' stores, for the rest of the test.
func fakeTranscripts(t *testing.T) func(name string, turns ...transcript.Turn) {
	t.Helper()
	var mu sync.Mutex
	byName := map[string][]transcript.Turn{}
	d := chatxStubDeps()
	d.SessionTurns = func(m session.Meta) ([]transcript.Turn, bool) {
		mu.Lock()
		defer mu.Unlock()
		turns, ok := byName[m.Name]
		return append([]transcript.Turn(nil), turns...), ok
	}
	chatx.Configure(d)
	t.Cleanup(func() { chatx.Configure(chatxStubDeps()) })
	return func(name string, turns ...transcript.Turn) {
		mu.Lock()
		defer mu.Unlock()
		byName[name] = append(byName[name], turns...)
	}
}

func turn(role, text string, at time.Time) transcript.Turn {
	return transcript.Turn{Role: role, Text: text, TS: at.UTC().Format(time.RFC3339Nano)}
}

const runPrompt = "check the nightly jobs"

// scheduleFixture raises a scheduled run's row (a minute ago) on a new session of kind and
// returns its sid. targets empty = reporting off.
func scheduleFixture(t *testing.T, name, kind string, silent bool, targets ...string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: kind, Title: "Nightly"}
	if kind != session.KindClaude {
		m.Driver = session.DriverManaged
	}
	session.WriteMeta(m)
	d := &chatx.ScheduleDelivery{ScheduleID: "sch_1", Slot: "2026-10-03T09:00:00Z", Targets: append([]string{}, targets...),
		Silent: silent, PromptSum: chatx.PromptSum(runPrompt)}
	if id := chatx.AddScheduledInstruction(name, "", "schedule", d, false); id == "" {
		t.Fatal("no row raised for a conversation-less scheduled run")
	}
	return session.UUID(m.Dir, m.Name)
}

// answered puts the run's prompt and answers in the session's transcript.
func answered(put func(string, ...transcript.Turn), name string, answers ...string) {
	at := time.Now().Add(-50 * time.Second)
	put(name, turn("user", runPrompt, at))
	for i, a := range answers {
		put(name, turn("assistant", a, at.Add(time.Duration(i+1)*time.Second)))
	}
}

// A Terminal run whose final message is the sentinel raises nothing, even though the turn's
// streamed prose ran its messages together.
func TestSilentScheduledTurnRaisesNoNotification(t *testing.T) {
	put := fakeTranscripts(t)
	sid := scheduleFixture(t, "s-silent", session.KindClaude, true)
	answered(put, "s-silent", "Checking the queue…", "[SILENT]")
	status.Persist(sid, "working")
	status.AppendPendingText(sid, "Checking the queue…")
	status.AppendPendingText(sid, "[SILENT]")
	RunSessionStatusHook([]string{"idle", sid})
	if events := notice.List(); len(events) != 0 {
		t.Fatalf("a silent scheduled turn raised %+v", events)
	}
}

// Review round 1, finding 2: silence is the run's own final message in the transcript, never the
// streamed text. A streamed [SILENT] the answer does not end with hides nothing.
func TestStreamedSentinelDoesNotSuppress(t *testing.T) {
	put := fakeTranscripts(t)
	sid := scheduleFixture(t, "s-streamed", session.KindClaude, true)
	answered(put, "s-streamed", "[SILENT] is an example token. Disk failed.")
	status.Persist(sid, "working")
	status.WriteLivePrompt(sid, "p")
	status.AppendLiveText(sid, status.LiveFlush{Prompt: "p", Turn: "t", Msg: "m", Index: 0, Final: true, Delta: "[SILENT]"})
	status.AppendPendingText(sid, "[SILENT]")
	RunSessionStatusHook([]string{"idle", sid})
	if events := notice.List(); len(events) != 1 {
		t.Fatalf("events = %+v, want the answer notified", events)
	}
}

// Without the sentinel enabled, the same answer is an ordinary answer-ready notification.
func TestSentinelNotEnabledStillNotifies(t *testing.T) {
	put := fakeTranscripts(t)
	sid := scheduleFixture(t, "s-loud", session.KindClaude, false)
	answered(put, "s-loud", "[SILENT]")
	status.Persist(sid, "working")
	status.AppendPendingText(sid, "[SILENT]")
	RunSessionStatusHook([]string{"idle", sid})
	if events := notice.List(); len(events) != 1 || events[0].Kind != chatx.ReportKindAnswerReady {
		t.Fatalf("events = %+v, want one answer-ready", events)
	}
}

// A failed turn is never silent, whatever it printed.
func TestFailedScheduledTurnStillNotifies(t *testing.T) {
	put := fakeTranscripts(t)
	sid := scheduleFixture(t, "s-failed", session.KindClaude, true)
	answered(put, "s-failed", "[SILENT]")
	RecordSessionNotification(sid, "working", "failed", "[SILENT]")
	if events := notice.List(); len(events) != 1 {
		t.Fatalf("events = %+v, want the failure notified", events)
	}
}

// Review round 1, finding 3: a silent run still being sent is not what another turn finished, so
// that turn's [SILENT] answer is notified as usual.
func TestUnstartedSilentRunDoesNotSuppressAnotherTurn(t *testing.T) {
	put := fakeTranscripts(t)
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s-sending", Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
	session.WriteMeta(m)
	d := &chatx.ScheduleDelivery{ScheduleID: "sch_1", Slot: "2026-10-03T09:00:00Z", Silent: true, PromptSum: chatx.PromptSum(runPrompt)}
	if chatx.AddScheduledInstruction(m.Name, "", "schedule", d, true) == "" {
		t.Fatal("no row")
	}
	put(m.Name, turn("user", "something else", time.Now().Add(-time.Second)), turn("assistant", "[SILENT]", time.Now()))
	RecordSessionNotification(session.UUID(m.Dir, m.Name), "working", "idle", "[SILENT]")
	if events := notice.List(); len(events) != 1 {
		t.Fatalf("events = %+v, want the other turn's answer notified", events)
	}
}

// waitSettled waits until the session owes no report.
func waitSettled(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for chatx.SessionReportPending(name) {
		if time.Now().After(deadline) {
			t.Fatal("the run never settled")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func bridgeQueueLen() int {
	ents, _ := os.ReadDir(filepath.Join(paths.AgentStateDir(), "bridge-queue"))
	return len(ents)
}

// Review round 1, finding 1, end to end: a run whose schedule named only the notification center
// puts nothing in the chat bridge's queue — not the hook's answer-ready, not the report's copy —
// and its result reaches the notification center through the sink.
func TestNamedTargetsBroadcastNothing(t *testing.T) {
	put := fakeTranscripts(t)
	sid := scheduleFixture(t, "s-private", session.KindClaude, false, chatx.DeliverNotifications)
	// Installed after the fixture's HOME, so its cleanup (LIFO) stops it before HOME is restored.
	stop := chatx.InstallReconcilerForTest(20 * time.Millisecond)
	t.Cleanup(stop)
	time.Sleep(1100 * time.Millisecond) // the turn ends in a later second than the row's cursor
	answered(put, "s-private", "private result")
	status.Persist(sid, "working")
	status.AppendPendingText(sid, "private result")
	RunSessionStatusHook([]string{"idle", sid})
	waitSettled(t, "s-private")
	if n := bridgeQueueLen(); n != 0 {
		t.Fatalf("%d bridge message(s) queued for a notifications-only schedule", n)
	}
	events := notice.List()
	if len(events) != 1 || events[0].Kind != chatx.NoticeKindScheduleResult || events[0].Payload["excerpt"] != "private result" {
		t.Fatalf("events = %+v, want the schedule result alone", events)
	}
}

// Review round 2, finding 8: a Managed driver ends a turn with no text at all (MarkTurnEnd hands
// the notifier an empty excerpt). The run's answer still reaches its target, read from the
// transcript, and a sentinel answer is still silent.
func TestManagedScheduledRunUsesItsTranscript(t *testing.T) {
	for name, tc := range map[string]struct {
		silent  bool
		answer  string
		targets []string
	}{
		"answer delivered": {false, "2 jobs failed", []string{chatx.DeliverNotifications}},
		"sentinel silent":  {true, "[SILENT]", []string{chatx.DeliverNotifications}},
		"report off quiet": {true, "[SILENT]", nil},
	} {
		t.Run(name, func(t *testing.T) {
			put := fakeTranscripts(t)
			sid := scheduleFixture(t, "s-managed", session.KindCodex, tc.silent, tc.targets...)
			stop := chatx.InstallReconcilerForTest(20 * time.Millisecond)
			t.Cleanup(stop)
			done := make(chan struct{}, 4)
			agents.SetStateNotifier(func(sid, previous, state, excerpt string) {
				RecordSessionNotification(sid, previous, state, excerpt)
				done <- struct{}{}
			})
			t.Cleanup(func() { agents.SetStateNotifier(nil) })
			agents.SetTurnEndRecorder(RecordTurnOutcome)
			t.Cleanup(func() { agents.SetTurnEndRecorder(nil) })
			time.Sleep(1100 * time.Millisecond)
			answered(put, "s-managed", tc.answer)
			agents.MarkTurnStart(sid)
			agents.MarkTurnEndErr(sid, agents.TurnCompleted, "")
			<-done
			waitSettled(t, "s-managed")
			events := notice.List()
			if tc.silent {
				if len(events) != 0 {
					t.Fatalf("a silent Managed run raised %+v", events)
				}
				return
			}
			if len(events) != 1 || events[0].Kind != chatx.NoticeKindScheduleResult || events[0].Payload["excerpt"] != tc.answer {
				t.Fatalf("events = %+v, want the answer from the transcript", events)
			}
		})
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

// Review round 3, finding 9: the reconciler settles the run and consumes its row before the hook
// for the same turn end runs (a Managed driver publishes the end before it notifies). The late
// hook still raises nothing broadcast for a run whose schedule named its targets.
func TestConsumedDeliveryStillPreventsBroadcast(t *testing.T) {
	put := fakeTranscripts(t)
	sid := scheduleFixture(t, "s-consumed", session.KindClaude, false, chatx.DeliverNotifications)
	stop := chatx.InstallReconcilerForTest(20 * time.Millisecond)
	t.Cleanup(stop)
	time.Sleep(1100 * time.Millisecond)
	answered(put, "s-consumed", "private result")
	status.Persist(sid, "working")
	chatx.NoteRunOutcome("s-consumed", "", time.Now()) // the outcome half of the hook, as the driver records it
	status.PersistTurnEndReason(sid, "idle", "")
	waitSettled(t, "s-consumed")
	RecordSessionNotification(sid, "working", "idle", "private result")
	if n := bridgeQueueLen(); n != 0 {
		t.Fatalf("%d broadcast queue entries after the row was consumed", n)
	}
	if events := notice.List(); len(events) != 1 || events[0].Kind != chatx.NoticeKindScheduleResult {
		t.Fatalf("events = %+v, want the schedule result alone", events)
	}
}

// Review round 4, finding 1: a Managed turn's outcome goes to the run whose input the driver
// started the turn with, never to the run that looks latest by time. Here the transcript keeps
// whole seconds and the next run's prompt shares the failed run's second, and the next run has
// already started when the recorder runs: timing would put A's failure on B.
func TestManagedOutcomeFollowsTheTurnsInput(t *testing.T) {
	put := fakeTranscripts(t)
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s-identity", Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverManaged}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)
	raise := func(slot string) string {
		d := &chatx.ScheduleDelivery{ScheduleID: "sch_1", Slot: slot, Targets: []string{chatx.DeliverNotifications},
			Silent: true, PromptSum: chatx.PromptSum(runPrompt)}
		id := chatx.AddScheduledInstruction(m.Name, "", "schedule", d, false)
		if id == "" {
			t.Fatal("no row")
		}
		return id
	}
	a, b := raise("2026-10-03T09:00:00Z"), raise("2026-10-03T09:05:00Z")
	sec := time.Now().Truncate(time.Second).Add(-20 * time.Second)
	put(m.Name, turn("user", runPrompt, sec), turn("assistant", "[SILENT]", sec),
		turn("user", runPrompt, sec), turn("assistant", "B result", sec))

	done := make(chan struct{})
	release := make(chan struct{})
	agents.SetTurnEndRecorder(func(sid string, endedAt time.Time, reason string, run agents.TurnRun) {
		<-release // the recorder runs late, after B has started
		RecordTurnOutcome(sid, endedAt, reason, run)
		close(done)
	})
	t.Cleanup(func() { agents.SetTurnEndRecorder(nil) })

	agents.MarkTurnStartRun(sid, agents.TurnInput{Instr: a})
	agents.MarkTurnEndErr(sid, agents.TurnFailed, "provider error")
	agents.MarkTurnStartRun(sid, agents.TurnInput{Instr: b})
	close(release)
	<-done

	outcome := func(id string) string {
		b, _ := os.ReadFile(filepath.Join(paths.AgentStateDir(), "schedule-outcome", id+".txt"))
		return string(b)
	}
	if got := outcome(a); got != chatx.ReportReasonTurnFailed {
		t.Fatalf("A's outcome = %q, want its failure", got)
	}
	if got := outcome(b); got != "" {
		t.Fatalf("B's outcome = %q, want none (B has not ended)", got)
	}
}
