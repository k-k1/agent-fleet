package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Message queues, step 2b (ADR 0137 section 6, issue #4203): an open delivery keeps its slot
// while its session, or a session that session started, has something to wait for, the last
// report is kept with the delivery, closing stops the session, and overdue retires. The Agent is
// the fake of memo_delivery_test.go with the queue report switched on.

// openReported fires one delivery of a queue (settings over the defaults) behind an Agent that
// sends the queue report and ticks until its session exists. The session row starts idle.
func (e *deliveryEnv) openReported(settings map[string]any) (*fakeQueueAgent, store.MemoDelivery) {
	e.t.Helper()
	q := e.queueWith("app", "impl", settings)
	e.pending("app", "impl", "one")
	agent := e.useQueueAgent("")
	agent.mu.Lock()
	agent.report = true
	agent.mu.Unlock()
	d := e.fired(q)
	e.tick(e.runner(), time.Now())
	open := e.delivery(d.ID)
	if open.State != store.DeliveryOpen {
		e.t.Fatalf("not open: %+v", open)
	}
	agent.setRow(open.Session, map[string]any{"alive": true, "state": "idle"})
	return agent, open
}

// child adds a session started by parent (origin "session") with these fields over an idle row.
func (a *fakeQueueAgent) child(name, parent string, kv map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	row := map[string]any{"name": name, "kind": "claude", "alive": true, "state": "idle",
		"origin": "session", "originSession": parent}
	for k, v := range kv {
		row[k] = v
	}
	a.rows = append(a.rows, row)
}

// settleNow is one tick's settle on a fresh runner: nothing is kept in memory between ticks, as
// across a Control Plane restart.
func (e *deliveryEnv) settleNow(now time.Time) { e.runner().settle(context.Background(), now) }

func (e *deliveryEnv) stopWorkspace(agent *fakeQueueAgent, session string, carried string) {
	e.t.Helper()
	ws, _, _ := e.st.GetWorkspaceByMembership(context.Background(), e.members["a"])
	rows := []store.SessionRow{{Name: session, Kind: "claude", State: "stopped", Carried: carried}}
	if err := e.st.ReplaceSessions(context.Background(), ws.ID, rows); err != nil {
		e.t.Fatal(err)
	}
	e.setRuntime(agent, "stopped")
}

func (e *deliveryEnv) notifications() []store.Notification {
	e.t.Helper()
	ns, err := e.st.ListNotifications(context.Background(), e.members["a"], "", 100)
	if err != nil {
		e.t.Fatal(err)
	}
	return ns
}

// A delivery whose session is idle and holds a watch stays open over many ticks, with the
// workspace running and stopped and across restarts. It closes two ticks after the count is
// zero, and its session is retired exactly as often as it takes for the Agent to confirm.
func TestMemoOccupancyWatchHoldsThenCloseRetires(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		watch := map[string]any{"watches": 1, "timers": 0}
		agent.setRow(d.Session, map[string]any{"wakers": watch})
		for i := 0; i < 5; i++ {
			e.settleNow(time.Now())
		}
		got := e.delivery(d.ID)
		if got.State != store.DeliveryOpen || got.HeldBy != store.HeldWakers || got.HeldSession != d.Session || !got.Reported {
			t.Fatalf("running with a watch: %+v", got)
		}
		// Stopped: the mirror shows a stopped session with nothing carried, and the delivery stays.
		e.stopWorkspace(agent, d.Session, "")
		for i := 0; i < 5; i++ {
			e.settleNow(time.Now())
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.HeldBy != store.HeldWakers {
			t.Fatalf("stopped after a report that held it: %+v", got)
		}
		e.setRuntime(agent, "")
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
			t.Fatalf("running again with the watch: %+v", got)
		}
		// The count is zero: two ticks.
		agent.mu.Lock()
		agent.retireFail = 2
		agent.mu.Unlock()
		agent.setRow(d.Session, map[string]any{"wakers": nil})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.IdleTicks != 1 || got.HeldBy != "" {
			t.Fatalf("first tick with nothing: %+v", got)
		}
		if agent.retireCalls() != 0 {
			t.Fatal("retired a delivery that is still open")
		}
		e.settleNow(time.Now())
		got = e.delivery(d.ID)
		if got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean || !got.RetirePending {
			t.Fatalf("second tick with nothing: %+v", got)
		}
		// The close is recorded first, then the retire is repeated until the Agent confirms:
		// two answers that could not end a session, then one that did.
		for i := 0; i < 6; i++ {
			e.settleNow(time.Now())
		}
		got = e.delivery(d.ID)
		if agent.retireCalls() != 3 || got.RetirePending || got.RetiredAt == "" {
			t.Fatalf("retire calls %d, delivery %+v", agent.retireCalls(), got)
		}
		if got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean {
			t.Fatalf("closed state changed: %+v", got)
		}
	})
}

