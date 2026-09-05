package chatx

// Tests for the compensation reopen and the self-report fast path (docs/log/51 Phase 3 /
// ADR 0035 decisions 4 and 5).
//
// The two layers are split as in Phase 1 and 2:
//   - Candidate selection (instrReopenCandidates) and the resume evidence (evalReportResumed)
//     are pure functions, so the grace boundary, "no compensation once a new instruction
//     arrived" and "evidence older than the report does not count" are pinned by table.
//   - The compensation itself (compensate) is a SINGLE observation, so it has no debounce time
//     axis. It is called directly instead of through the tick loop: otherwise a fake clock (a
//     deterministic fixed time) would be matched against status markers (real time) and the
//     test would depend on when it runs.
//   - The fast path is exactly the property "2 ticks become 1 tick", so it is measured by
//     driving the fake clock's ticks.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// --- Pure functions -------------------------------------------------------------

// TestInstrReopenCandidates pins WHICH reported rows stay under compensation watch.
func TestInstrReopenCandidates(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	row := func(f func(*instrRow)) instrRow {
		r := instrRow{ID: "i-1", Conv: "c", DeliveredAt: at(-30 * time.Minute),
			Cursor: instrCursor{At: at(-30 * time.Minute)}, State: instrReported, ReportedAt: at(-time.Minute)}
		f(&r)
		return r
	}
	cases := []struct {
		name string
		rows []instrRow
		want int
	}{
		{"a reported row inside the grace window is watched", []instrRow{row(func(*instrRow) {})}, 1},
		{"a report past the grace window is no longer compensated",
			[]instrRow{row(func(r *instrRow) { r.ReportedAt = at(-11 * time.Minute) })}, 0},
		{"a still-open row is not a compensation target",
			[]instrRow{row(func(r *instrRow) { r.State = instrPending; r.ReportedAt = "" })}, 0},
		{"a cancelled row is out of scope too",
			[]instrRow{row(func(r *instrRow) { r.State = instrCancelled })}, 0},
		{"a row with an unparsable report time cannot be watched (no start for the grace)",
			[]instrRow{row(func(r *instrRow) { r.ReportedAt = "???" })}, 0},
		{"no compensation when a new instruction arrived after the report (busy is explained)",
			[]instrRow{
				row(func(*instrRow) {}),
				{ID: "i-2", Conv: "c", DeliveredAt: at(-30 * time.Second),
					Cursor: instrCursor{At: at(-30 * time.Second)}, State: instrPending},
			}, 0},
		{"a row delivered before the report is not a new instruction",
			[]instrRow{
				row(func(*instrRow) {}),
				{ID: "i-2", Conv: "c", DeliveredAt: at(-5 * time.Minute),
					Cursor: instrCursor{At: at(-5 * time.Minute)}, State: instrReported, ReportedAt: at(-time.Minute)},
			}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := instrReopenCandidates(tc.rows, now, reportReopenGrace); len(got) != tc.want {
				t.Fatalf("candidates = %d (%+v), want %d", len(got), got, tc.want)
			}
		})
	}
}

// TestReportResumedEvidence pins the compensation predicate. It differs from the settle
// predicate by requiring "is it after the report"; without that, the afterglow right after a
// report (the previous turn's working marker) reopens the row every time.
func TestReportResumedEvidence(t *testing.T) {
	cases := []struct {
		name string
		sig  reportSignals
		want bool
	}{
		{"back to working after the report", reportSignals{MarkerState: "working", MarkerAfterArm: true}, true},
		{"a working marker older than the report does not count", reportSignals{MarkerState: "working"}, false},
		{"stopped on a question after the report", reportSignals{MarkerState: "question", MarkerAfterArm: true}, true},
		{"still at terminal idle means it has not resumed",
			reportSignals{MarkerState: "idle", MarkerTurnEnd: true, MarkerAfterArm: true}, false},
		{"a background subagent started running", reportSignals{SubagentBusy: true}, true},
		{"the transcript grew (appended after the marker)", reportSignals{TranscriptBusy: true}, true},
		{"the pane shows an interrupt affordance", reportSignals{PaneBusy: true}, true},
		{"an unanswered question is still pending", reportSignals{PendingQuestion: true}, true},
		// Reading a late completion (the real turn terminus written just after the report) as a
		// "resume" produces a false correction plus a re-report of the same content. When there
		// is terminus evidence, ignore the freshness evidence.
		{"terminal idle after the report = a late completion (freshness left over is not a resume)",
			reportSignals{MarkerState: "idle", MarkerTurnEnd: true, MarkerAfterArm: true,
				TranscriptBusy: true, PaneBusy: true}, false},
		{"the turn ended in an abort after the report = not a resume",
			reportSignals{Abort: true, TailAborted: true, AbortReason: ReportReasonTurnAborted,
				TranscriptBusy: true}, false},
		// A report coming out AFTER an abort is the ordinary shape of auto-resume (docs/log/47
		// §4-6): it retries twice, then gives up and reports, so the abort record is older than
		// the report time. Dropping it on the time lower bound and looking only at freshness
		// reads it as "started working after the report" and delivers a false correction.
		{"an abort older than the report is still not a resume (a give-up report after auto-resume)",
			reportSignals{TailAborted: true, TranscriptBusy: true}, false},
		{"nothing at all", reportSignals{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := evalReportResumed(tc.sig)
			if (len(ev) > 0) != tc.want {
				t.Fatalf("resumed = %v, want %v", ev, tc.want)
			}
		})
	}
}

