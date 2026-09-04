package chatx

// The settle decision for session reports — a level-driven reconciler (docs/log/51 Phase 1 /
// ADR 0035).
//
// v1 (docs/log/30) caught an edge (the Stop hook and friends) exactly once and consumed an
// irreversible single bit (the arm) in order to report. Every time mechanical idle drifted from
// semantic completion, the report vanished:
//   - a Stop fired right after a BG subagent started, consumed the arm, and the real completion
//     tens of minutes later was never reported — hence the pending waiter.
//   - that waiter then read the dozen seconds during which a bad idle heal had erased the marker
//     as "done" and consumed the arm — the real completion was rejected with armed=false.
//
// Phase 1 keeps the one-bit arm (session-report/<name>.json) as it is and concentrates only the
// consume decision here:
//
//   - hooks, the notify seam and the record-exit kick (POST /chat/report) are demoted to wake
//     hints. A kick delivers nothing and consumes nothing.
//   - a single goroutine in the server re-evaluates only the armed sessions, on a tick (15s by
//     default) plus hint wakeups, by LEVEL — the state currently written on disk.
//   - the settle predicate is "idle evidence >= 1 and busy evidence == 0" for two consecutive
//     ticks. No marker means UNKNOWN, not idle (this is what killed the v1 waiter).
//   - the arm is consumed only once delivery (the append to the conversation) has succeeded.
//     Dropping consume-then-deliver means a report whose append failed is retried next tick.
//
// The effect is that a miss degrades into a late report instead of a lost one: with every hint
// dead (a kick lost while the agent restarts, a drifting TUI string contract), the next tick sees
// the same state and picks it up. The decision is idempotent and re-evaluable, so a wrong "not
// yet" self-corrects on the next tick (the compensating reopen for a wrong "done" is Phase 3).

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

const (
	// reportTickDefault is the reconciler's sweep cadence. The steady-state cost is one readdir
	// when nothing is armed, so it can be short; settle demands two consecutive quiet ticks, so
	// losing the hints delays delivery by roughly 1-2 ticks (15-30s — no worse than the v1
	// waiter's 90s TTL: docs/log/51 §trade-offs).
	reportTickDefault = 15 * time.Second
	// reportSettleTicks is the debounce: settle only after observing quiet this many times in a
	// row. Temporal corroboration, so that one misread footer on the polling TUIs (kiro, cursor)
	// or a marker a heal erased for an instant cannot pass as "done".
	reportSettleTicks = 2
)

// reportSignals is the LEVEL snapshot the settle predicate consumes: reading every value the
// decision uses at once keeps the predicate itself a pure function (i.e. table-driven testable).
// Collection is the job of collectReportSignals, the side that touches files and tmux.
type reportSignals struct {
	MarkerState    string // status marker state. "" = no file = UNKNOWN
	MarkerTurnEnd  bool   // whether that idle was written at the end of a turn (status.TurnEnd)
	MarkerAfterArm bool   // marker written at or after the oldest unreported instruction = minimal progressed
	MarkerTS       string // that marker's RFC3339 (used to decide which instruction rows it covers)

	PendingQuestion   bool // waiting on a question (interim — not a completion at all)
	PendingPlan       bool // waiting on plan approval
	PendingPermission bool // waiting on tool permission

	SubagentBusy   bool // freshness of the BG subagent / Workflow jsonl (claude)
	TranscriptBusy bool // freshness of the main transcript (claude — covers thinking gaps)
	PaneBusy       bool // the pane's interrupt affordance (tmuxx.IsBusy, TUI only)

	Stopped bool   // stopped on purpose (rows are kept — the v1 rule is to report on completion after a resume)
	Exit    string // abnormal exit since the instruction (oom/crashed/killed) — a terminal fact
	ExitAt  string // that abnormal exit's RFC3339

	// SelfReported is the self-report fast path (docs/log/51 §self-report, Phase 3): the session
	// itself claimed through the af_report MCP tool that this instruction is finished. It is the
	// only signal that measures semantic completion directly, but it gets forgotten or called
	// early, so it must not be the backbone — busy evidence is stronger, and a claim while work
	// is still running does not settle.
	SelfReported bool
	SelfReportAt string // that claim's RFC3339
	// SelfReportAged is whether selfReportSettleDelay has passed since the claim. Reading the
	// claim ALONE as completion requires it (see the constant's comment below).
	SelfReportAged bool

	// Abort means the transcript tail ends in an API error, i.e. the turn ended in an
	// interruption (docs/log/47). claude does not fire the Stop hook then, so the marker does not
	// move at all. This used to be read only by the pace heal path (state != idle plus a pane
	// showing the waiting affordance), so after a bad heal erased the marker it was never
	// evaluated again and the interruption went unreported while the instruction hung (measured).
	// Read as a level, the entry point stops depending on state — the transcript tail IS the
	// state currently on disk, and it disappears naturally once the user resumes and the tail
	// changes.
	Abort       bool
	AbortReason string // turn-aborted (a resend fixes it) / turn-failed (pointless until the cause is fixed)
	AbortAt     string // RFC3339 at which that interruption was recorded

	// TailAborted is the fact that the transcript tail is an interruption, INDEPENDENT of time.
	// Unlike Abort it is not cut by since (this instruction's lower bound, or the report time in
	// the compensation path).
	//
	// The settle decision uses Abort — it needs the lower bound so that the previous
	// instruction's interruption does not close the current one. Compensation (reopen) is the
	// opposite: a lower bound makes it a false correction, because an interruption older than the
	// report is dropped and the transcript's freshness then looks like evidence that the session
	// started working after the report, when it is only the interruption record's own recency. v1
	// reported at the moment of the interruption, so the timestamps lined up and this was
	// accidentally safe; it surfaced once §4-6 made reports minutes late (caught by its own
	// regression test).
	TailAborted bool

	// AbortHeld means an interruption was found, but the Agent's own auto-resume has taken it on
	// (docs/log/47 §4-6). It only DELAYS the report, never swallows it: if the resume succeeds,
	// that turn's completion closes the instruction (one report instead of two); if the
	// interruption persists until the resume is capped, the hold is released and the interruption
	// is reported.
	//
	// The hold has to apply not only to Abort but to marker-derived idle evidence as well: there
	// is a shape of interruption that does fire Stop (the 429 usage limit — docs/log/47 §4-5),
	// and there the marker goes idle+turnEnd first, so dropping Abort alone would report it as a
	// plain completion. Hence it is checked at the entry of evalReportEvidence.
	AbortHeld bool

	HintReason string // the qualifier the latest hint carried (turn-failed / turn-aborted)
}

