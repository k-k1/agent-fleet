package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// silentMember makes member "c" a running workspace whose Agent never answers until the request's
// context ends, as a hung workspace does.
func (e *deliveryEnv) silentMember() {
	e.t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	e.t.Cleanup(srv.Close)
	ws := store.Workspace{ID: "ws-c", TenantID: e.t1.ID, MembershipID: e.members["c"], ContainerName: "c", Network: "n",
		DataDir: "/d", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
	if err := e.st.CreateWorkspace(context.Background(), ws); err != nil {
		e.t.Fatal(err)
	}
	e.mgr.mu.Lock()
	e.mgr.rts[e.members["c"]] = cachedRT{rt: stubRuntime{endpoint: srv.URL, token: "tok", state: "running"}, ws: ws}
	e.mgr.mu.Unlock()
}

// insertFairDelivery stores a delivery of a member, made ago before now.
func (e *deliveryEnv) insertFairDelivery(membership string, d store.MemoDelivery, ago time.Duration) {
	e.t.Helper()
	ts := store.DeliveryTS(time.Now().Add(-ago))
	d.ID = store.NewID()
	d.MessageIDs = []string{}
	d.CreatedAt, d.UpdatedAt, d.SentAt = ts, ts, ts
	if d.State == store.DeliveryClosed {
		d.ClosedAt = ts
	}
	if err := e.st.MemoTx(context.Background(), membership, func(tx store.MemoTxOps) error {
		if err := tx.InsertDelivery(d); err != nil {
			return err
		}
		if d.RetirePending {
			return tx.SetDeliveryRetirePending(d.ID)
		}
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
}

// A member whose workspace never answers, first in line, does not keep the delivery of the member
// behind it from being settled: every tick starts with the member tried longest ago, so the
// member behind is reached by the second tick and closes on its second clear read (third tick).
func TestMemoFairSettleSilentMemberFirst(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, d := e.openReported(nil)
		e.silentMember()
		e.insertFairDelivery(e.members["c"], store.MemoDelivery{QueueID: "qc", State: store.DeliveryOpen, Session: "slow-session"}, 3*time.Hour)
		r := e.runner()
		r.settleBudget = 300 * time.Millisecond
		r.retireReserve = 100 * time.Millisecond
		for i := 1; i <= 3; i++ {
			r.settle(context.Background(), time.Now())
			if got := e.delivery(d.ID); i < 3 && got.State == store.DeliveryClosed {
				t.Fatalf("tick %d: closed early: %+v", i, got)
			}
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed {
			t.Fatalf("a silent member first in line kept the delivery behind it open for 3 ticks: %+v", got)
		}
	})
}

// The same for a pending retire: the retire pass tries the member tried longest ago first, so the
// retire behind a silent member is made on the second tick.
func TestMemoFairRetireSilentMemberFirst(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.mu.Lock()
		agent.retireStatus = http.StatusBadGateway
		agent.mu.Unlock()
		e.settleNow(time.Now())
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || !got.RetirePending {
			t.Fatalf("setup: %+v", got)
		}
		agent.mu.Lock()
		agent.retireStatus = 0
		agent.mu.Unlock()
		e.silentMember()
		e.insertFairDelivery(e.members["c"], store.MemoDelivery{QueueID: "qc", State: store.DeliveryClosed, Outcome: store.DeliveryClean,
			Session: "slow-session", Reported: true, RetirePending: true}, 3*time.Hour)
		r := e.runner()
		r.settleBudget = 300 * time.Millisecond
		r.retireReserve = 100 * time.Millisecond
		r.settle(context.Background(), time.Now())
		if got := e.delivery(d.ID); got.RetireResult != "" {
			t.Fatalf("tick 1 retired behind the silent member: %+v", got)
		}
		r.settle(context.Background(), time.Now())
		if got := e.delivery(d.ID); got.RetireResult != "confirmed" {
			t.Fatalf("a silent member first in line kept the retire behind it pending for 2 ticks: %+v", got)
		}
	})
}

// Claimed deliveries need no workspace read: a stale one is closed in the first tick, and the sends
// keep their oldest-first order and per-tick cap, whatever a silent member at the head of the list
// does with the budget.
func TestMemoFairSettleKeepsSendsAndStaleClaims(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.useQueueAgent("")
		var queued []string
		for _, c := range []string{"a", "b", "c"} {
			q := e.queueWith("app", c, nil)
			e.pending("app", c, "x")
			queued = append(queued, e.fired(q).ID)
		}
		staleQ := e.queueWith("app", "stale", nil)
		e.insertFairDelivery(e.members["a"], store.MemoDelivery{QueueID: staleQ, State: store.DeliveryClaimed}, 3*time.Hour)
		e.silentMember()
		e.insertFairDelivery(e.members["c"], store.MemoDelivery{QueueID: "qc", State: store.DeliveryOpen, Session: "slow-session"}, 4*time.Hour)
		r := e.runner()
		r.settleBudget = 300 * time.Millisecond
		r.retireReserve = 100 * time.Millisecond
		sends := r.settle(context.Background(), time.Now())
		if len(sends) != memoQueueSendsPerTick {
			t.Fatalf("%d sends, want %d", len(sends), memoQueueSendsPerTick)
		}
		for i, d := range sends {
			if d.ID != queued[i] {
				t.Fatalf("send %d is %s, want %s (oldest first)", i, d.ID, queued[i])
			}
		}
		for _, d := range e.liveDeliveries(staleQ) {
			t.Fatalf("a stale claim waited behind the silent member: %+v", d)
		}
	})
}
