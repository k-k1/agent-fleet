package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Message queues, step 3 (ADR 0137 sections 6 and 7, issue #4204): the dispatcher. The Agent is
// the fake of memo_delivery_test.go with the queue report on.

// autoQueue makes an enabled queue (settings over the defaults) with n pending messages behind an
// Agent that sends the queue report.
func (e *deliveryEnv) autoQueue(repo, name string, settings map[string]any, n int) (*fakeQueueAgent, string) {
	e.t.Helper()
	body := map[string]any{"enabled": true}
	for k, v := range settings {
		body[k] = v
	}
	q := e.queueWith(repo, name, body)
	for i := 0; i < n; i++ {
		e.pending(repo, name, fmt.Sprintf("%s %d", name, i))
	}
	agent := e.useQueueAgent("")
	agent.mu.Lock()
	agent.report = true
	agent.mu.Unlock()
	return agent, q
}

// autoTick is one scheduler tick of queue work as the scheduler runs it: settle, dispatch into
// the room the settle left, then send. It answers the sends and the wakes.
func (e *deliveryEnv) autoTick(now time.Time) ([]store.MemoDelivery, []func(context.Context)) {
	r := e.runner()
	ctx := context.Background()
	sends := r.settle(ctx, now)
	fired, wakes := r.dispatch(ctx, now, memoQueueSendsPerTick-len(sends))
	sends = append(sends, fired...)
	for _, d := range sends {
		r.send(ctx, d)
	}
	return sends, wakes
}

// pauseQueue pauses a queue the way the breaker does, behind the membership lock.
func (e *deliveryEnv) pauseQueue(queueID, reason string) (bool, error) {
	won := false
	err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) (err error) {
		won, err = tx.PauseQueue(queueID, store.DeliveryTS(time.Now()), reason)
		return err
	})
	return won, err
}

func (e *deliveryEnv) liveDeliveries(queueID string) []store.MemoDelivery {
	e.t.Helper()
	ds, err := e.st.ListLiveMemoDeliveries(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	var out []store.MemoDelivery
	for _, d := range ds {
		if d.QueueID == queueID {
			out = append(out, d)
		}
	}
	return out
}

func (e *deliveryEnv) allDeliveries(queueID string) []store.MemoDelivery {
	e.t.Helper()
	ds, err := e.st.ListMemoDeliveries(context.Background(), e.members["a"], queueID, 100)
	if err != nil {
		e.t.Fatal(err)
	}
	return ds
}

func (e *deliveryEnv) queueRow(queueID string) store.MemoQueue {
	e.t.Helper()
	qs, err := e.st.ListMemoQueues(context.Background(), e.members["a"])
	if err != nil {
		e.t.Fatal(err)
	}
	for _, q := range qs {
		if q.CategoryID == queueID {
			return q
		}
	}
	e.t.Fatal("no such queue")
	return store.MemoQueue{}
}

func (e *deliveryEnv) notificationsOf(kind string) int {
	n := 0
	for _, x := range e.notifications() {
		if x.Kind == kind {
			n++
		}
	}
	return n
}

// With max_active 2 and five pending messages at most two deliveries are open at any time, and a
// third starts only after one has closed, by the occupancy rule.
func TestMemoDispatchHoldsAtMostMaxActive(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", nil, 5)
		for i := 0; i < 4; i++ {
			e.autoTick(time.Now())
			if n := len(e.liveDeliveries(q)); n != 2 {
				t.Fatalf("tick %d: %d live deliveries, want 2", i, n)
			}
		}
		if agent.createCount() != 2 {
			t.Fatalf("%d creates", agent.createCount())
		}
		// One session goes idle: two ticks later it has closed, and the next tick starts the third.
		live := e.liveDeliveries(q)
		agent.setRow(live[0].Session, map[string]any{"state": "idle"})
		e.autoTick(time.Now())
		if agent.createCount() != 2 {
			t.Fatal("a slot was taken back before the delivery closed")
		}
		e.autoTick(time.Now())
		e.autoTick(time.Now())
		if agent.createCount() != 3 || len(e.liveDeliveries(q)) != 2 {
			t.Fatalf("creates %d, live %d", agent.createCount(), len(e.liveDeliveries(q)))
		}
		for _, d := range e.allDeliveries(q) {
			if d.Auto != true {
				t.Fatalf("a delivery fired by itself is not marked: %+v", d)
			}
		}
	})
}

// A window opens at a cron time and stays open for openMinutes, in the queue's zone.
func TestMemoDispatchWindow(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now().UTC()
		match := func(at time.Time) string { return fmt.Sprintf("%d %d * * *", at.Minute(), at.Hour()) }
		agent, q := e.autoQueue("app", "impl", map[string]any{"openCron": match(now.Add(-3 * time.Hour)), "openMinutes": 60, "tz": "UTC"}, 2)
		e.autoTick(now)
		if agent.createCount() != 0 {
			t.Fatal("fired outside its window")
		}
		e.queueWith("app", "impl", map[string]any{"enabled": true, "openCron": match(now.Add(-10 * time.Minute)), "openMinutes": 60, "tz": "UTC"})
		e.autoTick(now)
		if agent.createCount() == 0 {
			t.Fatal("did not fire inside its window")
		}
		// The same window read in another zone: an offset zone moves the hour it opens.
		tokyo, _ := time.LoadLocation("Asia/Tokyo")
		at := now.In(tokyo).Add(-10 * time.Minute)
		if !windowOpen(store.MemoQueue{OpenCron: match(at), OpenMinutes: 60, TZ: "Asia/Tokyo"}, now) {
			t.Fatal("zone not applied")
		}
		if windowOpen(store.MemoQueue{OpenCron: match(at), OpenMinutes: 60, TZ: "UTC"}, now) && tokyo.String() != "UTC" {
			t.Fatal("the zone was ignored")
		}
		_ = q
	})
}