// A delivery's slot also belongs to the sessions that session started: a child that works, waits
// for a person or holds a timer keeps the parent's delivery open, at any depth. Wakers unknown
// holds as well, and a usage limit with a booked resume, running or stopped.
func TestMemoOccupancyTreeAndOtherHolds(t *testing.T) {
	cases := map[string]struct {
		rows func(a *fakeQueueAgent, root string)
		held string
	}{
		"child working": {func(a *fakeQueueAgent, root string) {
			a.child("c1", root, map[string]any{"state": "working", "progressAgeSec": 5})
		}, store.HeldMachine},
		"grandchild asks a person": {func(a *fakeQueueAgent, root string) {
			a.child("c1", root, nil)
			a.child("c2", "c1", map[string]any{"state": "question"})
		}, store.HeldPerson},
		"child holds a timer": {func(a *fakeQueueAgent, root string) {
			a.child("c1", root, map[string]any{"wakers": map[string]any{"watches": 0, "timers": 1}})
		}, store.HeldWakers},
		"wakers unknown": {func(a *fakeQueueAgent, root string) {
			a.setRow(root, map[string]any{"wakersUnknown": true})
		}, store.HeldUnknown},
		"waiting for a usage limit": {func(a *fakeQueueAgent, root string) {
			a.setRow(root, map[string]any{"state": "limited"})
		}, store.HeldLimit},
		"a booked resume on an idle row": {func(a *fakeQueueAgent, root string) {
			a.setRow(root, map[string]any{"rateLimitResumeAt": "2099-01-01T00:00:00Z"})
		}, store.HeldLimit},
		"stopped with the interaction carried": {func(a *fakeQueueAgent, root string) {
			a.setRow(root, map[string]any{"alive": false, "state": "", "carried": "question"})
		}, store.HeldPerson},
		"a child not started by a session is not in the tree": {func(a *fakeQueueAgent, root string) {
			a.child("c1", root, map[string]any{"state": "working", "origin": "", "progressAgeSec": 5})
		}, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
				agent, d := e.openReported(nil)
				c.rows(agent, d.Session)
				for i := 0; i < 4; i++ {
					e.settleNow(time.Now())
				}
				got := e.delivery(d.ID)
				if c.held == "" {
					if got.State != store.DeliveryClosed {
						t.Fatalf("nothing holds it: %+v", got)
					}
					return
				}
				if got.State != store.DeliveryOpen || got.HeldBy != c.held {
					t.Fatalf("want open held by %s: %+v", c.held, got)
				}
			})
		})
	}
}

// An Agent that does not send queueReport gets today's rule for its deliveries and no retire,
// whatever its rows say about wakers. A queueReport of another version is no report either.
func TestMemoOccupancyUnreportedAgentKeepsTodaysRule(t *testing.T) {
	for _, version := range []int{0, 2} {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			agent, d := e.openReported(nil)
			agent.mu.Lock()
			agent.report = version == 2
			agent.reportVersion = version
			agent.mu.Unlock()
			agent.setRow(d.Session, map[string]any{"wakers": map[string]any{"watches": 3, "timers": 0}})
			e.settleNow(time.Now())
			e.settleNow(time.Now())
			got := e.delivery(d.ID)
			if got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean || got.RetirePending || got.Reported {
				t.Fatalf("version %d: %+v", version, got)
			}
			e.settleNow(time.Now())
			if agent.retireCalls() != 0 {
				t.Fatalf("version %d: retired for an Agent that does not report", version)
			}
		})
	}
}

// A late or out-of-order report does not close a delivery early: a watch made between two reads
// restarts the count, a child started just before the parent goes idle holds it, a read that
// began earlier than the one kept is refused, and a close re-checks behind the membership lock.
func TestMemoOccupancyLateReportDoesNotCloseEarly(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		e.settleNow(time.Now()) // one read with nothing: the count is 1
		if got := e.delivery(d.ID); got.IdleTicks != 1 {
			t.Fatalf("count %d", got.IdleTicks)
		}
		// A watch made between two reads: the second read sees it.
		agent.setRow(d.Session, map[string]any{"wakers": map[string]any{"watches": 1, "timers": 0}})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.IdleTicks != 0 || got.HeldBy != store.HeldWakers {
			t.Fatalf("after the watch: %+v", got)
		}
		agent.setRow(d.Session, map[string]any{"wakers": nil})
		e.settleNow(time.Now())
		// A child started just before the parent went idle.
		agent.child("c1", d.Session, map[string]any{"state": "working", "progressAgeSec": 1})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.HeldBy != store.HeldMachine {
			t.Fatalf("after the child: %+v", got)
		}
		agent.dropRow("c1")

		// A read kept by another Control Plane that began later holds the delivery: this one's
		// older read is refused, and so is its close.
		e.settleNow(time.Now())
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed {
			t.Fatalf("two clean reads must close: %+v", got)
		}
	})
}

