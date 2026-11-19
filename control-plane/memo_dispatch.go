package main

// Message queues, step 3 (ADR 0137 sections 6 and 7, issue #4204): the dispatcher, the fires a
// queue makes by itself. It runs in the scheduler's tick after the settle pass.
//
// Everything it decides is a level read from stored state: whether a queue is open, draining,
// paused, over its count of the day, held by a usage limit or by an Agent that sends no report.
// No counter lives in memory, so a restart or a second Control Plane loses nothing. A fire goes
// through the same reserve-and-claim as "fire now" (memoReserveAndClaim, under the membership
// row's lock), so two Control Planes never start more sessions than there are free slots, and a
// slot is free only when the delivery that held it is closed (memo_delivery.go, the occupancy rule).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/memoafter"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// memoQueueBlockedKind is the notification of a message blocked by its prerequisites for longer
// than its queue's max_task_hours, counted from when it became pending.
const memoQueueBlockedKind = "memo-queue-blocked"

const (
	// memoDispatchBudget bounds one tick's dispatch pass, as memoSettleBudget bounds the settle pass.
	memoDispatchBudget = 45 * time.Second
	// memoThresholdQuiet is how long nothing may have been added to a threshold queue before it
	// opens, so that ten messages added in one go start together.
	memoThresholdQuiet = time.Minute
	// memoBreakerRun is how many deliveries in a row must end badly to pause a queue, and
	// memoQuickTurn what counts as "within a minute".
	memoBreakerRun = 2
	memoQuickTurn  = store.MemoQuickTurnMs * time.Millisecond
	// memoSettleWakeEvery is the least time between two wakes of one queue made only to settle:
	// the workspace needs its time to start, and every wake counts against the day.
	memoSettleWakeEvery = 10 * time.Minute
	memoFiresWindow     = 24 * time.Hour
	// memoWakesPerTick bounds the wakes made only to settle that one tick starts: the tick waits
	// for every one of them, and a cold start takes minutes.
	memoWakesPerTick = 2
	// memoClosedTurnWindow is how long after its close a delivery is still read for the first turn
	// of its session, and memoClosedTurnsPerTick how many are read in a tick.
	memoClosedTurnWindow   = memoafter.ClosedTurnWindow
	memoClosedTurnsPerTick = 50
)

// Notification kinds of the dispatcher. Their wording is the Console slice's (#4217).
const (
	memoPausedKind      = "memo-queue-paused"
	memoWaitingKind     = "memo-queue-waiting"
	memoUnconfirmedKind = "memo-delivery-unconfirmed"
)

// memoQueueWork is one enabled queue with what it has waiting.
type memoQueueWork struct {
	q       store.MemoQueue
	pending store.QueuePending
}

// memoMemberWork is one member's enabled queues.
type memoMemberWork struct {
	membershipID string
	queues       []*memoQueueWork
	// oldest is the creation time of the oldest pending message of any of its queues, "" when
	// none waits: members are served oldest first.
	oldest string
}