// A threshold queue waits until K messages are pending and nothing was added for a minute, then
// drains until none is pending, and the fact is kept in the row.
func TestMemoDispatchThresholdDrains(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"openAfter": 3, "maxActive": 1}, 2)
		later := time.Now().Add(5 * time.Minute)
		e.autoTick(later)
		if agent.createCount() != 0 {
			t.Fatal("fired below its threshold")
		}
		e.pending("app", "impl", "third")
		e.autoTick(time.Now())
		if agent.createCount() != 0 {
			t.Fatal("fired before nothing was added for a minute")
		}
		e.autoTick(later)
		if agent.createCount() != 1 || !e.queueRow(q).Draining {
			t.Fatalf("creates %d, draining %v", agent.createCount(), e.queueRow(q).Draining)
		}
		// Two pending are below K, but the queue is draining: the next fires after the first closes.
		live := e.liveDeliveries(q)
		agent.setRow(live[0].Session, map[string]any{"state": "idle"})
		for i := 0; i < 3; i++ {
			e.autoTick(later)
		}
		if agent.createCount() != 2 {
			t.Fatalf("a draining queue stopped below K: %d creates", agent.createCount())
		}
		live = e.liveDeliveries(q)
		agent.setRow(live[0].Session, map[string]any{"state": "idle"})
		for i := 0; i < 4; i++ {
			e.autoTick(later)
		}
		if agent.createCount() != 3 {
			t.Fatalf("%d creates", agent.createCount())
		}
		// Nothing is pending: draining ends, and one new message does not open it.
		for _, d := range e.liveDeliveries(q) {
			agent.setRow(d.Session, map[string]any{"state": "idle"})
		}
		for i := 0; i < 3; i++ {
			e.autoTick(later)
		}
		if e.queueRow(q).Draining {
			t.Fatal("still draining with nothing pending")
		}
		e.pending("app", "impl", "again")
		e.autoTick(later.Add(5 * time.Minute))
		if agent.createCount() != 3 {
			t.Fatal("one message opened a threshold queue")
		}
	})
}

// The tenant switch off, enabled off and a pause each stop fires by themselves and leave fire
// now as it is.
func TestMemoDispatchSwitchesAndFireNow(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 6}, 6)
		e.memoQueuesOn(false)
		e.autoTick(time.Now())
		if agent.createCount() != 0 {
			t.Fatal("fired with the tenant switch off")
		}
		e.memoQueuesOn(true)
		e.queueWith("app", "impl", map[string]any{"enabled": false, "maxActive": 6})
		e.autoTick(time.Now())
		if agent.createCount() != 0 {
			t.Fatal("fired with the queue not enabled")
		}
		e.queueWith("app", "impl", map[string]any{"enabled": true, "maxActive": 6})
		if ok, err := e.pauseQueue(q, "test"); err != nil || !ok {
			t.Fatal(ok, err)
		}
		e.autoTick(time.Now())
		if agent.createCount() != 0 {
			t.Fatal("fired while paused")
		}
		// Fire now still works under each of them.
		if rec := e.fire(q); rec.Code != http.StatusAccepted {
			t.Fatalf("fire now while paused: %d %s", rec.Code, rec.Body.String())
		}
		e.memoQueuesOn(false)
		if rec := e.fire(q); rec.Code == http.StatusAccepted {
			t.Fatal("fire now with the tenant switch off is refused as before")
		}
	})
}

// Two failed first turns in a row pause the queue once and notify once, resume lifts the pause
// and old failures do not pause it again, and a pause stops fires by themselves only.
func TestMemoDispatchBreaker(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", nil, 6)
		e.autoTick(time.Now())
		live := e.liveDeliveries(q)
		if len(live) != 2 {
			t.Fatalf("%d live", len(live))
		}
		failed := map[string]any{"outcome": "turn-failed", "ms": 120000, "endedAt": "2026-11-15T01:00:00Z"}
		// One bad, one clean: not a run.
		agent.setRow(live[0].Session, map[string]any{"queueTurn": failed})
		agent.setRow(live[1].Session, map[string]any{"queueTurn": map[string]any{"outcome": "clean", "ms": 900000, "endedAt": "2026-11-15T01:00:00Z"}})
		e.autoTick(time.Now())
		if e.queueRow(q).Paused {
			t.Fatal("paused by one failure")
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", nil, 6)
		e.autoTick(time.Now())
		live := e.liveDeliveries(q)
		agent.setRow(live[0].Session, map[string]any{"queueTurn": map[string]any{"outcome": "turn-failed", "ms": 120000, "endedAt": "2026-11-15T01:00:00Z"}})
		agent.setRow(live[1].Session, map[string]any{"queueTurn": map[string]any{"outcome": "", "ms": 1000, "endedAt": "2026-11-15T01:00:01Z"}})
		for i := 0; i < 3; i++ {
			e.autoTick(time.Now())
		}
		row := e.queueRow(q)
		if !row.Paused || row.PausedReason == "" || e.notificationsOf(memoPausedKind) != 1 {
			t.Fatalf("paused %v, notifications %d", row.Paused, e.notificationsOf(memoPausedKind))
		}
		// Every slot is taken and the queue is paused: close both, nothing starts by itself.
		for _, d := range live {
			agent.setRow(d.Session, map[string]any{"state": "idle"})
		}
		before := agent.createCount()
		for i := 0; i < 4; i++ {
			e.autoTick(time.Now())
		}
		if agent.createCount() != before || len(e.liveDeliveries(q)) != 0 {
			t.Fatalf("a paused queue fired: %d creates", agent.createCount())
		}
		if rec := e.fire(q); rec.Code != http.StatusAccepted {
			t.Fatalf("fire now while paused: %d", rec.Code)
		}
		// Resume: the old failures are not counted again, and the queue fires by itself.
		if rec := e.do("a", "POST", "/api/memo-queues/"+q+"/resume", nil); rec.Code != http.StatusNoContent {
			t.Fatalf("resume: %d %s", rec.Code, rec.Body.String())
		}
		e.autoTick(time.Now())
		e.autoTick(time.Now())
		row = e.queueRow(q)
		if row.Paused || e.notificationsOf(memoPausedKind) != 1 || agent.createCount() <= before+1 {
			t.Fatalf("after resume: paused %v, notifications %d, creates %d", row.Paused, e.notificationsOf(memoPausedKind), agent.createCount())
		}
		if rec := e.do("a", "POST", "/api/memo-queues/nope/resume", nil); rec.Code != http.StatusNotFound {
			t.Fatalf("resume of no queue: %d", rec.Code)
		}
	})
}