// reportVerdict is the predicate's answer for one session at one sweep.
type reportVerdict struct {
	Quiet    bool   // whether this sweep saw "idle evidence >= 1 and busy evidence == 0"
	Terminal bool   // abnormal exit — no debounce needed (the process is already dead)
	Fast     bool   // self-report present — shortens the debounce from 2 ticks to 1
	Kind     string // report kind (answer-ready / exit)
	Reason   string // report reason (turn-failed / turn-aborted / oom ...)
	At       string // RFC3339 of the evidence — used to decide which instruction rows it covers
	Why      string // the grounds for the decision (evidence names, for the log)
}

// busyEvidence lists the signals that forbid a settle. One of them is enough to mean "not yet".
func (s reportSignals) busyEvidence() []string {
	var ev []string
	switch s.MarkerState {
	case "working":
		ev = append(ev, "marker-working")
	case "question", "plan", "permission":
		// Interim states are not completions at all (v1 did not consume the arm for them either).
		ev = append(ev, "marker-"+s.MarkerState)
	}
	if s.PendingQuestion {
		ev = append(ev, "pending-question")
	}
	if s.PendingPlan {
		ev = append(ev, "pending-plan")
	}
	if s.PendingPermission {
		ev = append(ev, "pending-permission")
	}
	if s.SubagentBusy {
		ev = append(ev, "subagent-busy")
	}
	if s.TranscriptBusy {
		ev = append(ev, "transcript-busy")
	}
	if s.PaneBusy {
		ev = append(ev, "pane-busy")
	}
	return ev
}

// idleEvidence lists the signals that positively say "the instruction's turn ended".
// At least one is required — an empty state (no marker, state unknown) is never assumed idle.
// What counts is an explicit idle with all three parts:
//   - the marker exists and state=="idle" (written by the Stop hook / MarkTurnEnd)
//   - it came from the end of a turn (TurnEnd). The SessionStart idle reset and a managed runtime
//     loss (TurnUnknown) write the same "idle", so the state string alone cannot tell "finished"
//     from "unknown".
//   - it was written at or after the instruction (arm) = minimal progressed. The lower bound that
//     stops a leftover end-of-turn marker from the previous turn reading as this instruction's
//     completion.
//
// Phase 3 adds the self-report (af_report) as a second kind of evidence. It is only LISTED
// alongside the marker, never made stronger than busy evidence — an early claim degrades into
// "held until busy evidence reaches zero".
func (s reportSignals) idleEvidence() []string {
	var ev []string
	if s.markerIdle() {
		ev = append(ev, "marker-idle")
	}
	if s.SelfReported && s.SelfReportAged {
		ev = append(ev, "self-report")
	}
	if s.Abort {
		ev = append(ev, "abort")
	}
	return ev
}

// markerIdle reports whether the status marker itself is the explicit idle with all three parts.
func (s reportSignals) markerIdle() bool {
	return s.MarkerState == "idle" && s.MarkerTurnEnd && s.MarkerAfterArm
}