// dispatch fires queues that are open by themselves, at most room deliveries, oldest waiting
// message first across a member's queues and across members, one member at a time in turn so
// that a member with many queues does not starve the others. It answers the claimed deliveries,
// and the wakes the tick runs in its fire slots.
func (r *memoQueueRunner) dispatch(ctx context.Context, now time.Time, room int) ([]store.MemoDelivery, []func(context.Context)) {
	r.wakeJobs, r.tidyJobs = nil, nil
	ctx, cancel := context.WithTimeout(ctx, memoDispatchBudget)
	defer cancel()
	queues, err := r.mgr.store.ListEnabledMemoQueues(ctx)
	if err != nil {
		log.Printf("memo queue: list enabled queues: %v", err)
		return nil, nil
	}
	if len(queues) == 0 {
		return nil, nil
	}
	stats, err := r.mgr.store.ListQueuePendingStats(ctx)
	if err != nil {
		log.Printf("memo queue: pending stats: %v", err)
		return nil, nil
	}
	live, err := r.mgr.store.ListLiveMemoDeliveries(ctx)
	if err != nil {
		log.Printf("memo queue: list deliveries: %v", err)
		return nil, nil
	}
	views := r.tickViews
	if views == nil {
		views = map[string]*memoMemberView{}
	}
	members := groupMemoMembers(queues, stats)
	tenantOn := map[string]bool{}
	ready := make([]*memoMemberWork, 0, len(members))
	liveBy := map[string][]store.MemoDelivery{}
	for _, d := range live {
		liveBy[d.MembershipID] = append(liveBy[d.MembershipID], d)
	}
	for _, m := range members {
		if ctx.Err() != nil {
			break
		}
		res := r.resolveMember(ctx, m.membershipID)
		if res == nil {
			continue
		}
		on, known := tenantOn[res.mv.TenantID]
		if !known {
			on, err = r.mgr.store.GetTenantMemoQueues(ctx, res.mv.TenantID)
			if err != nil {
				continue
			}
			tenantOn[res.mv.TenantID] = on
		}
		if !on {
			continue
		}
		for _, w := range m.queues {
			r.houseKeep(ctx, w, liveBy[m.membershipID], now)
		}
		for _, w := range m.queues {
			if w.q.Tidy {
				r.takeOverTidy(ctx, m.membershipID, now)
				break
			}
		}
		ready = append(ready, m)
	}
	var out []store.MemoDelivery
	// Round after round, one fire per member: the member whose oldest message waited longest first.
	for room > 0 && ctx.Err() == nil {
		sort.SliceStable(ready, func(i, j int) bool { return olderPending(ready[i].oldest, ready[j].oldest) })
		fired := false
		for _, m := range ready {
			if room <= 0 || ctx.Err() != nil {
				break
			}
			if d, ok := r.fireOneOf(ctx, views, m, liveBy[m.membershipID], now); ok {
				out = append(out, d)
				room--
				fired = true
			}
		}
		if !fired {
			break
		}
		// What is pending has changed: read it again, and the member's deliveries with it.
		if stats, err = r.mgr.store.ListQueuePendingStats(ctx); err != nil {
			break
		}
		if live, err = r.mgr.store.ListLiveMemoDeliveries(ctx); err != nil {
			break
		}
		liveBy = map[string][]store.MemoDelivery{}
		for _, d := range live {
			liveBy[d.MembershipID] = append(liveBy[d.MembershipID], d)
		}
		for _, m := range ready {
			m.oldest = ""
			for _, w := range m.queues {
				w.pending = stats[w.q.CategoryID]
				if w.pending.Count > 0 && olderPending(w.pending.Oldest, m.oldest) {
					m.oldest = w.pending.Oldest
				}
			}
		}
	}
	// Wakes made only to settle do not need a free room: a tick whose sends filled it still wakes.
	woke := map[string]bool{}
	for _, m := range ready {
		for _, w := range m.queues {
			if woke[m.membershipID] || ctx.Err() != nil || len(r.wakeJobs) >= memoWakesPerTick {
				break
			}
			if w.pending.Count == 0 {
				continue
			}
			before := len(r.wakeJobs)
			r.wakeQueue(ctx, views, w, liveBy[m.membershipID], now)
			woke[m.membershipID] = len(r.wakeJobs) > before // one wake of a workspace per tick
		}
	}
	return out, append(r.wakeJobs, r.tidyJobs...)
}

// wakeQueue wakes the stopped workspace of a queue that would fire but for deliveries only a
// running Agent can settle (settleWake).
func (r *memoQueueRunner) wakeQueue(ctx context.Context, views map[string]*memoMemberView, w *memoQueueWork,
	live []store.MemoDelivery, now time.Time) {
	q := w.q
	if q.Paused || !q.Enabled || q.NoReportAt != "" {
		return
	}
	if open, _ := openState(q, w.pending, now); !open {
		return
	}
	v := r.viewOf(ctx, views, q.MembershipID)
	if v == nil || v.res.rt.State(ctx) != "stopped" {
		return
	}
	r.settleWake(ctx, w, live, now)
}

// notSentBackoff is how long a queue waits after deliveries that closed as not sent: the Agent
// refused, the wake failed or a quota held. It doubles with each one in a row, from five
// minutes to four hours, so a refusing Agent does not use up the day's fires a minute at a time
// (ADR 0137 section 6: the queue backs off, it is not a failure).
func notSentBackoff(ds []store.MemoDelivery) (time.Time, bool) {
	n := 0
	for _, d := range ds {
		if d.State != store.DeliveryClosed || d.Outcome != store.DeliveryNotSent {
			break
		}
		n++
	}
	if n == 0 {
		return time.Time{}, false
	}
	wait := 5 * time.Minute << min(n-1, 6)
	if wait > 4*time.Hour {
		wait = 4 * time.Hour
	}
	closed, err := time.Parse("2006-01-02T15:04:05.000000Z", ds[0].ClosedAt)
	if err != nil {
		return time.Time{}, false
	}
	return closed.Add(wait), true
}

// olderPending orders by oldest waiting message, a member with none last.
func olderPending(a, b string) bool {
	if a == "" {
		return false
	}
	return b == "" || a < b
}

func groupMemoMembers(queues []store.MemoQueue, stats map[string]store.QueuePending) []*memoMemberWork {
	byID := map[string]*memoMemberWork{}
	var out []*memoMemberWork
	for _, q := range queues {
		m := byID[q.MembershipID]
		if m == nil {
			m = &memoMemberWork{membershipID: q.MembershipID}
			byID[q.MembershipID] = m
			out = append(out, m)
		}
		w := &memoQueueWork{q: q, pending: stats[q.CategoryID]}
		m.queues = append(m.queues, w)
		if w.pending.Count > 0 && olderPending(w.pending.Oldest, m.oldest) {
			m.oldest = w.pending.Oldest
		}
	}
	return out
}

