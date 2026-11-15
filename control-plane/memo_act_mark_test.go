package main

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// A retire is decided on a read of the Agent's session list, and the Agent leaves a session a
// person acted on after that read (issue #4218). The read's actMark is kept with the delivery and
// sent as ifNoActSince on every retire; an Agent that sends none gets no body.

func wantBody(mark int64) string {
	if mark == 0 {
		return ""
	}
	return `{"ifNoActSince":` + strconv.FormatInt(mark, 10) + `}`
}

// The clean close on a running workspace keeps the mark of the read it closed on and sends it,
// not the mark of an earlier read of the same delivery and not the one a later tick sees.
func TestMemoActMarkCleanCloseKeepsAndSendsTheMarkOfTheClosingRead(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setActMark(100)
		e.settleNow(time.Now()) // first tick with nothing holding it
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.ActMark != 100 {
			t.Fatalf("the report is kept with its mark: %+v", got)
		}
		agent.setActMark(250)
		agent.mu.Lock()
		agent.retireStatus = http.StatusBadGateway // the first answers cannot confirm: the call repeats
		agent.mu.Unlock()
		e.settleNow(time.Now()) // the close, on the read that carried 250
		got := e.delivery(d.ID)
		if got.State != store.DeliveryClosed || !got.RetirePending || got.ActMark != 250 {
			t.Fatalf("closed on the read of 250: %+v", got)
		}
		agent.setActMark(900) // later reads carry later marks
		e.settleNow(time.Now())
		agent.mu.Lock()
		agent.retireStatus = 0
		agent.mu.Unlock()
		e.settleNow(time.Now())
		for i := 0; i < agent.retireCalls(); i++ {
			if b := agent.retireBody(i); b != wantBody(250) {
				t.Fatalf("retire call %d carried %q, want %q", i, b, wantBody(250))
			}
		}
		if agent.retireCalls() < 2 || e.delivery(d.ID).RetirePending {
			t.Fatalf("calls %d, %+v", agent.retireCalls(), e.delivery(d.ID))
		}
	})
}

// The close made while the workspace is stopped (on the mirror) retires with the kept report's
// mark, whatever the Agent sends once the workspace runs again.
func TestMemoActMarkClosedFromTheKeptReportSendsTheKeptMark(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setActMark(500)
		e.settleNow(time.Now())
		e.stopWorkspace(agent, d.Session, "")
		agent.setActMark(9000) // the restarted Agent's marks are higher
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.ActMark != 500 {
			t.Fatalf("closed on the kept report: %+v", got)
		}
		e.setRuntime(agent, "")
		e.settleNow(time.Now())
		if agent.retireCalls() != 1 || agent.retireBody(0) != wantBody(500) {
			t.Fatalf("calls %d, body %q", agent.retireCalls(), agent.retireBody(0))
		}
	})
}

// The overdue retire is decided on the read that is kept: its mark is the one sent.
func TestMemoActMarkOverdueRetireSendsTheMarkOfItsRead(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(map[string]any{"maxTaskHours": 1})
		agent.setRow(d.Session, map[string]any{"wakers": map[string]any{"watches": 1, "timers": 0}})
		agent.setActMark(300)
		e.settleNow(time.Now())
		agent.setActMark(777)
		later := time.Now().Add(3 * time.Hour)
		e.settleNow(later) // marks it overdue
		e.settleNow(later) // retires it
		if agent.retireCalls() == 0 {
			t.Fatal("no retire")
		}
		if b := agent.retireBody(0); b != wantBody(777) {
			t.Fatalf("overdue retire carried %q, want %q", b, wantBody(777))
		}
	})
}

// An Agent that sends no actMark gets no body, as today.
func TestMemoActMarkAnAgentWithoutOneGetsNoBody(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		e.settleNow(time.Now())
		e.settleNow(time.Now())
		if agent.retireCalls() != 1 || agent.retireBody(0) != "" {
			t.Fatalf("calls %d, body %q", agent.retireCalls(), agent.retireBody(0))
		}
		if got := e.delivery(d.ID); got.ActMark != 0 {
			t.Fatalf("%+v", got)
		}
	})
}

// What the Agent answers to a conditional retire is read as before: released ends it as released
// (a released root) or done (a released child), and neither is repeated.
func TestMemoActMarkReleasedAnswersEndThePendingRetire(t *testing.T) {
	cases := map[string]struct {
		setup  func(a *fakeQueueAgent, root string)
		result string
	}{
		"root": {func(a *fakeQueueAgent, root string) { a.retireRelease = root }, "released"},
		"child": {func(a *fakeQueueAgent, root string) {
			a.rows = append(a.rows, map[string]any{"name": "c1", "kind": "claude", "alive": true, "state": "idle",
				"origin": "session", "originSession": root})
			a.retireRelease = "c1"
		}, "done"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
				agent, d := e.openReported(nil)
				agent.mu.Lock()
				c.setup(agent, d.Session)
				agent.actMark = 42
				agent.mu.Unlock()
				for i := 0; i < 7; i++ {
					e.settleNow(time.Now())
				}
				got := e.delivery(d.ID)
				if got.RetirePending || got.RetireResult != c.result || agent.retireCalls() != 1 || agent.retireBody(0) != wantBody(42) {
					t.Fatalf("calls %d body %q, %+v", agent.retireCalls(), agent.retireBody(0), got)
				}
			})
		})
	}
}