// evidenceAt is the time the settle evidence was observed — the basis for "only the instruction
// rows submitted by this time can complete on this quiet period" (instrRowsCoveredBy). The
// marker's write time when the marker is up, the claim's time when only a self-report is there;
// with both, the marker wins (it points at the end of the turn itself, the stronger evidence).
func (s reportSignals) evidenceAt() string {
	if s.markerIdle() {
		return s.MarkerTS
	}
	if s.Abort && s.AbortAt != "" {
		return s.AbortAt
	}
	if s.SelfReported && s.SelfReportAged {
		return s.SelfReportAt
	}
	return ""
}

// evalReportEvidence is the settle predicate (docs/log/51 §settled/progressed predicates).
// A pure function: decided by its arguments alone. The two-tick debounce is the caller's (the
// reconciler's) responsibility; this answers "is this one sweep quiet".
func evalReportEvidence(s reportSignals) reportVerdict {
	// An abnormal exit is a terminal fact. The process is dead, so waiting for busy evidence to
	// disappear is pointless (the transcript stays fresh right up to the death), and there is no
	// debounce either. Reading ExitInfo as a level also catches the abnormal death of a managed
	// daemon, which has no kick.
	if s.Exit != "" {
		return reportVerdict{
			Quiet: true, Terminal: true, Kind: "exit", Reason: s.Exit,
			At: s.ExitAt, Why: "exit:" + s.Exit,
		}
	}
	if s.Stopped {
		// Stopping is not a cancellation of the instruction (that is stop_session's disarm). Keep
		// the arm and report on the completion after the resume (the docs/log/30 rule).
		return reportVerdict{Why: "stopped"}
	}
	if s.AbortHeld {
		// Auto-resume has taken the interruption on (docs/log/47 §4-6). Do not say "finished"
		// yet — only the abnormal exit above is checked first: if the process is dead there is
		// nobody left to resume.
		return reportVerdict{Why: "abort-held"}
	}
	if busy := s.busyEvidence(); len(busy) > 0 {
		return reportVerdict{Why: "busy:" + strings.Join(busy, ",")}
	}
	idle := s.idleEvidence()
	if len(idle) == 0 {
		return reportVerdict{Why: "unknown"} // unknown stays unknown — never defaulted to idle
	}
	// An interruption says why the turn ended straight from the transcript, so it wins over the
	// hint (hook-derived, and normally empty because an interruption fires no hook).
	reason := s.HintReason
	if s.Abort {
		reason = s.AbortReason
	}
	return reportVerdict{
		Quiet: true, Fast: s.SelfReported, Kind: ReportKindAnswerReady, Reason: reason,
		At: s.evidenceAt(), Why: "idle:" + strings.Join(idle, ","),
	}
}

// evalReportResumed is the COMPENSATION predicate (docs/log/51 §compensation): evidence that the
// session started working AFTER a completion report. It reads the same column of busy evidence as
// the settle predicate, but the direction is reversed, so one more condition is needed —
// everything that already looked that way at the time of the report has to be excluded. The
// marker-derived signals are cut by write time (with since = the report time, MarkerAfterArm
// means "a marker later than the report"). The freshness-based evidence (subagent, transcript,
// pane) was necessarily false when the report went out (no report is emitted while busy evidence
// is non-zero), so if it is true now there has been a new write.
func evalReportResumed(s reportSignals) []string {
	// If evidence that the turn ENDED arrives after the report, it is not work resuming: it is
	// the late arrival of the very completion just reported (or the fact that that turn ended in
	// an interruption). Without telling the two apart, freshness evidence — the last assistant
	// line, written seconds after the report — produces a FALSE correction ("the completion
	// report was premature, work is still running") and the next tick reports the same completion
	// again (measured: report at 09:59:34, the real answer written at 09:59:50, correction at
	// 10:00:08, the same content reported again at 10:00:34). On a genuine resume the newest
	// marker is on the working / question side, where the column below picks it up.
	if s.markerIdle() || s.TailAborted {
		return nil
	}
	var ev []string
	switch s.MarkerState {
	case "working", "question", "plan", "permission":
		if s.MarkerAfterArm {
			ev = append(ev, "marker-"+s.MarkerState)
		}
	}
	if s.PendingQuestion {
		ev = append(ev, "pending-question")
	}
	if s.PendingPlan {
		ev = append(ev, "pending-plan")
	}
	if s.PendingPermission {
		ev = append(ev, "pending-permission")
	}
	if s.SubagentBusy {
		ev = append(ev, "subagent-busy")
	}
	if s.TranscriptBusy {
		ev = append(ev, "transcript-busy")
	}
	if s.PaneBusy {
		ev = append(ev, "pane-busy")
	}
	return ev
}

