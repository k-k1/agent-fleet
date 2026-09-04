package chatx

// Tests for the consumption-decision reconciler (docs/log/51 Phase 1 / ADR 0035).
//
// Two layers:
//   - The predicate (evalReportEvidence) is a pure function, so the intersection of the
//     evidence is pinned with a table.
//   - The reconciler itself is time-driven: a fake clock advances it tick by tick. The
//     debounce (two consecutive ticks), the retry after a sink failure and the recovery
//     latency when the hint is lost are all "what happens as time passes" properties, so
//     none of them waits on real time.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// --- reconciler wiring for the tests -----------------------------------------------

// installReconciler swaps the process-wide reconciler for the test's own and runs it.
func installReconciler(t *testing.T, rc *reportReconciler) *reportReconciler {
	t.Helper()
	old := reportRec
	reportRec = rc
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); rc.run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		reportRec = old
	})
	return rc
}

// withTestReconciler runs the real (wall-clock) reconciler at a tiny interval — used by
// the v1 regression tests, which drive the whole hook -> kick -> conversation card path.
func withTestReconciler(t *testing.T, interval time.Duration) *reportReconciler {
	t.Helper()
	return installReconciler(t, newReportReconciler(interval))
}

// fakeReportClock drives the reconciler tick by tick.
type fakeReportClock struct {
	mu  sync.Mutex
	now time.Time
	c   chan time.Time
}

func newFakeReportClock() *fakeReportClock {
	return &fakeReportClock{
		now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
		c:   make(chan time.Time),
	}
}

func (f *fakeReportClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeReportClock) Ticker(time.Duration) (<-chan time.Time, func()) {
	return f.c, func() {}
}

// advance moves the clock by d, fires exactly one tick and waits for that sweep to
// finish, so each line of a test corresponds one-to-one with one tick's decision.
func (f *fakeReportClock) advance(t *testing.T, rc *reportReconciler, d time.Duration) {
	t.Helper()
	select { // drop a completion left over from a preceding wake-up
	case <-rc.swept:
	default:
	}
	f.mu.Lock()
	f.now = f.now.Add(d)
	now := f.now
	f.mu.Unlock()
	select {
	case f.c <- now:
	case <-time.After(3 * time.Second):
		t.Fatal("reconciler did not take the tick")
	}
	select {
	case <-rc.swept:
	case <-time.After(3 * time.Second):
		t.Fatal("sweep did not finish")
	}
}

// waitSweep waits for one already-triggered sweep (the hint's wake-up) to finish, so the
// tick-driven assertions stay one-to-one with advance().
func (f *fakeReportClock) waitSweep(t *testing.T, rc *reportReconciler) {
	t.Helper()
	select {
	case <-rc.swept:
	case <-time.After(3 * time.Second):
		t.Fatal("wake sweep did not finish")
	}
}

// awaitReported polls until the session has no open instruction row left. The row is moved
// to reported only after delivery (the append to the conversation) succeeds, so at the
// moment the report card appears the row can still be pending: wait before asserting it.
func awaitReported(t *testing.T, name string) {
	t.Helper()
	for i := 0; i < 150; i++ {
		if !SessionReportPending(name) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("a delivered report must move the instruction row to reported (one instruction = one report): %s", name)
}

// newFakeReconciler wires a fake clock + a counting sink and starts the loop.
func newFakeReconciler(t *testing.T, interval time.Duration, sink reportSink) (*reportReconciler, *fakeReportClock) {
	t.Helper()
	clock := newFakeReportClock()
	rc := newReportReconciler(interval)
	rc.clock = clock
	rc.sink = sink
	installReconciler(t, rc)
	return rc, clock
}

// ledgerFixture creates a temp HOME, an operator conversation and a session with an
// EMPTY ledger, for the cases where the test wants to choose when each instruction was
// delivered (queueing, folding). Automatic turns are switched off so a test using the real
// sink does not hit a provider.
func ledgerFixture(t *testing.T, name string) (session.Meta, string, string) {
	t.Helper()
	home := withTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	conv := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, Title: "リコンサイラ検証"}
	session.WriteMeta(m)
	return m, session.UUID(m.Dir, m.Name), conv.ID
}

// armedFixture is ledgerFixture plus one instruction row delivered now (the v1 arm).
func armedFixture(t *testing.T, name string) (session.Meta, string, string) {
	t.Helper()
	m, sid, convID := ledgerFixture(t, name)
	AddInstruction(m.Name, convID, "operator")
	return m, sid, convID
}

