package memreview

// The trigger (ADR 0117 decision 1): a level-triggered loop in the Agent, not tied to a Console
// poll. On every wake-up it compares the world with the persisted state and acts on the difference,
// so a restart, a stopped workspace or a missed tick only delays a review.
//
// A session is considered when ALL of these hold:
//   - the review switch is on, AF memory is on and the member's separate automatic-review switch
//     (memoryReviewAuto, off by default) is on - checked at every wake-up, between sessions and
//     windows, and again by the Reviewer before it claims and before it calls. Switching automatic
//     review off lets a helper call already in flight finish (its answer is queued like any other,
//     a failed one is not retried while the switch is off) and claims nothing after it. A manual
//     "Review now" needs only the review switch;
//   - the session has a transcript, is not archived, and its status shows a real end of turn - the
//     Source decides that from the evidence the status store keeps, never from an idle the Agent
//     merely assumes - at least Settle ago;
//   - at least MinTurns new turns, or MinPrompts new user prompts, exist after its watermark.
//
// A run that is open (claimed, or stopped half way) is resumed whatever the thresholds say: its
// window is fixed and its cost may already be spent.

import (
	"context"
	"errors"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// The numbers of the owner-confirmed P0 caps (docs/log/134 section 6).
const (
	// DefaultSettle is how long after a turn ended the session must have been quiet.
	DefaultSettle = 300 * time.Second
	// MinTurns and MinPrompts are the trigger: 30 turns or 3 user prompts, whichever comes first.
	MinTurns   = 30
	MinPrompts = 3
	// DefaultInterval is the wake-up period.
	DefaultInterval = time.Minute
	// maxWindowsPerTrigger bounds how many windows one trigger works through in a row; the rest of
	// a very long transcript is picked up by the next trigger.
	maxWindowsPerTrigger = 10
	// maxSessionsPerTick keeps one wake-up short; the rest wait for the next.
	maxSessionsPerTick = 5
	// DefaultLookback is how far back an automatic review looks for a turn end: a session whose
	// last turn ended longer ago is left to "Review now". It stops enabling the review from
	// spending the daily cap on every old session at once. Confirmed by the owner on 2026-10-18;
	// revisited from P1 traffic (#2333).
	DefaultLookback = 48 * time.Hour
)

// Candidate is a session the loop may review.
type Candidate struct {
	// Key is the session's identity (see Request.Key), Name its name.
	Key, Name string
	// EndedAt is when the newest turn ended according to the status evidence, zero when the status
	// does not show a finished turn (mid-turn, or an idle nobody can explain).
	EndedAt time.Time
}

// Source is what the loop needs from the session layer.
type Source interface {
	// Candidates lists the sessions that have a transcript and are not archived, with their
	// turn-end evidence. For the kinds that report no turn end themselves it polls their live
	// source, and it must never persist an end (see status.RecordTurnEnd).
	Candidates() []Candidate
	// Turns reads the session's whole conversation, numbered from 1 by position.
	Turns(c Candidate) ([]transcript.Turn, error)
}

// Loop drives Reviews from a Source.
type Loop struct {
	Reviewer *Reviewer
	Source   Source
	// Enabled is the switch (agentMemory and memoryReview); nil means uiprefs.MemoryReviewEnabled.
	Enabled func() bool
	// AutoEnabled gates the turn-end loop on top of Enabled; nil means uiprefs.MemoryReviewAutoEnabled,
	// or, when only Enabled is injected (tests), Enabled itself.
	AutoEnabled func() bool
	Now         func() time.Time
	Settle      time.Duration
	Interval    time.Duration
	Lookback    time.Duration

	mu      sync.Mutex
	running map[string]bool // by session name: a review of it is in progress
	wake    chan struct{}
	manual  map[string]ManualInfo // by session name: how the last "Review now" ended
	// ctx is the Agent's lifetime as far as the loop is concerned: Start sets it, Shutdown cancels
	// it, and every helper call, automatic or manual, runs under it (an exec.CommandContext on a
	// Background context would outlive the Agent as an orphaned CLI). wg counts the goroutines.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed bool
}

// NewLoop is the Loop the Agent runs.
func NewLoop(src Source) *Loop {
	return &Loop{Reviewer: New(), Source: src, Now: time.Now, Settle: DefaultSettle, Interval: DefaultInterval,
		Lookback: DefaultLookback, running: map[string]bool{}, wake: make(chan struct{}, 1)}
}

func (l *Loop) enabled() bool {
	if l.Enabled != nil {
		return l.Enabled()
	}
	return uiprefs.MemoryReviewEnabled()
}

func (l *Loop) autoEnabled() bool {
	if l.AutoEnabled != nil {
		return l.AutoEnabled() && l.enabled()
	}
	if l.Enabled != nil {
		return l.Enabled()
	}
	return uiprefs.MemoryReviewAutoEnabled()
}

// ensureCtxLocked creates the loop's cancellable context on first use (from Start's parent, or from
// Background when a manual review comes first) and returns it. Shutdown cancels it.
func (l *Loop) ensureCtxLocked(parent context.Context) context.Context {
	if l.ctx == nil {
		l.ctx, l.cancel = context.WithCancel(parent)
	}
	return l.ctx
}

func (l *Loop) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

// Shutdown stops the loop taking new work, cancels the reviews in flight (so the helper CLI is
// killed rather than left behind when the Agent exits) and waits for them up to budget. A review
// cut short stays `dispatched` in its run record, which the next start settles like any other lost
// attempt.
func (l *Loop) Shutdown(budget time.Duration) {
	l.mu.Lock()
	l.closed = true
	cancel := l.cancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(budget):
	}
}

