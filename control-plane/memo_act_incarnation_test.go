package main

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The act mark comes with the identity of the run of the Agent's counter that handed it out. The
// Control Plane keeps the two together and sends both with the retire, frozen as one pair; the Agent
// judges each session by them.

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

// A row frozen before the upgrade (a mark, no identity) is retired as before: no identity is sent
// for it, even once the Agent reports one.
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

// The mark and its identity stay frozen together: an Agent restart between the decision and the
// retry changes nothing the Control Plane sends. The Agent judges each session against the pair.
func TestMemoActIncarnationThePairStaysFrozenAcrossAnAgentRestart(t *testing.T) {
	t.Run("closed delivery", func(t *testing.T) {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			agent, d := e.openReported(nil)
			agent.setActMark(100)
			agent.setActIncarnation("run-a")
			e.settleNow(time.Now())
			agent.mu.Lock()
			agent.retireStatus = 502 // the close is made, its retire cannot be confirmed
			agent.mu.Unlock()
			e.settleNow(time.Now())
			agent.setActMark(900) // the Agent restarted: another run, other numbers
			agent.setActIncarnation("run-b")
			agent.mu.Lock()
			agent.retireStatus = 0
			agent.mu.Unlock()
			e.settleNow(time.Now())
			for i := 0; i < agent.retireCalls(); i++ {
				if b := agent.retireBody(i); b != wantBodyInc(100, "run-a") {
					t.Fatalf("retire call %d carried %q, want the pair of the decision %q", i, b, wantBodyInc(100, "run-a"))
				}
			}
			if got := e.delivery(d.ID); got.RetirePending || got.RetireResult != "confirmed" || got.RetireActMark != 100 || got.RetireActIncarnation != "run-a" {
				t.Fatalf("%+v", got)
			}
		})
	})
	t.Run("overdue retry", func(t *testing.T) {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			agent, d := e.openReported(map[string]any{"maxTaskHours": 1})
			agent.setRow(d.Session, map[string]any{"wakers": map[string]any{"watches": 1, "timers": 0}})
			agent.setActMark(300)
			agent.setActIncarnation("run-a")
			e.settleNow(time.Now())
			agent.mu.Lock()
			agent.retireStatus = 502
			agent.mu.Unlock()
			later := time.Now().Add(3 * time.Hour)
			e.settleNow(later) // marks it overdue
			e.settleNow(later) // the first attempt freezes (300, run-a)
			agent.setActMark(5000)
			agent.setActIncarnation("run-b")
			agent.mu.Lock()
			agent.retireStatus = 0
			agent.mu.Unlock()
			e.settleNow(later)
			for i := 0; i < agent.retireCalls(); i++ {
				if b := agent.retireBody(i); b != wantBodyInc(300, "run-a") {
					t.Fatalf("retire call %d carried %q", i, b)
				}
			}
			if got := e.delivery(d.ID); got.RetiredAt == "" {
				t.Fatalf("not retired: %+v", got)
			}
		})
	})
}

// The freeze keeps the identity with the mark.
func TestMemoActIncarnationFreezeKeepsThePair(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		ctx := context.Background()
		_, d := e.openReported(nil)
		e.observeInc(e.st, d.ID, 100, 10, "run-a")
		if m, inc, g, ok, err := e.st.FreezeDeliveryRetireMark(ctx, d.ID, 100, 10, "run-a"); err != nil || !ok || m != 10 || inc != "run-a" || g != 100 {
			t.Fatalf("freeze: %d %q %d %v %v", m, inc, g, ok, err)
		}
		e.observeInc(e.other, d.ID, 200, 20, "run-b")
		if m, inc, g, ok, err := e.st.FreezeDeliveryRetireMark(ctx, d.ID, 200, 20, "run-b"); err != nil || !ok || m != 10 || inc != "run-a" || g != 100 {
			t.Fatalf("a later report replaced the frozen pair: %d %q %d %v %v", m, inc, g, ok, err)
		}
		if got := e.delivery(d.ID); got.ActMark != 20 || got.ActIncarnation != "run-b" || got.RetireActMark != 10 || got.RetireActIncarnation != "run-a" {
			t.Fatalf("%+v", got)
		}
		e.observeInc(e.other, d.ID, 300, 30, "run-b") // no progress: nothing cleared
		if got := e.delivery(d.ID); got.RetireActIncarnation != "run-a" {
			t.Fatalf("%+v", got)
		}
		e.observe(e.other, d.ID, 400, 40, true) // progress clears the frozen pair together
		if got := e.delivery(d.ID); got.RetireActIncarnation != "" || got.RetireActMark != 0 || got.RetireGen != 0 {
			t.Fatalf("progress left the frozen identity: %+v", got)
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