// countReportCards counts the report cards in a conversation, reading under the
// conversation lock: saveConv is a non-atomic os.WriteFile, so a bare read can catch the
// file mid-truncate.
func countReportCards(t *testing.T, convID string) int {
	t.Helper()
	unlock := LockConv(convID)
	defer unlock()
	c, err := LoadConv(convID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for i := range c.Messages {
		if c.Messages[i].Role == "report" {
			n++
		}
	}
	return n
}

// waitPastCursor blocks until the wall clock has passed the row cursor, so the NEXT status
// marker (TS = now, second precision) counts as written after that instruction. The
// ordering between an instruction and its evidence is the core of the decision, so this is
// the one place that has to advance real time.
//
// The deadline must be comfortably longer than the worst case for crossing. The comparison
// is on second-precision strings, so with a cursor at now+2s the crossing only happens
// after the seconds field has advanced three times, i.e. just under 3 seconds. A deadline
// of exactly 3 seconds was not enough when the runner was slightly late and CI went red
// while the product was untouched. The slack goes into the deadline, not into the wait:
// this still returns the instant the cursor is crossed.
func waitPastCursor(t *testing.T, cursor string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().Format(time.RFC3339) > cursor {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("never got past the cursor %s", cursor)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// --- the predicate table -----------------------------------------------------------

// TestReportEvidenceTable pins the settle predicate: at least one idle piece of evidence
// and zero busy ones. What it really fixes is that "no marker = idle" is no longer the
// default (that was what defeated the v1 waiter), and that interim states and a running
// background agent count as busy evidence (where the waiter's saga5uc special case ended
// up).
func TestReportEvidenceTable(t *testing.T) {
	idle := reportSignals{MarkerState: "idle", MarkerTurnEnd: true, MarkerAfterArm: true}
	with := func(f func(*reportSignals)) reportSignals {
		s := idle
		f(&s)
		return s
	}
	cases := []struct {
		name  string
		sig   reportSignals
		quiet bool
		kind  string
		reasn string
		term  bool
	}{
		{"explicit idle marker", idle, true, ReportKindAnswerReady, "", false},
		{"no marker is unknown, not idle", reportSignals{}, false, "", "", false},
		{"idle that is not a turn end (boot / runtime lost) is unknown",
			with(func(s *reportSignals) { s.MarkerTurnEnd = false }), false, "", "", false},
		{"idle from before the instruction is not this completion",
			with(func(s *reportSignals) { s.MarkerAfterArm = false }), false, "", "", false},
		{"working marker", with(func(s *reportSignals) { s.MarkerState = "working" }), false, "", "", false},
		{"waiting for an answer (interim)", with(func(s *reportSignals) { s.MarkerState = "question" }), false, "", "", false},
		{"waiting for plan approval (interim)", with(func(s *reportSignals) { s.MarkerState = "plan" }), false, "", "", false},
		{"waiting for permission (interim)", with(func(s *reportSignals) { s.MarkerState = "permission" }), false, "", "", false},
		{"a pending question payload is still there",
			with(func(s *reportSignals) { s.PendingQuestion = true }), false, "", "", false},
		{"background subagent running (saga5uc)",
			with(func(s *reportSignals) { s.SubagentBusy = true }), false, "", "", false},
		{"main transcript is fresh (thinking gap, sqmconc)",
			with(func(s *reportSignals) { s.TranscriptBusy = true }), false, "", "", false},
		{"pane shows an interrupt affordance", with(func(s *reportSignals) { s.PaneBusy = true }), false, "", "", false},
		{"deliberately stopped keeps the arm", with(func(s *reportSignals) { s.Stopped = true }), false, "", "", false},
		{"completion carrying an abort hint",
			with(func(s *reportSignals) { s.HintReason = ReportReasonTurnAborted }),
			true, ReportKindAnswerReady, ReportReasonTurnAborted, false},
		{"an abnormal exit is terminal without debounce",
			with(func(s *reportSignals) { s.Exit = "oom" }), true, "exit", "oom", true},
		{"an abnormal exit outweighs busy evidence (the transcript is fresh right after a death)",
			with(func(s *reportSignals) { s.Exit = "crashed"; s.TranscriptBusy = true }),
			true, "exit", "crashed", true},
		// A self-report (docs/log/51 Phase 3 §fast path) is idle evidence of the same rank as
		// a marker. Not making it stronger than busy evidence is what keeps the fast path
		// from becoming the backbone.
		{"a self-report alone is idle evidence (kinds that have no marker)",
			reportSignals{SelfReported: true, SelfReportAt: "2026-07-29T12:00:00Z", SelfReportAged: true},
			true, ReportKindAnswerReady, "", false},
		// An early call (reporting, then continuing to write the final answer) waits for the
		// quiet window to close. Measured in sannme2: the real answer arrived 2m22s after the
		// self-report, and in between every piece of busy evidence had gone (thinking gap
		// 142s > freshness TTL 90s).
		{"a fresh self-report alone is not a completion (early call)",
			reportSignals{SelfReported: true, SelfReportAt: "2026-07-29T12:00:00Z"},
			false, "", "", false},
		// An abort (docs/log/47) is idle evidence that looks at no marker at all. claude does
		// not fire the Stop hook on an abort, so this pins that the report still happens with
		// no marker (measured in sp2qemx, where a bad heal removed it) and that the
		// classification becomes the report's reason.
		{"an abort is idle evidence even with no marker",
			reportSignals{Abort: true, AbortReason: ReportReasonTurnAborted, AbortAt: "2026-07-30T00:41:19Z"},
			true, ReportKindAnswerReady, ReportReasonTurnAborted, false},
		{"an abort a re-send cannot fix is reported as turn-failed",
			reportSignals{Abort: true, AbortReason: ReportReasonTurnFailed, AbortAt: "2026-07-30T00:41:19Z"},
			true, ReportKindAnswerReady, ReportReasonTurnFailed, false},
		{"an abort still waits while busy evidence remains (it may have resumed)",
			reportSignals{Abort: true, AbortReason: ReportReasonTurnAborted, PaneBusy: true},
			false, "", "", false},
		// docs/log/47 §4-6: an abort that a re-send fixes is resumed by the Agent itself
		// first. A report in the meantime would only send an already-executed request on the
		// assistant's turn, so none is emitted.
		{"an abort the auto-resume has taken over is not reported",
			reportSignals{AbortHeld: true}, false, "", "", false},
		// The hold must apply to idle evidence that comes from a marker as well. In the shape
		// where an abort still fires Stop (a 429 rate limit — docs/log/47 §4-5) the marker
		// reaches idle+turnEnd first, so dropping only Abort would misreport it as a plain
		// completion.
		{"while held, even a marker idle is not a completion",
			with(func(s *reportSignals) { s.AbortHeld = true }), false, "", "", false},
		// But if the process is dead there is nobody to resume: an abnormal exit outweighs the hold.
		{"an abnormal exit outweighs the hold",
			with(func(s *reportSignals) { s.AbortHeld = true; s.Exit = "oom" }),
			true, "exit", "oom", true},
		{"an early self-report is stopped by busy evidence",
			with(func(s *reportSignals) {
				s.MarkerState, s.SelfReported = "working", true
			}), false, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := evalReportEvidence(tc.sig)
			if v.Quiet != tc.quiet || v.Terminal != tc.term {
				t.Fatalf("quiet=%v terminal=%v (want %v/%v) why=%s", v.Quiet, v.Terminal, tc.quiet, tc.term, v.Why)
			}
			if v.Quiet && (v.Kind != tc.kind || v.Reason != tc.reasn) {
				t.Fatalf("kind=%q reason=%q, want %q/%q", v.Kind, v.Reason, tc.kind, tc.reasn)
			}
		})
	}
}

// --- time-driven ------------------------------------------------------------------

// countingSink records the deliveries and lets the test fail the first N of them.
type countingSink struct {
	mu    sync.Mutex
	calls []string   // the sequence of "kind:reason"
	rows  [][]string // ids of the instruction rows each delivery folded (Phase 2 idempotency key)
	fail  int        // how many more times to return Retry
}

func (cs *countingSink) sink(name, convID, kind, reason string, rows []instrRow) reportSinkResult {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.calls = append(cs.calls, kind+":"+reason)
	cs.rows = append(cs.rows, instrIDs(rows))
	if cs.fail > 0 {
		cs.fail--
		return reportSinkRetry
	}
	return reportSinkOK
}

// rowIDs returns the ledger row ids the i-th delivery folded.
func (cs *countingSink) rowIDs(i int) []string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if i >= len(cs.rows) {
		return nil
	}
	return cs.rows[i]
}

func (cs *countingSink) count() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return len(cs.calls)
}