// Two interrupted first turns (the Agent's turn-aborted, as an interrupt of a Terminal or a
// Managed session now reads) pause the queue, however long they ran.
func TestMemoDispatchBreakerCountsInterruptedFirstTurns(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", nil, 6)
		e.autoTick(time.Now())
		live := e.liveDeliveries(q)
		if len(live) != 2 {
			t.Fatalf("%d live", len(live))
		}
		for i, d := range live {
			agent.setRow(d.Session, map[string]any{"queueTurn": map[string]any{
				"outcome": "turn-aborted", "ms": 600000, "endedAt": "2026-11-15T01:00:0" + string(rune('0'+i)) + "Z"}})
		}
		for i := 0; i < 3; i++ {
			e.autoTick(time.Now())
		}
		if row := e.queueRow(q); !row.Paused || e.notificationsOf(memoPausedKind) != 1 {
			t.Fatalf("paused %v, notifications %d", row.Paused, e.notificationsOf(memoPausedKind))
		}
	})
}

// A turn counts as bad by its outcome, or by ending inside a minute without a clean end.
func TestMemoDispatchTurnBad(t *testing.T) {
	for _, c := range []struct {
		outcome string
		ms      int64
		bad     bool
	}{
		{"turn-failed", 600000, true}, {"turn-aborted", 600000, true}, {"clean", 100, false},
		{"", 100, true}, {"", 600000, false}, {"", 59999, true}, {"", 60000, false},
	} {
		if got := turnBad(store.MemoDelivery{TurnOutcome: c.outcome, TurnMs: c.ms}); got != c.bad {
			t.Fatalf("%+v: %v", c, got)
		}
	}
}

// Fires per day are counted from the deliveries of the last 24 hours: the 21st does not start.
func TestMemoDispatchDailyCap(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", nil, 3)
		insert := func(n int, at time.Time) {
			for i := 0; i < n; i++ {
				ts := store.DeliveryTS(at)
				if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
					return tx.InsertDelivery(store.MemoDelivery{ID: store.NewID(), QueueID: q, State: store.DeliveryClosed,
						Outcome: store.DeliveryClean, CreatedAt: ts, UpdatedAt: ts, ClosedAt: ts})
				}); err != nil {
					t.Fatal(err)
				}
			}
		}
		insert(20, time.Now().Add(-time.Hour))
		e.autoTick(time.Now())
		if agent.createCount() != 0 {
			t.Fatal("the 21st fire within 24 hours started")
		}
		// Fire now is not counted against the cap by this rule.
		if rec := e.fire(q); rec.Code != http.StatusAccepted {
			t.Fatalf("fire now: %d", rec.Code)
		}
		// A day later the count is from the last 24 hours only.
		e.autoTick(time.Now().Add(25 * time.Hour))
		if agent.createCount() == 0 {
			t.Fatal("the count did not age out")
		}
	})
}

// While a session waits for a usage limit, no session of that agent kind is started.
func TestMemoDispatchUsageLimit(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 3}, 5)
		e.autoTick(time.Now())
		before := agent.createCount()
		live := e.liveDeliveries(q)
		agent.setRow(live[0].Session, map[string]any{"state": "limited"})
		for i := 0; i < 4; i++ {
			e.autoTick(time.Now())
		}
		if agent.createCount() != before {
			t.Fatalf("started a session of the limited kind: %d creates", agent.createCount())
		}
		agent.setRow(live[0].Session, map[string]any{"state": "working"})
		e.autoTick(time.Now())
		e.autoTick(time.Now())
		if agent.createCount() == before {
			t.Fatal("did not start again after the limit lifted")
		}
	})
}

// An Agent that does not report is not fired into by itself, and the queue says why. One that
// starts to report is.
func TestMemoDispatchNeedsTheReport(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", nil, 3)
		agent.mu.Lock()
		agent.report = false
		agent.mu.Unlock()
		for i := 0; i < 3; i++ {
			e.autoTick(time.Now())
		}
		row := e.queueRow(q)
		if agent.createCount() != 0 || row.NoReportAt == "" {
			t.Fatalf("creates %d, noReportAt %q", agent.createCount(), row.NoReportAt)
		}
		if dto := memoQueueToDTO(row, store.MemoCategory{}); dto.NoReportAt == "" {
			t.Fatal("the DTO does not say why")
		}
		// Fire now is unchanged.
		if rec := e.fire(q); rec.Code != http.StatusAccepted {
			t.Fatalf("fire now: %d", rec.Code)
		}
		agent.mu.Lock()
		agent.report = true
		agent.mu.Unlock()
		e.autoTick(time.Now())
		e.autoTick(time.Now())
		if e.queueRow(q).NoReportAt != "" || agent.createCount() < 2 {
			t.Fatalf("after the report started: %+v creates %d", e.queueRow(q), agent.createCount())
		}
	})
	// A delivery fired by itself into a workspace that was stopped checks the report after its wake.
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", nil, 1)
		agent.mu.Lock()
		agent.report = false
		agent.mu.Unlock()
		res := e.runner().resolveMember(context.Background(), e.members["a"])
		d := store.MemoDelivery{ID: "x", QueueID: q, MembershipID: e.members["a"], Auto: true}
		if e.runner().autoAgentReports(context.Background(), res, d) || e.queueRow(q).NoReportAt == "" {
			t.Fatal("an Agent without the report passed")
		}
		agent.mu.Lock()
		agent.report = true
		agent.mu.Unlock()
		if !e.runner().autoAgentReports(context.Background(), res, d) || e.queueRow(q).NoReportAt != "" {
			t.Fatal("an Agent with the report did not pass")
		}
	})
}

