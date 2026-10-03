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
	scheduleBridgeSent = sentSet{}
	t.Cleanup(func() { recordScheduleSilentFn, bridgeTargetReady = oldRec, oldReady })
	return &silents
}

// bridgeQueue reads the bridge queue entries addressed to one connection. The copy of the
// notifications every connection gets (the operator report's session-report among them) is
// today's behaviour and not what these tests are about.
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
		if m.Target != "" {
			out = append(out, m)
		}
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
	deliverReportCard(m.Name, conv, ReportKindAnswerReady, "", []instrRow{r})
	if len(*silents) != 0 || convReports(t, conv) != 1 {
		t.Fatalf("silents=%v reports=%d — the earlier answer silenced this run", *silents, convReports(t, conv))
	}
}

// NoteTurnAnswer answers "silent" only while a row that enables the sentinel is open, only for a
// clean turn end, and for any of the forms the answer is known in.
func TestNoteTurnAnswer(t *testing.T) {
	m, _, conv := ledgerFixture(t, "sched6")
	if NoteTurnAnswer(m.Name, false, "[SILENT]") {
		t.Fatal("silent with no scheduled row open")
	}
	AddInstruction(m.Name, conv, "operator")
	if NoteTurnAnswer(m.Name, false, "[SILENT]") {
		t.Fatal("silent for an operator instruction")
	}
	addRowAt(m.Name, "", "schedule", "", delivery(false, DeliverNotifications), time.Now())
	if NoteTurnAnswer(m.Name, false, "[SILENT]") {
		t.Fatal("silent for a schedule that does not enable it")
	}
	addRowAt(m.Name, "", "schedule", "", delivery(true), time.Now())
	if !NoteTurnAnswer(m.Name, false, "[SILENT]", "Checking the queue…[SILENT]") {
		t.Fatal("the newest message alone was the sentinel")
	}
	if a, _ := scheduleAnswers.Read(m.Name); !a.Silent || a.Text != "Checking the queue…[SILENT]" {
		t.Fatalf("answer = %+v, want the longer form kept as text", a)
	}
	if NoteTurnAnswer(m.Name, true, "[SILENT]") {
		t.Fatal("a failed turn was silent")
	}
	if NoteTurnAnswer(m.Name, false, "", "Disk at 97%") {
		t.Fatal("a real answer was silent")
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