// callsSnapshot copies the recorded deliveries under the lock. A bare `cs.calls` is a
// shared slice written by the reconciler's goroutine, so it must not be read unlocked, not
// even inside a failure message: reads that go through advance() are ordered by the swept
// channel, but assertions that poll (chat_report_compensate_test.go) have no such ordering
// and -race catches it there.
func (cs *countingSink) callsSnapshot() []string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return append([]string(nil), cs.calls...)
}

// TestReportReconcilerSettleDebounce: one tick of quiet does not deliver; only two
// consecutive ticks (and the time between them) deliver and consume the arm. That is the
// temporal corroboration that keeps a misread TUI footer or a marker that vanishes for an
// instant during a heal from turning into a "completion".
func TestReportReconcilerSettleDebounce(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot50")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle") // the instruction's turn ended with Stop

	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 0 {
		t.Fatalf("delivered on the first tick (the debounce is not working): %v", cs.calls)
	}
	if !SessionReportPending(m.Name) {
		t.Fatal("consumed the arm on the first tick")
	}

	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 {
		t.Fatalf("two consecutive quiet ticks must deliver: %v", cs.calls)
	}
	if cs.calls[0] != ReportKindAnswerReady+":" {
		t.Fatalf("delivery = %q", cs.calls[0])
	}
	if SessionReportPending(m.Name) {
		t.Fatal("a successful delivery consumes the arm (one instruction = one report)")
	}

	// Once consumed, no number of ticks delivers again.
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 {
		t.Fatalf("delivered again on an already-consumed arm: %v", cs.calls)
	}
}