// --- The compensation itself ------------------------------------------------------

// reportedFixture builds a session whose single instruction has already been reported `ago`
// before now — the starting point for compensation (the state after a wrong "completion").
func reportedFixture(t *testing.T, name string, ago time.Duration) (session.Meta, string, string, string) {
	t.Helper()
	m, sid, conv := ledgerFixture(t, name)
	id := addInstructionAt(m.Name, conv, "operator", time.Now().Add(-10*time.Minute))
	markInstrReported(m.Name, []string{id}, time.Now().Add(-ago))
	return m, sid, conv, id
}

// stillReported re-closes the row so the next round of compensation has a candidate again
// (reproducing "another premature report came out", not a real completion).
func stillReported(t *testing.T, name, id string, ago time.Duration) {
	t.Helper()
	markInstrReported(name, []string{id}, time.Now().Add(-ago))
}

func rowByID(t *testing.T, name, id string) instrRow {
	t.Helper()
	for _, r := range ReadInstrRows(name) {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("row %s is not in the ledger", id)
	return instrRow{}
}

// newIdleReconciler builds a reconciler that is NOT running its loop — for tests that call
// compensate directly (no ticks needed).
func newIdleReconciler(sink reportSink) *reportReconciler {
	rc := newReportReconciler(reportTickDefault)
	rc.sink = sink
	return rc
}

// TestReportCompensationReopensOnBusyReturn is the body of ADR 0035 decision 4. In v1,
// consuming the arm by mistake was unrecoverable (a wrong consume = the report lost forever).
// In the ledger a report is only a row state, so going busy again within the grace window lets
// the correction be delivered and the row re-opened.
func TestReportCompensationReopensOnBusyReturn(t *testing.T) {
	m, sid, _, id := reportedFixture(t, "slot70", 30*time.Second)
	var cs countingSink
	rc := newIdleReconciler(cs.sink)

	status.Persist(sid, "working") // the session started running after the report

	rc.compensate(m.Name, time.Now())
	if got := cs.callsSnapshot(); len(got) != 1 || got[0] != reportKindReopened+":" {
		t.Fatalf("correction was not delivered: %v", got)
	}
	r := rowByID(t, m.Name, id)
	if r.State != instrReopened || r.ReopenCount != 1 {
		t.Fatalf("row was not re-opened: %+v", r)
	}
	if r.ReportedAt != "" {
		t.Fatalf("re-opened row still carries a report time: %q", r.ReportedAt)
	}
	if !SessionReportPending(m.Name) {
		t.Fatal("a re-opened row must count as unreported (the real completion is reported again)")
	}
	// Re-opening happens once only. The row is reopened, so the next observation no longer
	// sees it as a candidate.
	rc.compensate(m.Name, time.Now())
	if got := cs.callsSnapshot(); len(got) != 1 {
		t.Fatalf("corrected the same report twice: %v", got)
	}
}

// TestReportCompensationSkipsWhenNewInstruction: when a new instruction arrived after the
// report, that instruction explains the busy state. Missing this means correcting the
// preceding, CORRECT report on every queued instruction.
func TestReportCompensationSkipsWhenNewInstruction(t *testing.T) {
	m, sid, conv, id := reportedFixture(t, "slot71", 30*time.Second)
	var cs countingSink
	rc := newIdleReconciler(cs.sink)

	addInstructionAt(m.Name, conv, "operator", time.Now().Add(-10*time.Second))
	status.Persist(sid, "working")

	rc.compensate(m.Name, time.Now())
	if got := cs.callsSnapshot(); len(got) != 0 {
		t.Fatalf("treated a session running on a new instruction as a wrong report: %v", got)
	}
	if r := rowByID(t, m.Name, id); r.State != instrReported {
		t.Fatalf("row was re-opened: %+v", r)
	}
}

// TestReportCompensationStopsAtCap: re-opening is capped at instrReopenMax per row. At the cap
// it does not give up silently but reports the fact that the decision is oscillating exactly
// once, then stops (the same idiom as the auto-resume cap in docs/log/47).
func TestReportCompensationStopsAtCap(t *testing.T) {
	m, sid, _, id := reportedFixture(t, "slot72", 30*time.Second)
	var cs countingSink
	rc := newIdleReconciler(cs.sink)

	for i := 1; i <= instrReopenMax; i++ {
		status.Persist(sid, "working")
		rc.compensate(m.Name, time.Now())
		if r := rowByID(t, m.Name, id); r.ReopenCount != i {
			t.Fatalf("reopen #%d: ReopenCount = %d", i, r.ReopenCount)
		}
		stillReported(t, m.Name, id, 30*time.Second) // another premature report came out
	}
	if got := cs.callsSnapshot(); len(got) != instrReopenMax {
		t.Fatalf("corrections = %d, want %d: %v", len(got), instrReopenMax, got)
	}

	status.Persist(sid, "working")
	rc.compensate(m.Name, time.Now())
	got := cs.callsSnapshot()
	if len(got) != instrReopenMax+1 || got[instrReopenMax] != reportKindReopened+":"+reportReasonReopenCapped {
		t.Fatalf("hitting the cap was not reported to the user: %v", got)
	}
	r := rowByID(t, m.Name, id)
	if r.State != instrReported || r.ReopenCount != instrReopenMax {
		t.Fatalf("re-opened past the cap: %+v", r)
	}
	if SessionReportPending(m.Name) {
		t.Fatal("the row that was given up on is still left unreported")
	}
}

// TestReportCompensationCorrectionIsIdempotent: like the completion report, the correction is
// made idempotent by ROW ID + REOPEN GENERATION (handover item 2). The correction is delivered
// before the row is re-opened, so there is a window to crash in by construction and a retry must
// not double-post. At the same time the key namespace must stay separate from the completion
// report's — without that, the REAL completion report of a re-opened row is read as "already
// delivered" and swallowed.
func TestReportCompensationCorrectionIsIdempotent(t *testing.T) {
	m, sid, conv, id := reportedFixture(t, "slot73", 30*time.Second)
	rc := newIdleReconciler(deliverReportCard)

	// Put the premature completion report into the conversation first (the correction points
	// at it).
	first := ReadInstrRows(m.Name)
	if res := deliverReportCard(m.Name, conv, ReportKindAnswerReady, "", first); res != reportSinkOK {
		t.Fatalf("completion report delivery = %v", res)
	}
	status.Persist(sid, "working")

	rc.compensate(m.Name, time.Now())
	if n := countReportCards(t, conv); n != 2 {
		t.Fatalf("expected 2 cards including the correction: %d", n)
	}
	// The correction names WHICH report it corrects by the time on the conversation message
	// (handover item 1). The ledger's ReportedAt is already cleared at this point, so taking it
	// from there would come out empty.
	if r := rowByID(t, m.Name, id); r.ReportedAt != "" {
		t.Fatalf("ReportedAt is still set after the reopen: %+v", r)
	}
	body := lastReportCard(t, conv)
	if !strings.Contains(body, "早計") || !strings.Contains(body, "訂正の対象:") {
		t.Fatalf("correction card = %q", body)
	}

	// Reproduce "the correction was delivered but it crashed before the reopen" and retry.
	stillReported(t, m.Name, id, 30*time.Second)
	rows := ReadInstrRows(m.Name)
	for i := range rows {
		rows[i].ReopenCount = 0 // rewind the generation too = a full retry
	}
	unlock := lockInstr(m.Name)
	writeInstrRows(m.Name, rows)
	unlock()

	rc.compensate(m.Name, time.Now())
	if n := countReportCards(t, conv); n != 2 {
		t.Fatalf("correction was double-posted: %d cards", n)
	}

	// The real completion of a re-opened row is reported again even under the same row id
	// (separate key namespace).
	reopened := openInstrRows(m.Name)
	if len(reopened) != 1 {
		t.Fatalf("expected exactly 1 re-opened row: %+v", reopened)
	}
	if res := deliverReportCard(m.Name, conv, ReportKindAnswerReady, "", reopened); res != reportSinkOK {
		t.Fatalf("real completion delivery = %v", res)
	}
	if n := countReportCards(t, conv); n != 3 {
		t.Fatalf("the real completion report was swallowed: %d cards", n)
	}
}

// lastReportCard returns the newest report card's body.
func lastReportCard(t *testing.T, convID string) string {
	t.Helper()
	unlock := LockConv(convID)
	defer unlock()
	c, err := LoadConv(convID)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role == "report" {
			return c.Messages[i].Content
		}
	}
	t.Fatal("no report card in the conversation")
	return ""
}