func TestMemoOccupancyOlderReadIsRefused(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, d := e.openReported(nil)
		ctx := context.Background()
		newer := store.DeliveryTS(time.Now().Add(time.Minute))
		older := store.DeliveryTS(time.Now())
		if ok, err := e.other.ObserveMemoDelivery(ctx, d.ID, store.DeliveryObservation{At: newer, HeldBy: store.HeldWakers, HeldSession: d.Session}); err != nil || !ok {
			t.Fatalf("newer read: %v %v", ok, err)
		}
		if ok, err := e.st.ObserveMemoDelivery(ctx, d.ID, store.DeliveryObservation{At: older, IdleTicks: 2}); err != nil || ok {
			t.Fatalf("an older read was kept: %v %v", ok, err)
		}
		// This tick reads nothing held, but a read that began later is already kept.
		for i := 0; i < 3; i++ {
			e.settleNow(time.Now())
		}
		got := e.delivery(d.ID)
		if got.State != store.DeliveryOpen || got.HeldBy != store.HeldWakers || got.HeldSeenAt != newer {
			t.Fatalf("the newer read must decide: %+v", got)
		}
		// closeClean re-reads behind the lock: a delivery that another process holds is not closed
		// on this one's stale count.
		stale := got
		stale.IdleTicks = 2
		e.runner().closeClean(ctx, stale, 2)
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
			t.Fatalf("closed on a stale count: %+v", got)
		}
	})
}

// Two Control Plane processes settling the same tick close a delivery once, tell the member of
// an overdue delivery once, and a retire that both make does no harm.
func TestMemoOccupancyConcurrentProcessesAgree(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(map[string]any{"maxTaskHours": 1})
		now := time.Now().Add(2 * time.Hour)
		e.settleNow(now)
		run := func() {
			var wg sync.WaitGroup
			for i := 0; i < 3; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); e.settleNow(now) }()
			}
			wg.Wait()
		}
		// Overdue is marked and told once, whoever wins.
		run()
		run()
		kinds := 0
		for _, n := range e.notifications() {
			if n.Kind == memoOverdueKind {
				kinds++
				if n.TargetID != d.QueueID || !strings.Contains(n.Payload, d.ID) {
					t.Fatalf("notification %+v", n)
				}
			}
		}
		if kinds != 1 {
			t.Fatalf("%d overdue notifications, want 1", kinds)
		}
		got := e.delivery(d.ID)
		if got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean {
			t.Fatalf("an idle delivery closes: %+v", got)
		}
		if got.RetirePending {
			t.Fatalf("retire not confirmed after concurrent ticks: %+v", got)
		}
		if agent.retireCalls() < 1 {
			t.Fatal("no retire")
		}
	})
}