// A `sending` delivery in a stopped workspace that is all that holds an open queue with pending
// messages gets the workspace woken, once, and the wake counts as a fire of the day.
func TestMemoDispatchWakesOnlyToSettle(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 1}, 2)
		e.setRuntime(agent, "stopped")
		ts := store.DeliveryTS(time.Now())
		if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
			return tx.InsertDelivery(store.MemoDelivery{ID: store.NewID(), QueueID: q, State: store.DeliverySending,
				MessageIDs: []string{}, CreatedAt: ts, UpdatedAt: ts, SentAt: ts})
		}); err != nil {
			t.Fatal(err)
		}
		r := e.runner()
		sends, wakes := r.dispatch(context.Background(), time.Now(), 2)
		if len(sends) != 0 || len(wakes) != 1 {
			t.Fatalf("sends %d, wakes %d", len(sends), len(wakes))
		}
		if _, wakes := e.runner().dispatch(context.Background(), time.Now(), 2); len(wakes) != 0 {
			t.Fatal("woke again at once")
		}
		n := 0
		for _, d := range e.allDeliveries(q) {
			if d.Outcome == store.DeliveryWake {
				n++
			}
		}
		// The wake rows are hidden from the member's list but count toward the day.
		if n != 0 {
			t.Fatal("a wake row was listed")
		}
		cnt := 0
		_ = e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) (err error) {
			cnt, err = tx.CountDeliveriesCreatedSince(q, store.DeliveryTS(time.Now().Add(-time.Hour)))
			return err
		})
		if cnt != 2 { // the sending delivery and the wake
			t.Fatalf("fires of the day: %d", cnt)
		}
		// A queue whose slot is held by a working session is not woken for it.
		agent2, q2 := e.autoQueue("app", "other", map[string]any{"maxActive": 1}, 1)
		_ = agent2
		_ = q2
	})
}

// Two Control Plane processes dispatching at once never start more sessions than there are slots.
func TestMemoDispatchTwoProcessesNeverExceedSlots(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 2}, 6)
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				e.runner().dispatch(context.Background(), time.Now(), 6)
			}()
		}
		wg.Wait()
		if n := len(e.liveDeliveries(q)); n != 2 {
			t.Fatalf("%d live deliveries with 2 slots", n)
		}
	})
}

// The oldest waiting message goes first across a member's queues, and one member's many queues
// do not take the whole room.
func TestMemoDispatchOldestFirstAcrossQueues(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, qa := e.autoQueue("app", "second", nil, 1)
		time.Sleep(1100 * time.Millisecond)
		e.autoQueue("app", "first", nil, 1)
		sends, _ := e.runner().dispatch(context.Background(), time.Now(), 1)
		if len(sends) != 1 || sends[0].QueueID != qa {
			t.Fatalf("sends %+v, want the queue whose message is older", sends)
		}
	})
}

// A cancelled context ends the pass at once.
func TestMemoDispatchIsBounded(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, q := e.autoQueue("app", "impl", nil, 3)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if sends, _ := e.runner().dispatch(ctx, time.Now(), 2); len(sends) != 0 {
			t.Fatal("fired after the pass's time ran out")
		}
		if len(e.liveDeliveries(q)) != 0 {
			t.Fatal("a delivery was claimed")
		}
	})
}

// Every slot of a queue held by a session that waits for a person is told once, and a message
// that became unconfirmed is told once.
func TestMemoDispatchNotifications(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 1}, 3)
		e.autoTick(time.Now())
		live := e.liveDeliveries(q)
		agent.setRow(live[0].Session, map[string]any{"state": "question"})
		for i := 0; i < 4; i++ {
			e.autoTick(time.Now())
		}
		if got := e.notificationsOf(memoWaitingKind); got != 1 {
			t.Fatalf("%d waiting notifications", got)
		}
		// An unconfirmed close is told once, however many processes close it.
		ts := store.DeliveryTS(time.Now())
		d := store.MemoDelivery{ID: store.NewID(), MembershipID: e.members["a"], QueueID: q, State: store.DeliverySending,
			MessageIDs: []string{"m1"}, CreatedAt: ts, UpdatedAt: ts, SentAt: ts}
		if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error { return tx.InsertDelivery(d) }); err != nil {
			t.Fatal(err)
		}
		e.runner().closeUnknown(context.Background(), d)
		e.runner().closeUnknown(context.Background(), d)
		if got := e.notificationsOf(memoUnconfirmedKind); got != 1 {
			t.Fatalf("%d unconfirmed notifications", got)
		}
	})
}

// An enabled queue blocks deleting its repository, a disabled one does not.
func TestMemoDispatchDeletionGuard(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.autoQueue("app", "impl", nil, 0)
		err := scheduleGuardErr(context.Background(), e.st, e.members["a"], "app", "")
		if err == nil || !strings.Contains(err.Error(), "auto-run") {
			t.Fatalf("an enabled queue did not block: %v", err)
		}
		if err := scheduleGuardErr(context.Background(), e.st, e.members["a"], "other", ""); err != nil {
			t.Fatalf("another repo blocked: %v", err)
		}
		e.queueWith("app", "impl", map[string]any{"enabled": false})
		if err := scheduleGuardErr(context.Background(), e.st, e.members["a"], "app", ""); err != nil {
			t.Fatalf("a disabled queue blocked: %v", err)
		}
	})
}

// The settings of a window, a threshold and the daily bound are validated, and a PUT that
// replaces the settings never touches the state the Control Plane keeps: a pause, draining.
func TestMemoDispatchSettings(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		id := e.category("app", "impl")
		put := func(body map[string]any) int {
			body["instruction"] = "go"
			return e.do("a", "PUT", "/api/memo-queues/"+id, body).Code
		}
		bad := []map[string]any{
			{"openCron": "nonsense", "openMinutes": 30},
			{"openCron": "0 2 * * *"},
			{"openCron": "0 2 * * *", "openMinutes": 99999},
			{"openCron": "0 2 * * *", "openMinutes": 30, "tz": "Mars/Base"},
			{"openCron": "0 2 * * *", "openMinutes": 30, "openAfter": 3},
			{"openAfter": -1}, {"openAfter": 101},
			{"maxFiresPerDay": 0}, {"maxFiresPerDay": 201},
		}
		for _, b := range bad {
			if code := put(b); code != http.StatusBadRequest {
				t.Fatalf("%v: %d", b, code)
			}
		}
		if code := put(map[string]any{"openCron": "0 2 * * *", "openMinutes": 120, "tz": "Asia/Tokyo", "maxFiresPerDay": 200}); code != 200 {
			t.Fatalf("a window: %d", code)
		}
		row := e.queueRow(id)
		if row.OpenCron != "0 2 * * *" || row.OpenMinutes != 120 || row.TZ != "Asia/Tokyo" || row.MaxFiresPerDay != 200 {
			t.Fatalf("%+v", row)
		}
		if code := put(map[string]any{"openAfter": 4}); code != 200 {
			t.Fatal(code)
		}
		row = e.queueRow(id)
		if row.OpenCron != "" || row.OpenMinutes != 0 || row.OpenAfter != 4 || row.MaxFiresPerDay != 20 {
			t.Fatalf("a PUT replaces the whole settings row: %+v", row)
		}
		ctx := context.Background()
		e.pending("app", "impl", "waiting") // draining is kept only while a message is pending
		_, _ = e.pauseQueue(id, "why")
		_ = e.st.SetMemoQueueDraining(ctx, id, true)
		_ = e.st.SetMemoQueueNoReport(ctx, id, "t")
		put(map[string]any{"openAfter": 5})
		row = e.queueRow(id)
		if !row.Paused || row.PausedReason != "why" || !row.Draining || row.NoReportAt != "t" {
			t.Fatalf("a PUT touched the state: %+v", row)
		}
		// The older Control Plane's statement lists no new column: a queue row it writes keeps
		// every state column (MemoTx.PutQueue stands for the settings write of either binary here).
		dto := memoQueueToDTO(row, store.MemoCategory{})
		if !dto.Paused || dto.OpenAfter != 5 || dto.MaxFiresPerDay != 20 {
			t.Fatalf("%+v", dto)
		}
	})
}