// collectReportSignals reads the current level state for one session with open
// instruction rows. since is the cursor of the oldest unreported instruction (the docs/log/51
// §progressed lower bound) — evidence older than that belongs to the previous instruction and
// cannot complete this one. selfAt is the time of the self-report (empty if none); a claim older
// than since belongs to the previous instruction and is discarded, applying to the self-report
// the same progressed lower bound the marker gets.
func collectReportSignals(m session.Meta, since, hintReason, selfAt string) reportSignals {
	sid := session.UUID(m.Dir, m.Name)
	s := reportSignals{Stopped: m.StoppedAt != "", HintReason: hintReason}
	if selfAt != "" && !reportTimeBefore(selfAt, since) {
		s.SelfReported, s.SelfReportAt = true, selfAt
		if at, err := time.Parse(time.RFC3339, selfAt); err == nil {
			s.SelfReportAged = time.Since(at) >= selfReportSettleDelay
		}
	}
	var markerAt time.Time
	if st, ok := status.Read(sid); ok {
		s.MarkerState, s.MarkerTurnEnd, s.MarkerTS = st.State, st.TurnEnd, st.TS
		s.MarkerAfterArm = !reportTimeBefore(st.TS, since)
		if t, err := time.Parse(time.RFC3339, st.TS); err == nil && st.TurnEnd {
			markerAt = t
		}
	}
	if q, ok := status.ReadPendingQuestion(sid); ok && len(q) > 0 {
		s.PendingQuestion = true
	}
	if p, ok := status.ReadPendingPlan(sid); ok && p != "" {
		s.PendingPlan = true
	}
	if p, ok := status.ReadPendingPermission(sid); ok && p != "" {
		s.PendingPermission = true
	}
	// Subagent and main-transcript freshness are claude-specific signals (no other kind has an
	// equivalent transcript).
	if normalizeKind(m.Kind) == session.KindClaude {
		s.SubagentBusy = claude.SubagentBusy(sid)
		s.TranscriptBusy = reportTranscriptBusy(sid, markerAt)
		collectAbortSignal(&s, m.Name, sid, since)
	}
	if e, ok := status.ReadExit(m.Name); ok {
		switch e.Reason {
		case "oom", "crashed", "killed":
			// A death before the instruction belongs to the previous instruction (startup clears
			// Reason in the baseline, but cut by time as well so a surviving record cannot be
			// mistaken for this one). This is the single place that requires the timestamp to be
			// READABLE: an abnormal exit consumes the arm with no debounce, so resolving an
			// unparseable time as "this death" risks a false report against a live session (every
			// writer always fills At).
			if _, err := time.Parse(time.RFC3339, e.At); err == nil && !reportTimeBefore(e.At, since) {
				s.Exit, s.ExitAt = e.Reason, e.At
			}
		}
	}
	return s
}

// selfReportSettleDelay is how long a session must stay quiet AFTER calling af_report
// before that self-report alone may settle the instruction (docs/log/51 §self-report).
//
// The claim is the only signal that measures semantic completion directly, but it is sometimes
// made EARLY: a session says "finished" and then keeps writing its final answer. Measured: the
// real answer arrived 2 minutes 22 seconds after the claim; during that gap the transcript was
// silent for 142 seconds (past the 90s freshness TTL) and the pane's spinner was unreadable
// because of the agent's own rendering, so every piece of busy evidence vanished and a premature
// completion report went out.
//
// The normal path never pays this delay: when the turn ends after the claim, the Stop hook writes
// the end-of-turn marker and that (marker-idle) settles immediately. The window only matters when
// the marker never arrives and the claim is the only handle, and there avoiding a false report is
// worth minutes of delay. The value is the observed thinking gap (142s) plus margin.
const selfReportSettleDelay = 3 * time.Minute

// collectAbortSignal reads the transcript tail for a turn that died on an API error
// (docs/log/47). The point is that it never looks at the marker: claude fires no Stop hook on an
// interruption, so the marker may be left "working", erased by a bad heal, or still holding the
// previous turn's idle. The interruption itself is written to the LEVEL that is the transcript
// tail, so only that is read.
//
// An interruption older than since belongs to the previous instruction and is discarded (the same
// lower bound as the marker and the self-report). A record with no timestamp is taken rather than
// cut — an interruption is "the tail currently on disk", so it points at the present state even
// when the time cannot be read.
func collectAbortSignal(s *reportSignals, name, sid, since string) {
	a, ok := claude.AbortInfo(sid)
	if !ok {
		return
	}
	s.TailAborted = true // the fact that is not cut by time (compensation reads it — see above)
	at := ""
	if !a.At.IsZero() {
		at = a.At.Format(time.RFC3339)
	}
	if at != "" && reportTimeBefore(at, since) {
		return
	}
	// An interruption a resend can fix is first retried by the Agent itself (docs/log/47 §4-6).
	// No report while that holds — one would run a whole assistant turn whose content ("resume
	// it") is already done. Once the resume is capped, holds goes false and this falls through to
	// the normal path.
	if abortResumeHolds(name, a, time.Now()) {
		s.AbortHeld = true
		return
	}
	s.Abort, s.AbortAt = true, at
	s.AbortReason = ReportReasonTurnFailed
	if a.Retryable {
		s.AbortReason = ReportReasonTurnAborted
	}
	// With the interruption at the tail, the transcript's recency belongs to that interruption
	// record itself — it is how the turn ended, not evidence of work in progress. Without
	// clearing it here, every interruption stalls the report until the freshness window (90s)
	// expires. The pane and subagent busy evidence stand for different facts (a resume happened, a
	// BG task is running), so they are left alone.
	s.TranscriptBusy = false
}

