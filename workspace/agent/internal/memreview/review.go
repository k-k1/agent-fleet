package memreview

// reviewWindow end to end (ADR 0117 decisions 1, 3 and 8): claim one window durably, build the
// redacted input, make the helper call under the budget, validate the reply, queue the proposals,
// move the watermark. No trigger lives here (the loop and "Review now" are phase P1 part C's) and
// nothing is published: the output is the pending queue.
//
// Every step that cannot be repeated blindly is written down BEFORE it happens, so a stop at any
// point leaves a record that says what to do next:
//
//	claimed     the window is fixed          -> resume: make (or retry) the call
//	dispatched  an attempt is recorded       -> the call may have been billed; count it, then retry
//	                                            only within the budget
//	answered    the validated reply is kept  -> queue it; the helper is never asked again
//	done/failed the run has ended            -> nothing; the watermark follows it (and is repaired
//	                                            from it if a stop came in between)
//
// At most one review is in flight in the whole Agent, and at most one run is open per session.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// Triggers, the ledger's own values.
const (
	TriggerAuto   = "auto"
	TriggerManual = "manual"
)

// inflight is the one-review-at-a-time limit across sessions.
var inflight atomic.Bool

// Request is one review of one session.
type Request struct {
	// Key is the session's identity (the watermark's key: stable for the life of the session and
	// never reused); Session is its name (store lookup, ledger ref).
	Key, Session string
	Trigger      string
	// Turns are the session's turns from FirstNumber on, as the transcript reader returns them.
	// Turns at or below the watermark are ignored. After a restart the caller passes them again:
	// an open run rebuilds its fixed window from them.
	Turns       []transcript.Turn
	FirstNumber int
}

// Result is what a Review did.
type Result struct {
	RunID string
	State string // StateDone, StateFailed, or "empty" when there was nothing to review
	// Ops are the validated operations of the reply; Queued of them are new in the queue,
	// Skipped were already pending or rejected.
	Ops, Queued, Skipped int
	Failure              string
	Watermark            int
	// More is true when later windows of the same turns are still unreviewed.
	More bool
}

// Reviewer carries the seams. New wires the real ones; tests replace them.
type Reviewer struct {
	Now     func() time.Time
	Enabled func() bool
	// AutoEnabled is the member's separate switch for the turn-end loop (ui-prefs memoryReviewAuto).
	// It gates only TriggerAuto reviews, on top of Enabled; nil means the switch is not in play
	// (tests). A manual review never asks it.
	AutoEnabled func() bool
	Snapshot    func(session string) (memoryx.ReviewSnapshot, error)
	// Call makes one deny-all helper call under ctx's usage tag.
	Call func(ctx context.Context, persona, prompt string) (string, error)
	// Ledger finds the usage row of a lost attempt, if one was written.
	Ledger  func(LedgerQuery) LedgerHit
	Backoff time.Duration

	// hook is called when a stage has been persisted; tests use it to stop the process there.
	hook func(stage string)
}

// New is the Reviewer the Agent runs.
func New() *Reviewer {
	return &Reviewer{
		Now:         time.Now,
		Enabled:     uiprefs.MemoryReviewEnabled,
		AutoEnabled: uiprefs.MemoryReviewAuto,
		Snapshot:    memoryx.ReviewSnapshotFor,
		Call: func(ctx context.Context, persona, prompt string) (string, error) {
			return chatx.OneShotDenyAll(ctx, usagex.FeatureMemoryReview, chatx.OneShotShort, persona, prompt)
		},
		Ledger:  ledgerSpend,
		Backoff: DefaultBackoff,
	}
}

// allowed says whether a review with this trigger may start or continue now. It is asked before a
// window is claimed and again just before the helper call, so switching automatic review off stops
// the next claim and the next call; a call already running is not interrupted (see Loop).
func (r *Reviewer) allowed(trigger string) bool {
	if !r.Enabled() {
		return false
	}
	return trigger != TriggerAuto || r.AutoEnabled == nil || r.AutoEnabled()
}

// switchGate serializes the boundary where a review becomes real (a window is claimed, a call is
// recorded as dispatched) with the write of the member's prefs. Without it a review could pass its
// last check, the member turn automatic review off, and the call still start afterwards. It is held
// for a few file writes, never across a helper call.
var switchGate sync.Mutex

// WithSwitchWrite runs write (the prefs write) so that no claim or dispatch is halfway through
// while it happens: every boundary that follows sees the new switch.
func WithSwitchWrite(write func() error) error {
	switchGate.Lock()
	defer switchGate.Unlock()
	return write()
}