// TestReportReconcilerBusyResetsDebounce: busy evidence part-way through the quiet count
// starts it over. A wrong "not yet" self-corrects on the next tick; a wrong "completed" is
// never produced.
func TestReportReconcilerBusyResetsDebounce(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot51")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle")
	clock.advance(t, rc, reportTickDefault) // first quiet tick

	status.Persist(sid, "working") // the next instruction / chained turn started running
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 0 {
		t.Fatalf("settled across busy evidence: %v", cs.calls)
	}

	status.PersistTurnEnd(sid, "idle") // the real completion
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 0 {
		t.Fatalf("delivered on the first tick after the reset: %v", cs.calls)
	}
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 {
		t.Fatalf("must deliver on the second tick after the reset: %v", cs.calls)
	}
	if SessionReportPending(m.Name) {
		t.Fatal("the arm was not consumed")
	}
}

// TestReportReconcilerSinkRetry: a failed delivery leaves the ledger (the arm) alone and
// retries on the next tick (docs/log/51 §delivery, hole D). v1 was consume-then-deliver, so
// when the append to the conversation failed only the arm disappeared and the report was
// lost forever.
func TestReportReconcilerSinkRetry(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot52")
	cs := countingSink{fail: 2}
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle")
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault) // settle -> first delivery (fails)
	if cs.count() != 1 || !SessionReportPending(m.Name) {
		t.Fatalf("consumed the arm on a failed delivery (calls=%d armed=%v)", cs.count(), SessionReportPending(m.Name))
	}

	clock.advance(t, rc, reportTickDefault) // second attempt (fails)
	if cs.count() != 2 || !SessionReportPending(m.Name) {
		t.Fatalf("no retry happened (calls=%d armed=%v)", cs.count(), SessionReportPending(m.Name))
	}

	clock.advance(t, rc, reportTickDefault) // third attempt (succeeds)
	if cs.count() != 3 {
		t.Fatalf("calls = %d, want 3", cs.count())
	}
	if SessionReportPending(m.Name) {
		t.Fatal("a successful delivery consumes the arm")
	}
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 3 {
		t.Fatalf("still delivering after success: %d", cs.count())
	}
}

