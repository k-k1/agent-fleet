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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
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

// TurnVerdict is what a finished turn means for the notification the hook would raise.
type TurnVerdict struct {
	// Silent: every scheduled run this turn finishes enables the sentinel, and the answer is it.
	// Nothing at all is raised.
	Silent bool
	// Routed: every instruction this turn finishes is a scheduled run that chose its own
	// targets. The broadcast answer-ready is not raised: it would reach every chat connection,
	// the ones the schedule did not name and unbound ones included, and the sink delivers the
	// result (and any failure) where the schedule asked.
	Routed bool
}

// NoteTurnAnswer records a finished turn's answer for the scheduled runs it finishes, and says
// what the hook should raise for it.
//
// The rows considered are those the turn can have finished: open, not still being sent, not
// dropped, and not waiting in the session's queue. A scheduled prompt queued behind another
// turn has not run, so that other turn's answer must not be read as its result. An instruction
// without a delivery of its own (an operator's, or a schedule as it was before #1560) keeps
// today's notification whatever else the turn finishes.
//
// candidates are the forms the answer is known in (the newest message when it is known whole,
// the whole turn's prose); any one being the sentinel is enough, since the agent is asked to
// make its final message exactly that. failed is a turn that ended in an error or was cut off:
// never silent.
func NoteTurnAnswer(name string, failed bool, candidates ...string) TurnVerdict {
	rows := turnRows(name, time.Now())
	if len(rows) == 0 {
		return TurnVerdict{}
	}
	allSilent, allRouted, anyDelivery := true, true, false
	for _, r := range rows {
		if r.Delivery == nil {
			allSilent, allRouted = false, false
			continue
		}
		anyDelivery = true
		allSilent = allSilent && r.Delivery.Silent
		allRouted = allRouted && len(r.Delivery.Targets) > 0
	}
	if !anyDelivery {
		return TurnVerdict{}
	}
	ans := scheduleAnswer{At: time.Now().Format(time.RFC3339)}
	for _, c := range candidates {
		if !failed && IsSilentAnswer(c) {
			ans.Silent = true
		}
		// The longest candidate is the most complete answer to deliver when it is not silent.
		if t := HeadRunes(c, BridgeBodyCap); len(t) > len(ans.Text) {
			ans.Text = t
		}
	}
	_ = scheduleAnswers.Write(name, ans)
	silent := ans.Silent && allSilent
	return TurnVerdict{Silent: silent, Routed: silent || allRouted}
}

// turnRows are the open rows a turn ending at now can have finished.
func turnRows(name string, now time.Time) []instrRow {
	var out []instrRow
	for _, r := range withoutHeldInstr(name, openInstrRows(name), agents.HeldInstrs(name)) {
		if r.Dropped == "" && !reportTimeBefore(now.Format(time.RFC3339), r.Cursor.At) {
			out = append(out, r)
		}
	}
	return out
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

// scheduleAnswerGrace is how long a settled clean end waits for its answer to be recorded. Past
// it (the hook that knew the answer never ran, say) the result is delivered without a body,
// which is never silent: a missing answer must not hide an alert.
var scheduleAnswerGrace = 2 * time.Minute

// answerWaits remembers since when each row has waited for its answer.
type answerWaits struct {
	mu    sync.Mutex
	since map[string]time.Time
}

// wait reports whether the row named key should keep waiting at now, and forgets it once not.
func (w *answerWaits) wait(key string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.since == nil {
		w.since = map[string]time.Time{}
	}
	first, ok := w.since[key]
	if !ok {
		w.since[key] = now
		return true
	}
	if now.Sub(first) < scheduleAnswerGrace {
		return true
	}
	delete(w.since, key)
	return false
}

func (w *answerWaits) forget(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.since, key)
}

var scheduleAnswerWaits answerWaits

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

// deliverScheduledRow is the sink for one scheduled run's row. Every part is idempotent per row,
// report kind and target (the conversation by row key, the notification and each chat post by a
// durable marker), so a retry, or a restart before the row is consumed, delivers nothing twice.
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
		if reason == "" {
			a, ok := answerFor(name, r)
			waitKey := name + ":" + instrDeliveryKey(r)
			if ok {
				scheduleAnswerWaits.forget(waitKey)
			} else if scheduleAnswerWaits.wait(waitKey, time.Now()) {
				// The turn's end is visible before its answer is: the hook records the answer
				// after the status write, a Managed driver notifies on its own goroutine, and the
				// af_report fast path can settle in between. Delivering now would send an empty
				// result, or a sentinel run as a normal one, and consume the row for good.
				return reportSinkRetry
			}
			if ok && a.Silent && d.Silent {
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
		key := "schedule-result:" + name + ":" + instrDeliveryKeyFor(kind, r) + ":" + kind + ":" + t
		if err := bridge.EnqueueToOnce(key, t, scheduleBridgeMessage(name, d, kind, reason, body)); err != nil {
			log.Printf("session-report: %s: queue the schedule result for %s: %v", name, t, err)
			return reportSinkRetry
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