// Overdue: one notification at maxTaskHours and nothing freed by it. No retire while any session
// of the tree made progress within the hour, a retire after that, a failed retire repeated, and
// the slot free only when the report says nothing holds the tree.
func TestMemoOccupancyOverdueRetire(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(map[string]any{"maxTaskHours": 1})
		// A watch holds the session, and a child makes progress.
		agent.setRow(d.Session, map[string]any{"wakers": map[string]any{"watches": 1, "timers": 0}})
		agent.child("c1", d.Session, map[string]any{"state": "working", "progressAgeSec": 30})

		// Before the hour: nothing.
		e.settleNow(time.Now().Add(30 * time.Minute))
		if got := e.delivery(d.ID); got.OverdueAt != "" || len(e.notifications()) != 0 {
			t.Fatalf("overdue too early: %+v", got)
		}
		later := time.Now().Add(2 * time.Hour)
		e.settleNow(later)
		e.settleNow(later)
		got := e.delivery(d.ID)
		if got.OverdueAt == "" || got.State != store.DeliveryOpen {
			t.Fatalf("overdue must mark and free nothing: %+v", got)
		}
		over := 0
		for _, n := range e.notifications() {
			if n.Kind == memoOverdueKind {
				over++
			}
		}
		if over != 1 {
			t.Fatalf("%d notifications, want 1", over)
		}
		if agent.retireCalls() != 0 {
			t.Fatal("retired while a session of the tree makes progress")
		}
		// Progress stops: the child's row lapses. The progress seen before still counts for an hour.
		agent.setRow("c1", map[string]any{"progressAgeSec": 4000})
		e.settleNow(later)
		if agent.retireCalls() != 0 {
			t.Fatal("retired within the hour of the last progress")
		}
		later = time.Now().Add(3 * time.Hour)
		agent.mu.Lock()
		agent.retireFail = 1
		agent.mu.Unlock()
		e.settleNow(later)
		if agent.retireCalls() != 1 {
			t.Fatalf("retire calls %d, want 1", agent.retireCalls())
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.RetiredAt != "" {
			t.Fatalf("a retire the Agent did not complete leaves the slot: %+v", got)
		}
		// Repeated until it is done, then not again.
		e.settleNow(later)
		if agent.retireCalls() != 2 {
			t.Fatalf("retire calls %d, want 2", agent.retireCalls())
		}
		if got := e.delivery(d.ID); got.RetiredAt == "" {
			t.Fatalf("a completed retire is recorded: %+v", got)
		}
		// The retire stopped the tree and ended its watch: the ordinary rule frees the slot on
		// the report, two ticks later.
		e.settleNow(later)
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
			t.Fatalf("one clean tick must not close: %+v", got)
		}
		e.settleNow(later)
		got = e.delivery(d.ID)
		if got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean {
			t.Fatalf("slot not freed by the report: %+v", got)
		}
		e.settleNow(later)
		e.settleNow(later)
		if agent.retireCalls() < 3 {
			t.Fatalf("the close retires too: %d", agent.retireCalls())
		}
		before := agent.retireCalls()
		e.settleNow(later)
		if agent.retireCalls() != before {
			t.Fatal("retired again after confirmation")
		}
	})
}

// An Agent that does not report is told overdue but never retired.
func TestMemoOccupancyOverdueNotifiesWithoutAReport(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(map[string]any{"maxTaskHours": 1})
		agent.mu.Lock()
		agent.report = false
		agent.mu.Unlock()
		agent.setRow(d.Session, map[string]any{"state": "working", "progressAgeSec": 9000})
		later := time.Now().Add(5 * time.Hour)
		e.settleNow(later)
		e.settleNow(later)
		if got := e.delivery(d.ID); got.OverdueAt == "" || got.State != store.DeliveryOpen {
			t.Fatalf("%+v", got)
		}
		if agent.retireCalls() != 0 {
			t.Fatal("retired for an Agent that does not report")
		}
	})
}

// Closing never wakes a workspace: with it stopped the retire waits, and it is made when the
// Agent answers again. An aborted close retires nothing, and a session a person took over is
// not retired.
func TestMemoOccupancyRetireNeverWakesAndAbortRetiresNothing(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		e.settleNow(time.Now())
		e.stopWorkspace(agent, d.Session, "")
		// Seen with nothing holding it, then the workspace stopped: the mirror closes it.
		e.settleNow(time.Now())
		got := e.delivery(d.ID)
		if got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean || !got.RetirePending {
			t.Fatalf("stopped after a clean report: %+v", got)
		}
		for i := 0; i < 3; i++ {
			e.settleNow(time.Now())
		}
		if agent.retireCalls() != 0 || !e.delivery(d.ID).RetirePending {
			t.Fatalf("the retire must wait for a running workspace (calls %d)", agent.retireCalls())
		}
		e.setRuntime(agent, "")
		e.settleNow(time.Now())
		if agent.retireCalls() != 1 || e.delivery(d.ID).RetirePending {
			t.Fatalf("retire calls %d, %+v", agent.retireCalls(), e.delivery(d.ID))
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setRow(d.Session, map[string]any{"archived": true})
		e.settleNow(time.Now())
		got := e.delivery(d.ID)
		if got.State != store.DeliveryClosed || got.Outcome != store.DeliveryAborted || got.RetirePending {
			t.Fatalf("aborted: %+v", got)
		}
		e.settleNow(time.Now())
		if agent.retireCalls() != 0 {
			t.Fatal("an aborted close retires nothing")
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.mu.Lock()
		agent.retireStatus = http.StatusBadGateway
		agent.mu.Unlock()
		e.settleNow(time.Now())
		e.settleNow(time.Now())
		if !e.delivery(d.ID).RetirePending || agent.retireCalls() == 0 {
			t.Fatal("an error answer is not a confirmation")
		}
		// A person resumed the session and a machine works in it: the retire is given up.
		agent.setRow(d.Session, map[string]any{"alive": true, "state": "working", "progressAgeSec": 1})
		before := agent.retireCalls()
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.RetirePending || got.RetiredAt != "" || agent.retireCalls() != before {
			t.Fatalf("retired work a person took over: %+v", got)
		}
	})
}

