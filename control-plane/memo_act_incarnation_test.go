package main

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The act mark comes with the identity of the run of the Agent's counter that handed it out
// (issue #4265). The Control Plane keeps the two together, sends both with the retire, and when the
// Agent refuses the mark as another run's it drops the condition and decides again on a fresh read.

func wantBodyInc(mark int64, inc string) string {
	return `{"ifNoActSince":` + strconv.FormatInt(mark, 10) + `,"actIncarnation":"` + inc + `"}`
}

// The identity of the closing read is kept with the mark and sent with every retire of it.
func TestMemoActIncarnationIsKeptAndSentWithTheMark(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setActMark(100)
		agent.setActIncarnation("run-a")
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.ActMark != 100 || got.ActIncarnation != "run-a" {
			t.Fatalf("the report is kept with its identity: %+v", got)
		}
		e.settleNow(time.Now()) // the close
		e.settleNow(time.Now())
		if agent.retireCalls() == 0 || agent.retireBody(0) != wantBodyInc(100, "run-a") {
			t.Fatalf("calls %d, body %q", agent.retireCalls(), agent.retireBody(0))
		}
	})
}

// An Agent that sends no identity (an older one), or none beside the mark: nothing is stored and the
// retire is sent as before.
func TestMemoActIncarnationAnOlderAgentIsSentNone(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setActMark(100)
		e.settleNow(time.Now())
		e.settleNow(time.Now())
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.ActIncarnation != "" || got.RetireActIncarnation != "" {
			t.Fatalf("an identity was stored for an Agent that sent none: %+v", got)
		}
		if agent.retireCalls() == 0 || agent.retireBody(0) != wantBody(100) {
			t.Fatalf("calls %d, body %q, want %q", agent.retireCalls(), agent.retireBody(0), wantBody(100))
		}
	})
}

// A row frozen before the upgrade (a mark, no identity) is retired as before, and the Agent's
// refusal never applies to it.
func TestMemoActIncarnationAFrozenRowWithoutOneIsSentAsBefore(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setActMark(100) // kept with no identity, as a row of the old schema
		e.settleNow(time.Now())
		agent.mu.Lock()
		agent.retireStatus = 502
		agent.mu.Unlock()
		e.settleNow(time.Now())          // the close freezes (100, no identity)
		agent.setActIncarnation("run-b") // the Agent is a new one now, the row is not
		agent.mu.Lock()
		agent.retireStatus = 0
		agent.mu.Unlock()
		e.settleNow(time.Now())
		if agent.retireCalls() == 0 || agent.retireBody(0) != wantBody(100) || e.delivery(d.ID).RetirePending {
			t.Fatalf("calls %d, body %q, %+v", agent.retireCalls(), agent.retireBody(0), e.delivery(d.ID))
		}
	})
}

// A closed delivery's pending retire whose mark the Agent refuses (it was restarted) is decided
// again on the next read: that read's mark and identity replace the refused ones, the retire is not
// repeated in the same tick, and it then goes through.
func TestMemoActIncarnationRefusedPendingRetireTakesTheFreshRead(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setActMark(100)
		agent.setActIncarnation("run-a")
		e.settleNow(time.Now())
		agent.mu.Lock()
		agent.retireStatus = 502 // the close is made, its retire cannot be confirmed
		agent.mu.Unlock()
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.RetireActIncarnation != "run-a" || got.RetireActMark != 100 {
			t.Fatalf("closed with the condition frozen: %+v", got)
		}
		// The Agent restarted: another run, other numbers.
		agent.setActMark(900)
		agent.setActIncarnation("run-b")
		agent.mu.Lock()
		agent.retireStatus = 0
		before := len(agent.retires)
		agent.mu.Unlock()
		e.settleNow(time.Now())
		agent.mu.Lock()
		callsInTick := len(agent.retires) - before
		agent.mu.Unlock()
		if callsInTick != 1 || agent.retireBody(agent.retireCalls()-1) != wantBodyInc(100, "run-a") {
			t.Fatalf("the refused call: %d calls this tick, last body %q", callsInTick, agent.retireBody(agent.retireCalls()-1))
		}
		got := e.delivery(d.ID)
		if !got.RetirePending || got.RetireActMark != 900 || got.RetireActIncarnation != "run-b" {
			t.Fatalf("the condition was not renewed from the fresh read: %+v", got)
		}
		e.settleNow(time.Now())
		if last := agent.retireBody(agent.retireCalls() - 1); last != wantBodyInc(900, "run-b") {
			t.Fatalf("the second attempt carried %q", last)
		}
		if got := e.delivery(d.ID); got.RetirePending || got.RetireResult != "confirmed" {
			t.Fatalf("not retired: %+v", got)
		}
	})
}