// A queue that is turned off while draining is not draining any more: turning it on again starts
// its threshold anew, and one message does not fire it.
func TestMemoDispatchDisabledQueueEndsDraining(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"openAfter": 5}, 1)
		if err := e.st.SetMemoQueueDraining(context.Background(), q, true); err != nil {
			t.Fatal(err)
		}
		e.queueWith("app", "impl", map[string]any{"enabled": false, "openAfter": 5})
		e.autoTick(time.Now())
		if e.queueRow(q).Draining {
			t.Fatal("a disabled queue stayed draining")
		}
		e.queueWith("app", "impl", map[string]any{"enabled": true, "openAfter": 5})
		e.autoTick(time.Now().Add(5 * time.Minute))
		if agent.createCount() != 0 {
			t.Fatal("one message fired a threshold queue that had been draining")
		}
	})
}

// A claim that is refused leaves nothing marked: a threshold queue whose slot is taken does not
// start draining.
func TestMemoDispatchRefusedClaimDoesNotStartDraining(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, q := e.autoQueue("app", "impl", map[string]any{"openAfter": 2, "maxActive": 1}, 2)
		later := time.Now().Add(5 * time.Minute)
		e.autoTick(later) // opens, fires one, drains
		if !e.queueRow(q).Draining {
			t.Fatal("did not start draining with a free slot")
		}
		if err := e.st.SetMemoQueueDraining(context.Background(), q, false); err != nil {
			t.Fatal(err)
		}
		e.pending("app", "impl", "more")
		e.autoTick(later.Add(5 * time.Minute)) // two pending, quiet, but the one slot is taken
		if e.queueRow(q).Draining {
			t.Fatal("a refused claim marked the queue draining")
		}
	})
}

// A delivery that closes as not sent backs the queue off, doubling with each one in a row, so a
// refusing Agent does not use up the day's fires a minute at a time.
func TestMemoDispatchBacksOffAfterNotSent(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 1}, 3)
		agent.mu.Lock()
		agent.createMode = "refuse"
		agent.mu.Unlock()
		now := time.Now()
		e.autoTick(now)
		first := len(e.allDeliveries(q))
		if first != 1 || e.allDeliveries(q)[0].Outcome != store.DeliveryNotSent {
			t.Fatalf("%d deliveries, %+v", first, e.allDeliveries(q))
		}
		for _, d := range []time.Duration{time.Minute, 4 * time.Minute} {
			e.autoTick(now.Add(d))
			if len(e.allDeliveries(q)) != first {
				t.Fatalf("fired again after %v", d)
			}
		}
		e.autoTick(now.Add(6 * time.Minute)) // five minutes after the first
		if len(e.allDeliveries(q)) != 2 {
			t.Fatalf("did not try again after the backoff: %d", len(e.allDeliveries(q)))
		}
		// Two in a row: ten minutes. (A close is stamped by the real clock, so the second
		// delivery's ten minutes end at the first's tenth.)
		e.autoTick(now.Add(9 * time.Minute))
		if len(e.allDeliveries(q)) != 2 {
			t.Fatal("the second backoff was not doubled")
		}
		e.autoTick(now.Add(11 * time.Minute))
		if len(e.allDeliveries(q)) != 3 {
			t.Fatalf("%d", len(e.allDeliveries(q)))
		}
	})
	d := func(outcome, closed string) store.MemoDelivery {
		return store.MemoDelivery{State: store.DeliveryClosed, Outcome: outcome, ClosedAt: closed}
	}
	ts := store.DeliveryTS(time.Now())
	if _, ok := notSentBackoff([]store.MemoDelivery{d(store.DeliveryClean, ts), d(store.DeliveryNotSent, ts)}); ok {
		t.Fatal("a delivery that was sent ends the backoff")
	}
	var many []store.MemoDelivery
	for i := 0; i < 8; i++ {
		many = append(many, d(store.DeliveryNotSent, ts))
	}
	until, _ := notSentBackoff(many)
	if got := time.Until(until); got > 4*time.Hour+time.Minute || got < 3*time.Hour {
		t.Fatalf("the backoff is capped at four hours, got %v", got)
	}
}

// A wake made only to settle is found even when the tick's sends filled every room, and two
// queues of one workspace wake it once.
func TestMemoDispatchWakeNeedsNoRoomAndIsOncePerWorkspace(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q1 := e.autoQueue("app", "one", map[string]any{"maxActive": 1}, 2)
		_, q2 := e.autoQueue("app", "two", map[string]any{"maxActive": 1}, 2)
		e.setRuntime(agent, "stopped")
		ts := store.DeliveryTS(time.Now())
		for _, q := range []string{q1, q2} {
			if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
				return tx.InsertDelivery(store.MemoDelivery{ID: store.NewID(), QueueID: q, State: store.DeliverySending,
					MessageIDs: []string{}, CreatedAt: ts, UpdatedAt: ts, SentAt: ts})
			}); err != nil {
				t.Fatal(err)
			}
		}
		sends, wakes := e.runner().dispatch(context.Background(), time.Now(), 0)
		if len(sends) != 0 || len(wakes) != 1 {
			t.Fatalf("room 0: sends %d, wakes %d, want one wake of the one workspace", len(sends), len(wakes))
		}
		wakeRows := 0
		for _, q := range []string{q1, q2} {
			_ = e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
				n, err := tx.CountWakesSince(q, store.DeliveryTS(time.Now().Add(-time.Hour)))
				wakeRows += n
				return err
			})
		}
		if wakeRows != 1 {
			t.Fatalf("%d wakes counted against the day, want 1", wakeRows)
		}
	})
}

