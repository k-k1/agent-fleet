package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// An open run held by something the occupancy rule never lets go of on its own (a child that
// keeps working) is ended by the person: it closes as "ended" (not an abort), frees exactly its
// slot, and its session tree is retired without the act mark. A second press and a press after
// the close change nothing.
func TestMemoEndDeliveryFreesSlotAndRetires(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.child("c1", d.Session, map[string]any{"state": "working", "progressAgeSec": 5})
		agent.setActMark(500) // a kept report with a mark: a plain retire would send it as a condition
		for i := 0; i < 3; i++ {
			e.settleNow(time.Now())
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.ActMark != 500 {
			t.Fatalf("the child should hold the run, with the mark kept: %+v", got)
		}
		url := "/api/memo-queues/" + d.QueueID + "/deliveries/" + d.ID + "/end"

		rec := e.do("a", "POST", url, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("end: %d %s", rec.Code, rec.Body.String())
		}
		dto := decode[memoDeliveryDTO](t, rec)
		if dto.State != store.DeliveryClosed || dto.Outcome != store.DeliveryEnded || dto.Retire != "pending" {
			t.Fatalf("dto: %+v", dto)
		}
		got := e.delivery(d.ID)
		if got.State != store.DeliveryClosed || got.Outcome != store.DeliveryEnded || !got.RetirePending || got.RetireActMark != 0 {
			t.Fatalf("stored: %+v", got)
		}
		if n := len(e.liveDeliveries(d.QueueID)); n != 0 {
			t.Fatalf("the slot is still held by %d runs", n)
		}

		// Twice: nothing changes, nothing is retired twice by the press.
		closedAt := got.ClosedAt
		if rec := e.do("a", "POST", url, nil); rec.Code != http.StatusOK {
			t.Fatalf("second end: %d", rec.Code)
		}
		if again := e.delivery(d.ID); again.ClosedAt != closedAt || again.Outcome != store.DeliveryEnded {
			t.Fatalf("second press changed the run: %+v", again)
		}

		// The retire pass stops the session and the retire carries no act mark.
		e.settleNow(time.Now())
		got = e.delivery(d.ID)
		if agent.retireCalls() != 1 || got.RetirePending || got.RetireResult == "" {
			t.Fatalf("retire calls %d, delivery %+v", agent.retireCalls(), got)
		}
		if b := agent.retireBody(0); b != "" {
			t.Fatalf("the retire carried a condition: %q", b)
		}
		if got.Outcome != store.DeliveryEnded {
			t.Fatalf("outcome changed: %+v", got)
		}
	})
}

// Ending frees only its own run's slot, refuses what it must not touch, and leaves a run the
// tick already closed as it is.
func TestMemoEndDeliveryScopeAndStates(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.child("c1", d.Session, map[string]any{"state": "working", "progressAgeSec": 5})
		e.settleNow(time.Now())
		url := "/api/memo-queues/" + d.QueueID + "/deliveries/" + d.ID + "/end"

		// Another member, another queue and an unknown run find nothing, and the run stays open.
		if rec := e.do("b", "POST", url, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("other member: %d", rec.Code)
		}
		if rec := e.do("a", "POST", "/api/memo-queues/other/deliveries/"+d.ID+"/end", nil); rec.Code != http.StatusNotFound {
			t.Fatalf("other queue: %d", rec.Code)
		}
		if rec := e.do("a", "POST", "/api/memo-queues/"+d.QueueID+"/deliveries/nope/end", nil); rec.Code != http.StatusNotFound {
			t.Fatalf("unknown run: %d", rec.Code)
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
			t.Fatalf("a refused press moved the run: %+v", got)
		}

		// A second run of the same queue keeps its slot when the first is ended.
		e.pending("app", "impl", "two")
		other := e.fired(d.QueueID)
		if rec := e.do("a", "POST", url, nil); rec.Code != http.StatusOK {
			t.Fatalf("end: %d", rec.Code)
		}
		if got := e.delivery(other.ID); got.State == store.DeliveryClosed {
			t.Fatalf("the other run lost its slot: %+v", got)
		}
		// A run not started yet is refused.
		if rec := e.do("a", "POST", "/api/memo-queues/"+d.QueueID+"/deliveries/"+other.ID+"/end", nil); rec.Code != http.StatusConflict {
			t.Fatalf("unsent run: %d", rec.Code)
		}
	})
}

// A run the tick closed first keeps its own outcome: the press answers it and writes nothing.
func TestMemoEndDeliveryAfterTheTickClosedIt(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, d := e.openReported(nil)
		for i := 0; i < 3; i++ {
			e.settleNow(time.Now())
		}
		closed := e.delivery(d.ID)
		if closed.State != store.DeliveryClosed || closed.Outcome != store.DeliveryClean {
			t.Fatalf("the tick should have closed it: %+v", closed)
		}
		rec := e.do("a", "POST", "/api/memo-queues/"+d.QueueID+"/deliveries/"+d.ID+"/end", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("end: %d", rec.Code)
		}
		if got := e.delivery(d.ID); got.Outcome != store.DeliveryClean || got.ClosedAt != closed.ClosedAt {
			t.Fatalf("the press rewrote a closed run: %+v", got)
		}
	})
}
