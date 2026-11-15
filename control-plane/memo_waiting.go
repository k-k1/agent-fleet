package main

// Why a queue with notes waiting starts nothing (issue #4257, ADR 0137 section 7).
//
// The reason is produced by the decision the dispatcher makes, not by a second copy of its
// rules: memoWaitingFor is the one function that says whether a queue may fire now, called by the
// dispatcher behind the membership's lock (fireQueue) and by the list a member reads
// (memoQueueWaiting) with the same reads. Nothing is stored, so there is nothing to go stale
// when no Control Plane ticks, and a queue the dispatcher never looks at (the organisation's
// switch off, the category's own switch off) gets its reason by the same call.

import (
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The reasons, in the order the checks are made: the first that holds is the one reported.
const (
	waitTenantOff      = "tenant_off"
	waitDisabled       = "disabled"
	waitNoReport       = "no_report"
	waitPaused         = "paused"
	waitWindowClosed   = "window_closed"
	waitBelowThreshold = "below_threshold"
	waitQuietMinute    = "quiet_minute"
	waitDailyCap       = "daily_cap"
	waitBackoff        = "backoff"
	waitUsageLimit     = "usage_limit"
	waitNoPlace        = "no_place"
)

// memoWaiting is "waiting" on the wire: why a queue with notes waiting starts nothing now.
// Until is RFC 3339 and only present when the dispatcher will really act then.
type memoWaiting struct {
	Reason string             `json:"reason"`
	Until  string             `json:"until,omitempty"`
	Detail *memoWaitingDetail `json:"detail,omitempty"`
}

// memoWaitingDetail holds what a reason adds: Needed and Waiting for below_threshold, AgentKind
// for usage_limit, By ("category" or "member") for no_place.
type memoWaitingDetail struct {
	Needed    int    `json:"needed,omitempty"`
	Waiting   int    `json:"waiting,omitempty"`
	AgentKind string `json:"agentKind,omitempty"`
	By        string `json:"by,omitempty"`
}

// untilOf rounds a part of a second up: "until" is never earlier than the moment the dispatcher acts.
func untilOf(t time.Time) string {
	if t.Nanosecond() != 0 {
		t = t.Truncate(time.Second).Add(time.Second)
	}
	return t.UTC().Format(time.RFC3339)
}

// memoWaitInput is what memoWaitingFor reads besides the store.
type memoWaitInput struct {
	q        store.MemoQueue
	tenantOn bool
	now      time.Time
	// pending is read only when a threshold queue needs it, and at most once.
	pending func() (store.QueuePending, error)
	// skipNoReport leaves the stored no-report mark out: the dispatcher decides that one before
	// the lock, against the running workspace itself.
	skipNoReport bool
}

// memoWaitingFor says why the queue may not fire by itself now, nil when none of the rules holds
// (a lack of a place or a quota is then found by the claim, memoReserveAndClaim, and by
// memoPlaceTaken for the display). startDrain is openState's: a threshold queue begins to drain.
// Every check is a read, so their order changes which reason is reported and never whether
// the queue fires.
func memoWaitingFor(tx store.MemoWaitOps, in memoWaitInput) (w *memoWaiting, startDrain bool, err error) {
	q, now := in.q, in.now
	switch {
	case !in.tenantOn:
		return &memoWaiting{Reason: waitTenantOff}, false, nil
	case !q.Enabled:
		return &memoWaiting{Reason: waitDisabled}, false, nil
	case q.NoReportAt != "" && !in.skipNoReport:
		return &memoWaiting{Reason: waitNoReport}, false, nil
	case q.Paused:
		return &memoWaiting{Reason: waitPaused}, false, nil
	}
	var pending store.QueuePending
	if q.OpenCron == "" && q.OpenAfter > 0 && !q.Draining {
		if pending, err = in.pending(); err != nil {
			return nil, false, err
		}
	}
	open, start, why := openCheck(q, pending, now)
	if !open {
		return why, false, nil
	}
	fires, err := tx.CountDeliveriesCreatedSince(q.CategoryID, store.DeliveryTS(now.Add(-memoFiresWindow)))
	if err != nil {
		return nil, false, err
	}
	if limit := memoQueueFiresPerDay(q); fires >= limit {
		w := &memoWaiting{Reason: waitDailyCap}
		// The cap lifts when enough runs have left the 24 hours that fewer than the limit remain.
		at, ok, err := tx.NthOldestDeliverySince(q.CategoryID, store.DeliveryTS(now.Add(-memoFiresWindow)), fires-limit)
		if err != nil {
			return nil, false, err
		}
		if t, perr := time.Parse("2006-01-02T15:04:05.000000Z", at); ok && perr == nil {
			w.Until = untilOf(t.Add(memoFiresWindow))
		}
		return w, false, nil
	}
	held, err := autoHeld(tx, q, now)
	if err != nil || held != nil {
		return held, false, err
	}
	return nil, start, nil
}

// autoHeld says whether a fire by itself of the queue has to wait, from the stored state seen
// inside the transaction of the fire: a delivery of the queue closed as not sent lately (the
// back-off), or a session of the member waits for a usage limit under the queue's agent kind, in
// any session of any tree (the report's own list, not its strongest reason).
func autoHeld(tx store.MemoWaitOps, q store.MemoQueue, now time.Time) (*memoWaiting, error) {
	recent, err := tx.RecentQueueDeliveries(q.CategoryID, 8)
	if err != nil {
		return nil, err
	}
	if until, ok := notSentBackoff(recent); ok && now.Before(until) {
		return &memoWaiting{Reason: waitBackoff, Until: untilOf(until)}, nil
	}
	n, err := tx.CountLimitedDeliveries(scheduleInjectKind(q.AgentKind))
	if err != nil || n == 0 {
		return nil, err
	}
	return &memoWaiting{Reason: waitUsageLimit, Detail: &memoWaitingDetail{AgentKind: q.AgentKind}}, nil
}

// memoPlaceTaken says which limit leaves a queue without a place for a run, "" when it has one:
// "category" when the queue's own slots are taken, "member" when the member's queues together
// run the most there may be. The claim answers its refusals from the same function.
func memoPlaceTaken(q store.MemoQueue, inQueue, inMember int) string {
	switch {
	case inQueue >= q.MaxActive:
		return "category"
	case inMember >= memoQueueMemberLimit:
		return "member"
	}
	return ""
}

// memoQueueWaiting is the "waiting" of one queue as the member's list shows it: nil when
// nothing waits or a run can start.
func memoQueueWaiting(tx store.MemoWaitOps, q store.MemoQueue, tenantOn bool, pending store.QueuePending, now time.Time) (*memoWaiting, error) {
	if pending.Count == 0 {
		return nil, nil
	}
	w, _, err := memoWaitingFor(tx, memoWaitInput{q: q, tenantOn: tenantOn, now: now,
		pending: func() (store.QueuePending, error) { return pending, nil }})
	if err != nil || w != nil {
		return w, err
	}
	inQueue, inMember, err := tx.CountLiveDeliveries(q.CategoryID)
	if err != nil {
		return nil, err
	}
	if by := memoPlaceTaken(q, inQueue, inMember); by != "" {
		return &memoWaiting{Reason: waitNoPlace, Detail: &memoWaitingDetail{By: by}}, nil
	}
	return nil, nil
}