// staleWork is the dispatcher's snapshot of a queue as a tick read it before taking the lock.
func (e *deliveryEnv) staleWork(queueID string) *memoQueueWork {
	return &memoQueueWork{q: e.queueRow(queueID), pending: store.QueuePending{Count: 1, Oldest: "2000-01-01T00:00:00Z", Latest: "2000-01-01T00:00:00Z"}}
}

// What a tick read before it took the lock is read again behind it: a tenant switch turned off,
// a queue disabled or paused, or a delivery that just closed as not sent after the snapshot stop
// the fire, and a wake made only to settle is not recorded either.
func TestMemoDispatchRechecksBehindTheLock(t *testing.T) {
	cases := map[string]func(e *deliveryEnv, q string){
		"tenant switch off": func(e *deliveryEnv, q string) { e.memoQueuesOn(false) },
		"queue disabled": func(e *deliveryEnv, q string) {
			e.queueWith("app", "impl", map[string]any{"enabled": false})
		},
		"queue paused": func(e *deliveryEnv, q string) { _, _ = e.pauseQueue(q, "x") },
		"closed as not sent": func(e *deliveryEnv, q string) {
			ts := store.DeliveryTS(time.Now())
			_ = e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
				return tx.InsertDelivery(store.MemoDelivery{ID: store.NewID(), QueueID: q, State: store.DeliveryClosed,
					Outcome: store.DeliveryNotSent, CreatedAt: ts, UpdatedAt: ts, ClosedAt: ts})
			})
		},
		"daily cap lowered": func(e *deliveryEnv, q string) {
			e.queueWith("app", "impl", map[string]any{"enabled": true, "maxFiresPerDay": 1})
			ts := store.DeliveryTS(time.Now())
			_ = e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
				return tx.InsertDelivery(store.MemoDelivery{ID: store.NewID(), QueueID: q, State: store.DeliveryClosed,
					Outcome: store.DeliveryClean, CreatedAt: ts, UpdatedAt: ts, ClosedAt: ts})
			})
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
				agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 1}, 2)
				r := e.runner()
				w := e.staleWork(q)
				views := map[string]*memoMemberView{}
				change(e, q)
				if d, ok := r.fireQueue(context.Background(), views, w, nil, time.Now()); ok {
					t.Fatalf("fired on a stale snapshot: %+v", d)
				}
				if len(e.liveDeliveries(q)) != 0 || agent.createCount() != 0 {
					t.Fatal("a delivery was claimed")
				}
			})
		})
	}
	// A wake made only to settle: the same conditions.
	for name, change := range map[string]func(e *deliveryEnv, q string){
		"tenant switch off": func(e *deliveryEnv, q string) { e.memoQueuesOn(false) },
		"queue disabled": func(e *deliveryEnv, q string) {
			e.queueWith("app", "impl", map[string]any{"enabled": false, "maxActive": 1})
		},
		"queue paused": func(e *deliveryEnv, q string) { _, _ = e.pauseQueue(q, "x") },
		"slot freed": func(e *deliveryEnv, q string) {
			_ = e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
				for _, d := range mustLive(tx, q) {
					if _, err := tx.MoveDelivery(d.ID, store.DeliverySending, store.DeliveryClosed, "", store.DeliveryNotSent, store.DeliveryTS(time.Now())); err != nil {
						return err
					}
				}
				return nil
			})
		},
	} {
		t.Run("wake "+name, func(t *testing.T) {
			eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
				agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 1}, 2)
				e.setRuntime(agent, "stopped")
				ts := store.DeliveryTS(time.Now())
				live := store.MemoDelivery{ID: store.NewID(), MembershipID: e.members["a"], QueueID: q, State: store.DeliverySending,
					MessageIDs: []string{}, CreatedAt: ts, UpdatedAt: ts, SentAt: ts}
				if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error { return tx.InsertDelivery(live) }); err != nil {
					t.Fatal(err)
				}
				r := e.runner()
				w := e.staleWork(q)
				change(e, q)
				r.settleWake(context.Background(), w, []store.MemoDelivery{live}, time.Now())
				if len(r.wakeJobs) != 0 {
					t.Fatal("a wake was scheduled on a stale snapshot")
				}
				_ = e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
					n, _ := tx.CountWakesSince(q, store.DeliveryTS(time.Now().Add(-time.Hour)))
					if n != 0 {
						t.Fatalf("%d wake rows", n)
					}
					return nil
				})
			})
		})
	}
}

func mustLive(tx store.MemoTxOps, q string) []store.MemoDelivery {
	ds, _ := tx.LiveQueueDeliveries(q)
	return ds
}

// A usage limit is read from every session of the tree, not from the strongest reason, and by the
// kind the session really has: a parent that waits for a limit while its child works still stops
// a new session of that kind, and a queue changed to another kind is not stopped by it.
func TestMemoDispatchUsageLimitReadsTheTreeAndTheKind(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 3}, 1)
		e.autoTick(time.Now())
		live := e.liveDeliveries(q)
		if len(live) != 1 {
			t.Fatalf("%d live", len(live))
		}
		agent.setRow(live[0].Session, map[string]any{"state": "limited"})
		agent.child("c1", live[0].Session, map[string]any{"state": "working", "progressAgeSec": 1})
		e.autoTick(time.Now())
		if got := e.delivery(live[0].ID); got.HeldBy != store.HeldMachine || got.LimitKinds != ",claude," {
			t.Fatalf("the strongest reason is the child's, the limit is kept apart: %+v", got)
		}
		e.pending("app", "impl", "two")
		before := agent.createCount()
		for i := 0; i < 3; i++ {
			e.autoTick(time.Now())
		}
		if agent.createCount() != before {
			t.Fatal("started a session of the limited kind while the parent waited for a limit")
		}
		// The same queue changed to another kind is not held by a claude session.
		e.queueWith("app", "impl", map[string]any{"enabled": true, "maxActive": 3, "agentKind": "codex"})
		e.autoTick(time.Now())
		if agent.createCount() == before {
			t.Fatal("a claude limit held a codex queue")
		}
	})
}

