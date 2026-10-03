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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/url"
	"strconv"
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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
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
	// PromptSum is PromptSum of the prompt as delivered, set by the Agent when it raises the row:
	// how the run's own answer is found in the transcript (runAnswers).
	PromptSum string `json:"prompt_sum,omitempty"`
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

// PromptSum identifies a delivered prompt by its text, for finding the run's own turn in the
// transcript later. The same trimming as the injection store, which badges the same turn.
func PromptSum(prompt string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(prompt)))
	return hex.EncodeToString(sum[:16])
}

// runAnswer is one scheduled run's answer, read from the session's transcript: the assistant
// turns that follow the run's own prompt, up to the next prompt.
type runAnswer struct {
	Final string // the last assistant message: the one the sentinel is matched against
	Body  string // every assistant message of the run, for delivery
	At    int    // the run's prompt's position in the transcript: which run answered last
}

// scheduleClaims records which transcript prompt each scheduled run's row was matched to (row id
// -> turn key). A reuse session takes the same prompt text run after run; without the record a
// later run could be matched to an earlier run's prompt, and read that run's answer. Written by
// the reconciler alone (one goroutine), never from a hook process.
var scheduleClaims = fstore.JSON[map[string]string](paths.AgentStateDir, "schedule-claims", ".json")

// runPromptSlack is how much earlier than its row a run's prompt may appear in the transcript: a
// Terminal session's row is raised after the prompt's delivery was confirmed. Shorter than the
// shortest interval a schedule may have (60 s), so the previous fire's prompt is never in reach.
const runPromptSlack = 50 * time.Second

// runAnswers reads the answer of every open scheduled run of the session from its transcript,
// matching each run to its own prompt, oldest run first, each prompt to one run. A run whose
// prompt or answer is not in the transcript yet is absent from the result. persist writes the
// matches (the reconciler); a hook process passes false and only reads.
//
// One answer per run, never the session's latest: two runs of a reuse session can both be open
// (the first not delivered yet when the second answers), and each must get its own answer, or a
// later [SILENT] would hide an earlier alert.
func runAnswers(name string, persist bool) map[string]runAnswer {
	turns, matched := matchRuns(name, persist)
	out := map[string]runAnswer{}
	for id, key := range matched {
		if a, ok := answerAfter(turns, key); ok {
			out[id] = a
		}
	}
	return out
}