// An overdue retire whose frozen mark the Agent refuses drops the frozen condition: the next read
// is kept with the fresh mark, and the retire decided on it goes through. The refused attempt is not
// recorded as a retire.
func TestMemoActIncarnationRefusedOverdueRetireDropsTheFrozenMark(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(map[string]any{"maxTaskHours": 1})
		agent.setRow(d.Session, map[string]any{"wakers": map[string]any{"watches": 1, "timers": 0}})
		agent.setActMark(300)
		agent.setActIncarnation("run-a")
		e.settleNow(time.Now())
		agent.mu.Lock()
		agent.retireStatus = 502 // the attempts freeze (300, run-a) and cannot be confirmed
		agent.mu.Unlock()
		later := time.Now().Add(3 * time.Hour)
		e.settleNow(later) // marks it overdue
		e.settleNow(later) // the first attempt
		if got := e.delivery(d.ID); got.RetireActIncarnation != "run-a" || got.RetireGen == 0 {
			t.Fatalf("frozen: %+v", got)
		}
		agent.setActMark(5000)
		agent.setActIncarnation("run-b") // the Agent restarted
		agent.mu.Lock()
		agent.retireStatus = 0
		agent.mu.Unlock()
		e.settleNow(later) // the retry carries the frozen mark and is refused
		got := e.delivery(d.ID)
		if got.RetiredAt != "" || got.RetireGen != 0 || got.RetireActMark != 0 || got.RetireActIncarnation != "" {
			t.Fatalf("the refused condition was not dropped: %+v", got)
		}
		e.settleNow(later) // the fresh read is kept and frozen, and the retire goes through
		e.settleNow(later)
		if last := agent.retireBody(agent.retireCalls() - 1); last != wantBodyInc(5000, "run-b") {
			t.Fatalf("the decision on the fresh read carried %q", last)
		}
		if got := e.delivery(d.ID); got.RetiredAt == "" {
			t.Fatalf("not retired: %+v", got)
		}
	})
}

// Dropping and renewing are conditional writes.
func TestMemoActIncarnationDropAndRefreshAreConditional(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		ctx := context.Background()
		_, d := e.openReported(nil)
		e.observeInc(e.st, d.ID, 100, 10, "run-a")
		if _, _, _, ok, err := e.st.FreezeDeliveryRetireMark(ctx, d.ID, 100, 10, "run-a"); err != nil || !ok {
			t.Fatalf("freeze: %v %v", ok, err)
		}
		if won, err := e.st.DropDeliveryRetireMark(ctx, d.ID, 100, "run-x"); err != nil || won {
			t.Fatalf("a drop of another identity went through: %v %v", won, err)
		}
		if won, err := e.st.DropDeliveryRetireMark(ctx, d.ID, 99, "run-a"); err != nil || won {
			t.Fatalf("a drop of another generation went through: %v %v", won, err)
		}
		if won, err := e.st.DropDeliveryRetireMark(ctx, d.ID, 100, "run-a"); err != nil || !won {
			t.Fatalf("the drop: %v %v", won, err)
		}
		if got := e.delivery(d.ID); got.RetireGen != 0 || got.RetireActMark != 0 || got.RetireActIncarnation != "" || got.ActIncarnation != "run-a" {
			t.Fatalf("%+v", got)
		}
		if won, err := e.st.DropDeliveryRetireMark(ctx, d.ID, 100, "run-a"); err != nil || won {
			t.Fatalf("nothing frozen, yet a drop won: %v %v", won, err)
		}
		// An open delivery is not renewed (the refresh is for a closed one's pending retire).
		if won, err := e.st.RefreshDeliveryRetireMark(ctx, d.ID, "", 7, "run-z"); err != nil || won {
			t.Fatalf("an open delivery was renewed: %v %v", won, err)
		}
	})
}

// observeInc is observe with an identity.
func (e *deliveryEnv) observeInc(st store.MemoStore, id string, gen, mark int64, inc string) {
	e.t.Helper()
	err := st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
		_, _, err := tx.ObserveDelivery(id, store.DeliveryObservation{Gen: gen, At: store.DeliveryTS(time.Now()),
			ProgressAt: store.DeliveryTS(time.Now()), ActMark: mark, ActIncarnation: inc})
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}