// reportMarkerGrace absorbs the RFC3339 second-truncation of the status marker when
// comparing it against a transcript record's timestamp: the Stop hook's marker and that turn's
// last transcript write can land in the same second, so only an append later than this margin
// counts as "the transcript kept growing after the marker".
const reportMarkerGrace = 2 * time.Second

// reportTranscriptBusy reports whether the main transcript says the turn is still
// running. The v1 waiter looked at bare freshness (90s) — all it could do with no corroboration
// from Stop, but as a permanent gate on the completion decision it delays EVERY healthy
// completion report by 90s. Phase 1 has positive evidence in the end-of-turn marker Stop wrote,
// so the test becomes relative: if the transcript grew after the marker, the turn is still going
// (i.e. that marker was not the end — the thinking-gap case). Freshness is still required as a
// safety valve, so that a stalled transcript (the same 90s ceiling as v1) cannot let a
// disagreement in the comparison block the report forever.
//
// TranscriptTouched returns the time of the last user/assistant line (bookkeeping lines do not
// move it). It is read once and used both for freshness and for the relative comparison.
func reportTranscriptBusy(sid string, marker time.Time) bool {
	at, ok := claude.TranscriptTouched(sid)
	if !ok || !claude.TranscriptFresh(at) {
		return false // a stalled transcript is no evidence of "running" (same ceiling as v1)
	}
	if marker.IsZero() {
		return true // no end-of-turn marker: freshness is the only handle (the v1 waiter's position)
	}
	return at.After(marker.Add(reportMarkerGrace))
}

// reportPaneBusy checks the pane's interrupt affordance (the same grounds as the reverse heal).
// It hits tmux, so it runs only for a settle candidate (docs/log/51: keep the tmux load down).
// It is limited to claude's TUI because tmuxx.IsBusy reads claude's spinner contract (the same
// scope as the v1 waiter): misreading another kind's pane as busy would mean that kind's reports
// never come out at all, and a loss is worse than a delay.
func reportPaneBusy(m session.Meta) bool {
	if m.DriverKind() == session.DriverManaged || normalizeKind(m.Kind) != session.KindClaude {
		return false
	}
	return tmuxx.IsBusy(m.Name)
}

// reportTimeBefore reports whether RFC3339 a is strictly before b. An undecidable comparison
// (empty or malformed) falls to false, i.e. "not older": discarding genuine completion evidence
// because a timestamp cannot be parsed means the report never comes out (v1's loss mode exactly).
func reportTimeBefore(a, b string) bool {
	ta, err1 := time.Parse(time.RFC3339, a)
	tb, err2 := time.Parse(time.RFC3339, b)
	if err1 != nil || err2 != nil {
		return false
	}
	return ta.Before(tb)
}

// --- Sink (delivery) ---------------------------------------------------------

// reportSinkResult is what the sink tells the reconciler about the delivery, and it is
// what decides whether the arm may be consumed (this is what closes gap D — the "exactly once"
// duty moves off the detection side, and the ledger advances only when delivery succeeded).
type reportSinkResult int

const (
	reportSinkOK    reportSinkResult = iota // appended -> consume the arm
	reportSinkRetry                         // append failed -> keep the arm, retry next tick
	reportSinkDrop                          // the target conversation is gone -> nowhere to deliver, fold the rows
)

// reportSink is the delivery seam (tests substitute a fake to drive the reconciler
// without a conversation store). rows are the folded instruction rows — the idempotency key (the
// row ID) and the "N instructions" body are assembled by the sink.
type reportSink func(name, convID, kind, reason string, rows []instrRow) reportSinkResult

// deliverReportCard is the production sink: the append to the conversation is synchronous (so a
// failure can be returned), while the operator's automatic turn goes to the debouncer
// (chat_report_autoturn.go — it bundles nearby reports into one turn). It fires on the timer
// goroutine, so a provider call taking minutes does not block the reconciler's single goroutine.
func deliverReportCard(name, convID, kind, reason string, rows []instrRow) reportSinkResult {
	res := recordSessionReport(name, convID, kind, reason, rows)
	if res == reportSinkOK && uiprefs.ChatAutoTurn() && !quietReport(kind, reason) {
		reportAutoTurns.schedule(convID)
	}
	return res
}