func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Start runs the loop until ctx ends: one wake-up at once (this is what resumes a run a restart
// interrupted), then one per Interval or Wake.
func (l *Loop) Start(parent context.Context) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	ctx := l.ensureCtxLocked(parent)
	l.wg.Add(1)
	l.mu.Unlock()
	go func() {
		defer l.wg.Done()
		for {
			l.Tick(ctx)
			select {
			case <-ctx.Done():
				return
			case <-l.wake:
			case <-time.After(l.Interval):
			}
		}
	}()
}

// Wake asks for a wake-up now (the switch was just turned on).
func (l *Loop) Wake() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// examined remembers, durably, the turn end a session was last looked at for, so a restart does not
// parse every session's transcript again to learn there is nothing new.
type examined struct {
	Key string    `json:"key"`
	At  time.Time `json:"at"`
}

var examinedStore = store[examined]{"examined"}

// CountNew counts what was added after the watermark: turns and user prompts. A turn is a row of
// the normalized transcript other than a compaction summary (docs/log/134 section 1.1): the
// normalizer already emits only displayable rows, a thinking-only one included, whatever the review
// later leaves out of the prompt it builds. Turns are numbered from firstNumber.
func CountNew(turns []transcript.Turn, firstNumber, watermark int) (n, prompts int) {
	for i, t := range turns {
		if firstNumber+i <= watermark || t.Compact {
			continue
		}
		n++
		if t.Role == "user" {
			prompts++
		}
	}
	return n, prompts
}

// Tick is one wake-up.
func (l *Loop) Tick(ctx context.Context) {
	if l.isClosed() {
		return
	}
	// Owed releases are finished whether or not the review is on: a rejection must not outlive the
	// memory it concerned because the member switched the review off.
	ReconcileReleases()
	ReconcileNotices(l.now())
	if !l.autoEnabled() {
		return
	}
	now := l.now()
	cands := l.Source.Candidates()
	byKey := map[string]Candidate{}
	for _, c := range cands {
		byKey[c.Key] = c
	}
	l.forgetGone(byKey)

	budget := maxSessionsPerTick
	// Open runs first: they are somebody's claimed window.
	for _, run := range runs() {
		if budget == 0 || ctx.Err() != nil || !l.autoEnabled() {
			return
		}
		c, ok := byKey[run.Key]
		if run.Terminal() || !ok {
			continue
		}
		budget--
		// Only the claimed window is resumed here. What came after it waits for its own turn end,
		// settle time and threshold like any other.
		if stop := l.drive(ctx, c, TriggerAuto, run.Last); stop {
			return
		}
	}
	// Then the sessions that have settled, oldest end first.
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].EndedAt.Before(cands[j].EndedAt) })
	for _, c := range cands {
		if budget == 0 || ctx.Err() != nil || !l.autoEnabled() {
			return
		}
		if c.EndedAt.IsZero() || now.Sub(c.EndedAt) < l.Settle || now.Sub(c.EndedAt) > l.Lookback {
			continue
		}
		if _, open := unfinishedRun(c.Key); open {
			continue // handled above
		}
		if e, ok := examinedStore.Read(keyHash(c.Key)); ok && e.At.Equal(c.EndedAt) {
			continue
		}
		turns, err := l.Source.Turns(c)
		if err != nil {
			continue
		}
		if n, prompts := CountNew(turns, 1, Watermark(c.Key)); n < MinTurns && prompts < MinPrompts {
			_ = examinedStore.Write(keyHash(c.Key), examined{Key: c.Key, At: c.EndedAt})
			continue
		}
		budget--
		stop, done := l.driveTurns(ctx, c, turns, TriggerAuto)
		if done {
			_ = examinedStore.Write(keyHash(c.Key), examined{Key: c.Key, At: c.EndedAt})
		}
		if stop {
			return
		}
	}
}

