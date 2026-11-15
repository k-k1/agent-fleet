package main

import (
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