// resolveMember resolves a member's workspace without waking it or asking its Agent.
func (r *memoQueueRunner) resolveMember(ctx context.Context, membershipID string) *resolved {
	identityID, ok, err := r.mgr.store.IdentityIDForMembership(ctx, membershipID)
	if err != nil || !ok {
		return nil
	}
	res, aerr := r.mgr.resolveByMembership(ctx, identityID, membershipID)
	if aerr != nil {
		return nil
	}
	return res
}

// --- Housekeeping: the breaker, draining, and the member's waiting notice ---------------------

// houseKeep keeps the stored state of one queue in step with what the deliveries say, whether or
// not anything waits to be fired.
func (r *memoQueueRunner) houseKeep(ctx context.Context, w *memoQueueWork, live []store.MemoDelivery, now time.Time) {
	q := w.q
	if !q.Enabled {
		// A disabled queue is not draining: turning it on again starts its threshold anew.
		if q.Draining {
			if err := r.mgr.store.SetMemoQueueDraining(ctx, q.CategoryID, false); err != nil {
				log.Printf("memo queue: queue %s: end draining: %v", q.CategoryID, err)
			}
		}
		return
	}
	if q.Draining && w.pending.Count == 0 {
		if err := r.mgr.store.SetMemoQueueDraining(ctx, q.CategoryID, false); err != nil {
			log.Printf("memo queue: queue %s: end draining: %v", q.CategoryID, err)
		}
	}
	if !q.Paused {
		r.checkBreaker(ctx, q, now)
	}
	r.checkWaiting(ctx, q, live)
	if w.pending.Count > w.pending.Eligible {
		r.checkAfter(ctx, q, now)
	}
}

// checkAfter puts to held every pending message of a queue that waits for a prerequisite that can
// no longer be satisfied, with the reason, and tells the member once about each message that has
// waited for its prerequisites longer than the queue's max_task_hours since it became pending
// (ADR 0137, addendum 2026-11-19). It runs behind the membership lock, from the rows as they are.
func (r *memoQueueRunner) checkAfter(ctx context.Context, q store.MemoQueue, now time.Time) {
	var late []memoafter.Blocked
	err := r.mgr.store.MemoTx(ctx, q.MembershipID, func(tx store.MemoTxOps) error {
		late = nil
		c, ok, err := tx.GetCategory(q.CategoryID)
		if err != nil || !ok {
			return err
		}
		cur, ok, err := tx.GetQueue(q.CategoryID)
		if err != nil || !ok {
			return err
		}
		rows, err := tx.ListMemosIn(c.Repo, c.Name)
		if err != nil {
			return err
		}
		late, err = memoafter.HoldUnsatisfiable(tx, rows, now, now.Add(-time.Duration(memoQueueTaskHours(cur))*time.Hour))
		return err
	})
	if err != nil {
		log.Printf("memo queue: queue %s: prerequisites: %v", q.CategoryID, err)
		return
	}
	for _, b := range late {
		// Told once per occurrence: the message and the release it waits under.
		r.notifyQueueRow(ctx, q, memoQueueBlockedKind, "memo-blocked-"+b.Memo.ID+"-"+b.Memo.ReleasedAt,
			map[string]any{"memo_id": b.Memo.ID, "memo_label": memoafter.Label(b.Memo), "after": b.Items,
				"max_task_hours": memoQueueTaskHours(q)})
	}
}

// turnBad reports whether a delivery's first turn counts against the queue: it failed or was
// aborted, or it ended within a minute without a clean end. A turn with no recorded outcome (a
// kind that records none) counts by the minute alone.
func turnBad(d store.MemoDelivery) bool { return store.TurnBad(d) }

// checkBreaker pauses a queue whose last two deliveries with a recorded first turn, created
// since it was last resumed, both ended badly, and tells the member once. The decision is made
// again behind the membership lock, from the stored queue and deliveries, where a resume made
// since this tick began has already moved breaker_after: failures from before it are not
// counted again.
func (r *memoQueueRunner) checkBreaker(ctx context.Context, q store.MemoQueue, now time.Time) {
	var reason, eventID string
	err := r.mgr.store.MemoTx(ctx, q.MembershipID, func(tx store.MemoTxOps) error {
		cur, ok, err := tx.GetQueue(q.CategoryID)
		if err != nil || !ok || cur.Paused {
			return err
		}
		ds, err := tx.RecentTurnDeliveries(q.CategoryID, cur.BreakerAfter, memoBreakerRun)
		if err != nil || len(ds) < memoBreakerRun {
			return err
		}
		var outcomes []string
		for _, d := range ds {
			if !turnBad(d) {
				return nil
			}
			o := d.TurnOutcome
			if o == "" {
				o = "ended within a minute"
			}
			outcomes = append(outcomes, o)
		}
		why := "the first turns of the last two runs ended badly: " + strings.Join(outcomes, ", ")
		won, err := tx.PauseQueue(q.CategoryID, store.DeliveryTS(now), why)
		if err != nil || !won {
			return err
		}
		reason, eventID = why, "memo-paused-"+q.CategoryID+"-"+ds[0].ID
		return nil
	})
	if err != nil {
		log.Printf("memo queue: queue %s: breaker: %v", q.CategoryID, err)
		return
	}
	if eventID != "" {
		// Told once per occurrence: the EventID is the newest of the two deliveries.
		r.notifyQueueRow(ctx, q, memoPausedKind, eventID, map[string]any{"reason": reason})
	}
}