// quietReport reports whether quiet completion reports (Settings > Assistant, off by default)
// suppress the automatic turn for this report. That covers only a NORMAL completion (answer-ready
// with no reason) and its correction (reopened with no reason — in quiet mode there is no "it is
// finished" utterance to take back). The report card and the delivery to the notification centre
// stay immediate, and the report itself remains undelivered so it rides along on the next turn
// (the user speaking, or another report's automatic turn) via injectPendingReports — only the
// LLM's follow-up turn disappears. The abnormal cases (interruption, failure, exit) and a capped
// correction (reopen-capped) still run, because they need the operator to judge and act (auto
// resume, explaining the cause).
func quietReport(kind, reason string) bool {
	if !uiprefs.ChatQuietCompletion() {
		return false
	}
	return reason == "" && (kind == ReportKindAnswerReady || kind == reportKindReopened)
}

// --- The reconciler itself ---------------------------------------------------

// reportClock is the reconciler's time source (a seam, so a fake clock can drive the debounce and
// the retries in time).
type reportClock interface {
	Now() time.Time
	Ticker(d time.Duration) (<-chan time.Time, func())
}

type reportRealClock struct{}

func (reportRealClock) Now() time.Time { return time.Now() }

func (reportRealClock) Ticker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// reportSettleState is the per-session debounce counter: how many sweeps in a row were quiet, and
// when that quiet period started.
type reportSettleState struct {
	quiet      int
	quietSince time.Time
}

type reportReconciler struct {
	interval time.Duration
	clock    reportClock
	sink     reportSink
	wake     chan struct{}
	swept    chan struct{} // tests only: one notification per completed sweep (nobody reads it in production)

	mu     sync.Mutex
	hints  map[string]string // name -> the latest hint's reason (turn-failed / turn-aborted)
	selfs  map[string]string // name -> RFC3339 of the self-report (af_report)
	states map[string]reportSettleState
}

func newReportReconciler(interval time.Duration) *reportReconciler {
	return &reportReconciler{
		interval: interval,
		clock:    reportRealClock{},
		sink:     deliverReportCard,
		wake:     make(chan struct{}, 1),
		swept:    make(chan struct{}, 1),
		hints:    map[string]string{},
		selfs:    map[string]string{},
		states:   map[string]reportSettleState{},
	}
}

// reportRec is the process-wide reconciler. The decision is concentrated into one goroutine in one
// process (which is what makes v1's reportArmMu / reportWaiters / generation races go away). The
// same variable exists in the hook subprocess, but run() is not going there, so hint() is only a
// memo (the real delivery is done by the server process's tick).
var reportRec = newReportReconciler(reportTickDefault)

// StartReportReconciler launches the sweep loop (once, at main's startup).
func StartReportReconciler() { go reportRec.run(context.Background()) }

func (rc *reportReconciler) run(ctx context.Context) {
	tick, stop := rc.clock.Ticker(rc.interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case <-rc.wake:
		}
		rc.sweep(rc.clock.Now())
		select {
		case rc.swept <- struct{}{}:
		default:
		}
	}
}

// hint wakes the reconciler for a kick that used to deliver a report itself
// (POST /chat/report, the notify seam, record-exit). A hint is only a "go and look now" wakeup,
// never decision material itself — the one thing it carries is the answer-ready qualifier
// (interruption vs failure, which the marker cannot express; losing it degrades to a plain
// completion report, i.e. missing information rather than a lost report).
func (rc *reportReconciler) hint(name, kind, reason string) {
	if kind == ReportKindAnswerReady && reason != "" {
		rc.mu.Lock()
		rc.hints[name] = reason
		rc.mu.Unlock()
	}
	rc.nudge()
}

// selfReport records the session's own claim that it is finished (docs/log/51 §self-report fast
// path) and wakes the sweep. This is NOT the backbone — all that is recorded is the time of the
// claim, and the report itself is still decided by the reconciler's predicate. With no claim the
// settle picks it up; too early a claim is stopped by busy evidence.
//
// Holding it in process memory is deliberate: if the agent dies the claim is lost, but all that
// degrades is the speedup from 2 ticks to 1 — whether a report happens at all is held by the
// ledger on disk. Persisting the fast path's state would put a second source of truth next to the
// ledger, which costs more.
func (rc *reportReconciler) selfReport(name string, at time.Time) {
	if !session.ValidName(name) {
		return
	}
	rc.mu.Lock()
	rc.selfs[name] = at.Format(time.RFC3339)
	rc.mu.Unlock()
	rc.nudge()
}

func (rc *reportReconciler) selfReportFor(name string) string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.selfs[name]
}

// nudge wakes the sweep loop without blocking (wake has capacity 1, so bursts collapse).
func (rc *reportReconciler) nudge() {
	select {
	case rc.wake <- struct{}{}:
	default:
	}
}

// forget drops the per-session bookkeeping — called both on a new instruction (re-arm) and once a
// report has been delivered.
func (rc *reportReconciler) forget(name string) {
	rc.mu.Lock()
	delete(rc.states, name)
	delete(rc.hints, name)
	delete(rc.selfs, name)
	rc.mu.Unlock()
}