// A pause decided on failures from before a resume does not land after it, and the old failures
// are not counted again.
func TestMemoDispatchBreakerPauseIsDecidedBehindTheLock(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", nil, 6)
		e.autoTick(time.Now())
		live := e.liveDeliveries(q)
		agent.setRow(live[0].Session, map[string]any{"queueTurn": map[string]any{"outcome": "turn-failed", "ms": 120000, "endedAt": "2026-11-15T01:00:00Z"}})
		agent.setRow(live[1].Session, map[string]any{"queueTurn": map[string]any{"outcome": "turn-failed", "ms": 120000, "endedAt": "2026-11-15T01:00:01Z"}})
		e.settleNow(time.Now()) // the turns are copied, nothing decides yet
		stale := e.queueRow(q)  // tick A read the queue unpaused
		if stale.Paused {
			t.Fatal("paused early")
		}
		// Another process paused it, and the member resumed.
		if won, err := e.pauseQueue(q, "other"); err != nil || !won {
			t.Fatal(won, err)
		}
		time.Sleep(10 * time.Millisecond)
		if rec := e.do("a", "POST", "/api/memo-queues/"+q+"/resume", nil); rec.Code != http.StatusNoContent {
			t.Fatal(rec.Code)
		}
		e.runner().checkBreaker(context.Background(), stale, time.Now())
		if e.queueRow(q).Paused || e.notificationsOf(memoPausedKind) != 0 {
			t.Fatal("a pause decided before the resume landed after it")
		}
	})
}

// Draining ends in the transaction that leaves nothing pending, so a message added afterwards
// begins a new batch that has to reach its threshold: after the last pending message is claimed,
// and after every pending message is deleted. A pause keeps it while messages are left.
func TestMemoDispatchDrainingEndsWhenNothingIsPending(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"openAfter": 2, "maxActive": 6}, 2)
		later := time.Now().Add(2 * time.Minute)
		e.autoTick(later) // two pending, quiet: opens, fires one at a time per round
		if agent.createCount() == 0 {
			t.Fatal("did not open")
		}
		// Whatever is left, press fire now until nothing is pending: the claim ends the drain.
		for e.fire(q).Code == http.StatusAccepted {
		}
		if e.queueRow(q).Draining {
			t.Fatal("still draining after the last pending message was claimed")
		}
		before := agent.createCount()
		e.pending("app", "impl", "one new")
		// Within a fire's budget: a later "now" would return the claimed fire-now delivery's
		// message to pending, which is another story.
		soon := time.Now().Add(2 * time.Minute)
		e.autoTick(soon)
		e.autoTick(soon)
		if agent.createCount() != before+1 { // the fire-now delivery is sent, nothing else is fired
			t.Fatal("one message after an empty moment fired a threshold queue")
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, q := e.autoQueue("app", "impl", map[string]any{"openAfter": 3}, 3)
		if err := e.st.SetMemoQueueDraining(context.Background(), q, true); err != nil {
			t.Fatal(err)
		}
		memos := decode[[]memoDTO](t, e.do("a", "GET", "/api/memos", nil))
		for _, m := range memos {
			if rec := e.do("a", "DELETE", "/api/memos/"+m.ID, nil); rec.Code >= 300 {
				t.Fatalf("delete: %d", rec.Code)
			}
		}
		if e.queueRow(q).Draining {
			t.Fatal("still draining with every message deleted")
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"openAfter": 2, "maxActive": 1}, 3)
		later := time.Now().Add(5 * time.Minute)
		e.autoTick(later)
		if !e.queueRow(q).Draining {
			t.Fatal("not draining")
		}
		_, _ = e.pauseQueue(q, "x")
		e.autoTick(later)
		if !e.queueRow(q).Draining {
			t.Fatal("a pause ended draining while messages are left: the drain resumes with the queue")
		}
		e.do("a", "POST", "/api/memo-queues/"+q+"/resume", nil)
		live := e.liveDeliveries(q)
		agent.setRow(live[0].Session, map[string]any{"state": "idle"})
		before := agent.createCount()
		for i := 0; i < 4; i++ {
			e.autoTick(later)
		}
		if agent.createCount() <= before {
			t.Fatal("did not go on draining after the resume")
		}
	})
}

// The batch that ended a drain by claiming the last pending message and came back as not sent puts
// the queue back to draining: the returned message does not wait for a threshold it passed.
func TestMemoDispatchDrainSurvivesAReturnedLastBatch(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"openAfter": 2, "maxActive": 1}, 2)
		now := time.Now().Add(2 * time.Minute)
		e.autoTick(now) // opens, fires one, one pending left, draining
		live := e.liveDeliveries(q)
		agent.setRow(live[0].Session, map[string]any{"state": "idle"})
		e.settleNow(now)
		e.settleNow(now) // closed clean: the slot is free
		agent.mu.Lock()
		agent.createMode = "refuse"
		agent.mu.Unlock()
		e.autoTick(now) // claims the last pending, the Agent refuses, the message returns
		if p := e.pendingCount(q); p != 1 {
			t.Fatalf("%d pending, want the returned one", p)
		}
		if !e.queueRow(q).Draining {
			t.Fatal("the drain was not restored with the returned batch")
		}
		agent.mu.Lock()
		agent.createMode = ""
		agent.mu.Unlock()
		before := agent.createCount()
		e.autoTick(now.Add(6 * time.Minute)) // after the back-off: one pending, below K, still draining
		if agent.createCount() != before+1 {
			t.Fatal("the returned batch of a drain stayed below its threshold for good")
		}
	})
}

