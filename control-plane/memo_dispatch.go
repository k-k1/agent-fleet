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

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	// memoDispatchBudget bounds one tick's dispatch pass, as memoSettleBudget bounds the settle pass.
	memoDispatchBudget = 45 * time.Second
	// memoThresholdQuiet is how long nothing may have been added to a threshold queue before it
	// opens, so that ten messages added in one go start together.
	memoThresholdQuiet = time.Minute
	// memoBreakerRun is how many deliveries in a row must end badly to pause a queue, and
	// memoQuickTurn what counts as "within a minute".
	memoBreakerRun = 2
	memoQuickTurn  = time.Minute
	// memoSettleWakeEvery is the least time between two wakes of one queue made only to settle:
	// the workspace needs its time to start, and every wake counts against the day.
	memoSettleWakeEvery = 10 * time.Minute
	memoFiresWindow     = 24 * time.Hour
	// memoWakesPerTick bounds the wakes made only to settle that one tick starts: the tick waits
	// for every one of them, and a cold start takes minutes.
	memoWakesPerTick = 2
	// memoClosedTurnWindow is how long after its close a delivery is still read for the first turn
	// of its session, and memoClosedTurnsPerTick how many are read in a tick.
	memoClosedTurnWindow   = 24 * time.Hour
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
	r.wakeJobs = nil
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
	return out, r.wakeJobs
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
}

// turnBad reports whether a delivery's first turn counts against the queue: it failed or was
// aborted, or it ended within a minute without a clean end. A turn with no recorded outcome (a
// kind that records none) counts by the minute alone.
func turnBad(d store.MemoDelivery) bool {
	switch d.TurnOutcome {
	case "turn-failed", "turn-aborted":
		return true
	case "clean":
		return false
	}
	return time.Duration(d.TurnMs)*time.Millisecond < memoQuickTurn
}

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
		if pending.Count < q.OpenAfter {
			return false, false, &memoWaiting{Reason: waitBelowThreshold,
				Detail: &memoWaitingDetail{Needed: q.OpenAfter, Waiting: pending.Count}}
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
		if w.pending.Count > 0 {
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
		// The rules of "may this queue fire now" are memoWaitingFor's, which the member's list reads
		// too; the Agent's report was decided above, against the running workspace.
		why, start, err := memoWaitingFor(tx, memoWaitInput{q: cur, tenantOn: on, now: now, skipNoReport: true,
			pending: func() (store.QueuePending, error) {
				c, ok, err := tx.GetCategory(q.CategoryID)
				if err != nil || !ok {
					return store.QueuePending{}, err
				}
				rows, err := tx.ListMemosIn(c.Repo, c.Name)
				return pendingOf(rows), err
			}})
		if err != nil || why != nil {
			return err
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
	return out, claimed
}

// pendingOf summarizes the pending messages of a bucket the way ListQueuePendingStats does.
func pendingOf(rows []store.Memo) store.QueuePending {
	var p store.QueuePending
	for _, m := range rows {
		if m.State != store.MemoStatePending || m.SentAt != "" || m.SendToken != "" || m.DeliveryID != "" {
			continue
		}
		p.Count++
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
		pending := pendingOf(rows)
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
		return tx.InsertDelivery(wakeRow)
	})
	if err != nil {
		log.Printf("memo queue: queue %s: record a wake: %v", q.CategoryID, err)
		return true
	}
	if !record {
		return true
	}
	// The wake itself runs in one of the tick's fire slots, not here: a cold start takes minutes
	// and the rest of the pass must not wait for it.
	r.wakeJobs = append(r.wakeJobs, func(ctx context.Context) {
		if err := r.wakeOnly(ctx, res); err != nil {
			log.Printf("memo queue: queue %s: wake to settle: %v", q.CategoryID, err)
		}
	})
	return true
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