func (rc *reportReconciler) hintFor(name string) string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.hints[name]
}

// debounce records one quiet observation and reports whether the settle may fire:
// quiet observed reportSettleTicks times in a row, and at least one tick interval elapsed since
// the first observation. The time condition is what stops hint wakeups from running sweeps
// back to back and settling on "observed twice" (the interval IS the debounce).
func (rc *reportReconciler) debounce(name string, now time.Time) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	st := rc.states[name]
	if st.quiet == 0 {
		st.quietSince = now
	}
	st.quiet++
	rc.states[name] = st
	return st.quiet >= reportSettleTicks && !now.Before(st.quietSince.Add(rc.interval))
}

// resetSettle clears the debounce for a session that is demonstrably not quiet.
// The hint's qualifier is dropped too: if the next turn started before the interruption was
// reported, that interruption is no longer "the current state".
//
// The self-report is NOT dropped. It is not a statement about the current state but a
// per-instruction claim that "as far as I am concerned this instruction is finished", so expiring
// it on an early call (still moving after the claim) would make the fast path work only when the
// session called it after the model emitted its last token. The claim is dropped when a new
// instruction arrives and when delivery is done (both go through forget).
func (rc *reportReconciler) resetSettle(name string) {
	rc.mu.Lock()
	if _, ok := rc.states[name]; ok {
		delete(rc.states, name)
	}
	delete(rc.hints, name)
	rc.mu.Unlock()
}

// sweep re-evaluates every session with open instruction rows once, then compensates
// the sessions whose recent completion report may have been premature (docs/log/51
// §compensation).
func (rc *reportReconciler) sweep(now time.Time) {
	pending, grace := instrSweepSessions(now)
	for _, name := range pending {
		rc.evaluate(name, now)
	}
	for _, name := range grace {
		rc.compensate(name, now)
	}
	rc.prune(pending)
}

// prune drops bookkeeping for sessions with no open rows left (reported, cancelled, or the
// session was deleted).
func (rc *reportReconciler) prune(armed []string) {
	live := make(map[string]bool, len(armed))
	for _, n := range armed {
		live[n] = true
	}
	rc.mu.Lock()
	for name := range rc.states {
		if !live[name] {
			delete(rc.states, name)
		}
	}
	for name := range rc.hints {
		if !live[name] {
			delete(rc.hints, name)
		}
	}
	for name := range rc.selfs {
		if !live[name] {
			delete(rc.selfs, name)
		}
	}
	rc.mu.Unlock()
}

// evaluate is the whole decision for one session: read the unreported instruction rows, gather
// the evidence, run the predicate, debounce, deliver only the rows the evidence covers, and mark
// reported only the rows that were delivered.
//
// The point of docs/log/51 Phase 2 is the last two steps. The decision is PER SESSION ("is the
// session quiet"), but the duty to report is PER INSTRUCTION, so an instruction submitted AFTER
// the quiet evidence (the idle marker / ExitInfo) cannot complete on that same quiet period. Its
// row stays pending and is reported at the end of the next turn — gap A, where v1 overwrote the
// arm and lost it, falls out of the definition here as "a row that cannot disappear".
func (rc *reportReconciler) evaluate(name string, now time.Time) {
	open := openInstrRows(name)
	if len(open) == 0 {
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		return // no meta means no material to decide on (keep the rows — better than a false report)
	}
	sig := collectReportSignals(m, open[0].Cursor.At, rc.hintFor(name), rc.selfReportFor(name))
	v := evalReportEvidence(sig)
	if v.Quiet && !v.Terminal && reportPaneBusy(m) {
		sig.PaneBusy = true
		v = evalReportEvidence(sig)
	}
	if !v.Quiet {
		rc.resetSettle(name)
		return
	}
	covered := instrRowsCoveredBy(open, v.At)
	if len(covered) == 0 {
		// The evidence is quiet, but every instruction was submitted after it (i.e. none has
		// started running yet). Do not accumulate debounce either — wait for the next real end
		// of turn.
		rc.resetSettle(name)
		return
	}
	// Fast: with a self-report present the debounce shortens to one tick (docs/log/51 §fast path).
	// Temporal corroboration is needed because mechanical idle drifts from semantic completion,
	// and once the session itself says it is finished the two have not drifted. The busy-evidence
	// gate is still passed, so an early claim is not shortened (it never reaches Quiet at all).
	if !v.Terminal && !v.Fast && !rc.debounce(name, now) {
		return // not yet two consecutive ticks
	}
	retry := false
	delivered := false
	for _, conv := range instrConvs(covered) {
		rows := instrRowsForConv(covered, conv)
		switch rc.sink(name, conv, v.Kind, v.Reason, rows) {
		case reportSinkRetry:
			// Leave the ledger alone. The next tick comes back to the same decision and resends
			// (idempotent by row ID).
			retry = true
			continue
		case reportSinkDrop:
			log.Printf("session-report: %s: target conversation %s is gone — folding the rows", name, conv)
		default:
			delivered = true
		}
		markInstrReported(name, instrIDs(rows), now)
		log.Printf("session-report: settled %s kind=%s reason=%q rows=%v (%s)",
			name, v.Kind, v.Reason, instrIDs(rows), v.Why)
	}
	// The auto-resume counter (docs/log/47) counts PER-SESSION events. Adding one per conversation
	// when a single quiet period is delivered to several operator conversations would push a
	// session instructed from two conversations to the cap (2) on one interruption. What is
	// counted is the single fact "an interruption report was delivered", so it moves exactly once,
	// outside the conversation loop.
	if delivered && v.Kind == ReportKindAnswerReady {
		switch v.Reason {
		case ReportReasonTurnAborted:
			bumpAutoResume(name)
		case "":
			ResetAutoResume(name)
		}
	}
	if !retry {
		rc.forget(name)
	}
}