// matchRuns matches every open scheduled run of the session to its own prompt in the transcript
// (row id -> turn key), oldest run first, each prompt to one run. A run whose prompt is not in
// the transcript yet is absent. persist writes the matches (the reconciler); a hook process
// passes false and only reads.
func matchRuns(name string, persist bool) ([]transcript.Turn, map[string]string) {
	var rows []instrRow
	for _, r := range openInstrRows(name) {
		if r.Delivery != nil && r.Delivery.PromptSum != "" && r.Sending == "" && r.Dropped == "" {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return nil, nil
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		return nil, nil
	}
	turns, ok := deps.SessionTurns(m)
	if !ok || len(turns) == 0 {
		return nil, nil
	}
	claims, _ := scheduleClaims.Read(name)
	if claims == nil {
		claims = map[string]string{}
	}
	taken := map[string]bool{}
	for _, k := range claims {
		taken[k] = true
	}
	out := map[string]string{}
	changed := false
	for _, r := range rows {
		key, ok := claims[r.ID]
		if !ok {
			if key = claimRunPrompt(turns, r, taken); key == "" {
				continue
			}
			claims[r.ID], taken[key], changed = key, true, true
		}
		out[r.ID] = key
	}
	if persist && changed {
		// Only rows still in the ledger keep a claim, so the record stays as small as the ledger.
		live := map[string]bool{}
		for _, r := range ReadInstrRows(name) {
			live[r.ID] = true
		}
		for id := range claims {
			if !live[id] {
				delete(claims, id)
			}
		}
		_ = scheduleClaims.Write(name, claims)
	}
	return turns, out
}

// turnKey names a transcript prompt stably enough to be claimed: its time and text. The index
// shifts when a transcript is compacted.
func turnKey(t transcript.Turn, sum string) string {
	if t.TS != "" {
		return t.TS + "|" + sum
	}
	return "idx:" + strconv.Itoa(t.Idx) + "|" + sum
}

// promptSumOf is the sum of a user turn's text, with the queue-time mark a held prompt
// delivered after a restart carries removed (agents.MarkHeldEnvelope).
func promptSumOf(text string) string {
	if orig, ok := agents.StripHeldMark(strings.TrimSpace(text)); ok {
		text = orig
	}
	return PromptSum(text)
}

// claimRunPrompt finds the run's prompt: the oldest unclaimed user turn with the run's text that
// is not older than the row by more than runPromptSlack. "" when there is none yet.
func claimRunPrompt(turns []transcript.Turn, r instrRow, taken map[string]bool) string {
	floor, err := time.Parse(time.RFC3339, r.DeliveredAt)
	for _, t := range turns {
		if t.Role != "user" || t.Sidechain || t.Compact || promptSumOf(t.Text) != r.Delivery.PromptSum {
			continue
		}
		if at, perr := time.Parse(time.RFC3339, t.TS); err == nil && perr == nil && at.Before(floor.Add(-runPromptSlack)) {
			continue
		}
		if k := turnKey(t, r.Delivery.PromptSum); !taken[k] {
			return k
		}
	}
	return ""
}

// answerAfter collects the assistant messages that follow the prompt named key, up to the next
// prompt. Not found until the run has an assistant message with text.
func answerAfter(turns []transcript.Turn, key string) (runAnswer, bool) {
	start := -1
	for i, t := range turns {
		if t.Role == "user" && !t.Sidechain && turnKey(t, promptSumOf(t.Text)) == key {
			start = i
			break
		}
	}
	if start < 0 {
		return runAnswer{}, false
	}
	var parts []string
	final := ""
	for _, t := range turns[start+1:] {
		if t.Sidechain {
			continue
		}
		if t.Role == "user" && !t.Compact {
			break
		}
		if t.Role == "assistant" && strings.TrimSpace(t.Text) != "" {
			parts = append(parts, strings.TrimSpace(t.Text))
			final = t.Text
		}
	}
	if final == "" {
		return runAnswer{}, false
	}
	return runAnswer{Final: final, Body: HeadRunes(strings.Join(parts, "\n\n"), BridgeBodyCap), At: start}, true
}

// TurnVerdict is what a finished turn means for the notification the hook would raise.
type TurnVerdict struct {
	// Silent: every scheduled run this turn finishes enables the sentinel, and each one's own
	// answer is it. Nothing at all is raised.
	Silent bool
	// Routed: every instruction this turn finishes is a scheduled run that chose its own
	// targets. The broadcast answer-ready is not raised: it would reach every chat connection,
	// the ones the schedule did not name and unbound ones included, and the sink delivers the
	// result (and any failure) where the schedule asked.
	Routed bool
}

// TurnVerdictFor says what the hook should raise for a turn of the session that just ended.
// key is the turn end's completion key (status.ReadCompletionKey), "" when unknown.
//
// The rows considered are those the turn can have finished: open, not still being sent, not
// dropped, and not waiting in the session's queue. An instruction without a delivery of its own
// (an operator's, or a schedule as it was before #1560) keeps today's notification whatever else
// the turn finishes. Silence is decided from each run's own answer in the transcript, so it is
// the same for a Terminal and a Managed session; a run whose answer cannot be read yet is not
// silent (the notification is raised: a wrong miss costs one message). failed is a turn that
// ended in an error or was cut off: never silent.
//
// The reconciler can settle the turn first (a Managed driver publishes the end before it
// notifies), and then no row is open any more. The verdict it recorded for the same turn end
// before consuming the rows (rememberTurnVerdict) answers instead: without it, the late hook
// would broadcast a result the schedule routed, or a run it already recorded as silent.
func TurnVerdictFor(name string, failed bool, key string) TurnVerdict {
	rows := turnRows(name, time.Now())
	if len(rows) == 0 {
		if rec, ok := turnVerdicts.Read(name); ok && key != "" && rec.Key == key {
			return rec.Verdict
		}
		return TurnVerdict{}
	}
	return verdictOf(name, rows, failed)
}

// verdictOf is TurnVerdictFor over a given set of rows.
func verdictOf(name string, rows []instrRow, failed bool) TurnVerdict {
	allSilent, allRouted := true, true
	for _, r := range rows {
		if r.Delivery == nil {
			return TurnVerdict{}
		}
		allSilent = allSilent && r.Delivery.Silent
		allRouted = allRouted && len(r.Delivery.Targets) > 0
	}
	silent := !failed && allSilent
	if silent {
		answers := runAnswers(name, false)
		for _, r := range rows {
			if a, ok := answers[r.ID]; !ok || !IsSilentAnswer(a.Final) {
				silent = false
			}
		}
	}
	return TurnVerdict{Silent: silent, Routed: silent || allRouted}
}

// turnVerdictRec is the verdict the reconciler reached for one turn end, by its completion key.
type turnVerdictRec struct {
	Key     string      `json:"key"`
	Verdict TurnVerdict `json:"verdict"`
}

var turnVerdicts = fstore.JSON[turnVerdictRec](paths.AgentStateDir, "schedule-turn-verdict", ".json")

// rememberTurnVerdict records, before the reconciler consumes the rows a turn end covers, what a
// hook arriving later for that same end has to raise. Only for an end whose rows include a
// scheduled run's own delivery, and only when the end has a completion key to be matched by.
func rememberTurnVerdict(m session.Meta, covered []instrRow, failed bool) {
	any := false
	for _, r := range covered {
		any = any || r.Delivery != nil
	}
	if !any {
		return
	}
	key, ok := status.ReadCompletionKey(session.UUID(m.Dir, m.Name))
	if !ok || key == "" {
		return
	}
	_ = turnVerdicts.Write(m.Name, turnVerdictRec{Key: key, Verdict: verdictOf(m.Name, covered, failed)})
}

// Run outcomes (clean, or the failure reason) by row id. The reason a reconciler settles with is
// the session's latest turn end, which can be a later run's: a run that failed, then a queued run
// that ended cleanly, would both settle as clean, and the first one's last words read as its
// result (a [SILENT] said before the error, recorded as a silent success). So each turn end
// records its outcome for the run it ended, and the sink trusts that over the folded reason.
var scheduleOutcomes = fstore.Strings(paths.AgentStateDir, "schedule-outcome", ".txt")

const outcomeClean = "clean"

// NoteRunOutcome records how a turn went (reason: "" clean, else the failure qualifier) for the
// scheduled run it ended. endedAt is when the turn ended, taken where the end was seen (the hook,
// or the Managed driver before it hands the end to an async notifier): the run is the open one
// whose prompt is the latest in the transcript at or before that instant. Never "the latest
// prompt now": by the time a delayed notifier runs, the next queued run may have started and
// answered, and its prompt must not take the earlier run's outcome.
//
// A failure is not overwritten by a later clean end of the same run; an abort is (an aborted run
// that resumed and finished did finish).
func NoteRunOutcome(name, reason string, endedAt time.Time) {
	turns, matched := matchRuns(name, false)
	best, bestIdx := "", -1
	for id, key := range matched {
		for i, t := range turns {
			if t.Role != "user" || t.Sidechain || turnKey(t, promptSumOf(t.Text)) != key {
				continue
			}
			if at, err := time.Parse(time.RFC3339Nano, t.TS); err == nil && at.After(endedAt) {
				break // asked after the turn ended: not the run that ended
			}
			if i > bestIdx {
				best, bestIdx = id, i
			}
			break
		}
	}
	if best == "" {
		return
	}
	outcome := reason
	if outcome == "" {
		outcome = outcomeClean
	}
	if prev, ok := scheduleOutcomes.Read(best); ok && prev == ReportReasonTurnFailed && outcome != ReportReasonTurnFailed {
		return
	}
	_ = scheduleOutcomes.Write(best, outcome)
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

// scheduleAnswerGrace is how long a settled clean end waits for its answer to appear in the
// transcript. Past it (a kind whose transcript cannot be read, say) the result is delivered
// without a body, which is never silent: a missing answer must not hide an alert.
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
	var answer *runAnswer
	switch kind {
	case reportKindReopened:
		// The correction of a premature completion. Only a conversation can take a completion
		// back; the other targets get the real completion when it settles.
		if convID == "" {
			return reportSinkOK
		}
		return deliverConvReport(name, convID, kind, reason, []instrRow{r})
	case ReportKindAnswerReady:
		// The run's own outcome, when its turn end recorded one, is the truth about it: the
		// reason this settle carries may belong to a later run of the same session.
		outcome, hasOutcome := scheduleOutcomes.Read(r.ID)
		if hasOutcome {
			reason = ""
			if outcome != outcomeClean {
				reason = outcome
			}
		}
		if reason == "" {
			a, ok := runAnswers(name, true)[r.ID]
			waitKey := name + ":" + instrDeliveryKey(r)
			if ok && hasOutcome {
				scheduleAnswerWaits.forget(waitKey)
			} else if scheduleAnswerWaits.wait(waitKey, time.Now()) {
				// The turn's end can be settled before its answer is in the transcript (a
				// Managed driver's store and a Terminal CLI's file are written on their own
				// schedule), or before its hook recorded how the run ended. Delivering now would
				// send an empty result, or a sentinel run as a normal one, and consume the row
				// for good.
				return reportSinkRetry
			}
			if ok {
				answer = &a
			}
			// Silent only on the run's own recorded clean end: past the wait, a run whose
			// outcome never arrived is delivered, never silenced.
			if ok && hasOutcome && d.Silent && IsSilentAnswer(a.Final) {
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
	if !failure && answer != nil {
		body = answer.Body
	}
	// Chat posts are queued after the notification, and a queue that cannot be written does not
	// hold the notification back: the bridge never blocks the notification center (ADR 0020
	// decision 4), and a failure must reach it. Both are idempotent, so the retry a queue error
	// asks for repeats neither.
	var undelivered, queueFailed []string
	for _, t := range []string{DeliverDiscord, DeliverSlack} {
		if d.has(t) && !bridgeTargetReady(t) {
			undelivered = append(undelivered, t)
		}
	}
	if d.has(DeliverNotifications) || len(undelivered) > 0 || (failure && !notified && !cpNotified) {
		putScheduleResultNotice(name, r, kind, reason, body, undelivered)
	}
	for _, t := range []string{DeliverDiscord, DeliverSlack} {
		if !d.has(t) || !bridgeTargetReady(t) {
			continue
		}
		key := "schedule-result:" + name + ":" + instrDeliveryKeyFor(kind, r) + ":" + kind + ":" + t
		if err := bridge.EnqueueToOnce(key, t, scheduleBridgeMessage(name, d, kind, reason, body)); err != nil {
			log.Printf("session-report: %s: queue the schedule result for %s: %v", name, t, err)
			queueFailed = append(queueFailed, t)
		}
	}
	if len(queueFailed) > 0 {
		return reportSinkRetry
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
