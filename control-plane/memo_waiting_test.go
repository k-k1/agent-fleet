package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Why a queue with notes waiting starts nothing (issue #4257). Each test sets up one reason, runs
// the dispatcher's own tick to show that it fires nothing, and reads the reason the member's list
// gives at the same moment; where the reason lifts, it shows the reason gone and the next tick
// firing.

// waitingOf is what the member's list says of a queue at now.
func (e *deliveryEnv) waitingOf(queueID string, now time.Time) *memoWaiting {
	e.t.Helper()
	ctx := context.Background()
	on, err := e.st.GetTenantMemoQueues(ctx, e.t1.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	stats, err := e.st.ListMemberQueuePendingStats(ctx, e.members["a"])
	if err != nil {
		e.t.Fatal(err)
	}
	var w *memoWaiting
	err = e.st.MemoRead(ctx, e.members["a"], func(rd store.MemoWaitOps) (err error) {
		w, err = memoQueueWaiting(rd, e.queueRow(queueID), on, stats[queueID], now)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return w
}

// expectWaiting runs a tick and checks that nothing started and the reason is want.
func (e *deliveryEnv) expectWaiting(agent *fakeQueueAgent, q string, now time.Time, want string) *memoWaiting {
	e.t.Helper()
	before := agent.createCount()
	e.autoTick(now)
	if agent.createCount() != before {
		e.t.Fatalf("the dispatcher fired although the reason is %q", want)
	}
	w := e.waitingOf(q, now)
	if w == nil || w.Reason != want {
		e.t.Fatalf("waiting = %+v, want %q", w, want)
	}
	return w
}

// expectFires checks that no reason is given and the next tick starts a session.
func (e *deliveryEnv) expectFires(agent *fakeQueueAgent, q string, now time.Time) {
	e.t.Helper()
	if w := e.waitingOf(q, now); w != nil {
		e.t.Fatalf("waiting = %+v, but a run can start", w)
	}
	before := agent.createCount()
	e.autoTick(now)
	if agent.createCount() == before {
		e.t.Fatal("no reason was given and the dispatcher did not fire")
	}
}

func (e *deliveryEnv) insertDelivery(q string, at time.Time, outcome string) {
	e.t.Helper()
	ts := store.DeliveryTS(at)
	if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
		return tx.InsertDelivery(store.MemoDelivery{ID: store.NewID(), QueueID: q, State: store.DeliveryClosed,
			Outcome: outcome, CreatedAt: ts, UpdatedAt: ts, ClosedAt: ts})
	}); err != nil {
		e.t.Fatal(err)
	}
}

func wantUntil(t *testing.T, w *memoWaiting, at time.Time) {
	t.Helper()
	if w.Until != at.UTC().Format(time.RFC3339) {
		t.Fatalf("until = %q, want %s", w.Until, at.UTC().Format(time.RFC3339))
	}
}

func TestMemoWaitingTenantOffAndDisabled(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", nil, 1)
		now := time.Now()
		e.memoQueuesOn(false)
		if w := e.expectWaiting(agent, q, now, waitTenantOff); w.Until != "" || w.Detail != nil {
			t.Fatalf("%+v", w)
		}
		// Both hold: the organisation's switch is first.
		e.memoQueuesOn(true)
		e.queueWith("app", "impl", map[string]any{"enabled": false})
		e.memoQueuesOn(false)
		e.expectWaiting(agent, q, now, waitTenantOff)
		e.memoQueuesOn(true)
		e.expectWaiting(agent, q, now, waitDisabled)
		e.queueWith("app", "impl", map[string]any{"enabled": true})
		e.expectFires(agent, q, now)
	})
}

// Nothing waiting, nothing to say, whatever else holds.
func TestMemoWaitingAbsentWithoutNotes(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, q := e.autoQueue("app", "impl", map[string]any{"enabled": false}, 0)
		e.memoQueuesOn(false)
		if w := e.waitingOf(q, time.Now()); w != nil {
			t.Fatalf("%+v", w)
		}
	})
}