// forgetGone drops the examined records of sessions that no longer exist.
func (l *Loop) forgetGone(live map[string]Candidate) {
	for _, h := range examinedStore.keys() {
		if e, ok := examinedStore.Read(h); ok {
			if _, ok := live[e.Key]; !ok {
				_ = examinedStore.Remove(h)
			}
		}
	}
}

// drive resumes or runs a session, reading its turns first.
func (l *Loop) drive(ctx context.Context, c Candidate, trigger string, upTo int) (stop bool) {
	turns, err := l.Source.Turns(c)
	if err != nil {
		return false
	}
	if upTo > 0 && len(turns) > upTo {
		turns = turns[:upTo]
	}
	stop, _ = l.driveTurns(ctx, c, turns, trigger)
	return stop
}

// driveTurns reviews windows of the session until its turns are done, the bound is reached or
// something stops the loop. stop says the rest of this wake-up should not go on (a global limit);
// done says the session was dealt with (so it need not be looked at again for this turn end).
func (l *Loop) driveTurns(ctx context.Context, c Candidate, turns []transcript.Turn, trigger string) (stop, done bool) {
	if !l.claimName(c.Name) {
		return false, false
	}
	defer l.releaseName(c.Name)
	for i := 0; i < maxWindowsPerTrigger; i++ {
		res, err := l.Reviewer.Review(ctx, Request{Key: c.Key, Session: c.Name, Trigger: trigger, Turns: turns, FirstNumber: 1})
		switch {
		case err == nil:
			if !res.Terminal() {
				return false, false // a retry waits for its backoff
			}
			if !res.More {
				return false, true
			}
		case errors.Is(err, ErrBackoff):
			return false, false
		case errors.Is(err, ErrBusy), errors.Is(err, ErrDisabled), errors.Is(err, ErrDailyCap),
			errors.Is(err, ErrQueueFull), errors.Is(err, ErrNoBackend), ctx.Err() != nil:
			logOnce(err)
			return true, false
		default:
			log.Printf("memory review: %s: %v", c.Name, err)
			return false, false
		}
	}
	return false, false
}

var (
	logMu   sync.Mutex
	logLast = map[string]time.Time{}
)

// logOnce writes a global condition (no backend, cap spent, queue full) to the log at most once an
// hour: it repeats at every wake-up until it clears.
func logOnce(err error) {
	logMu.Lock()
	defer logMu.Unlock()
	k := err.Error()
	if time.Since(logLast[k]) < time.Hour {
		return
	}
	logLast[k] = time.Now()
	log.Printf("memory review: waiting: %v", err)
}

func (l *Loop) claimName(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running[name] {
		return false
	}
	if l.running == nil {
		l.running = map[string]bool{}
	}
	l.running[name] = true
	return true
}

func (l *Loop) releaseName(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.running, name)
}

// Running reports whether a review of the named session is in progress in this loop.
func (l *Loop) Running(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.running[name]
}

// ErrNoSession is ReviewNow's answer for a name the Source does not list.
var ErrNoSession = errors.New("no such session to review")

// ErrShuttingDown is the answer once Shutdown has begun.
var ErrShuttingDown = errors.New("the Agent is shutting down")

// ReviewNow starts a manual review of the named session in the background and returns at once:
// a review is a model call and takes longer than a request should wait. It ignores the settle time,
// the thresholds and the backoff, and nothing else: the switch, the budget, the daily cap and
// the one-in-flight limit all apply. How it ended is in Status (Manual), including when there was
// nothing new to review: an answer the caller can wait for.
func (l *Loop) ReviewNow(name string) error {
	if l.isClosed() {
		return ErrShuttingDown
	}
	if !l.enabled() {
		return ErrDisabled
	}
	if inflight.Load() {
		return ErrBusy
	}
	var found *Candidate
	for _, c := range l.Source.Candidates() {
		if c.Name == name {
			c := c
			found = &c
		}
	}
	if found == nil {
		return ErrNoSession
	}
	if !l.claimName(name) {
		return ErrBusy
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		l.releaseName(name)
		return ErrShuttingDown
	}
	ctx := l.ensureCtxLocked(context.Background())
	l.wg.Add(1)
	l.mu.Unlock()
	go func() {
		defer l.wg.Done()
		defer l.releaseName(name)
		l.setManual(name, l.reviewManually(ctx, *found))
	}()
	return nil
}