func (e *deliveryEnv) pendingCount(queueID string) int {
	st, err := e.st.ListQueuePendingStats(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return st[queueID].Count
}

// Settle wakes are bounded per tick, and one workspace is woken once in the guard time whatever
// queue asks: two queues of a member (two Control Planes included) record one wake row.
func TestMemoDispatchWakesAreBoundedAndOnePerMember(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q1 := e.autoQueue("app", "one", map[string]any{"maxActive": 1}, 2)
		_, q2 := e.autoQueue("app", "two", map[string]any{"maxActive": 1}, 2)
		e.setRuntime(agent, "stopped")
		ts := store.DeliveryTS(time.Now())
		for _, q := range []string{q1, q2} {
			if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
				return tx.InsertDelivery(store.MemoDelivery{ID: store.NewID(), QueueID: q, State: store.DeliverySending,
					MessageIDs: []string{}, CreatedAt: ts, UpdatedAt: ts, SentAt: ts})
			}); err != nil {
				t.Fatal(err)
			}
		}
		// Two Control Planes reach the two queues at once, each through its own snapshot.
		now := time.Now()
		r1, r2 := e.runner(), e.runner()
		live1 := []store.MemoDelivery{{QueueID: q1, State: store.DeliverySending}}
		live2 := []store.MemoDelivery{{QueueID: q2, State: store.DeliverySending}}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); r1.settleWake(context.Background(), e.staleWork(q1), live1, now) }()
		go func() { defer wg.Done(); r2.settleWake(context.Background(), e.staleWork(q2), live2, now) }()
		wg.Wait()
		rows := 0
		_ = e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) (err error) {
			rows, err = tx.CountMemberWakesSince(store.DeliveryTS(now.Add(-time.Hour)))
			return err
		})
		if rows != 1 || len(r1.wakeJobs)+len(r2.wakeJobs) != 1 {
			t.Fatalf("%d wake rows, %d jobs, want one workspace woken once", rows, len(r1.wakeJobs)+len(r2.wakeJobs))
		}
	})
}

// The first turn of a session that ended or was archived after its delivery closed is copied onto
// the closed delivery, so the breaker counts it.
func TestMemoOccupancyFirstTurnAfterTheClose(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setRow(d.Session, map[string]any{"archived": true})
		e.settleNow(time.Now()) // closed aborted before any turn showed
		got := e.delivery(d.ID)
		if got.State != store.DeliveryClosed || got.TurnEndedAt != "" {
			t.Fatalf("%+v", got)
		}
		agent.setRow(d.Session, map[string]any{"queueTurn": map[string]any{"outcome": "turn-failed", "ms": 5000, "endedAt": "2026-11-15T01:00:00Z"}})
		e.settleNow(time.Now())
		got = e.delivery(d.ID)
		if got.TurnOutcome != "turn-failed" || got.TurnMs != 5000 || got.TurnEndedAt == "" {
			t.Fatalf("the turn of a closed delivery was not collected: %+v", got)
		}
		agent.setRow(d.Session, map[string]any{"queueTurn": map[string]any{"outcome": "clean", "ms": 9, "endedAt": "2026-11-15T09:00:00Z"}})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.TurnOutcome != "turn-failed" {
			t.Fatalf("the first turn was rewritten: %+v", got)
		}
	})
}

// A manual fire that claims the last pending message of a draining queue and comes back as not
// sent puts the queue back to draining too, the same as a fire by itself.
func TestMemoDispatchDrainSurvivesAReturnedManualFire(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"openAfter": 2, "maxActive": 2}, 2)
		now := time.Now().Add(2 * time.Minute)
		e.autoTick(now) // opens, fires one, one pending left, draining
		if !e.queueRow(q).Draining || e.pendingCount(q) != 1 {
			t.Fatalf("draining %v, pending %d", e.queueRow(q).Draining, e.pendingCount(q))
		}
		agent.mu.Lock()
		agent.createMode = "refuse"
		agent.mu.Unlock()
		if rec := e.fire(q); rec.Code != http.StatusAccepted { // claims the last pending: the drain ends
			t.Fatalf("fire now: %d %s", rec.Code, rec.Body.String())
		}
		if e.queueRow(q).Draining {
			t.Fatal("the claim of the last pending message did not end the drain")
		}
		e.autoTick(now) // the send is refused, the message returns
		if e.pendingCount(q) != 1 || !e.queueRow(q).Draining {
			t.Fatalf("pending %d, draining %v: the drain was not restored", e.pendingCount(q), e.queueRow(q).Draining)
		}
		agent.mu.Lock()
		agent.createMode = ""
		agent.mu.Unlock()
		before := agent.createCount()
		e.autoTick(now.Add(6 * time.Minute))
		if agent.createCount() != before+1 {
			t.Fatal("the returned message of a manual fire stayed below its threshold for good")
		}
	})
}

// Closed deliveries whose first turn never shows (a deleted session) cannot keep the ones behind
// them from being read: with more of them than one pass reads, the pass goes on with the rest.
func TestMemoOccupancyClosedTurnsAreReadFairly(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", nil, 0)
		insert := func(session string, closedAgo time.Duration) string {
			id := store.NewID()
			ts := store.DeliveryTS(time.Now().Add(-closedAgo))
			if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
				return tx.InsertDelivery(store.MemoDelivery{ID: id, QueueID: q, State: store.DeliveryClosed, Outcome: store.DeliveryClean,
					Session: session, MessageIDs: []string{}, CreatedAt: ts, UpdatedAt: ts, ClosedAt: ts, Reported: true, RetireResult: "confirmed"})
			}); err != nil {
				t.Fatal(err)
			}
			return id
		}
		for i := 0; i < memoClosedTurnsPerTick+5; i++ {
			insert(fmt.Sprintf("gone-%d", i), 3*time.Hour)
		}
		realIDs := []string{insert("real-1", time.Hour), insert("real-2", time.Hour)}
		agent.mu.Lock()
		for _, n := range []string{"real-1", "real-2"} {
			agent.rows = append(agent.rows, map[string]any{"name": n, "kind": "claude", "alive": false, "state": "",
				"queueTurn": map[string]any{"outcome": "turn-failed", "ms": 4000, "endedAt": "2026-11-15T01:00:00Z"}})
		}
		agent.mu.Unlock()
		e.settleNow(time.Now()) // reads the fifty oldest
		e.settleNow(time.Now()) // goes on with the ones never tried
		for _, id := range realIDs {
			if got := e.delivery(id); got.TurnOutcome != "turn-failed" {
				t.Fatalf("a delivery behind fifty that never fill was not read: %+v", got)
			}
		}
	})
}