// TestReportReconcilerRecoversWithoutHint pins the latency (docs/log/51 §trade-offs): even
// when not a single kick (hint) arrives — lost during an agent restart, a dead hook, drift
// in the TUI string contract — the tick picks the same state up by level, and does not get
// worse than the v1 waiter's 90s wait.
func TestReportReconcilerRecoversWithoutHint(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot53")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle") // it completed, but nobody kicked

	const v1WaiterWait = 90 * time.Second // v1: waiting out the SubagentBusy TTL (docs/log/30)
	var elapsed time.Duration
	for elapsed < v1WaiterWait && cs.count() == 0 {
		clock.advance(t, rc, reportTickDefault)
		elapsed += reportTickDefault
	}
	if cs.count() != 1 {
		t.Fatalf("nothing was delivered after %v without a hint", elapsed)
	}
	if elapsed > v1WaiterWait {
		t.Fatalf("delivery took %v, worse than the v1 waiter's %v", elapsed, v1WaiterWait)
	}
	if SessionReportPending(m.Name) {
		t.Fatal("the arm was not consumed")
	}
	if elapsed != 2*reportTickDefault {
		t.Logf("delivery took %v (2 ticks expected)", elapsed)
	}
}

// TestReportReconcilerExitReportsWithoutDebounce: an abnormal exit is a terminal fact, so
// it is reported without waiting for the evidence to line up, i.e. with no debounce.
// ExitInfo is read by level, so a death on a path that has no kick is picked up on the same
// tick.
func TestReportReconcilerExitReportsWithoutDebounce(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot54")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.Persist(sid, "working") // in the middle of a turn
	status.PersistExit(m.Name, status.ExitInfo{
		Reason: "oom", Code: 137, Signal: 9, At: time.Now().Format(time.RFC3339),
	})

	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 || cs.calls[0] != "exit:oom" {
		t.Fatalf("the abnormal exit was not reported within one tick: %v", cs.calls)
	}
	if SessionReportPending(m.Name) {
		t.Fatal("the arm was not consumed")
	}
}

// TestReportReconcilerHintCarriesReason: the hint carries only the qualifier that cannot be
// read from a marker (abort vs failure, docs/log/47). A hint is discarded once it crosses
// busy evidence, because that abort is no longer the current state.
func TestReportReconcilerHintCarriesReason(t *testing.T) {
	_, sid, _ := armedFixture(t, "slot55")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle")
	rc.hint("slot55", ReportKindAnswerReady, ReportReasonTurnAborted)
	clock.waitSweep(t, rc) // the sweep from the hint's wake-up (first quiet tick)
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 || cs.calls[0] != ReportKindAnswerReady+":"+ReportReasonTurnAborted {
		t.Fatalf("the abort qualifier did not make it into the report: %v", cs.calls)
	}
}

// --- the instruction ledger (docs/log/51 Phase 2) -----------------------------------

// TestReportReconcilerQueuedInstructionSurvives pins that hole A is closed. In v1 the
// re-arm of instruction 2 overwrote the one-bit arm of instruction 1, and once the Stop of
// instruction 1 arrived and consumed that bit, the completion of instruction 2 was reported
// to nobody. In the ledger an instruction is a row, so when the earlier one becomes
// reported the later one's row stays pending and is reported separately on its own
// completion. That is the practical gain of moving identity from a bit to a row id.
func TestReportReconcilerQueuedInstructionSurvives(t *testing.T) {
	m, sid, conv := ledgerFixture(t, "slot60")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	now := time.Now()
	id1 := addInstructionAt(m.Name, conv, "operator", now.Add(-60*time.Second))
	status.PersistTurnEnd(sid, "idle") // instruction 1's turn ended with Stop
	// Queued: instruction 2 arrived after that turn end. Not a single character of it has
	// run, so the same quiet window must not count as its completion.
	id2 := addInstructionAt(m.Name, conv, "operator", now.Add(2*time.Second))

	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 {
		t.Fatalf("the completion of instruction 1 was not reported: %v", cs.calls)
	}
	if got := cs.rowIDs(0); len(got) != 1 || got[0] != id1 {
		t.Fatalf("rows folded into the report = %v, want [%s] (it swept in the later instruction)", got, id1)
	}
	open := openInstrRows(m.Name)
	if len(open) != 1 || open[0].ID != id2 || open[0].State != instrPending {
		t.Fatalf("the later instruction's row did not survive: %+v", open)
	}
	// While the evidence (the marker) stays older than instruction 2, no number of ticks
	// reports it.
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 {
		t.Fatalf("reported an instruction that never started as completed: %v", cs.calls)
	}

	// Instruction 2's turn ends: the marker moves past instruction 2's cursor.
	waitPastCursor(t, open[0].Cursor.At)
	status.PersistTurnEnd(sid, "idle")
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 2 {
		t.Fatalf("the later instruction was not reported separately: %v", cs.calls)
	}
	if got := cs.rowIDs(1); len(got) != 1 || got[0] != id2 {
		t.Fatalf("rows folded into the second report = %v, want [%s]", got, id2)
	}
	if n := len(openInstrRows(m.Name)); n != 0 {
		t.Fatalf("%d unreported rows are left", n)
	}
}

