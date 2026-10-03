package chatx

// Per-schedule delivery targets and the silent sentinel (#1560, ADR 0021 note of 2026-10-03).
//
// A scheduled run used to report in one way only: its completion report to the operator
// conversation (report_to), which also leaves a notification. A schedule may now name its
// targets — the operator conversation, the notification center, and the member's own Discord /
// Slack connections — and may enable the sentinel: a run whose final answer is exactly
// SilentSentinel delivers nothing and is recorded on the Control Plane as fired_silent.
//
// The decision rides the instruction ledger rather than a path of its own. The run's row carries
// the schedule's choice (instrRow.Delivery), the reconciler settles it exactly like any other
// row, and the production sink routes it here instead of to the conversation alone. So a
// scheduled run's result keeps every guarantee the report has: settled by level, never lost to a
// missed hook, delivered before it is consumed.

import (
	"encoding/json"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// SilentSentinel is the whole final answer that means "nothing to report". Compared after
// trimming surrounding whitespace, case-sensitive: anything else, even the sentinel inside a
// longer answer, is delivered, because a wrong match hides an alert and a wrong miss only costs
// one message.
const SilentSentinel = "[SILENT]"

// Delivery targets (the Control Plane validates the same four words).
const (
	DeliverOperator      = "operator"
	DeliverNotifications = "notifications"
	DeliverDiscord       = "discord"
	DeliverSlack         = "slack"
)

// ScheduleDelivery is what a scheduled run's row knows about where its result goes. The
// Control Plane sends it on the create / input body as schedule_delivery only when the schedule
// asks for more than today's report; the slot comes from schedule_slot beside it.
type ScheduleDelivery struct {
	ScheduleID string   `json:"schedule_id"`
	Slot       string   `json:"slot,omitempty"`
	Targets    []string `json:"targets"`
	Silent     bool     `json:"silent,omitempty"`
	Label      string   `json:"label,omitempty"`
}

func (d *ScheduleDelivery) has(target string) bool {
	if d == nil {
		return false
	}
	for _, t := range d.Targets {
		if t == target {
			return true
		}
	}
	return false
}

// IsSilentAnswer reports whether text is the sentinel and nothing else.
func IsSilentAnswer(text string) bool { return strings.TrimSpace(text) == SilentSentinel }

// scheduleAnswer is the final answer of the session's latest turn, kept while the session owes a
// scheduled delivery: the reconciler settles later, in another process, and the hook that knew
// the answer is gone by then.
type scheduleAnswer struct {
	At     string `json:"at"` // RFC3339 (seconds), comparable with a row's cursor
	Text   string `json:"text,omitempty"`
	Silent bool   `json:"silent,omitempty"`
}

var scheduleAnswers = fstore.JSON[scheduleAnswer](paths.AgentStateDir, "schedule-answer", ".json")

// NoteTurnAnswer records a finished turn's answer for the session's scheduled deliveries and
// reports whether the turn is a silent one: a clean end whose answer is the sentinel while a
// row that enables it is open. The caller then raises no answer-ready notification, which would
// otherwise carry the run to the notification center and the bridge after all.
//
// candidates are the forms the answer is known in (the newest message alone, the whole turn's
// prose); any one being the sentinel is enough, since the agent is asked to make its final
// message exactly that. failed is a turn that ended in an error or was cut off: never silent.
func NoteTurnAnswer(name string, failed bool, candidates ...string) bool {
	var silentRow, any bool
	for _, r := range openInstrRows(name) {
		if r.Delivery != nil {
			any = true
			silentRow = silentRow || r.Delivery.Silent
		}
	}
	if !any {
		return false
	}
	ans := scheduleAnswer{At: time.Now().Format(time.RFC3339)}
	for _, c := range candidates {
		if !failed && IsSilentAnswer(c) {
			ans.Silent = true
		}
		if ans.Text == "" {
			ans.Text = HeadRunes(c, BridgeBodyCap)
		}
	}
	// The longest candidate is the most complete answer to deliver when it is not silent.
	for _, c := range candidates {
		if t := HeadRunes(c, BridgeBodyCap); len(t) > len(ans.Text) {
			ans.Text = t
		}
	}
	_ = scheduleAnswers.Write(name, ans)
	return ans.Silent && silentRow
}

// answerFor returns the answer recorded for a row: the latest turn's, when it ended no earlier
// than the row was raised. An older one belongs to an earlier instruction.
func answerFor(name string, r instrRow) (scheduleAnswer, bool) {
	a, ok := scheduleAnswers.Read(name)
	if !ok || reportTimeBefore(a.At, r.Cursor.At) {
		return scheduleAnswer{}, false
	}
	return a, true
}

// scheduleReportFailure is whether a report kind/reason is a failure. Failures are never silent:
// they reach the notification center whatever the targets say.
func scheduleReportFailure(kind, reason string) bool {
	switch kind {
	case ReportKindAnswerReady:
		return reason != ""
	case reportKindReopened:
		return reason == reportReasonReopenCapped
	}
	return true // exit, not-run, unconfirmed
}

// deliverScheduledRow is the sink for one scheduled run's row. Retry only when the operator
// conversation could not be written: the other targets are local queue writes that do not fail
// in a way a retry would fix, and repeating them would post twice.
func deliverScheduledRow(name, convID, kind, reason string, r instrRow) reportSinkResult {
	d := r.Delivery
	switch kind {
	case reportKindReopened:
		// The correction of a premature completion. Only a conversation can take a completion
		// back; the other targets get the real completion when it settles.
		if convID == "" {
			return reportSinkOK
		}
		return deliverConvReport(name, convID, kind, reason, []instrRow{r})
	case ReportKindAnswerReady:
		if reason == "" && d.Silent {
			if a, ok := answerFor(name, r); ok && a.Silent {
				recordScheduleSilentFn(name, d)
				log.Printf("session-report: %s: schedule %s answered %s — nothing delivered", name, d.ScheduleID, SilentSentinel)
				return reportSinkOK
			}
		}
	}
	failure := scheduleReportFailure(kind, reason)
	res := reportSinkOK
	notified := false
	if convID != "" {
		res = deliverConvReport(name, convID, kind, reason, []instrRow{r})
		switch res {
		case reportSinkRetry:
			return res
		case reportSinkOK:
			notified = true // recordSessionReport mirrors the report into the notification center
		}
	}
	// A run dropped before it ran is notified by the Control Plane itself (runNotExecuted).
	cpNotified := kind == reportKindNotRun
	var body string
	if !failure {
		if a, ok := answerFor(name, r); ok {
			body = a.Text
		}
	}
	var undelivered []string
	for _, t := range []string{DeliverDiscord, DeliverSlack} {
		if !d.has(t) {
			continue
		}
		if !bridgeTargetReady(t) {
			undelivered = append(undelivered, t)
			continue
		}
		if scheduleBridgeSent.first(name + ":" + instrDeliveryKeyFor(kind, r) + ":" + kind + ":" + t) {
			bridge.EnqueueTo(t, scheduleBridgeMessage(name, d, kind, reason, body))
		}
	}
	if d.has(DeliverNotifications) || len(undelivered) > 0 || (failure && !notified && !cpNotified) {
		putScheduleResultNotice(name, r, kind, reason, body, undelivered)
	}
	if res == reportSinkDrop && (d.has(DeliverNotifications) || d.has(DeliverDiscord) || d.has(DeliverSlack)) {
		res = reportSinkOK // the conversation is gone, but the result reached its other targets
	}
	return res
}

// bridgeTargetReady is bridge.TargetReady behind a seam: the connection lives in the encrypted
// secrets store, which a test does not set up.
var bridgeTargetReady = bridge.TargetReady

// sentSet remembers what this process already queued. A row whose group is retried (another
// row's conversation write failed) is sunk again on the next tick, and its post must not be.
type sentSet struct {
	mu   sync.Mutex
	keys map[string]bool
}

// scheduleBridgeSentKeep bounds the set: a retry follows within a few ticks, so an old key is
// never asked about again.
const scheduleBridgeSentKeep = 512

func (s *sentSet) first(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys[key] {
		return false
	}
	if s.keys == nil || len(s.keys) >= scheduleBridgeSentKeep {
		s.keys = map[string]bool{}
	}
	s.keys[key] = true
	return true
}

var scheduleBridgeSent sentSet

// scheduleBridgeMessage is the chat-bridge post for a scheduled run. The body is the answer: the
// member chose this connection for this schedule's result, which is the explicit opt-in the
// full-text mode exists to ask for, so it does not also wait on that toggle. It is still
// secret-scrubbed and chunked by the provider like any body.
func scheduleBridgeMessage(name string, d *ScheduleDelivery, kind, reason, body string) bridge.Message {
	m := bridge.Message{Kind: bridge.KindScheduleResult, SessionName: name, DisplayName: d.Label, Body: body}
	if m.DisplayName == "" {
		m.DisplayName = d.ScheduleID
	}
	if meta, ok := session.ReadMeta(name); ok {
		m.SessionKind = meta.Kind
	}
	if scheduleReportFailure(kind, reason) {
		m.Detail = kind
		if reason != "" {
			m.Detail = reason
		}
	}
	return m
}

// putScheduleResultNotice raises the schedule-result notification once per row and report kind.
// The kind is not bridged (bridge.eventKeyFor), so it never reaches a chat the schedule did not
// name.
func putScheduleResultNotice(name string, r instrRow, kind, reason, body string, undelivered []string) {
	d := r.Delivery
	display, sessKind := name, ""
	if m, ok := session.ReadMeta(name); ok {
		display, sessKind = session.Display(m), m.Kind
	}
	ev := notice.New(NoticeKindScheduleResult, name, sessKind, display)
	ev.Payload["schedule_id"] = d.ScheduleID
	ev.Payload["spec_label"] = d.Label
	ev.Payload["report_kind"] = kind
	ev.Payload["report_reason"] = reason
	if body != "" {
		ev.Payload["excerpt"] = HeadRunes(body, scheduleNoticeExcerpt)
	}
	if len(undelivered) > 0 {
		ev.Payload["undelivered"] = undelivered
	}
	_ = notice.PutOnce("schedule-result:"+name+":"+instrDeliveryKeyFor(kind, r)+":"+kind, ev)
}

// NoticeKindScheduleResult is the notification of a scheduled run's result (#1560).
const NoticeKindScheduleResult = "schedule-result"

// scheduleNoticeExcerpt bounds the answer a notification carries: the center shows a line or
// two, and the whole answer is one click away in the session.
const scheduleNoticeExcerpt = 400

// scheduleSilents counts in-flight Control Plane calls, so a test can wait for them.
var scheduleSilents sync.WaitGroup

// recordScheduleSilentFn is recordScheduleSilent behind a seam: tests must not reach a CP.
var recordScheduleSilentFn = func(name string, d *ScheduleDelivery) {
	scheduleSilents.Add(1)
	go func() {
		defer scheduleSilents.Done()
		recordScheduleSilent(name, d)
	}()
}

// recordScheduleSilent asks the Control Plane to record the run as fired_silent. A failure is
// logged only: the history then keeps "fired", which is still true.
func recordScheduleSilent(name string, d *ScheduleDelivery) {
	if d.ScheduleID == "" || d.Slot == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{"session": name, "slot": d.Slot})
	if _, err := mcpx.CPScheduleDo("POST", "/internal/schedules/"+url.PathEscape(d.ScheduleID)+"/runs/silent", body); err != nil {
		log.Printf("session-report: %s: record schedule %s run %s as silent: %v", name, d.ScheduleID, d.Slot, err)
	}
}