// underGate checks the switch and, if it allows the trigger, runs step, all under switchGate.
func (r *Reviewer) underGate(trigger string, step func() error) error {
	switchGate.Lock()
	defer switchGate.Unlock()
	if !r.allowed(trigger) {
		return ErrDisabled
	}
	return step()
}

func (r *Reviewer) stage(s string) {
	if r.hook != nil {
		r.hook(s)
	}
}

func (r *Reviewer) backoff() time.Duration {
	if r.Backoff > 0 {
		return r.Backoff
	}
	return DefaultBackoff
}

// Review reviews the oldest unreviewed window of a session, or resumes the run that is open for
// it. Errors (ErrBusy, ErrBackoff, ErrDailyCap, ErrQueueFull, ErrNoBackend, ErrDisabled, or a
// store error) leave the open run as it was; a run that ends, successfully or not, is a Result.
func (r *Reviewer) Review(ctx context.Context, req Request) (Result, error) {
	if req.Key == "" || req.Session == "" || (req.Trigger != TriggerAuto && req.Trigger != TriggerManual) {
		return Result{}, errors.New("a review needs a session key, a session name and an auto or manual trigger")
	}
	if !r.allowed(req.Trigger) {
		return Result{}, ErrDisabled
	}
	if !inflight.CompareAndSwap(false, true) {
		return Result{}, ErrBusy
	}
	defer inflight.Store(false)

	now := r.Now()
	run, open := unfinishedRun(req.Key)
	more := false
	if !open {
		if err := repairWatermark(req.Key, now); err != nil {
			return Result{}, err
		}
		wm := Watermark(req.Key)
		turns, first := trimTurns(req, wm)
		wins := BuildWindows(turns, first)
		if len(wins) == 0 {
			if last := req.FirstNumber + len(req.Turns) - 1; last > wm {
				if err := moveWatermark(req.Key, last, now); err != nil {
					return Result{}, err
				}
			}
			return Result{State: "empty", Watermark: Watermark(req.Key)}, nil
		}
		w := wins[0]
		more = len(wins) > 1
		if pendingCount() >= MaxPending {
			return Result{}, ErrQueueFull
		}
		run = Run{ID: runID(req.Key, w.Last), Key: req.Key, Session: req.Session, Trigger: req.Trigger,
			Base: wm, First: w.First, Last: w.Last, State: StateClaimed, Created: now}
		// The check and the save are one step against the switch being written (see switchGate):
		// a claim that passed first exists, one that did not claims nothing.
		if err := r.underGate(req.Trigger, func() error { return saveRun(&run, now) }); err != nil {
			return Result{}, err
		}
		r.stage("claimed")
		pruneRuns(now)
	}
	res, err := r.advance(ctx, &run, req)
	if err == nil && res.Terminal() {
		res.More = more || Watermark(req.Key) < req.FirstNumber+len(req.Turns)-1
	}
	return res, err
}

// Terminal reports whether the run the result describes has ended.
func (r Result) Terminal() bool { return r.State == StateDone || r.State == StateFailed }

// trimTurns drops the turns at or below the watermark and returns the number of the first kept.
func trimTurns(req Request, wm int) ([]transcript.Turn, int) {
	skip := wm + 1 - req.FirstNumber
	if skip <= 0 {
		return req.Turns, req.FirstNumber
	}
	if skip >= len(req.Turns) {
		return nil, req.FirstNumber + len(req.Turns)
	}
	return req.Turns[skip:], req.FirstNumber + skip
}

// windowOf rebuilds the fixed window of an open run from the caller's turns. It fails when the
// turns no longer contain it (the transcript was truncated or replaced) or build another window.
func windowOf(run Run, req Request) (Window, bool) {
	from, to := run.First-req.FirstNumber, run.Last-req.FirstNumber
	if from < 0 || to >= len(req.Turns) || from > to {
		return Window{}, false
	}
	ws := BuildWindows(req.Turns[from:to+1], run.First)
	if len(ws) == 0 || ws[0].First != run.First || ws[0].Last != run.Last {
		return Window{}, false
	}
	return ws[0], true
}