// reportReopenGrace is how long a reported row stays under compensation watch
// (docs/log/51 §compensation). The line drawn is that busy activity returning after this window
// is new work, not the continuation of a wrong report.
const reportReopenGrace = 10 * time.Minute

// compensate is the self-repair for a WRONG completion (docs/log/51 §compensation / ADR 0035
// decision 4).
//
// This was v1's asymmetry: consuming the arm by mistake meant the report never came again (a
// wrong consume was unrecoverable). In the ledger a report is only a row's state, so for the
// grace period the row is watched for "has the session started working since that report", and if
// it has, a correction is delivered BEFORE the row is reopened. From there the normal settle path
// reports the real completion.
//
// The correction-then-reopen order cannot be dropped: reversed, a correction whose delivery fails
// leaves the row silently reopened, which from the user's side is indistinguishable from v1's
// loss.
func (rc *reportReconciler) compensate(name string, now time.Time) {
	cands := instrReopenCandidates(ReadInstrRows(name), now, reportReopenGrace)
	if len(cands) == 0 {
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		return
	}
	// since = the newest report time among the watched rows. Only evidence later than this counts
	// as a resume.
	since := ""
	for _, r := range cands {
		if r.ReportedAt > since {
			since = r.ReportedAt
		}
	}
	sig := collectReportSignals(m, since, "", "")
	if sig.Stopped || sig.Exit != "" {
		return // stopped or exited abnormally is not "still running" (another row picks the exit up)
	}
	resumed := evalReportResumed(sig)
	if len(resumed) == 0 {
		if !reportPaneBusy(m) {
			return
		}
		sig.PaneBusy = true
		resumed = evalReportResumed(sig)
	}
	why := strings.Join(resumed, ",")
	reopened := false
	for _, conv := range instrConvs(cands) {
		var reopen, capped []instrRow
		for _, r := range instrRowsForConv(cands, conv) {
			if r.ReopenCount >= instrReopenMax {
				capped = append(capped, r)
			} else {
				reopen = append(reopen, r)
			}
		}
		if len(reopen) > 0 && rc.sink(name, conv, reportKindReopened, "", reopen) == reportSinkOK {
			for _, r := range reopen {
				if reopenInstrRow(name, r.ID) {
					reopened = true
				}
			}
			log.Printf("session-report: reopened %s rows=%v (%s)", name, instrIDs(reopen), why)
		}
		// Rows at the cap are not reopened. Cutting them off silently would leave only "the
		// report never comes", so the fact that the decision is oscillating is itself reported to
		// the user once (the same idiom as the auto-resume cap in docs/log/47). Delivery is
		// idempotent by row ID, so this does not repeat every tick.
		if len(capped) > 0 {
			rc.sink(name, conv, reportKindReopened, reportReasonReopenCapped, capped)
		}
	}
	if reopened {
		// A reopened row is an instruction that is yet to complete, so the decision starts clean.
		rc.forget(name)
	}
}

// InstallReconcilerForTest is the installation seam for tests. It starts a reconciler at the given
// interval, installs it as the package's live one, and returns the teardown function.
//
// Why it is exported: the reconciler itself (`reportRec` / `run`) lives inside chatx, but the
// three tests that remain in main (`TestSessionReport*`) go end-to-end over real HTTP from
// claude's Stop hook, and a real reconciler running is a premise of what they check. Avoiding
// this seam would mean changing how those tests are driven, from "run the real thing" to
// "substitute the schedule", which shrinks the set of bugs they can catch (README §4, #310).
// Taking `reportRec` under another name as a var is NOT possible — it would be a copy, and the
// reassignment would not reach it.
func InstallReconcilerForTest(interval time.Duration) (stop func()) {
	rc := newReportReconciler(interval)
	old := reportRec
	reportRec = rc
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); rc.run(ctx) }()
	return func() {
		cancel()
		<-done
		reportRec = old
	}
}