// checkWaiting tells the member once when every slot of a queue is held by a session that waits
// for a person. The occurrence is the set of deliveries, so the same ones are told only once.
func (r *memoQueueRunner) checkWaiting(ctx context.Context, q store.MemoQueue, live []store.MemoDelivery) {
	var ids []string
	all := 0
	for _, d := range live {
		if d.QueueID != q.CategoryID {
			continue
		}
		all++
		if d.State == store.DeliveryOpen && d.Reported && d.HeldBy == store.HeldPerson {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) == 0 || len(ids) != all || all < q.MaxActive {
		return
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	r.notifyQueueRow(ctx, q, memoWaitingKind, "memo-waiting-"+q.CategoryID+"-"+hex.EncodeToString(sum[:6]),
		map[string]any{"deliveries": ids})
}

func (r *memoQueueRunner) notifyQueue(ctx context.Context, d store.MemoDelivery, kind, eventID string, payload map[string]any) {
	r.notifyQueueRow(ctx, store.MemoQueue{CategoryID: d.QueueID, MembershipID: d.MembershipID}, kind, eventID, payload)
}

// notifyQueueRow inserts a notification of a queue. The EventID makes it told once: a second
// insert, by another Control Plane or a later tick, does nothing.
func (r *memoQueueRunner) notifyQueueRow(ctx context.Context, q store.MemoQueue, kind, eventID string, payload map[string]any) {
	label := q.CategoryID
	if cats, err := r.mgr.store.ListCategories(ctx, q.MembershipID); err == nil {
		for _, c := range cats {
			if c.ID == q.CategoryID {
				label = c.Repo + "/" + c.Name
			}
		}
	}
	payload["queue_id"], payload["label"] = q.CategoryID, label
	b, _ := json.Marshal(payload)
	if err := r.mgr.store.InsertNotification(ctx, store.Notification{
		EventID: eventID, MembershipID: q.MembershipID, Kind: kind, TargetType: "memo-queue", TargetID: q.CategoryID,
		DisplayName: label, Payload: string(b), CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		log.Printf("memo queue: queue %s: notify %s: %v", q.CategoryID, kind, err)
	}
}

// --- Open conditions ---------------------------------------------------------------------------

// windowOpen reports whether now lies within openMinutes after a time the cron matches, in the
// queue's zone (UTC when it names none).
func windowOpen(q store.MemoQueue, now time.Time) bool {
	f, err := parseCron(q.OpenCron)
	if err != nil || q.OpenMinutes < 1 {
		return false
	}
	t := now.In(scheduleLocation(q.TZ)).Truncate(time.Minute)
	for i := 0; i < q.OpenMinutes; i++ {
		if cronMatch(f, t.Add(-time.Duration(i)*time.Minute)) {
			return true
		}
	}
	return false
}

// quietSince reports whether nothing was added or released at or after latest within the last
// memoThresholdQuiet. A stamp that cannot be read does not hold a queue back.
func quietSince(latest string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, latest)
	if err != nil {
		return true
	}
	return !t.After(now.Add(-memoThresholdQuiet))
}

// openCheck says whether the queue is open now and, when it is not, why and until when. For a
// threshold queue startDrain says it has just begun to drain (to be written with the claim).
func openCheck(q store.MemoQueue, pending store.QueuePending, now time.Time) (open, startDrain bool, why *memoWaiting) {
	switch {
	case q.OpenCron != "":
		if windowOpen(q, now) {
			return true, false, nil
		}
		w := &memoWaiting{Reason: waitWindowClosed}
		if next, err := nextCron(q.OpenCron, now, scheduleLocation(q.TZ)); err == nil {
			w.Until = untilOf(next)
		}
		return false, false, w
	case q.OpenAfter > 0:
		if q.Draining {
			return true, false, nil
		}
		// Only the messages a claim may take count towards the threshold: one blocked by its
		// prerequisites holds no place in it (ADR 0137, addendum 2026-11-19).
		if pending.Eligible < q.OpenAfter {
			return false, false, &memoWaiting{Reason: waitBelowThreshold,
				Detail: &memoWaitingDetail{Needed: q.OpenAfter, Waiting: pending.Eligible, Blocked: pending.Count - pending.Eligible}}
		}
		if !quietSince(pending.Latest, now) {
			w := &memoWaiting{Reason: waitQuietMinute}
			if t, err := time.Parse(time.RFC3339, pending.Latest); err == nil {
				w.Until = untilOf(t.Add(memoThresholdQuiet))
			}
			return false, false, w
		}
		return true, true, nil
	}
	return true, false, nil
}

// openState is openCheck without the reason.
func openState(q store.MemoQueue, pending store.QueuePending, now time.Time) (open, startDrain bool) {
	open, startDrain, _ = openCheck(q, pending, now)
	return open, startDrain
}

// --- One fire ------------------------------------------------------------------------------------

// fireOneOf fires at most one delivery for a member: the queue whose oldest pending message has
// waited longest among those that may fire now.
func (r *memoQueueRunner) fireOneOf(ctx context.Context, views map[string]*memoMemberView, m *memoMemberWork,
	live []store.MemoDelivery, now time.Time) (store.MemoDelivery, bool) {
	cands := make([]*memoQueueWork, 0, len(m.queues))
	for _, w := range m.queues {
		if w.pending.Eligible > 0 {
			cands = append(cands, w)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].pending.Oldest < cands[j].pending.Oldest })
	for _, w := range cands {
		if ctx.Err() != nil {
			break
		}
		if d, ok := r.fireQueue(ctx, views, w, live, now); ok {
			return d, true
		}
	}
	return store.MemoDelivery{}, false
}

// fireQueue tries one automatic fire of a queue. It reports whether a delivery was claimed.
func (r *memoQueueRunner) fireQueue(ctx context.Context, views map[string]*memoMemberView, w *memoQueueWork,
	live []store.MemoDelivery, now time.Time) (store.MemoDelivery, bool) {
	q := w.q
	if q.Paused || !q.Enabled {
		return store.MemoDelivery{}, false
	}
	if open, _ := openState(q, w.pending, now); !open {
		return store.MemoDelivery{}, false
	}
	v := r.viewOf(ctx, views, q.MembershipID)
	if v == nil {
		return store.MemoDelivery{}, false
	}
	res := v.res
	// tidyKnown is whether this tick has seen the Agent's tidy marker: a running workspace is asked
	// now, a stopped one is not, and its queue's last finding stands until a running one shows it.
	tidyKnown, tidyOK := false, false
	switch res.rt.State(ctx) {
	case "running":
		// A running workspace is asked now: only an Agent that sends the report is fired into by itself.
		if !v.liveOK {
			return store.MemoDelivery{}, false
		}
		if !v.report {
			r.setNoReport(ctx, q, store.DeliveryTS(now))
			return store.MemoDelivery{}, false
		}
		r.setNoReport(ctx, q, "")
		r.setNoRefs(ctx, q, !v.refs)
		r.setNoTidy(ctx, q, !v.tidy)
		tidyKnown, tidyOK = true, v.tidy
	case "stopped":
		// A stopped workspace is not asked. One found without the report stays skipped until a
		// running workspace shows it (the send checks again after its wake).
		if q.NoReportAt != "" {
			return store.MemoDelivery{}, false
		}
	default:
		return store.MemoDelivery{}, false
	}
	running, limit := -1, 0
	if !r.mgr.nativeRuntime {
		limit = r.mgr.sessionLimit(ctx, res)
		if limit > 0 && res.rt.State(ctx) == "running" {
			if n, err := r.mgr.countSessions(ctx, res.rt); err == nil {
				running = n
			}
		}
	}
	var out store.MemoDelivery
	var job *memoTidyJob
	claimed := false
	err := r.mgr.store.MemoTx(ctx, q.MembershipID, func(tx store.MemoTxOps) error {
		// Everything below is read again behind the membership lock: what the tick read before
		// the lock (the queue, the tenant switch, a usage limit, the deliveries) may be old.
		on, err := tx.LockTenantSwitch()
		if err != nil {
			return err
		}
		cur, ok, err := tx.GetQueue(q.CategoryID)
		if err != nil || !ok {
			return err
		}
		// A run that has been running too long is ended first: it would hold this queue back.
		if _, err := memoTidyTakeOver(tx, now); err != nil {
			return err
		}
		// The rules of "may this queue fire now" are memoWaitingFor's, which the member's list reads
		// too; the Agent's report was decided above, against the running workspace.
		why, start, err := memoWaitingFor(tx, memoWaitInput{q: cur, tenantOn: on, now: now, skipNoReport: true,
			pending: func() (store.QueuePending, error) {
				c, ok, err := tx.GetCategory(q.CategoryID)
				if err != nil || !ok {
					return store.QueuePending{}, err
				}
				rows, err := tx.ListMemosIn(c.Repo, c.Name)
				if err != nil {
					return store.QueuePending{}, err
				}
				return pendingOf(rows, memoafter.EligibleIn(memoafter.TxLookup(tx, rows, now))), nil
			}})
		if err != nil || why != nil {
			return err
		}
		// Tidying comes after "may fire" and before the claim. "Fire now" never gets here: the press
		// claims what the person sees.
		if cur.Tidy && (tidyOK || (!tidyKnown && cur.NoTidyAt == "")) {
			c, ok, err := tx.GetCategory(q.CategoryID)
			if err != nil || !ok {
				return err
			}
			j, hold, err := r.reserveTidy(tx, cur, c, start, now)
			if err != nil || hold {
				return err
			}
			if j != nil {
				job = j
				return nil
			}
		}
		d, aerr, err := memoReserveAndClaim(tx, q.CategoryID, running, limit, true)
		if err != nil {
			return err
		}
		if aerr != nil {
			return nil // no slot, nothing pending, a quota: not now, and nothing is marked
		}
		if start {
			if err := tx.SetQueueDraining(q.CategoryID, true); err != nil {
				return err
			}
		}
		if start || cur.Draining {
			// If this batch returns as not sent, the drain it belonged to goes on.
			if err := tx.MarkDeliveryDrain(d.ID); err != nil {
				return err
			}
		}
		d.MembershipID = q.MembershipID // the claim does not carry it, and the send looks the delivery up by it
		out, claimed = d, true
		return nil
	})
	if err != nil {
		log.Printf("memo queue: queue %s: fire by itself: %v", q.CategoryID, err)
		return store.MemoDelivery{}, false
	}
	if job != nil {
		r.tidyJobs = append(r.tidyJobs, func(ctx context.Context) { r.runTidy(ctx, job) })
	}
	return out, claimed
}

// pendingOf summarizes the pending messages of a bucket the way ListQueuePendingStats does.
// eligible says whether a message's prerequisites are satisfied (memoafter.EligibleIn), the Go side of
// the statistics' SQL.
func pendingOf(rows []store.Memo, eligible func(store.Memo) bool) store.QueuePending {
	var p store.QueuePending
	for _, m := range rows {
		if m.State != store.MemoStatePending || m.SentAt != "" || m.SendToken != "" || m.DeliveryID != "" {
			continue
		}
		p.Count++
		if eligible(m) {
			p.Eligible++
		}
		if m.RefKey != "" {
			p.Refs++
		}
		if m.Provenance == store.MemoProvenanceCurator {
			p.Curators++
		}
		if p.Oldest == "" || m.CreatedAt < p.Oldest {
			p.Oldest = m.CreatedAt
		}
		for _, t := range []string{m.CreatedAt, m.ReleasedAt} {
			if t > p.Latest {
				p.Latest = t
			}
		}
	}
	return p
}

func (r *memoQueueRunner) setNoReport(ctx context.Context, q store.MemoQueue, at string) {
	if q.NoReportAt == at || (at != "" && q.NoReportAt != "") {
		return
	}
	if err := r.mgr.store.SetMemoQueueNoReport(ctx, q.CategoryID, at); err != nil {
		log.Printf("memo queue: queue %s: note the Agent's report: %v", q.CategoryID, err)
	}
}

// setNoRefs keeps the queue's mark of an Agent that cannot show a reference in line with the
// Agent a running workspace answered with.
func (r *memoQueueRunner) setNoRefs(ctx context.Context, q store.MemoQueue, none bool) {
	at := ""
	if none {
		if q.NoRefsAt != "" {
			return
		}
		at = store.DeliveryTS(time.Now())
	}
	// A clear is written whatever q says: q may be older than the row, and the write changes
	// nothing when the mark is already empty.
	if err := r.mgr.store.SetMemoQueueNoRefs(ctx, q.CategoryID, at); err != nil {
		log.Printf("memo queue: queue %s: note the Agent's references: %v", q.CategoryID, err)
	}
}

// setNoTidy keeps the queue's mark of an Agent that cannot tidy in line with the Agent a running
// workspace answered with, as setNoRefs does for references. A later finding of a running
// workspace clears it; so does a send that finds the marker. The queue's settings do not.
func (r *memoQueueRunner) setNoTidy(ctx context.Context, q store.MemoQueue, none bool) {
	at := ""
	if none {
		if q.NoTidyAt != "" {
			return
		}
		at = store.DeliveryTS(time.Now())
	}
	if err := r.mgr.store.SetMemoQueueNoTidy(ctx, q.CategoryID, at); err != nil {
		log.Printf("memo queue: queue %s: note the Agent's tidy: %v", q.CategoryID, err)
	}
}

// viewOf is view, which reads the workspace once per tick and member.
func (r *memoQueueRunner) viewOf(ctx context.Context, views map[string]*memoMemberView, membershipID string) *memoMemberView {
	return r.view(ctx, views, membershipID)
}

// autoAgentReports is the check of a delivery fired by itself, after its wake: the Agent must
// send the queue report. It records what it found on the queue, so that a workspace without the
// report is not woken again for it.
func (r *memoQueueRunner) autoAgentReports(ctx context.Context, res *resolved, d store.MemoDelivery) bool {
	cctx, cancel := context.WithTimeout(ctx, memoAgentCallWait)
	defer cancel()
	env, err := r.mgr.agentSessionsEnv(cctx, res.rt)
	if err != nil {
		return false // nothing is known of the Agent, and nothing has been sent
	}
	at := ""
	if !env.reportsQueue() {
		at = store.DeliveryTS(time.Now())
	}
	if err := r.mgr.store.SetMemoQueueNoReport(ctx, d.QueueID, at); err != nil {
		log.Printf("memo queue: queue %s: note the Agent's report: %v", d.QueueID, err)
	}
	return env.reportsQueue()
}

// --- Wakes made only to settle ---------------------------------------------------------------------

// settleWake wakes a stopped workspace when the deliveries that hold every slot of an open queue
// with pending messages are ones only a running Agent can settle: a `sending` one, or an overdue
// one that waits for its retire. It reports whether the queue is held by them (so no fire is
// tried this tick), and counts the wake as a fire of the day by a closed row with outcome wake.
func (r *memoQueueRunner) settleWake(ctx context.Context, w *memoQueueWork, live []store.MemoDelivery, now time.Time) bool {
	q := w.q
	var mine []store.MemoDelivery
	for _, d := range live {
		if d.QueueID == q.CategoryID {
			mine = append(mine, d)
		}
	}
	if len(mine) < q.MaxActive || len(mine) == 0 {
		return false
	}
	for _, d := range mine {
		sending := d.State == store.DeliverySending
		overdue := d.State == store.DeliveryOpen && d.OverdueAt != "" && d.RetiredAt == ""
		if !sending && !overdue {
			return false
		}
	}
	res := r.resolveMember(ctx, q.MembershipID)
	if res == nil {
		return true
	}
	wakeRow := store.MemoDelivery{ID: store.NewID(), QueueID: q.CategoryID, State: store.DeliveryClosed, Outcome: store.DeliveryWake,
		MessageIDs: []string{}, CreatedAt: store.DeliveryTS(now), UpdatedAt: store.DeliveryTS(now), ClosedAt: store.DeliveryTS(now), Auto: true}
	record := false
	var held []store.MemoDelivery // the deliveries behind the wake, as read under the lock
	err := r.mgr.store.MemoTx(ctx, q.MembershipID, func(tx store.MemoTxOps) error {
		// The same conditions as the snapshot above, read again behind the lock: a switch turned
		// off, a queue disabled or paused, a cap lowered, or a slot freed since this tick read
		// them makes a wake that nothing needs.
		if on, err := tx.LockTenantSwitch(); err != nil || !on {
			return err
		}
		cur, ok, err := tx.GetQueue(q.CategoryID)
		if err != nil || !ok || !cur.Enabled || cur.Paused {
			return err
		}
		c, ok, err := tx.GetCategory(q.CategoryID)
		if err != nil || !ok {
			return err
		}
		rows, err := tx.ListMemosIn(c.Repo, c.Name)
		if err != nil {
			return err
		}
		pending := pendingOf(rows, memoafter.EligibleIn(memoafter.TxLookup(tx, rows, now)))
		if open, _ := openState(cur, pending, now); !open || pending.Count == 0 {
			return nil
		}
		if held, err := autoHeld(tx, cur, now); err != nil || held != nil {
			return err
		}
		liveNow, err := tx.LiveQueueDeliveries(q.CategoryID)
		if err != nil || len(liveNow) == 0 || len(liveNow) < cur.MaxActive {
			return err
		}
		for _, d := range liveNow {
			if d.State != store.DeliverySending && !(d.State == store.DeliveryOpen && d.OverdueAt != "" && d.RetiredAt == "") {
				return nil
			}
		}
		// One wake per workspace in the guard time, whatever queue made it: the queues of a member
		// share one workspace, and two Control Planes may reach different queues at once.
		recent, err := tx.CountMemberWakesSince(store.DeliveryTS(now.Add(-memoSettleWakeEvery)))
		if err != nil || recent > 0 {
			return err
		}
		fires, err := tx.CountDeliveriesCreatedSince(q.CategoryID, store.DeliveryTS(now.Add(-memoFiresWindow)))
		if err != nil || fires >= memoQueueFiresPerDay(cur) {
			return err
		}
		record = true
		held = liveNow
		return tx.InsertDelivery(wakeRow)
	})
	if err != nil {
		log.Printf("memo queue: queue %s: record a wake: %v", q.CategoryID, err)
		return true
	}
	if !record {
		return true
	}
	detail := settleWakeDetail(q.CategoryID, held)
	r.auditSettleWake(ctx, res, q.CategoryID, detail)
	log.Printf("memo queue: waking workspace %s to settle: %s", res.ws.ContainerName, detail)
	// The wake itself runs in one of the tick's fire slots, not here: a cold start takes minutes
	// and the rest of the pass must not wait for it.
	r.wakeJobs = append(r.wakeJobs, func(ctx context.Context) {
		if err := r.wakeOnly(ctx, res); err != nil {
			log.Printf("memo queue: queue %s: wake to settle: %v", q.CategoryID, err)
		}
	})
	return true
}

// auditSettleWake writes the system's entry for a wake that was recorded (a request to start
// the workspace: the start itself runs later and may fail). It hangs on the
// recorded outcome, never on the attempt, so two Control Planes reaching the same queue write one
// entry. Best effort: a failed insert is logged and the wake still runs. The detail names the
// runs and why each one needed the wake, and carries no memo or instruction text.
func (r *memoQueueRunner) auditSettleWake(ctx context.Context, res *resolved, queueID, detail string) {
	a := store.AuditLog{ID: store.NewID(), TenantID: res.mv.TenantID, ActorKind: "system", ActorID: "memo-queue",
		Action: "workspace.queue_settle_wake", Target: res.ws.ID, Detail: detail, At: store.NowTS()}
	if err := r.mgr.store.InsertAudit(context.WithoutCancel(ctx), a); err != nil {
		log.Printf("memo queue: queue %s: audit the wake to settle: %v", queueID, err)
	}
}

// settleWakeDetail names the queue and the runs that made a wake necessary, with the reason for
// each; it is shared by the audit entry and the log line.
func settleWakeDetail(queueID string, held []store.MemoDelivery) string {
	runs := make([]string, 0, len(held))
	for _, d := range held {
		why := "overdue and not yet retired"
		if d.State == store.DeliverySending {
			why = "sending"
		}
		runs = append(runs, d.ID+" ("+why+")")
	}
	return "queue " + queueID + "; runs: " + strings.Join(runs, ", ")
}

// wakeOnly brings a workspace up and waits for its Agent, for the settle pass of the next tick:
// the keep-alive is taken first, as a fire's wake does.
func (r *memoQueueRunner) wakeOnly(ctx context.Context, res *resolved) error {
	release, err := r.mgr.trackWorkspaceConnection(ctx, res.ws.ID, "")
	if err != nil {
		return fmt.Errorf("keep-alive: %w", err)
	}
	r.f.scheduleRelease(release)
	if aerr := r.f.wsAPI.ensureWorkspaceStartedUnattended(ctx, res); aerr != nil {
		return fmt.Errorf("wake: %s", aerr.message)
	}
	return r.f.awaitAgentReady(ctx, res.rt)
}

// --- Why a queue with notes waiting starts nothing (issue #4257) ---------------------------------
//
// The reason is produced by the decision the dispatcher makes, not by a second copy of its rules:
// memoWaitingFor is the one function that says whether a queue may fire now, called by the
// dispatcher behind the membership lock (fireQueue) and by the list a member reads
// (memoQueueWaiting) with the same reads. Nothing is stored, so there is nothing to go stale when
// no Control Plane ticks, and a queue the dispatcher never looks at (the organisation's switch
// off, the category's own switch off) gets its reason by the same call.

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
	// waitNoRefs: every message waiting carries a reference and the Agent cannot show one.
	waitNoRefs = "no_references"
	// waitTidying: a tidy run of the member is in flight, and this queue neither tidies nor fires
	// until it ends.
	waitTidying = "tidying"
	// waitNoTidy: every message waiting is one a tidy run derived and the Agent cannot take it.
	waitNoTidy = "no_tidy"
	// waitPrerequisites: every message waiting waits for a prerequisite that is not satisfied yet.
	waitPrerequisites = "prerequisites"
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
	// Blocked is how many pending messages wait for their prerequisites, for below_threshold and
	// prerequisites.
	Blocked int `json:"blocked,omitempty"`
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
	if q.Tidy {
		// One run at a time per member, across queues and across Control Planes (a unique index).
		if _, running, err := tx.RunningTidyRun(); err != nil || running {
			if err != nil {
				return nil, false, err
			}
			return &memoWaiting{Reason: waitTidying}, false, nil
		}
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
	if pending.Eligible == 0 {
		return &memoWaiting{Reason: waitPrerequisites, Detail: &memoWaitingDetail{Blocked: pending.Count}}, nil
	}
	if q.NoRefsAt != "" && pending.Refs == pending.Count {
		return &memoWaiting{Reason: waitNoRefs}, nil
	}
	if q.NoTidyAt != "" && pending.Curators == pending.Count {
		return &memoWaiting{Reason: waitNoTidy}, nil
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