// advance drives an open run to its next resting point.
func (r *Reviewer) advance(ctx context.Context, run *Run, req Request) (Result, error) {
	now := r.Now()
	if run.State == StateAnswered {
		return r.finish(run) // the helper is never asked again for this window
	}
	if run.State == StateDispatched {
		r.settleLost(run)
		run.State = StateClaimed
		if err := saveRun(run, now); err != nil {
			return Result{}, err
		}
	}
	if run.used() >= MaxAttempts {
		return r.fail(run, "attempt budget spent")
	}
	if !r.allowed(req.Trigger) {
		return Result{}, ErrDisabled
	}
	if now.Before(run.NotBefore) && req.Trigger != TriggerManual {
		return Result{}, ErrBackoff
	}
	if pendingCount() >= MaxPending {
		return Result{}, ErrQueueFull
	}
	win, ok := windowOf(*run, req)
	if !ok {
		return r.fail(run, "window no longer available")
	}
	snap, err := r.Snapshot(run.Session)
	if err != nil {
		return Result{}, errors.New("the session's memory could not be read for the review")
	}
	in := BuildInput(win, snap)

	// The last check, the budget and the dispatch record are one step against the switch being
	// written (see switchGate): a call whose dispatch passed first is in flight and finishes; one
	// that did not never starts. The helper call itself runs outside the gate.
	if err := r.underGate(req.Trigger, func() error {
		if err := consumeCall(now); err != nil {
			return err
		}
		run.Attempts = append(run.Attempts, Attempt{N: len(run.Attempts) + 1, StartedAt: now, Outcome: OutcomeDispatched})
		run.State = StateDispatched
		if err := saveRun(run, now); err != nil {
			refundCall(now)
			return err
		}
		return nil
	}); err != nil {
		return Result{}, err
	}
	r.stage("dispatched")

	ctx = usagex.WithTag(ctx, usagex.Tag{Feature: usagex.FeatureMemoryReview, Trigger: req.Trigger, Ref: run.Session})
	reply, callErr := r.Call(ctx, in.Persona, in.Prompt)
	now = r.Now() // when the call ended: EndedAt is read off this, not off the start
	at := &run.Attempts[len(run.Attempts)-1]
	switch {
	case callErr != nil && ctx.Err() != nil:
		return Result{}, ctx.Err() // stopped mid-call: the record stays "dispatched" for recovery
	case errors.Is(callErr, chatx.ErrDenyAllUnavailable):
		// Nothing reached a model: no attempt spent, no call counted. The attempt written before
		// the call is taken back and only a counter remains, so an unusable pin cannot grow the
		// record by one entry every backoff.
		run.Attempts = run.Attempts[:len(run.Attempts)-1]
		run.Unsent++
		refundCall(now)
		run.State, run.NotBefore = StateClaimed, now.Add(r.backoff())
		if err := saveRun(run, now); err != nil {
			return Result{}, err
		}
		return Result{}, ErrNoBackend
	case callErr != nil:
		return r.retryOrFail(run, at, OutcomeFailed, "the helper call failed", now)
	}

	ops, perr := ParseReply(reply, in)
	if perr != nil {
		var re *ReplyError
		if errors.As(perr, &re) && re.Kind == ReplySecret {
			at.Outcome, at.EndedAt = OutcomeSecret, now // not retried: the helper echoed something it was shown
			return r.fail(run, "reply refused by the secret scan")
		}
		return r.retryOrFail(run, at, OutcomeMalformed, "the reply was dropped", now)
	}
	at.Outcome, at.EndedAt = OutcomeAnswered, now
	run.Ops, run.ProjectID, run.State = ops, snap.ProjectID, StateAnswered
	if err := saveRun(run, now); err != nil {
		return Result{}, err
	}
	r.stage("answered")
	return r.finish(run)
}

// retryOrFail ends an attempt that produced nothing usable: the run goes back to "claimed" with a
// backoff while budget remains, and is parked as failed (watermark moved) when it does not.
func (r *Reviewer) retryOrFail(run *Run, at *Attempt, outcome, failure string, now time.Time) (Result, error) {
	at.Outcome, at.EndedAt = outcome, now
	if run.used() >= MaxAttempts {
		return r.fail(run, failure)
	}
	run.State, run.NotBefore = StateClaimed, now.Add(r.backoff())
	if err := saveRun(run, now); err != nil {
		return Result{}, err
	}
	return Result{RunID: run.ID, State: StateClaimed, Failure: failure, Watermark: Watermark(run.Key)}, nil
}