func TestMemoWaitingNoReportAndPaused(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", nil, 2)
		agent.mu.Lock()
		agent.report = false
		agent.mu.Unlock()
		now := time.Now()
		e.expectWaiting(agent, q, now, waitNoReport)
		// Paused as well: the report is checked first.
		if ok, err := e.pauseQueue(q, "test"); err != nil || !ok {
			t.Fatal(ok, err)
		}
		e.expectWaiting(agent, q, now, waitNoReport)
		// The mark is the dispatcher's own record: a paused queue is not looked at, so it stays
		// until the first tick after the resume finds the report again and fires.
		agent.mu.Lock()
		agent.report = true
		agent.mu.Unlock()
		e.do("a", "POST", "/api/memo-queues/"+q+"/resume", nil)
		if w := e.waitingOf(q, now); w == nil || w.Reason != waitNoReport {
			t.Fatalf("%+v", w)
		}
		e.autoTick(now)
		if agent.createCount() == 0 {
			t.Fatal("did not fire once the report was found")
		}
		if w := e.waitingOf(q, now); w != nil {
			t.Fatalf("%+v", w)
		}
	})
}

func TestMemoWaitingWindowClosedUntilNextStart(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now().UTC().Truncate(time.Minute).Add(20 * time.Second)
		tokyo, _ := time.LoadLocation("Asia/Tokyo")
		start := now.Add(3 * time.Hour).Truncate(time.Minute)
		cron := fmt.Sprintf("%d %d * * *", start.In(tokyo).Minute(), start.In(tokyo).Hour())
		agent, q := e.autoQueue("app", "impl", map[string]any{"openCron": cron, "openMinutes": 30, "tz": "Asia/Tokyo"}, 1)
		w := e.expectWaiting(agent, q, now, waitWindowClosed)
		wantUntil(t, w, start)
		// Paused as well: the pause is first.
		if ok, err := e.pauseQueue(q, "test"); err != nil || !ok {
			t.Fatal(ok, err)
		}
		e.expectWaiting(agent, q, now, waitPaused)
		e.do("a", "POST", "/api/memo-queues/"+q+"/resume", nil)
		// Inside the window it fires, and the reason is gone.
		e.expectFires(agent, q, start.Add(time.Minute))
	})
}

func TestMemoWaitingThreshold(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"openAfter": 3}, 2)
		now := time.Now()
		w := e.expectWaiting(agent, q, now, waitBelowThreshold)
		if w.Detail == nil || w.Detail.Needed != 3 || w.Detail.Waiting != 2 || w.Until != "" {
			t.Fatalf("%+v", w)
		}
		e.pending("app", "impl", "third")
		// Three are waiting, but one was added just now: wait for a quiet minute.
		w = e.expectWaiting(agent, q, now, waitQuietMinute)
		stats, _ := e.st.ListMemberQueuePendingStats(context.Background(), e.members["a"])
		latest, err := time.Parse(time.RFC3339, stats[q].Latest)
		if err != nil {
			t.Fatal(err)
		}
		wantUntil(t, w, latest.Add(memoThresholdQuiet))
		e.expectFires(agent, q, now.Add(2*time.Minute))
	})
}

func TestMemoWaitingDailyCapUntilOldestLeaves(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"maxFiresPerDay": 3}, 2)
		now := time.Now().UTC().Truncate(time.Second)
		for _, h := range []int{5, 3, 9, 7} { // four runs of the day against a cap of 3: the second oldest has to leave
			e.insertDelivery(q, now.Add(-time.Duration(h)*time.Hour), store.DeliveryClean)
		}
		w := e.expectWaiting(agent, q, now, waitDailyCap)
		wantUntil(t, w, now.Add(-7*time.Hour).Add(24*time.Hour))
		// The first of the two oldest leaving is not enough.
		if w := e.waitingOf(q, now.Add(24*time.Hour-9*time.Hour+time.Minute)); w == nil || w.Reason != waitDailyCap {
			t.Fatalf("%+v", w)
		}
		e.expectFires(agent, q, now.Add(24*time.Hour-7*time.Hour+time.Minute))
	})
}

func TestMemoWaitingBackoffAndUsageLimit(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 3}, 5)
		now := time.Now().UTC().Truncate(time.Second)
		e.insertDelivery(q, now.Add(-time.Minute), store.DeliveryNotSent)
		w := e.expectWaiting(agent, q, now, waitBackoff)
		wantUntil(t, w, now.Add(-time.Minute).Add(5*time.Minute))
		e.expectFires(agent, q, now.Add(5*time.Minute))

		// A session waiting for a usage limit holds the queue; no time is known.
		live := e.liveDeliveries(q)
		agent.setRow(live[0].Session, map[string]any{"state": "limited"})
		later := now.Add(6 * time.Minute)
		w = e.expectWaiting(agent, q, later, waitUsageLimit)
		if w.Until != "" || w.Detail == nil || w.Detail.AgentKind != e.queueRow(q).AgentKind {
			t.Fatalf("%+v", w)
		}
		// A back-off as well: the back-off is checked first.
		e.insertDelivery(q, later, store.DeliveryNotSent)
		e.expectWaiting(agent, q, later, waitBackoff)
	})
}