// reviewManually works through the windows of one manual review and says how it ended: "done",
// "empty" (nothing new after the watermark), "unreadable" (the transcript could not be read), or
// the refusal code of the error that stopped it.
func (l *Loop) reviewManually(ctx context.Context, c Candidate) ManualInfo {
	turns, err := l.Source.Turns(c)
	if err != nil {
		log.Printf("memory review: %s: transcript unreadable", c.Name)
		return ManualInfo{Outcome: "unreadable"}
	}
	out := ManualInfo{Outcome: "done"}
	for i := 0; i < maxWindowsPerTrigger; i++ {
		res, err := l.Reviewer.Review(ctx, Request{Key: c.Key, Session: c.Name, Trigger: TriggerManual, Turns: turns, FirstNumber: 1})
		switch {
		case err != nil:
			out.Outcome = refusalCode(err) // what the earlier windows queued stays in the counts
			return out
		case res.State == "empty":
			if i == 0 {
				out.Outcome = "empty"
			}
			return out
		}
		out.Windows++
		out.Queued += res.Queued
		out.Skipped += res.Skipped
		if res.State != StateDone {
			out.Failed++ // parked, or waiting to retry
		}
		if !res.Terminal() || !res.More {
			return out
		}
	}
	return out
}

// ManualInfo is how the last "Review now" of a session ended: the outcome, and the totals over
// every window that review worked through (counts only; the last run alone says nothing of the
// others). A refusal part way keeps what the earlier windows queued.
type ManualInfo struct {
	At      time.Time `json:"at"`
	Outcome string    `json:"outcome"`
	Windows int       `json:"windows"`
	Queued  int       `json:"queued"`
	Skipped int       `json:"skipped"`
	// Failed counts the windows that ended without a usable reply (parked, or waiting to retry).
	Failed int `json:"failed"`
}

func (l *Loop) setManual(name string, info ManualInfo) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.manual == nil {
		l.manual = map[string]ManualInfo{}
	}
	info.At = l.now()
	l.manual[name] = info
}

// refusalCode is the closed vocabulary Status reports.
func refusalCode(err error) string {
	switch {
	case errors.Is(err, ErrDisabled):
		return "disabled"
	case errors.Is(err, ErrBusy):
		return "busy"
	case errors.Is(err, ErrDailyCap):
		return "daily_cap"
	case errors.Is(err, ErrQueueFull):
		return "queue_full"
	case errors.Is(err, ErrNoBackend):
		return "no_backend"
	case errors.Is(err, ErrBackoff):
		return "backoff"
	case errors.Is(err, context.Canceled), errors.Is(err, ErrShuttingDown):
		return "shutting_down"
	}
	return "failed"
}

// Info is a session's review status.
type Info struct {
	Running bool `json:"running"`
	// Last is the newest run of the session, nil when there is none.
	Last *LastRun `json:"last,omitempty"`
	// Manual is how the last "Review now" ended (see reviewManually), nil before the first.
	Manual *ManualInfo `json:"manual,omitempty"`
}

// LastRun is the newest run of a session, as counts and a state: no text.
type LastRun struct {
	State   string    `json:"state"`
	Queued  int       `json:"queued"`
	Skipped int       `json:"skipped"`
	Failure string    `json:"failure,omitempty"`
	Trigger string    `json:"trigger"`
	At      time.Time `json:"at"`
}

// Status reports on one session.
func (l *Loop) Status(name string) Info {
	info := Info{Running: l.Running(name)}
	for _, r := range runs() { // newest first
		if r.Session == name {
			info.Last = &LastRun{State: r.State, Queued: r.Queued, Skipped: r.Skipped, Failure: r.Failure, Trigger: r.Trigger, At: r.Updated}
			break
		}
	}
	l.mu.Lock()
	if m, ok := l.manual[name]; ok {
		info.Manual = &m
	}
	l.mu.Unlock()
	return info
}