// settleLost closes an attempt that was dispatched and never answered in this process: it is
// linked to its usage row when the ledger can say which row is its, and marked cost-unknown when it
// cannot. Linking the wrong row is worse than linking none, so doubt means unknown.
//
// The ledger stamps a row with the second the call finished, the attempt with the instant it
// started, so the search starts at the start's second. An earlier attempt of the same session (in
// this run or an earlier one) that ended in that very second could own a row stamped in it too, so
// then the search starts one second later. A call that finished within a second of starting is
// not a case worth risking the wrong row for.
func (r *Reviewer) settleLost(run *Run) {
	n := len(run.Attempts) - 1
	at := &run.Attempts[n]
	at.Outcome = OutcomeLost
	since := at.StartedAt.Truncate(time.Second)
	if end := latestEnd(run, at.StartedAt); !end.IsZero() && end.Truncate(time.Second).Equal(since) {
		since = since.Add(time.Second)
	}
	if end, open := latestOpenStart(run, at); open && at.StartedAt.Sub(end) < ledgerAttemptWindow {
		// An earlier attempt was lost: its end is unknown, so its call may have finished in this
		// attempt's window and the row cannot be told apart from this attempt's own.
		at.Cost = "unknown"
		return
	}
	if r.Ledger != nil {
		q := LedgerQuery{Session: run.Session, Since: since, Exclude: linkedCalls(run)}
		if hit := r.Ledger(q); hit.Found {
			at.Cost, at.Spend, at.Call = "linked", hit.Spend, hit.Call
			return
		}
	}
	at.Cost = "unknown"
}

// LedgerQuery asks the ledger for the one call of Session stamped from Since that is not in
// Exclude (the calls already linked to other attempts).
type LedgerQuery struct {
	Session string
	Since   time.Time
	Exclude map[string]bool
}

// LedgerHit is the answer: the call's summed spend and its id.
type LedgerHit struct {
	Spend int
	Call  string
	Found bool
}

// linkedCalls are the calls every attempt of the session (in any run) is already linked to.
func linkedCalls(run *Run) map[string]bool {
	out := map[string]bool{}
	add := func(as []Attempt) {
		for _, a := range as {
			if a.Call != "" {
				out[a.Call] = true
			}
		}
	}
	for _, other := range runs() {
		if other.Session == run.Session && other.ID != run.ID {
			add(other.Attempts)
		}
	}
	add(run.Attempts)
	return out
}

// latestOpenStart is the start of the latest earlier attempt of the session that was lost (its end
// is unknown) and began before at.
func latestOpenStart(run *Run, at *Attempt) (time.Time, bool) {
	var latest time.Time
	found := false
	consider := func(as []Attempt) {
		for i := range as {
			a := &as[i]
			if a != at && a.Outcome == OutcomeLost && a.StartedAt.Before(at.StartedAt) && (!found || a.StartedAt.After(latest)) {
				latest, found = a.StartedAt, true
			}
		}
	}
	for _, other := range runs() {
		if other.Session == run.Session && other.ID != run.ID {
			consider(other.Attempts)
		}
	}
	consider(run.Attempts)
	return latest, found
}

// latestEnd is the latest time an attempt of the run's session ended before limit, across every
// run the record still holds (the current run's earlier attempts included).
func latestEnd(run *Run, limit time.Time) time.Time {
	var latest time.Time
	consider := func(as []Attempt) {
		for _, a := range as {
			if !a.EndedAt.IsZero() && !a.EndedAt.After(limit) && a.EndedAt.After(latest) {
				latest = a.EndedAt
			}
		}
	}
	for _, other := range runs() {
		if other.Session == run.Session && other.ID != run.ID {
			consider(other.Attempts)
		}
	}
	consider(run.Attempts)
	return latest
}

// OnQueued is told when a run ends with proposals in the queue: the run, how many it queued and how
// many are pending in all. The Agent raises the Console notification from it (package main). It
// carries counts only. It returns an error when the notification could not be made durable; the
// run then keeps NotifyPending and ReconcileNotices tries again, so the notification is neither
// lost to a stop nor repeated (the Agent keys it by the run id).
var OnQueued func(runID string, queued, pending int) error

// finish queues the validated operations and ends the run. It is safe to repeat after a stop at
// any of its steps: proposals have derived ids, one that already got a decision is skipped, and
// the run is marked done only once everything is queued. When the queue has no room it stops with
// ErrQueueFull and the run stays `answered`, so nothing of the reply is lost and the helper is not
// asked again; the rest is queued when room appears.
//
// The run ends before the watermark moves (repairWatermark closes the gap after a stop), and the
// proposal text leaves the run record in the same write that ends it: from then on the queue is
// the only place it lives, and a decision deletes it there.
func (r *Reviewer) finish(run *Run) (Result, error) {
	now := r.Now()
	res := Result{RunID: run.ID, State: StateDone, Ops: len(run.Ops)}
	for i, op := range run.Ops {
		p := Proposal{ID: proposalID(run.ID, i), RunID: run.ID, Index: i, Session: run.Session, Op: op, Created: run.Created}
		p.Hash = OpHash(scopeProject(run, op), op)
		p.ProjectID = scopeProject(run, op)
		how, err := enqueue(p)
		if err != nil {
			return Result{}, err
		}
		if how == EnqueuedNew || how == EnqueuedExisting {
			res.Queued++
		} else {
			res.Skipped++
		}
	}
	r.stage("enqueued")
	run.State, run.Queued, run.Skipped, run.Ops = StateDone, res.Queued, res.Skipped, nil
	run.NotifyPending = res.Queued > 0 && OnQueued != nil
	if err := saveRun(run, now); err != nil {
		return Result{}, err
	}
	r.stage("ended")
	if err := moveWatermark(run.Key, run.Last, now); err != nil {
		return Result{}, err
	}
	notifyRun(run, now)
	stateMu.Lock()
	gcDecidedLocked()
	stateMu.Unlock()
	res.Watermark = Watermark(run.Key)
	return res, nil
}