func TestMemoWaitingOrderOfCapAndBackoff(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"maxFiresPerDay": 1}, 2)
		now := time.Now().UTC().Truncate(time.Second)
		e.insertDelivery(q, now.Add(-time.Minute), store.DeliveryNotSent)
		e.expectWaiting(agent, q, now, waitDailyCap) // the cap is checked before the back-off
	})
}

func TestMemoWaitingNoPlace(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 1}, 3)
		now := time.Now()
		e.autoTick(now)
		if len(e.liveDeliveries(q)) != 1 {
			t.Fatal("the first run did not start")
		}
		w := e.expectWaiting(agent, q, now, waitNoPlace)
		if w.Until != "" || w.Detail == nil || w.Detail.By != "category" {
			t.Fatalf("%+v", w)
		}
	})
	// The member's own limit, with places left in the queue itself.
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, other := e.autoQueue("app", "other", map[string]any{"maxActive": 6}, memoQueueMemberLimit)
		now := time.Now()
		for i := 0; i < 4 && len(e.liveDeliveries(other)) < memoQueueMemberLimit; i++ {
			e.autoTick(now)
		}
		if len(e.liveDeliveries(other)) != memoQueueMemberLimit {
			t.Fatalf("%d live", len(e.liveDeliveries(other)))
		}
		q := e.queueWith("app", "impl", map[string]any{"enabled": true, "maxActive": 6})
		e.pending("app", "impl", "waits")
		w := e.expectWaiting(agent, q, now, waitNoPlace)
		if w.Detail == nil || w.Detail.By != "member" {
			t.Fatalf("%+v", w)
		}
	})
}

// The list answers the field, and leaves it out when a run can start.
func TestMemoWaitingOnTheWire(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, q := e.autoQueue("app", "impl", map[string]any{"openAfter": 5}, 2)
		list := decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil))
		if len(list.Queues) != 1 || list.Queues[0].Waiting == nil || list.Queues[0].Waiting.Reason != waitBelowThreshold {
			t.Fatalf("%+v", list.Queues)
		}
		e.queueWith("app", "impl", map[string]any{"enabled": true})
		rec := e.do("a", "GET", "/api/memo-queues", nil)
		if rec.Code != http.StatusOK || containsWaiting(rec.Body.String()) {
			t.Fatalf("a queue that can fire carries waiting: %s", rec.Body.String())
		}
		_ = q
	})
}

func containsWaiting(s string) bool {
	for i := 0; i+9 <= len(s); i++ {
		if s[i:i+9] == `"waiting"` {
			return true
		}
	}
	return false
}

// fireBlock is about a press of "Run now", which ignores the window, the threshold and the caps
// that hold back a fire by itself: only the claim's own refusals show.
func TestMemoFireBlockOnTheWire(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// Nothing waiting: a press has nothing to take.
		e.queueWith("app", "empty", map[string]any{"enabled": true})
		list0 := decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil))
		for _, dq := range list0.Queues {
			if dq.Category == "empty" && (dq.FireBlock == nil || dq.FireBlock.Reason != waitNothing) {
				t.Fatalf("empty queue: %+v", dq.FireBlock)
			}
		}
		// Below its threshold the queue waits, but a press would claim the two notes.
		_, q := e.autoQueue("app", "impl", map[string]any{"openAfter": 5, "maxActive": 1}, 2)
		list := decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil))
		var impl memoQueueDTO
		for _, dq := range list.Queues {
			if dq.Category == "impl" {
				impl = dq
			}
		}
		if impl.Waiting == nil || impl.FireBlock != nil {
			t.Fatalf("below threshold: %+v", list.Queues)
		}
		// A press takes the one slot; the next press would be refused for want of a place.
		if rec := e.do("a", "POST", "/api/memo-queues/"+q+"/fire", nil); rec.Code != http.StatusAccepted {
			t.Fatalf("fire: %d %s", rec.Code, rec.Body.String())
		}
		e.pending("app", "impl", "one more")
		list = decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil))
		for _, dq := range list.Queues {
			if dq.Category != "impl" {
				continue
			}
			if fb := dq.FireBlock; fb == nil || fb.Reason != waitNoPlace || fb.Detail == nil || fb.Detail.By != "category" {
				t.Fatalf("slots full: %+v", dq.FireBlock)
			}
		}
	})
}