// A child that is archived or deleted while its parent's delivery is open holds nothing, and a
// tree with a cycle or a very long chain in originSession is walked once.
func TestMemoOccupancyChildGoneAndTreeShape(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.child("c1", d.Session, map[string]any{"state": "working", "progressAgeSec": 1})
		agent.child("c2", d.Session, map[string]any{"wakers": map[string]any{"watches": 1, "timers": 0}})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.HeldBy != store.HeldMachine {
			t.Fatalf("%+v", got)
		}
		agent.setRow("c2", map[string]any{"archived": true})
		agent.dropRow("c1")
		e.settleNow(time.Now())
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean {
			t.Fatalf("an archived and a deleted child hold nothing: %+v", got)
		}
	})

	rows := []sessionWire{{Name: "root", Origin: "session", OriginSession: "b"}}
	rows = append(rows,
		sessionWire{Name: "a", Origin: "session", OriginSession: "root"},
		sessionWire{Name: "b", Origin: "session", OriginSession: "a"})
	if tree := deliveryTree(rows, "root"); len(tree) != 3 {
		t.Fatalf("a cycle through the root: %d sessions", len(tree))
	}
	rows = []sessionWire{{Name: "s0"}}
	for i := 1; i < 5000; i++ {
		rows = append(rows, sessionWire{Name: "s" + itoa(i), Origin: "session", OriginSession: "s" + itoa(i-1)})
	}
	if tree := deliveryTree(rows, "s0"); len(tree) != 5000 {
		t.Fatalf("a long chain: %d sessions", len(tree))
	}
	rows = []sessionWire{{Name: "x", Origin: "session", OriginSession: "y"}, {Name: "y", Origin: "session", OriginSession: "x"}, {Name: "root"}}
	if tree := deliveryTree(rows, "root"); len(tree) != 1 {
		t.Fatalf("a cycle that does not touch the root: %d", len(tree))
	}
}

// The first turn's end is copied onto the delivery when it first appears and not changed later.
func TestMemoOccupancyFirstTurnCopiedOnce(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setRow(d.Session, map[string]any{"state": "working", "progressAgeSec": 1})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.TurnEndedAt != "" {
			t.Fatalf("no turn yet: %+v", got)
		}
		agent.setRow(d.Session, map[string]any{"queueTurn": map[string]any{"outcome": "turn-failed", "ms": 1234, "endedAt": "2026-11-15T01:02:03Z"}})
		e.settleNow(time.Now())
		got := e.delivery(d.ID)
		if got.TurnOutcome != "turn-failed" || got.TurnMs != 1234 || got.TurnEndedAt != "2026-11-15T01:02:03Z" {
			t.Fatalf("%+v", got)
		}
		agent.setRow(d.Session, map[string]any{"queueTurn": map[string]any{"outcome": "clean", "ms": 9, "endedAt": "2026-11-15T09:00:00Z"}})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.TurnOutcome != "turn-failed" || got.TurnEndedAt != "2026-11-15T01:02:03Z" {
			t.Fatalf("a later turn rewrote the first: %+v", got)
		}
		dto := memoDeliveryToDTO(e.delivery(d.ID))
		if dto.FirstTurn == nil || dto.FirstTurn.Outcome != "turn-failed" || dto.HeldBy != store.HeldMachine || dto.Overdue {
			t.Fatalf("dto %+v", dto)
		}
	})
}

// The queue settings carry maxTaskHours: default 12, 1 to 72.
func TestMemoOccupancyMaxTaskHoursSetting(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		id := e.category("app", "impl")
		put := func(body map[string]any) int {
			body["instruction"] = "go"
			return e.do("a", "PUT", "/api/memo-queues/"+id, body).Code
		}
		if put(map[string]any{}) != http.StatusOK {
			t.Fatal("default refused")
		}
		list := decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil))
		if len(list.Queues) != 1 || list.Queues[0].MaxTaskHours != 12 {
			t.Fatalf("%+v", list.Queues)
		}
		for h, want := range map[int]int{1: 200, 72: 200, 0: 400, 73: 400, -5: 400} {
			if got := put(map[string]any{"maxTaskHours": h}); got != want {
				t.Fatalf("maxTaskHours %d: %d, want %d", h, got, want)
			}
		}
		put(map[string]any{"maxTaskHours": 48})
		list = decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil))
		if list.Queues[0].MaxTaskHours != 48 {
			t.Fatalf("%+v", list.Queues)
		}
	})
}