// scopeProject is the project a proposal belongs to: the reviewed session's for a project-scoped
// operation, none for the user scope.
func scopeProject(run *Run, op Op) string {
	if op.Scope == "project" {
		return run.ProjectID
	}
	return ""
}

// fail parks the run. The ending is saved FIRST, in one write with the attempt's outcome, so a stop
// or a failed write after it can never make a later start take the attempt for a lost one and call
// again (a refused reply is not retried); the watermark then moves past the window, and
// repairWatermark does it on the next start if this process stopped before.
func (r *Reviewer) fail(run *Run, reason string) (Result, error) {
	now := r.Now()
	run.State, run.Failure, run.Ops = StateFailed, reason, nil
	if err := saveRun(run, now); err != nil {
		return Result{}, err
	}
	r.stage("ended")
	if err := moveWatermark(run.Key, run.Last, now); err != nil {
		return Result{}, err
	}
	return Result{RunID: run.ID, State: StateFailed, Failure: reason, Watermark: Watermark(run.Key)}, nil
}

// ledgerAttemptWindow is how long after an attempt started its usage row can appear in the
// search; a helper call is bounded far below it.
const ledgerAttemptWindow = 10 * time.Minute

// ledgerSpend looks for the usage row of one attempt: the memory.review rows of the session
// stamped from Since on, grouped by call (a call that used several models is split over several
// rows), minus the calls already linked to another attempt. Exactly one such call means it is the
// attempt's, and its rows are summed; none means there is no row; several mean it cannot be told
// which is the attempt's, and that is reported as not found.
func ledgerSpend(q LedgerQuery) LedgerHit {
	session, since := q.Session, q.Since
	spend := map[string]int{} // by call id; a row with none is a call of its own
	var order []string
	for _, day := range []string{since.UTC().Format("2006-01-02"), since.UTC().Add(24 * time.Hour).Format("2006-01-02")} {
		for i, row := range usagex.ReadDay(day) {
			if row.Feature != usagex.FeatureMemoryReview || row.Ref != session {
				continue
			}
			ts, err := time.Parse(time.RFC3339, row.TS)
			if err != nil || ts.Before(since) || ts.After(since.Add(ledgerAttemptWindow)) {
				continue
			}
			key := row.Call
			if key == "" {
				key = fmt.Sprintf("%s#%d", day, i)
			}
			if q.Exclude[key] {
				continue
			}
			if _, seen := spend[key]; !seen {
				order = append(order, key)
			}
			spend[key] += row.Spend
		}
	}
	if len(order) != 1 {
		return LedgerHit{}
	}
	return LedgerHit{Spend: spend[order[0]], Call: order[0], Found: true}
}

// notifyRun makes the run's Console notification durable and clears its pending mark. A failure is
// logged and leaves the mark for ReconcileNotices.
func notifyRun(run *Run, now time.Time) {
	if !run.NotifyPending || OnQueued == nil {
		return
	}
	// Everything this run proposed was decided before the notification went out: there is nothing
	// to announce (another run's proposals do not make this one's news).
	stateMu.Lock()
	mine := runHasPendingLocked(run.ID)
	stateMu.Unlock()
	if mine {
		if err := OnQueued(run.ID, run.Queued, pendingCount()); err != nil {
			log.Printf("memory review: the notification for a finished run was not made; it is retried")
			return
		}
	}
	run.NotifyPending = false
	if err := saveRun(run, now); err != nil {
		log.Printf("memory review: could not clear a finished notification mark")
	}
}

// ReconcileNotices finishes the notifications of runs that ended with proposals but whose
// notification was not made durable (a stop in between, or the outbox failing).
func ReconcileNotices(now time.Time) {
	for _, run := range runs() {
		if run.Terminal() && run.NotifyPending {
			r := run
			notifyRun(&r, now)
		}
	}
}