// --- Self-report fast path ----------------------------------------------------------

// awaitSinkCalls waits until the sink has recorded exactly n deliveries and returns the
// locked snapshot.
//
// Never count sweeps and assert on that number. `swept` is a capacity-1 "one finished"
// notification that does not carry which sweep it came from — a self-report pushes one extra
// wake-up through `nudge`, so there is a window where the notification left by the previous
// wake-up is picked up and read before the intended state is observed (the cause both of flakes
// and of -race reports from reading a slice while a goroutine writes it). What we want to wait
// for is the delivery, not the sweep, so wait on the delivery itself under the lock.
func awaitSinkCalls(t *testing.T, cs *countingSink, n int) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := cs.callsSnapshot()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("deliveries did not reach %d: %v", n, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// expectSinkQuiet asserts the sink stays at n over a bounded window — "nothing is delivered"
// never becomes certain by waiting, so it can only be observed over a bounded time. The failure
// direction is the safe one (it fails only when something really was delivered).
func expectSinkQuiet(t *testing.T, cs *countingSink, n int, why string) {
	t.Helper()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := cs.callsSnapshot(); len(got) != n {
			t.Fatalf("%s: deliveries = %v, want %d", why, got, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSelfReportKickWiresHintSeam: the MCP tool's entry point is the existing kick (POST
// /chat/report) — the self-report rides on it as "a hint plus one piece of evidence", not as
// its own delivery path.
func TestSelfReportKickWiresHintSeam(t *testing.T) {
	m, _, _ := armedFixture(t, "slot74")
	rc := newIdleReconciler(nil)
	old := reportRec
	reportRec = rc
	t.Cleanup(func() { reportRec = old })

	req := httptest.NewRequest(http.MethodPost, "/chat/report",
		strings.NewReader(`{"name":"slot74","kind":"self-report"}`))
	rec := httptest.NewRecorder()
	HandleChatReport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"reported":true`) {
		t.Fatalf("the self-report itself was delivered as a report: %s", rec.Body.String())
	}
	if rc.selfReportFor(m.Name) == "" {
		t.Fatal("the self-report was not recorded (not wired to the hint seam)")
	}
}

// TestSelfReportSettlesInOneTick is exactly what ADR 0035 decision 5 buys: the self-report is
// the only signal that measures semantic completion directly, so it shortens the 2-tick
// confirmation required of mechanical idle to 1 tick.
func TestSelfReportSettlesInOneTick(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot75")
	var cs countingSink
	rc, _ := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle")
	rc.selfReport(m.Name, time.Now()) // tool call → hint wake-up

	// Delivered without advancing a single tick = the 2-tick debounce was not paid
	// (TestNoSelfReportStillSettles shows the same setup taking 2 ticks).
	awaitSinkCalls(t, &cs, 1)
	awaitReported(t, m.Name) // the ledger advances only after the delivery returns
}

// TestNoSelfReportStillSettles: even with no self-report the reconciler picks it up at settle
// as before (the fast path is not the backbone — ADR 0035 §rejected alternatives). 2 ticks on
// the same setup.
func TestNoSelfReportStillSettles(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot76")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle") // no self-report

	clock.advance(t, rc, reportTickDefault)
	expectSinkQuiet(t, &cs, 0, "tick 1 with no self-report")
	clock.advance(t, rc, reportTickDefault)
	awaitSinkCalls(t, &cs, 1)
	awaitReported(t, m.Name)
}

// TestSelfReportTooEarlyIsHeld: calling too early (self-reporting while still running) is held
// back by the busy evidence. "A self-report is not stronger than busy" is the implementation of
// the decision not to make the fast path the backbone. The self-report is not discarded while it
// is held — discarding it would make it work only when called right after the last token.
func TestSelfReportTooEarlyIsHeld(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot77")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.Persist(sid, "working") // still mid-turn
	rc.selfReport(m.Name, time.Now())
	expectSinkQuiet(t, &cs, 0, "called too early (right after the wake-up)")
	clock.advance(t, rc, reportTickDefault)
	expectSinkQuiet(t, &cs, 0, "1 tick while still busy")

	status.PersistTurnEnd(sid, "idle") // really finished now
	clock.advance(t, rc, reportTickDefault)
	// The self-report is still alive, so it is delivered on the first tick after busy clears
	// (no 2-tick wait).
	awaitSinkCalls(t, &cs, 1)
	awaitReported(t, m.Name)
}

// TestSelfReportIsIdleEvidenceWithoutMarker: for kinds with no marker (the TUI polling ones)
// the self-report itself counts as idle evidence. Its evidence time is the self-report time, so
// instructions delivered after it are not caught in the crossfire.
func TestSelfReportIsIdleEvidenceWithoutMarker(t *testing.T) {
	m, _, conv := ledgerFixture(t, "slot78")
	// Build the ledger BEFORE waking the reconciler. AddInstruction makes the decision start
	// over (forget), so a self-report placed in between is discarded, and the wake-ups grow with
	// the number of instructions, leaving "which sweep to wait for" undefined. The self-report
	// time is an argument, so build the ordering from timestamps rather than from call order.
	// Settling on the self-report ALONE requires at least selfReportSettleDelay to have passed
	// since it (the guard against calling too early). This is the path where the self-report is
	// the only evidence, so build it with a sufficiently old one.
	selfAt := time.Now().Add(-selfReportSettleDelay - time.Minute)
	id1 := addInstructionAt(m.Name, conv, "operator", selfAt.Add(-60*time.Second))
	id2 := addInstructionAt(m.Name, conv, "operator", selfAt.Add(2*time.Second))

	var cs countingSink
	rc, _ := newFakeReconciler(t, reportTickDefault, cs.sink)
	rc.selfReport(m.Name, selfAt) // the only wake-up

	awaitSinkCalls(t, &cs, 1) // settles on the self-report alone, with no marker
	if got := cs.rowIDs(0); len(got) != 1 || got[0] != id1 {
		t.Fatalf("rows folded into the report = %v, want [%s] (an instruction newer than the self-report was caught)",
			got, id1)
	}
	// Wait until instruction 1's row closes, then check that only instruction 2 remains.
	var open []instrRow
	for i := 0; i < 150; i++ {
		if open = openInstrRows(m.Name); len(open) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(open) != 1 || open[0].ID != id2 {
		t.Fatalf("only the later instruction's row should remain: %+v", open)
	}
	expectSinkQuiet(t, &cs, 1, "an instruction delivered after the self-report")
}

// TestAutoResumeCountsSessionEventsNotConversations: the auto-resume counter (docs/log/47)
// counts the session's abort events. Incrementing once per conversation when one quiet period
// is delivered to two operator conversations means a session instructed from two conversations
// hits the cap after a single abort.
func TestAutoResumeCountsSessionEventsNotConversations(t *testing.T) {
	m, sid, conv1 := ledgerFixture(t, "slot79")
	conv2 := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv2); err != nil {
		t.Fatal(err)
	}
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	past := time.Now().Add(-60 * time.Second)
	addInstructionAt(m.Name, conv1, "operator", past)
	addInstructionAt(m.Name, conv2.ID, "operator", past)
	status.PersistTurnEnd(sid, "idle")
	rc.hint(m.Name, ReportKindAnswerReady, ReportReasonTurnAborted)
	clock.waitSweep(t, rc) // first quiet period
	clock.advance(t, rc, reportTickDefault)

	if got := cs.callsSnapshot(); len(got) != 2 {
		t.Fatalf("one message per conversation is expected: %v", got)
	}
	if n := AutoResumeAttempts(m.Name); n != 1 {
		t.Fatalf("auto-resume counter = %d, want 1 (incremented per conversation)", n)
	}
}
