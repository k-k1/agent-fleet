package chatx

// #1560: a scheduled run's row carries its own targets and the silent sentinel, and the
// production sink routes it to them.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// scheduleSeams replaces the Control Plane call and the bridge readiness check for one test, and
// returns the runs recorded as silent.
func scheduleSeams(t *testing.T, ready map[string]bool) *[]string {
	t.Helper()
	var mu sync.Mutex
	var silents []string
	oldRec, oldReady := recordScheduleSilentFn, bridgeTargetReady
	recordScheduleSilentFn = func(name string, d *ScheduleDelivery) {
		mu.Lock()
		defer mu.Unlock()
		silents = append(silents, d.ScheduleID+"@"+d.Slot+" "+name)
	}
	bridgeTargetReady = func(name string) bool { return ready[name] }
	t.Cleanup(func() { recordScheduleSilentFn, bridgeTargetReady = oldRec, oldReady })
	return &silents
}

// bridgeQueue reads every bridge queue entry, the copies meant for every connection included: a
// scheduled run that named its targets must leave nothing else there.
func bridgeQueue(t *testing.T) []bridge.Message {
	t.Helper()
	dir := filepath.Join(paths.AgentStateDir(), "bridge-queue")
	ents, _ := os.ReadDir(dir)
	var out []bridge.Message
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var m bridge.Message
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func noticesOf(kind string) []notice.Event {
	var out []notice.Event
	for _, e := range notice.List() {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func convReports(t *testing.T, conv string) int {
	t.Helper()
	c, err := LoadConv(conv)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range c.Messages {
		if m.Role == "report" {
			n++
		}
	}
	return n
}

func delivery(silent bool, targets ...string) *ScheduleDelivery {
	return &ScheduleDelivery{ScheduleID: "sch_1", Slot: "2026-10-03T09:00:00Z", Targets: targets, Silent: silent, Label: "nightly check"}
}

// scheduledRow raises a scheduled run's row a minute ago and records the turn's answer after it.
func scheduledRow(t *testing.T, name, conv string, d *ScheduleDelivery, answer string) instrRow {
	t.Helper()
	id := addRowAt(name, conv, "schedule", "", d, time.Now().Add(-time.Minute))
	if id == "" {
		t.Fatal("no row raised")
	}
	if answer != "" {
		NoteTurnAnswer(name, false, answer)
	}
	for _, r := range ReadInstrRows(name) {
		if r.ID == id {
			return r
		}
	}
	t.Fatal("row not found")
	return instrRow{}
}

// A run that answered with the sentinel delivers nothing anywhere and is recorded as silent.
func TestScheduledRowSilentDeliversNothing(t *testing.T) {
	m, _, conv := ledgerFixture(t, "sched1")
	silents := scheduleSeams(t, map[string]bool{"discord": true})
	r := scheduledRow(t, m.Name, conv, delivery(true, DeliverOperator, DeliverNotifications, DeliverDiscord), "  [SILENT]\n")

	if res := deliverReportCard(m.Name, conv, ReportKindAnswerReady, "", []instrRow{r}); res != reportSinkOK {
		t.Fatalf("sink = %v", res)
	}
	if n := convReports(t, conv); n != 0 {
		t.Errorf("the operator conversation got %d report(s)", n)
	}
	if n := len(notice.List()); n != 0 {
		t.Errorf("%d notification(s) raised: %+v", n, notice.List())
	}
	if q := bridgeQueue(t); len(q) != 0 {
		t.Errorf("bridge queue = %+v", q)
	}
	if len(*silents) != 1 || (*silents)[0] != "sch_1@2026-10-03T09:00:00Z sched1" {
		t.Errorf("silent runs recorded = %v", *silents)
	}
}

// The same answer without the sentinel enabled, or a different answer with it enabled, is
// delivered to every target: the operator conversation, the notification center and the bridge.
func TestScheduledRowDeliversToEachTarget(t *testing.T) {
	for name, tc := range map[string]struct {
		silent bool
		answer string
	}{
		"sentinel not enabled":  {false, "[SILENT]"},
		"answer is not exactly": {true, "All good. [SILENT]"},
		"lower case":            {true, "[silent]"},
	} {
		t.Run(name, func(t *testing.T) {
			m, _, conv := ledgerFixture(t, "sched2")
			silents := scheduleSeams(t, map[string]bool{"discord": true, "slack": true})
			r := scheduledRow(t, m.Name, conv, delivery(tc.silent, DeliverOperator, DeliverNotifications, DeliverDiscord), tc.answer)

			if res := deliverReportCard(m.Name, conv, ReportKindAnswerReady, "", []instrRow{r}); res != reportSinkOK {
				t.Fatalf("sink = %v", res)
			}
			if n := convReports(t, conv); n != 1 {
				t.Errorf("operator reports = %d, want 1", n)
			}
			res := noticesOf(NoticeKindScheduleResult)
			if len(res) != 1 || res[0].Payload["excerpt"] != strings.TrimSpace(tc.answer) || res[0].Payload["schedule_id"] != "sch_1" {
				t.Errorf("schedule-result notices = %+v", res)
			}
			q := bridgeQueue(t)
			if len(q) != 1 || q[0].Target != "discord" || q[0].Kind != bridge.KindScheduleResult || q[0].Body != strings.TrimSpace(tc.answer) {
				t.Errorf("bridge queue = %+v, want one schedule-result for discord only", q)
			}
			if len(*silents) != 0 {
				t.Errorf("recorded as silent: %v", *silents)
			}
			// A repeated sink (the row's group was retried) posts nothing twice.
			deliverReportCard(m.Name, conv, ReportKindAnswerReady, "", []instrRow{r})
			if len(bridgeQueue(t)) != 1 || len(noticesOf(NoticeKindScheduleResult)) != 1 || convReports(t, conv) != 1 {
				t.Errorf("a repeated sink delivered again")
			}
		})
	}
}

// Failures are never silent: a failed turn reaches the notification center even when the
// schedule enables the sentinel and names only a bridge, and a bridge that is not connected or
// not bound is reported instead of skipped.
func TestScheduledRowFailureIsNeverSilent(t *testing.T) {
	m, _, _ := ledgerFixture(t, "sched3")
	silents := scheduleSeams(t, map[string]bool{})
	r := scheduledRow(t, m.Name, "", delivery(true, DeliverSlack), "[SILENT]")
	if r.Conv != "" {
		t.Fatalf("row conv = %q", r.Conv)
	}
	if res := deliverReportCard(m.Name, "", ReportKindAnswerReady, ReportReasonTurnFailed, []instrRow{r}); res != reportSinkOK {
		t.Fatalf("sink = %v", res)
	}
	res := noticesOf(NoticeKindScheduleResult)
	if len(res) != 1 || res[0].Payload["report_reason"] != ReportReasonTurnFailed {
		t.Fatalf("notices = %+v", res)
	}
	if u, _ := res[0].Payload["undelivered"].([]any); len(u) != 1 || u[0] != "slack" {
		t.Errorf("undelivered = %v, want the unbound slack named", res[0].Payload["undelivered"])
	}
	if q := bridgeQueue(t); len(q) != 0 {
		t.Errorf("posted to an unready bridge: %+v", q)
	}
	if len(*silents) != 0 {
		t.Errorf("a failure was recorded as silent: %v", *silents)
	}
}

// With reporting off and the sentinel on, a normal answer goes nowhere (that is what report=false
// means), yet the row exists so that a sentinel answer is still recorded as silent.
func TestScheduledRowReportOffSilentOn(t *testing.T) {
	m, _, _ := ledgerFixture(t, "sched4")
	silents := scheduleSeams(t, nil)
	r := scheduledRow(t, m.Name, "", delivery(true), "Found 2 failing jobs")
	deliverReportCard(m.Name, "", ReportKindAnswerReady, "", []instrRow{r})
	if len(notice.List()) != 0 || len(bridgeQueue(t)) != 0 || len(*silents) != 0 {
		t.Fatalf("report off delivered: notices=%+v queue=%+v silents=%v", notice.List(), bridgeQueue(t), *silents)
	}
	r2 := scheduledRow(t, m.Name, "", delivery(true), "[SILENT]")
	deliverReportCard(m.Name, "", ReportKindAnswerReady, "", []instrRow{r2})
	if len(*silents) != 1 {
		t.Fatalf("silent runs = %v", *silents)
	}
}

// An answer recorded before the row was raised belongs to an earlier instruction and does not
// make this run silent.
func TestScheduledRowIgnoresAnEarlierAnswer(t *testing.T) {
	m, _, conv := ledgerFixture(t, "sched5")
	silents := scheduleSeams(t, nil)
	_ = scheduleAnswers.Write(m.Name, scheduleAnswer{At: time.Now().Add(-time.Hour).Format(time.RFC3339), Text: "[SILENT]", Silent: true})
	r := scheduledRow(t, m.Name, conv, delivery(true, DeliverOperator), "")
	if res := deliverReportCard(m.Name, conv, ReportKindAnswerReady, "", []instrRow{r}); res != reportSinkRetry || len(*silents) != 0 {
		t.Fatalf("with only an earlier answer: %v silents=%v, want a wait", res, *silents)
	}
	NoteTurnAnswer(m.Name, false, "Two jobs failed")
	deliverReportCard(m.Name, conv, ReportKindAnswerReady, "", []instrRow{r})
	if len(*silents) != 0 || convReports(t, conv) != 1 {
		t.Fatalf("silents=%v reports=%d — the earlier answer silenced this run", *silents, convReports(t, conv))
	}
}

// NoteTurnAnswer answers "silent" only when every run the turn finishes enables the sentinel and
// the turn ended cleanly, and "routed" only when every instruction it finishes chose its own
// targets. An operator instruction, or a run with nowhere to deliver, keeps today's notification.
func TestNoteTurnAnswer(t *testing.T) {
	m, _, conv := ledgerFixture(t, "sched6")
	past := time.Now().Add(-time.Minute)
	if v := NoteTurnAnswer(m.Name, false, "[SILENT]"); v != (TurnVerdict{}) {
		t.Fatalf("no scheduled row open: %+v", v)
	}
	silentRow := addRowAt(m.Name, "", "schedule", "", delivery(true), past)
	if v := NoteTurnAnswer(m.Name, false, "[SILENT]", "Checking the queue…[SILENT]"); !v.Silent || !v.Routed {
		t.Fatalf("the newest message alone was the sentinel: %+v", v)
	}
	if a, _ := scheduleAnswers.Read(m.Name); !a.Silent || a.Text != "Checking the queue…[SILENT]" {
		t.Fatalf("answer = %+v, want the longer form kept as text", a)
	}
	if v := NoteTurnAnswer(m.Name, true, "[SILENT]"); v.Silent || v.Routed {
		t.Fatalf("a failed turn of a report-off run: %+v, want today's notification", v)
	}
	if v := NoteTurnAnswer(m.Name, false, "", "Disk at 97%"); v.Silent || v.Routed {
		t.Fatalf("a real answer of a report-off run: %+v, want today's notification", v)
	}
	// A run that named targets: routed, even when it failed (the sink notifies the failure).
	markInstrReported(m.Name, []string{silentRow}, time.Now())
	addRowAt(m.Name, "", "schedule", "", delivery(false, DeliverNotifications), past)
	if v := NoteTurnAnswer(m.Name, true, "boom"); v.Silent || !v.Routed {
		t.Fatalf("targeted run: %+v", v)
	}
	if v := NoteTurnAnswer(m.Name, false, "[SILENT]"); v.Silent {
		t.Fatalf("silent for a schedule that does not enable it: %+v", v)
	}
	// An operator instruction finished by the same turn keeps the ordinary notification.
	addInstructionAt(m.Name, conv, "operator", past)
	if v := NoteTurnAnswer(m.Name, false, "[SILENT]"); v.Silent || v.Routed {
		t.Fatalf("mixed with an operator instruction: %+v", v)
	}
}

// Review round 1, finding 3: a silent run whose prompt has not started (still being sent, or
// dropped) is not what the ending turn finished, so it decides nothing about that turn.
func TestNoteTurnAnswerIgnoresRunsThatHaveNotStarted(t *testing.T) {
	m, _, _ := ledgerFixture(t, "sched9")
	past := time.Now().Add(-time.Minute)
	addRowAt(m.Name, "", "schedule", instrBoot, delivery(true, DeliverNotifications), past)
	if v := NoteTurnAnswer(m.Name, false, "[SILENT]"); v != (TurnVerdict{}) {
		t.Fatalf("a run still being sent: %+v", v)
	}
	if _, ok := scheduleAnswers.Read(m.Name); ok {
		t.Fatal("another turn's answer was recorded for a run that has not started")
	}
	id := addRowAt(m.Name, "", "schedule", "", delivery(true, DeliverNotifications), past)
	MarkInstrNotRun(m.Name, id, "archived")
	if v := NoteTurnAnswer(m.Name, false, "[SILENT]"); v != (TurnVerdict{}) {
		t.Fatalf("a dropped run: %+v", v)
	}
}

// Review round 1, finding 5: the end of the turn can be settled before its answer is recorded.
// The row waits for the answer instead of being consumed as an empty, non-silent result; past the
// grace, the result is delivered without a body (never as silent).
func TestScheduledRowWaitsForItsAnswer(t *testing.T) {
	m, _, _ := ledgerFixture(t, "sched10")
	silents := scheduleSeams(t, nil)
	r := scheduledRow(t, m.Name, "", delivery(true, DeliverNotifications), "")
	if res := deliverReportCard(m.Name, "", ReportKindAnswerReady, "", []instrRow{r}); res != reportSinkRetry {
		t.Fatalf("sink before the answer = %v, want retry", res)
	}
	if len(notice.List()) != 0 {
		t.Fatalf("delivered before the answer: %+v", notice.List())
	}
	NoteTurnAnswer(m.Name, false, "[SILENT]")
	if res := deliverReportCard(m.Name, "", ReportKindAnswerReady, "", []instrRow{r}); res != reportSinkOK || len(*silents) != 1 {
		t.Fatalf("after the answer: %v silents=%v", res, *silents)
	}

	old := scheduleAnswerGrace
	scheduleAnswerGrace = 0
	t.Cleanup(func() { scheduleAnswerGrace = old })
	r2 := scheduledRow(t, m.Name, "", delivery(true, DeliverNotifications), "")
	_ = scheduleAnswers.Write(m.Name, scheduleAnswer{At: time.Now().Add(-time.Hour).Format(time.RFC3339), Silent: true})
	deliverReportCard(m.Name, "", ReportKindAnswerReady, "", []instrRow{r2}) // starts the wait
	if res := deliverReportCard(m.Name, "", ReportKindAnswerReady, "", []instrRow{r2}); res != reportSinkOK {
		t.Fatalf("past the grace = %v", res)
	}
	if len(*silents) != 1 || len(noticesOf(NoticeKindScheduleResult)) != 1 {
		t.Fatalf("past the grace: silents=%v notices=%+v", *silents, notice.List())
	}
}

// Review round 1, finding 4: the chat post is queued once per row, kind and target even when the
// sink runs again after a restart, before the row was consumed.
func TestScheduledRowBridgePostSurvivesARestart(t *testing.T) {
	m, _, _ := ledgerFixture(t, "sched11")
	scheduleSeams(t, map[string]bool{"discord": true})
	r := scheduledRow(t, m.Name, "", delivery(false, DeliverDiscord), "Result")
	deliverReportCard(m.Name, "", ReportKindAnswerReady, "", []instrRow{r})
	scheduleAnswerWaits = answerWaits{} // what a restart loses
	deliverReportCard(m.Name, "", ReportKindAnswerReady, "", []instrRow{r})
	if q := bridgeQueue(t); len(q) != 1 {
		t.Fatalf("bridge queue = %+v, want one post", q)
	}
}

// End to end through the reconciler: a scheduled row with no conversation settles like any row,
// and the real sink records it as silent.
func TestReconcilerSettlesAConversationlessScheduledRow(t *testing.T) {
	m, sid, _ := ledgerFixture(t, "sched7")
	silents := scheduleSeams(t, nil)
	rc, clock := newFakeReconciler(t, reportTickDefault, deliverReportCard)
	id := addRowAt(m.Name, "", "schedule", "", delivery(true, DeliverNotifications), time.Now().Add(-time.Minute))
	if !SessionReportPending(m.Name) {
		t.Fatal("a conversation-less scheduled row does not count as owing a report")
	}
	NoteTurnAnswer(m.Name, false, "[SILENT]")
	status.PersistTurnEnd(sid, "idle")
	for i := 0; i < 3; i++ {
		clock.advance(t, rc, reportTickDefault)
	}
	rows := ReadInstrRows(m.Name)
	if len(rows) != 1 || rows[0].ID != id || rows[0].State != instrReported {
		t.Fatalf("rows = %+v", rows)
	}
	if len(*silents) != 1 || len(notice.List()) != 0 {
		t.Fatalf("silents=%v notices=%+v", *silents, notice.List())
	}
}

// A failure posted to a connected bridge still reaches the notification center, and the post
// says it failed rather than carrying an answer.
func TestScheduledRowFailureReachesNotificationsBesideTheBridge(t *testing.T) {
	m, _, _ := ledgerFixture(t, "sched8")
	scheduleSeams(t, map[string]bool{"discord": true})
	r := scheduledRow(t, m.Name, "", delivery(true, DeliverDiscord), "half an answer")
	deliverReportCard(m.Name, "", "exit", "oom", []instrRow{r})
	if res := noticesOf(NoticeKindScheduleResult); len(res) != 1 || res[0].Payload["report_kind"] != "exit" {
		t.Fatalf("notices = %+v", res)
	}
	if q := bridgeQueue(t); len(q) != 1 || q[0].Detail != "oom" || q[0].Body != "" {
		t.Fatalf("bridge queue = %+v", q)
	}
}