// TestReportReconcilerFoldsOverlappingInstructions: when several instructions are all
// covered by the same quiet window, they are folded into one report rather than spamming,
// and every row becomes reported (docs/log/51 §data model: bundle them explicitly instead
// of letting them collapse).
func TestReportReconcilerFoldsOverlappingInstructions(t *testing.T) {
	m, sid, conv := ledgerFixture(t, "slot61")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	now := time.Now()
	id1 := addInstructionAt(m.Name, conv, "operator", now.Add(-90*time.Second))
	id2 := addInstructionAt(m.Name, conv, "operator", now.Add(-60*time.Second))
	status.PersistTurnEnd(sid, "idle")

	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 {
		t.Fatalf("the deliveries must be folded into one: %v", cs.calls)
	}
	got := cs.rowIDs(0)
	if len(got) != 2 || got[0] != id1 || got[1] != id2 {
		t.Fatalf("folded rows = %v, want [%s %s]", got, id1, id2)
	}
	if n := len(openInstrRows(m.Name)); n != 0 {
		t.Fatalf("folded, yet %d rows are still unreported", n)
	}
	// The body carries "N instructions" and each delivery time; with a single row nothing is
	// added.
	rows := ReadInstrRows(m.Name)
	note := foldFact(len(rows), instrFoldAts(rows), "ja")
	if !strings.Contains(note, "2 件") || !strings.Contains(note, rows[0].DeliveredAt) {
		t.Fatalf("fold note = %q", note)
	}
	// With one instruction the note is absent entirely: the body is character-for-character
	// what v1 produced.
	single := reportView{kind: ReportKindAnswerReady, args: map[string]string{"display": "d", "name": m.Name}}
	if strings.Contains(single.displayText("ja"), "件ぶんの完了") {
		t.Fatal("the report body for a single instruction must not differ from v1 by one character")
	}
}

// TestReportReconcilerDeliveryIsIdempotent: a duplicate delivery of the same row ids is
// safe (docs/log/51 §delivery). Because this is deliver-then-consume, there is a structural
// window where the append to the conversation succeeded but the process died before the
// ledger moved. The re-send is matched on row ids by the sink, so it does not post twice
// and only the ledger moves forward.
func TestReportReconcilerDeliveryIsIdempotent(t *testing.T) {
	m, sid, conv := ledgerFixture(t, "slot62")
	id := AddInstruction(m.Name, conv, "operator")
	status.PersistTurnEnd(sid, "idle")

	// First round: deliver through the real sink but do not move the ledger (i.e. it died).
	rows := openInstrRows(m.Name)
	if res := deliverReportCard(m.Name, conv, ReportKindAnswerReady, "", rows); res != reportSinkOK {
		t.Fatalf("delivery = %v", res)
	}
	if n := countReportCards(t, conv); n != 1 {
		t.Fatalf("report cards = %d", n)
	}
	if !SessionReportPending(m.Name) {
		t.Fatal("the row closed even though the ledger was never moved")
	}

	// Second round: the reconciler re-sends the same rows. No extra card, and the row becomes
	// reported.
	rc, clock := newFakeReconciler(t, reportTickDefault, deliverReportCard)
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if n := countReportCards(t, conv); n != 1 {
		t.Fatalf("re-sending the same rows grew the report cards to %d", n)
	}
	awaitReported(t, m.Name)

	// The idempotency key mixes in the reopen generation: a row reopened by compensation is
	// not mistaken for "already delivered" and is reported again on the real completion.
	// Keying on the row id alone would break Phase 3.
	if !reopenInstrRow(m.Name, id) {
		t.Fatal("cannot reopen")
	}
	reopened := openInstrRows(m.Name)
	if res := deliverReportCard(m.Name, conv, ReportKindAnswerReady, "", reopened); res != reportSinkOK {
		t.Fatalf("delivery after the reopen = %v", res)
	}
	if n := countReportCards(t, conv); n != 2 {
		t.Fatalf("report cards after the reopen = %d, want 2", n)
	}
}