// An overdue retire whose answer was lost is repeated with the condition of the first attempt,
// not of the newer read the open delivery has kept since.
func TestMemoActMarkOverdueRetryKeepsTheFirstCondition(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(map[string]any{"maxTaskHours": 1})
		agent.setRow(d.Session, map[string]any{"wakers": map[string]any{"watches": 1, "timers": 0}})
		agent.setActMark(300)
		e.settleNow(time.Now())
		agent.mu.Lock()
		agent.retireDrop = 1
		agent.mu.Unlock()
		later := time.Now().Add(3 * time.Hour)
		e.settleNow(later) // marks it overdue
		agent.setActMark(900)
		e.settleNow(later) // the first attempt: its answer is lost
		agent.setActMark(1500)
		e.settleNow(later) // the retry, after two newer reads were kept
		e.settleNow(later)
		if agent.retireCalls() < 2 {
			t.Fatalf("calls %d", agent.retireCalls())
		}
		if b := agent.retireBody(0); b != wantBody(300) && b != wantBody(900) {
			t.Fatalf("first call carried %q", b)
		}
		if agent.retireBody(0) != agent.retireBody(1) {
			t.Fatalf("the retry moved the condition: %q then %q", agent.retireBody(0), agent.retireBody(1))
		}
		if got := e.delivery(d.ID); got.RetiredAt == "" {
			t.Fatalf("the retry was not recorded: %+v", got)
		}
	})
}

// observe keeps a report of the delivery through the given store handle.
func (e *deliveryEnv) observe(st store.MemoStore, id string, gen, mark int64, progress bool) {
	e.t.Helper()
	err := st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
		_, _, err := tx.ObserveDelivery(id, store.DeliveryObservation{Gen: gen, At: store.DeliveryTS(time.Now()),
			Progress: progress, ProgressAt: store.DeliveryTS(time.Now()), ActMark: mark})
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// The condition of an overdue retire is fixed in one conditional write on the report it was decided
// on: a newer report kept between the decision and the freeze stops it, a frozen condition is never
// replaced by a later report (a frozen "no mark" included), and progress clears it together with
// the record of a retire that was made under it.
func TestMemoActMarkFreezeIsOneConditionalWrite(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		ctx := context.Background()
		_, d := e.openReported(nil)

		// A newer report kept between the decision (gen 100, mark 10) and the freeze.
		e.observe(e.st, d.ID, 100, 10, false)
		e.observe(e.other, d.ID, 200, 20, false)
		if _, _, ok, err := e.st.FreezeDeliveryRetireMark(ctx, d.ID, 100, 10); err != nil || ok {
			t.Fatalf("a freeze on an overtaken report went through: ok=%v err=%v", ok, err)
		}
		if got := e.delivery(d.ID); got.RetireGen != 0 || got.RetireActMark != 0 {
			t.Fatalf("%+v", got)
		}

		// Frozen on the current report; a later report does not replace it.
		if m, g, ok, err := e.st.FreezeDeliveryRetireMark(ctx, d.ID, 200, 20); err != nil || !ok || m != 20 || g != 200 {
			t.Fatalf("freeze: %d %d %v %v", m, g, ok, err)
		}
		e.observe(e.other, d.ID, 300, 30, false)
		if m, g, ok, err := e.st.FreezeDeliveryRetireMark(ctx, d.ID, 300, 30); err != nil || !ok || m != 20 || g != 200 {
			t.Fatalf("a later report replaced the frozen condition: %d %d %v %v", m, g, ok, err)
		}

		// Progress clears it; the completion of the old attempt is then not recorded.
		e.observe(e.other, d.ID, 400, 40, true)
		if won, err := e.st.MarkMemoDeliveryRetired(ctx, d.ID, store.DeliveryTS(time.Now()), 200); err != nil || won {
			t.Fatalf("the completion of a stale attempt was recorded: %v %v", won, err)
		}
		if m, g, ok, err := e.st.FreezeDeliveryRetireMark(ctx, d.ID, 400, 40); err != nil || !ok || m != 40 || g != 400 {
			t.Fatalf("after progress: %d %d %v %v", m, g, ok, err)
		}
		if won, err := e.st.MarkMemoDeliveryRetired(ctx, d.ID, store.DeliveryTS(time.Now()), 400); err != nil || !won {
			t.Fatalf("the current attempt's completion: %v %v", won, err)
		}
	})
	// A frozen "no mark" stays "no mark": 0 is a value, not "unfrozen".
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		ctx := context.Background()
		_, d := e.openReported(nil)
		e.observe(e.st, d.ID, 100, 0, false)
		if m, _, ok, err := e.st.FreezeDeliveryRetireMark(ctx, d.ID, 100, 0); err != nil || !ok || m != 0 {
			t.Fatalf("%d %v %v", m, ok, err)
		}
		e.observe(e.other, d.ID, 200, 50, false)
		if m, g, ok, err := e.st.FreezeDeliveryRetireMark(ctx, d.ID, 200, 50); err != nil || !ok || m != 0 || g != 100 {
			t.Fatalf("a mark appeared in a frozen retire without one: %d %d %v %v", m, g, ok, err)
		}
	})
}
