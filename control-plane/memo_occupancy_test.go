package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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
		"started again with the interaction carried": {func(a *fakeQueueAgent, root string) {
			a.setRow(root, map[string]any{"carriedLive": "plan"})
		}, store.HeldPerson},
		"a machine at work outranks a carried interaction": {func(a *fakeQueueAgent, root string) {
			a.setRow(root, map[string]any{"carriedLive": "plan", "state": "working", "progressAgeSec": 5})
		}, store.HeldMachine},
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

// reserve and observe drive the store the way two Control Plane processes do: each reserves its
// read's number before it reads, and keeps the read later.
func reserve(t *testing.T, st store.MemoStore, e *deliveryEnv, id string) int64 {
	t.Helper()
	var gens map[string]int64
	if err := st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) (err error) {
		gens, err = tx.ReserveDeliveryReads()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return gens[id]
}

func observe(t *testing.T, st store.MemoStore, e *deliveryEnv, id string, o store.DeliveryObservation) (int, bool) {
	t.Helper()
	var ticks int
	var kept bool
	if err := st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) (err error) {
		ticks, kept, err = tx.ObserveDelivery(id, o)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return ticks, kept
}

// The order of two reads is the number the database handed out, not a clock, and the count of
// reads with nothing held is worked out from the stored row. So: a later read that holds the
// delivery is kept even from a Control Plane whose clock is behind, an older read that found
// nothing cannot be kept over it (and cannot add a tick), and a clean read after a hold counts
// one, not the two a copy read before the hold would have made.
func TestMemoOccupancyReadOrderIsTheDatabasesNotTheClocks(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, d := e.openReported(nil)
		past := store.DeliveryTS(time.Now().Add(-time.Hour)) // a clock an hour behind
		future := store.DeliveryTS(time.Now().Add(time.Hour))

		// A reserves read 1, B reserves read 2. A's clock is ahead, B's behind.
		a := reserve(t, e.st, e, d.ID)
		b := reserve(t, e.other, e, d.ID)
		if b <= a {
			t.Fatalf("numbers %d %d", a, b)
		}
		// B (later read, slow clock) saw a watch, A (older read, fast clock) saw nothing.
		if _, kept := observe(t, e.other, e, d.ID, store.DeliveryObservation{Gen: b, At: past, HeldBy: store.HeldWakers, HeldSession: d.Session}); !kept {
			t.Fatal("the later read was refused")
		}
		ticks, kept := observe(t, e.st, e, d.ID, store.DeliveryObservation{Gen: a, At: future})
		got := e.delivery(d.ID)
		if kept || ticks != 0 || got.HeldBy != store.HeldWakers || got.HeldGen != b {
			t.Fatalf("an older clean read beat a later hold: kept=%v ticks=%d %+v", kept, ticks, got)
		}
		// An older read that found a hold resets the count even when it is refused.
		c := reserve(t, e.st, e, d.ID)
		if ticks, _ := observe(t, e.st, e, d.ID, store.DeliveryObservation{Gen: c}); ticks != 1 {
			t.Fatalf("clean read after a hold counts 1, got %d", ticks)
		}
		if _, kept := observe(t, e.other, e, d.ID, store.DeliveryObservation{Gen: b, HeldBy: store.HeldMachine}); kept {
			t.Fatal("an older read replaced a newer one")
		}
		if got := e.delivery(d.ID); got.IdleTicks != 0 {
			t.Fatalf("a refused hold must still reset the count: %+v", got)
		}
		// The stale-copy case: a process that holds a copy of the row with IdleTicks 1 cannot
		// close on one clean read after another process kept a hold.
		x := reserve(t, e.st, e, d.ID)
		observe(t, e.st, e, d.ID, store.DeliveryObservation{Gen: x})
		y := reserve(t, e.other, e, d.ID)
		observe(t, e.other, e, d.ID, store.DeliveryObservation{Gen: y, HeldBy: store.HeldWakers, HeldSession: d.Session})
		z := reserve(t, e.st, e, d.ID)
		if ticks, _ := observe(t, e.st, e, d.ID, store.DeliveryObservation{Gen: z}); ticks != 1 {
			t.Fatalf("one clean read after a hold made %d", ticks)
		}
		if e.delivery(d.ID).State != store.DeliveryOpen {
			t.Fatal("closed on a single clean read")
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
		// A session that came back to work (a watch fired again after the failed retire) is
		// retired like any other: the Agent's word decides whether a person took it over.
		agent.mu.Lock()
		agent.retireStatus = 0
		agent.mu.Unlock()
		agent.setRow(d.Session, map[string]any{"alive": true, "state": "working", "progressAgeSec": 1})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.RetirePending || got.RetiredAt == "" || got.RetireResult != "confirmed" {
			t.Fatalf("a session that worked again was not retired: %+v", got)
		}
	})
	// No time bound: days later the retire is still pending, and made when the Agent answers.
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.mu.Lock()
		agent.retireStatus = http.StatusBadGateway
		agent.mu.Unlock()
		e.settleNow(time.Now())
		e.settleNow(time.Now())
		later := time.Now().Add(10 * 24 * time.Hour)
		e.settleNow(later)
		if got := e.delivery(d.ID); !got.RetirePending {
			t.Fatalf("a pending retire was dropped by time: %+v", got)
		}
		agent.mu.Lock()
		agent.retireStatus = 0
		agent.mu.Unlock()
		e.settleNow(later)
		if got := e.delivery(d.ID); got.RetirePending || got.RetiredAt == "" {
			t.Fatalf("%+v", got)
		}
	})
}

// An overdue retire decides on the row as it is when the report is kept, not on the copy the
// tick began with: progress another Control Plane kept meanwhile, or a report overtaken by a
// newer one, sends no retire.
func TestMemoOccupancyOverdueRetireUsesTheFreshRow(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(map[string]any{"maxTaskHours": 1})
		agent.setRow(d.Session, map[string]any{"wakers": map[string]any{"watches": 1, "timers": 0}})
		later := time.Now().Add(3 * time.Hour)
		stale := e.delivery(d.ID)
		stale.OverdueAt = store.DeliveryTS(later)
		views := map[string]*memoMemberView{}
		r := e.runner()
		v := r.view(context.Background(), views, e.members["a"])
		// Another process, whose read was numbered before ours, kept fresh progress of a child.
		g0 := v.gens[d.ID] - 1
		observe(t, e.other, e, d.ID, store.DeliveryObservation{Gen: g0, HeldBy: store.HeldMachine, HeldSession: "c1",
			Progress: true, ProgressAt: store.DeliveryTS(later)})
		r.closeIfDoneReported(context.Background(), v, stale, later)
		if agent.retireCalls() != 0 {
			t.Fatal("retired on a stale ProgressSeenAt")
		}
		// A report overtaken by a newer one sends nothing either.
		v2 := r.view(context.Background(), map[string]*memoMemberView{}, e.members["a"])
		observe(t, e.other, e, d.ID, store.DeliveryObservation{Gen: v2.gens[d.ID] + 1, HeldBy: store.HeldWakers, HeldSession: d.Session})
		r.closeIfDoneReported(context.Background(), v2, stale, later.Add(5*time.Hour))
		if agent.retireCalls() != 0 {
			t.Fatal("retired on a report a newer one overtook")
		}
	})
}

// A 404 from the retire route is not "the session is gone": a gateway, or an Agent without the
// route, answers it too, so it is never a confirmation. Absence is proven by a successful report
// that no longer lists the session, and then no call is made.
func TestMemoOccupancyRetire404IsNotAConfirmation(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.mu.Lock()
		agent.retireStatus = http.StatusNotFound // agentErr writes code "boom"
		agent.retireCode = "not_found"
		agent.mu.Unlock()
		for i := 0; i < 4; i++ {
			e.settleNow(time.Now())
		}
		got := e.delivery(d.ID)
		if got.State != store.DeliveryClosed || !got.RetirePending || got.RetiredAt != "" {
			t.Fatalf("a route 404 confirmed the retire: %+v", got)
		}
		calls := agent.retireCalls()
		if calls < 2 {
			t.Fatalf("not repeated: %d", calls)
		}
		// The session is gone from the Agent's list: pending ends without a call.
		agent.dropRow(d.Session)
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.RetirePending || got.RetiredAt != "" || agent.retireCalls() != calls {
			t.Fatalf("%+v", got)
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

// The Agent's word decides what a retire came to. A root released to a person drops the pending
// retire and says why. A released member below the root ends the repeats once everything else is
// stopped. A 409 retire_not_root is a fault, logged and not repeated. A cancelled wait (503) and
// an incomplete answer without a release are repeated.
func TestMemoOccupancyRetireAnswers(t *testing.T) {
	type tc struct {
		setup  func(a *fakeQueueAgent, root string)
		result string
		calls  int // retire calls over six more ticks
	}
	cases := map[string]tc{
		"root released": {func(a *fakeQueueAgent, root string) { a.retireRelease = root }, "released", 1},
		"child released": {func(a *fakeQueueAgent, root string) {
			a.rows = append(a.rows, map[string]any{"name": "c1", "kind": "claude", "alive": true, "state": "idle",
				"origin": "session", "originSession": root})
			a.retireRelease = "c1"
		}, "done", 1},
		"not a root": {func(a *fakeQueueAgent, root string) {
			a.rows[0]["origin"], a.rows[0]["originSession"] = "session", "someone"
		}, "fault", 1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
				agent, d := e.openReported(nil)
				agent.mu.Lock()
				c.setup(agent, d.Session)
				agent.mu.Unlock()
				for i := 0; i < 7; i++ {
					e.settleNow(time.Now())
				}
				got := e.delivery(d.ID)
				if got.RetirePending || got.RetireResult != c.result || got.RetiredAt != "" || agent.retireCalls() != c.calls {
					t.Fatalf("%s: calls %d, %+v", name, agent.retireCalls(), got)
				}
				if dto := memoDeliveryToDTO(got); dto.Retire != c.result {
					t.Fatalf("dto retire %q", dto.Retire)
				}
			})
		})
	}
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.mu.Lock()
		agent.retireStatus, agent.retireCode = http.StatusServiceUnavailable, "retire_wait_cancelled"
		agent.mu.Unlock()
		for i := 0; i < 4; i++ {
			e.settleNow(time.Now())
		}
		if got := e.delivery(d.ID); !got.RetirePending || agent.retireCalls() < 2 {
			t.Fatalf("a cancelled wait must repeat: calls %d, %+v", agent.retireCalls(), got)
		}
	})
}

// A Control Plane that predates this version closes by its own rule and ignores what was kept.
// The database refuses its clean close while the last kept report holds the delivery, so the
// slot is not freed under a watch, and a clean close it makes when the report was clear sets no
// retire mark, so this version retires that session all the same. (The older binary's write is
// MoveDelivery's statement: the memo tx op is what an older binary runs.)
func TestMemoOccupancyOlderControlPlaneCannotCloseAHeldDelivery(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setRow(d.Session, map[string]any{"wakers": map[string]any{"watches": 1, "timers": 0}})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.HeldBy != store.HeldWakers {
			t.Fatalf("%+v", got)
		}
		oldClose := func() error {
			return e.other.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
				_, err := tx.MoveDelivery(d.ID, store.DeliveryOpen, store.DeliveryClosed, "", store.DeliveryClean, store.DeliveryTS(time.Now()))
				return err
			})
		}
		if err := oldClose(); err == nil {
			t.Fatal("an older binary's clean close of a held delivery went through")
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
			t.Fatalf("%+v", got)
		}
		// The report is clear: its close is allowed, sets no mark, and is retired anyway.
		agent.setRow(d.Session, map[string]any{"wakers": nil})
		e.settleNow(time.Now())
		if err := oldClose(); err != nil {
			t.Fatalf("a close of a clear delivery was refused: %v", err)
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.RetirePending {
			t.Fatalf("%+v", got)
		}
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.RetireResult != "confirmed" || agent.retireCalls() != 1 {
			t.Fatalf("calls %d, %+v", agent.retireCalls(), got)
		}
	})
}

// A count written by the rule for an Agent that does not report (MarkMemoDeliveryIdle, all an
// older Control Plane has) is no clear read of the report: with the kept report holding the
// delivery, the next clear read counts one, not two, and does not close.
func TestMemoOccupancyLegacyIdleCountIsNotAClearRead(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setRow(d.Session, map[string]any{"wakers": map[string]any{"watches": 1, "timers": 0}})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.HeldBy != store.HeldWakers || got.IdleTicks != 0 {
			t.Fatalf("%+v", got)
		}
		// An older Control Plane sees the session idle and writes its count.
		if ok, err := e.other.MarkMemoDeliveryIdle(context.Background(), d.ID, 1); err != nil || !ok {
			t.Fatal(ok, err)
		}
		agent.setRow(d.Session, map[string]any{"wakers": nil})
		e.settleNow(time.Now())
		got := e.delivery(d.ID)
		if got.State != store.DeliveryOpen || got.IdleTicks != 1 {
			t.Fatalf("one clear read closed on the legacy count: %+v", got)
		}
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed {
			t.Fatalf("two clear reads must close: %+v", got)
		}
	})
}

// `gone` is decided from a read that began after the delivery closed, not from the tick's cached
// list: a cached list that predates the session does not end a pending retire.
func TestMemoOccupancyGoneNeedsAReadAfterTheClose(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.mu.Lock()
		agent.retireStatus = http.StatusBadGateway
		agent.mu.Unlock()
		e.settleNow(time.Now())
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || !got.RetirePending {
			t.Fatalf("%+v", got)
		}
		agent.mu.Lock()
		agent.retireStatus = 0
		agent.mu.Unlock()
		// The cached list of this tick was taken before the session existed.
		r := e.runner()
		views := map[string]*memoMemberView{}
		v := r.view(context.Background(), views, e.members["a"])
		v.live = nil
		r.retirePending(context.Background(), views, time.Now())
		got := e.delivery(d.ID)
		if got.RetireResult != "confirmed" || agent.retireCalls() < 2 {
			t.Fatalf("a stale cache decided gone: calls %d, %+v", agent.retireCalls(), got)
		}
	})
	// A session that is gone from a read made after the close is gone.
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.mu.Lock()
		agent.retireStatus = http.StatusBadGateway
		agent.mu.Unlock()
		e.settleNow(time.Now())
		e.settleNow(time.Now())
		agent.dropRow(d.Session)
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.RetireResult != "gone" || got.RetirePending {
			t.Fatalf("%+v", got)
		}
	})
}

// A pass whose first-turn reads run into a slow workspace stamps what it reached with a context
// that does not inherit its deadline, so the next tick goes on with the ones behind it and the
// breaker sees a failed first turn there.
func TestMemoOccupancyClosedTurnsProgressSurvivesTheDeadline(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", nil, 0)
		// A second member whose workspace never answers /sessions in time.
		slowURL, closeSlow := registerInMemAgent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
		}))
		t.Cleanup(closeSlow)
		ws := store.Workspace{ID: "ws-c", TenantID: e.t1.ID, MembershipID: e.members["c"], ContainerName: "c", Network: "n",
			DataDir: "/d", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
		if err := e.st.CreateWorkspace(context.Background(), ws); err != nil {
			t.Fatal(err)
		}
		e.mgr.mu.Lock()
		e.mgr.rts[e.members["c"]] = cachedRT{rt: stubRuntime{endpoint: slowURL, token: "tok", state: "running"}, ws: ws}
		e.mgr.mu.Unlock()
		insert := func(membership, queue, session string, closedAgo time.Duration) string {
			id := store.NewID()
			ts := store.DeliveryTS(time.Now().Add(-closedAgo))
			if err := e.st.MemoTx(context.Background(), membership, func(tx store.MemoTxOps) error {
				return tx.InsertDelivery(store.MemoDelivery{ID: id, QueueID: queue, State: store.DeliveryClosed, Outcome: store.DeliveryClean,
					Session: session, MessageIDs: []string{}, CreatedAt: ts, UpdatedAt: ts, ClosedAt: ts, Reported: true, RetireResult: "confirmed"})
			}); err != nil {
				t.Fatal(err)
			}
			return id
		}
		insert(e.members["c"], "qc", "slow-session", 3*time.Hour) // first in line, never answers
		real := insert(e.members["a"], q, "real-1", time.Hour)
		agent.mu.Lock()
		agent.rows = append(agent.rows, map[string]any{"name": "real-1", "kind": "claude", "alive": false, "state": "",
			"queueTurn": map[string]any{"outcome": "turn-failed", "ms": 4000, "endedAt": "2026-11-15T01:00:00Z"}})
		agent.mu.Unlock()
		r := e.runner()
		// The pass's budget runs out inside the slow workspace.
		tctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		r.collectClosedTurns(tctx, map[string]*memoMemberView{}, time.Now())
		cancel()
		if got := e.delivery(real); got.TurnEndedAt != "" {
			t.Fatalf("the later delivery was reached in a pass that had run out: %+v", got)
		}
		// The next tick starts with the delivery not reached and records the failed first turn.
		tctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		r.collectClosedTurns(tctx, map[string]*memoMemberView{}, time.Now())
		cancel()
		if got := e.delivery(real); got.TurnOutcome != "turn-failed" {
			t.Fatalf("progress was lost with the deadline: the slow head is tried first again: %+v", got)
		}
	})
}

// A session started again without answering the card it was stopped with is still waiting for a
// person (ADR 0137 section 6): the run stays open on two reads, shows the person's hold, and
// closes on two reads once the Agent no longer reports the interaction (answered, discarded or
// expired).
func TestMemoOccupancyLiveCarriedHoldsUntilGone(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setRow(d.Session, map[string]any{"carriedLive": "question"})
		for i := 0; i < 4; i++ {
			e.settleNow(time.Now())
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.HeldBy != store.HeldPerson {
			t.Fatalf("carried and live: %+v", got)
		}
		agent.setRow(d.Session, map[string]any{"carriedLive": nil})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
			t.Fatalf("first read without it: %+v", got)
		}
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean {
			t.Fatalf("second read without it: %+v", got)
		}
	})
}

// The rule for an Agent that does not report does not count an idle row with the interaction
// carried as idle.
func TestMemoOccupancyUnreportedAgentLiveCarriedIsNotIdle(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.mu.Lock()
		agent.report = false
		agent.reportVersion = 0
		agent.mu.Unlock()
		agent.setRow(d.Session, map[string]any{"carriedLive": "permission"})
		for i := 0; i < 4; i++ {
			e.settleNow(time.Now())
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
			t.Fatalf("unreported, carried and live: %+v", got)
		}
		agent.setRow(d.Session, map[string]any{"carriedLive": nil})
		e.settleNow(time.Now())
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean {
			t.Fatalf("unreported, carried gone: %+v", got)
		}
	})
}

// A run held by a watch keeps the id and source of what holds it, and drops them when the hold
// ends. The reason names the watch only: nothing but id and source reaches the delivery.
func TestMemoOccupancyWatchHoldNamesTheWatch(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setRow(d.Session, map[string]any{"wakers": map[string]any{"watches": 1, "timers": 0, "holders": []any{
			map[string]any{"id": "w_42", "source": "github", "payload": "secret-token"},
		}}})
		e.settleNow(time.Now())
		got := e.delivery(d.ID)
		if got.HeldBy != store.HeldWakers {
			t.Fatalf("held: %+v", got)
		}
		dto := memoDeliveryToDTO(got)
		if len(dto.HeldWakers) != 1 || dto.HeldWakers[0].ID != "w_42" || dto.HeldWakers[0].Source != "github" {
			t.Fatalf("heldWakers: %+v", dto.HeldWakers)
		}
		if strings.Contains(got.HeldDetail, "secret") {
			t.Fatalf("a payload reached the delivery: %q", got.HeldDetail)
		}
		agent.setRow(d.Session, map[string]any{"wakers": nil})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.HeldDetail != "" || len(memoDeliveryToDTO(got).HeldWakers) != 0 {
			t.Fatalf("name kept after the hold ended: %+v", got)
		}
	})
}

// An old row (empty detail), a garbled one and a hold other than wakers all read as no names,
// and the field is an empty array, never null.
func TestMemoHeldWakersToleratesNoDetail(t *testing.T) {
	for _, c := range []struct{ by, detail string }{
		{store.HeldWakers, ""}, {store.HeldWakers, "not json"}, {store.HeldWakers, "null"},
		{store.HeldMachine, `[{"id":"a","source":"b"}]`},
	} {
		dto := memoDeliveryToDTO(store.MemoDelivery{State: store.DeliveryOpen, Reported: true, HeldBy: c.by, HeldDetail: c.detail})
		b, _ := json.Marshal(dto)
		if dto.HeldWakers == nil || len(dto.HeldWakers) != 0 || !strings.Contains(string(b), `"heldWakers":[]`) {
			t.Fatalf("%+v: %s", c, b)
		}
	}
}

// treeHeldDetail keeps names only while wakers is the winning hold, skips blank ids, and bounds
// the count and the length of what it keeps (by rune, so Japanese text stays valid UTF-8).
func TestTreeHeldDetailBounds(t *testing.T) {
	now := time.Now()
	long := strings.Repeat("あ", 200)
	holders := []sessionWakerHolder{{ID: "", Source: "x"}, {ID: "   ", Source: "x"}, {ID: " " + long + " ", Source: " s "}}
	for i := 0; i < 8; i++ {
		holders = append(holders, sessionWakerHolder{ID: "w" + string(rune('0'+i)), Source: "github"})
	}
	row := &sessionWire{Name: "s", Alive: true, State: "idle", Wakers: &sessionWakers{Watches: 11, Holders: holders}}
	if got := sessionHold(*row, now); got != store.HeldWakers {
		t.Fatalf("setup: hold %q", got)
	}
	var out []memoHeldWakerDTO
	raw := treeHeldDetail([]*sessionWire{row}, store.HeldWakers, now)
	if !utf8.ValidString(raw) || json.Unmarshal([]byte(raw), &out) != nil {
		t.Fatalf("not valid JSON/UTF-8: %q", raw)
	}
	if len(out) != memoHeldDetailMax {
		t.Fatalf("want %d entries, got %d: %+v", memoHeldDetailMax, len(out), out)
	}
	if n := utf8.RuneCountInString(out[0].ID); n != memoHeldDetailFieldN || out[0].Source != "s" {
		t.Fatalf("first entry not clipped and trimmed: %d runes, source %q", n, out[0].Source)
	}
	for _, h := range out {
		if strings.TrimSpace(h.ID) == "" {
			t.Fatalf("blank id kept: %+v", out)
		}
	}
	// A stronger hold elsewhere in the tree: nothing is kept.
	if got := treeHeldDetail([]*sessionWire{row}, store.HeldMachine, now); got != "" {
		t.Fatalf("kept under another hold: %q", got)
	}
}

// With a watch still on the row, a stronger hold that takes over clears the stored names.
func TestMemoOccupancyStrongerHoldClearsTheNames(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		wk := map[string]any{"watches": 1, "timers": 0, "holders": []any{map[string]any{"id": "w_42", "source": "github"}}}
		agent.setRow(d.Session, map[string]any{"wakers": wk})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.HeldBy != store.HeldWakers || got.HeldDetail == "" {
			t.Fatalf("setup: %+v", got)
		}
		agent.setRow(d.Session, map[string]any{"wakers": wk, "carriedLive": "question"})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.HeldBy != store.HeldPerson || got.HeldDetail != "" {
			t.Fatalf("names kept under a stronger hold: %+v", got)
		}
	})
}

// A child whose turn ended after its idle parent's last turn start keeps the run open for
// memoChildGrace, so the parent can read the answer. The parent starting a turn, or the grace
// running out, lets it close clean. Nothing is retired meanwhile.
func TestMemoOccupancyIdleChildHoldsRunForItsParent(t *testing.T) {
	ts := func(x time.Time) string { return x.UTC().Format(time.RFC3339Nano) }
	t0 := time.Now()
	setup := func(e *deliveryEnv) (*fakeQueueAgent, store.MemoDelivery, *memoQueueRunner) {
		agent, d := e.openReported(nil)
		agent.setRow(d.Session, map[string]any{"lastTurnStartAt": ts(t0.Add(-time.Minute))})
		agent.child("rev", d.Session, map[string]any{"lastTurnEndAt": ts(t0)})
		return agent, d, e.runner()
	}
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d, r := setup(e)
		for i := 1; i <= 6; i++ {
			r.settle(context.Background(), t0.Add(time.Duration(i)*time.Minute))
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || agent.retireCalls() != 0 {
			t.Fatalf("closed while a child's answer is unread: %+v", got)
		}
		// The parent has a turn after the child ended.
		agent.setRow(d.Session, map[string]any{"lastTurnStartAt": ts(t0.Add(7 * time.Minute))})
		for i := 7; i <= 9; i++ {
			r.settle(context.Background(), t0.Add(time.Duration(i)*time.Minute))
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean {
			t.Fatalf("not closed after the parent's turn: %+v", got)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d, r := setup(e)
		// The parent never reads it: open just before the grace ends, clean after.
		for i := 1; i <= 3; i++ {
			r.settle(context.Background(), t0.Add(memoChildGrace-time.Duration(i)*time.Second))
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || agent.retireCalls() != 0 {
			t.Fatalf("closed inside the grace: %+v", got)
		}
		for i := 0; i < 3; i++ {
			r.settle(context.Background(), t0.Add(memoChildGrace+time.Duration(i)*time.Second))
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean {
			t.Fatalf("not closed after the grace: %+v", got)
		}
	})
}

func TestMemoChildAnswerUnreadBounds(t *testing.T) {
	now := time.Now()
	f := func(x time.Time) string { return x.UTC().Format(time.RFC3339Nano) }
	mk := func(pStart, cEnd string) []*sessionWire {
		return []*sessionWire{{Name: "p", Alive: true, LastTurnStartAt: pStart},
			{Name: "c", Alive: true, Origin: "session", OriginSession: "p", LastTurnEndAt: cEnd}}
	}
	for name, c := range map[string]struct {
		tree []*sessionWire
		at   time.Time
		want bool
	}{
		"child just ended":              {mk(f(now.Add(-time.Hour)), f(now.Add(-time.Minute))), now, true},
		"parent never started":          {mk("", f(now.Add(-time.Minute))), now, true},
		"grace over":                    {mk(f(now.Add(-time.Hour)), f(now.Add(-time.Minute))), now.Add(memoChildGrace), false},
		"future end (skew)":             {mk(f(now.Add(-time.Hour)), f(now.Add(time.Minute))), now, true},
		"same second, parent first":     {mk(f(now.Truncate(time.Second).Add(200*time.Millisecond)), f(now.Truncate(time.Second))), now, true},
		"parent a second later":         {mk(f(now.Truncate(time.Second).Add(1500*time.Millisecond)), f(now.Truncate(time.Second))), now, false},
		"end beyond the skew bound":     {mk(f(now.Add(-time.Hour)), f(now.Add(memoClockSkew+time.Minute))), now, false},
		"parent exactly a second later": {mk(f(now.Truncate(time.Second).Add(time.Second)), f(now.Truncate(time.Second))), now, false},
		"parent started after":          {mk(f(now), f(now.Add(-time.Minute))), now, false},
		"child mid-turn":                {mk(f(now.Add(-time.Hour)), ""), now, false},
		"unparsable child time":         {mk("", "garbage"), now, false},
		"unparsable parent time":        {mk("garbage", f(now.Add(-time.Minute))), now, false},
	} {
		if got := childAnswerUnread(c.tree, c.at); got != c.want {
			t.Errorf("%s: got %v", name, got)
		}
	}
	dead := mk("", f(now.Add(-time.Minute)))
	dead[0].Alive = false
	if childAnswerUnread(dead, now) {
		t.Error("a stopped parent cannot read anything")
	}
}

// #5270: a run is not closed while a peer message to a member of its tree is on its way. It is no
// hold: nothing is kept as the reason. A read that sees one starts the count of clear reads again,
// so the run closes on the usual two clear reads once the message has landed, and, for one that
// never lands, once memoPeerHoldBound has passed since the later of its queue time and the
// member's last turn end.
func TestMemoOccupancyPeerMessageInTransitKeepsTheRunOpen(t *testing.T) {
	ts := func(x time.Time) string { return x.UTC().Format(time.RFC3339) }
	t0 := time.Now().Truncate(time.Second)
	pending := func(at time.Time) map[string]any { return map[string]any{"count": 1, "newestAt": ts(at)} }
	t.Run("delivered", func(t *testing.T) {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			agent, d := e.openReported(nil)
			agent.child("rev", d.Session, map[string]any{"lastTurnEndAt": ts(t0.Add(-time.Hour)), "pendingPeers": pending(t0)})
			r := e.runner()
			for i := 1; i <= 6; i++ {
				r.settle(context.Background(), t0.Add(time.Duration(i)*time.Minute))
			}
			got := e.delivery(d.ID)
			if got.State != store.DeliveryOpen || got.HeldBy != "" || agent.retireCalls() != 0 {
				t.Fatalf("closed, or kept a hold, under a message in transit: %+v", got)
			}
			// One read without it: the spool could not be read that once, or the read caught the
			// session idle just before the message landed. It must not close the run.
			agent.setRow("rev", map[string]any{"pendingPeers": nil})
			r.settle(context.Background(), t0.Add(7*time.Minute))
			if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.IdleTicks != 1 {
				t.Fatalf("one read without the message closed the run: %+v", got)
			}
			// The message started a turn: a hold.
			agent.setRow("rev", map[string]any{"state": "working", "progressAgeSec": 5})
			r.settle(context.Background(), t0.Add(8*time.Minute))
			if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.HeldBy != store.HeldMachine {
				t.Fatalf("want held by the turn the message started: %+v", got)
			}
			// The turn ended: the usual two clear reads close the run.
			agent.setRow("rev", map[string]any{"state": "idle", "progressAgeSec": nil})
			for i := 9; i <= 10; i++ {
				r.settle(context.Background(), t0.Add(time.Duration(i)*time.Minute))
			}
			if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean {
				t.Fatalf("not closed after the message landed: %+v", got)
			}
		})
	})
	t.Run("never delivered", func(t *testing.T) {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			agent, d := e.openReported(nil)
			// Queued long ago and refused on every try, but the session's turn ended later: the
			// bound counts from the end. On the root, so no child's unread answer keeps it open.
			agent.setRow(d.Session, map[string]any{"lastTurnEndAt": ts(t0), "pendingPeers": pending(t0.Add(-time.Hour))})
			r := e.runner()
			for i := 3; i >= 1; i-- {
				r.settle(context.Background(), t0.Add(memoPeerHoldBound-time.Duration(i)*time.Second))
			}
			if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
				t.Fatalf("closed inside the bound: %+v", got)
			}
			for i := 0; i < 2; i++ {
				r.settle(context.Background(), t0.Add(memoPeerHoldBound+time.Duration(i)*time.Second))
			}
			if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean {
				t.Fatalf("an undeliverable message kept the run open past the bound: %+v", got)
			}
		})
	})
	t.Run("stopped member counts from the queue time", func(t *testing.T) {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			agent, d := e.openReported(nil)
			// A stop clears the turn record, so a stopped row has no lastTurnEndAt.
			agent.child("rev", d.Session, map[string]any{"alive": false, "state": "", "pendingPeers": pending(t0)})
			r := e.runner()
			for i := 1; i <= 3; i++ {
				r.settle(context.Background(), t0.Add(time.Duration(i)*time.Minute))
			}
			if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
				t.Fatalf("closed inside the bound: %+v", got)
			}
			for i := 0; i < 2; i++ {
				r.settle(context.Background(), t0.Add(memoPeerHoldBound+time.Duration(i)*time.Second))
			}
			if got := e.delivery(d.ID); got.State != store.DeliveryClosed {
				t.Fatalf("not closed after the bound: %+v", got)
			}
		})
	})
	t.Run("a stronger hold is kept as the reason", func(t *testing.T) {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			agent, d := e.openReported(nil)
			agent.setRow(d.Session, map[string]any{"wakersUnknown": true, "pendingPeers": pending(t0)})
			e.runner().settle(context.Background(), t0.Add(time.Minute))
			if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.HeldBy != store.HeldUnknown {
				t.Fatalf("want held by unknown: %+v", got)
			}
		})
	})
	// The workspace stops right after a read that saw a message in transit: no hold was kept, so
	// the run closes from the mirror as it would on main, instead of staying open until a start.
	t.Run("workspace stopped", func(t *testing.T) {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			agent, d := e.openReported(nil)
			agent.setRow(d.Session, map[string]any{"pendingPeers": pending(time.Now())})
			e.settleNow(time.Now())
			if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.HeldBy != "" {
				t.Fatalf("setup: %+v", got)
			}
			e.stopWorkspace(agent, d.Session, "")
			for i := 0; i < 3; i++ {
				e.settleNow(time.Now())
			}
			if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean {
				t.Fatalf("a stopped workspace kept the run open: %+v", got)
			}
		})
	})
}

func TestMemoPeerInTransitBounds(t *testing.T) {
	now := time.Now()
	f := func(x time.Time) string { return x.UTC().Format(time.RFC3339Nano) }
	row := func(count int, newest, end string) sessionWire {
		s := sessionWire{Name: "m", LastTurnEndAt: end}
		if newest != "" || count != 0 {
			s.PendingPeers = &sessionPendingPeers{Count: count, NewestAt: newest}
		}
		return s
	}
	for name, c := range map[string]struct {
		row  sessionWire
		at   time.Time
		want bool
	}{
		"nothing reported":                 {row(0, "", ""), now, false},
		"a zero count":                     {row(0, f(now), ""), now, false},
		"just queued":                      {row(1, f(now.Add(-time.Minute)), ""), now, true},
		"bound over":                       {row(1, f(now.Add(-time.Minute)), ""), now.Add(memoPeerHoldBound), false},
		"old message, recent turn end":     {row(1, f(now.Add(-time.Hour)), f(now.Add(-time.Minute))), now, true},
		"old message, old turn end":        {row(1, f(now.Add(-time.Hour)), f(now.Add(-memoPeerHoldBound))), now, false},
		"turn end before the message":      {row(1, f(now.Add(-time.Minute)), f(now.Add(-time.Hour))), now, true},
		"queue time a little ahead (skew)": {row(1, f(now.Add(30*time.Second)), ""), now, true},
		"queue time beyond the skew":       {row(1, f(now.Add(memoClockSkew+time.Minute)), ""), now, false},
		"turn end beyond the skew":         {row(1, f(now.Add(-time.Hour)), f(now.Add(memoClockSkew+time.Hour))), now, false},
		"unparsable queue time":            {row(1, "garbage", f(now)), now, false},
		"unparsable turn end":              {row(1, f(now.Add(-time.Minute)), "garbage"), now, true},
	} {
		if got := peerInTransit(c.row, c.at); got != c.want {
			t.Errorf("%s: got %v", name, got)
		}
	}
}

// A session list that cannot be read, or that is stale, must not count toward closing a run; a
// consistent idle tree still closes on the usual two reads.
func TestMemoOccupancyUnsoundReadKeepsRunOpen(t *testing.T) {
	t.Run("unreadable list", func(t *testing.T) {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			agent, d := e.openReported(nil)
			e.settleNow(time.Now()) // one idle read is stored
			agent.mu.Lock()
			agent.sessionsDown = true
			agent.mu.Unlock()
			for i := 0; i < 4; i++ {
				e.settleNow(time.Now())
			}
			if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.IdleTicks != 0 {
				t.Fatalf("unreadable list must keep it open and reset the count: %+v", got)
			}
		})
	})
	t.Run("stale list after one idle read", func(t *testing.T) {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			_, d := e.openReported(nil)
			e.settleNow(time.Now()) // idle read 1 of 2
			if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.IdleTicks != 1 {
				t.Fatalf("setup: %+v", got)
			}
			for i := 0; i < 3; i++ {
				r := e.runner()
				r.staleReadMax = -time.Second // every read is already older than the bound
				r.settle(context.Background(), time.Now())
			}
			if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.IdleTicks != 0 {
				t.Fatalf("stale lists must reset the count: %+v", got)
			}
			e.settleNow(time.Now())
			if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
				t.Fatalf("the first consistent read after a stale one must not close: %+v", got)
			}
			e.settleNow(time.Now())
			if got := e.delivery(d.ID); got.State != store.DeliveryClosed {
				t.Fatalf("consistent idle tree must close: %+v", got)
			}
		})
	})
	t.Run("member with unknown state", func(t *testing.T) {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			agent, d := e.openReported(nil)
			agent.child("c1", d.Session, map[string]any{"state": ""})
			for i := 0; i < 4; i++ {
				e.settleNow(time.Now())
			}
			if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.HeldBy != store.HeldUnknown {
				t.Fatalf("a member of unknown state must hold the run: %+v", got)
			}
		})
	})
	t.Run("deleted root still aborts", func(t *testing.T) {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			agent, d := e.openReported(nil)
			agent.dropRow(d.Session)
			e.settleNow(time.Now())
			if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryAborted {
				t.Fatalf("a deletion the Agent's own list proves must abort: %+v", got)
			}
		})
	})
}

// A stale read resets the count even when the settle pass's deadline has passed and this tick's
// copy of the delivery still says 0 (another Control Plane stored a 1 since).
func TestMemoOccupancyStaleReadResetsOnExpiredContext(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, d := e.openReported(nil)
		stale := d // the copy this tick began with: IdleTicks 0
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.IdleTicks != 1 {
			t.Fatalf("setup: %+v", got)
		}
		r := e.runner()
		v := r.view(context.Background(), map[string]*memoMemberView{}, d.MembershipID)
		if v == nil || !v.liveOK {
			t.Fatalf("no live view: %+v", v)
		}
		v.readDone = time.Now().Add(-2 * r.staleReadMax)
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second)) // the settle pass's deadline has passed
		defer cancel()
		r.closeIfDoneReported(ctx, v, stale, time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.IdleTicks != 0 {
			t.Fatalf("the stale tick must reset the stored count: %+v", got)
		}
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
			t.Fatalf("first read after the stale one must not close: %+v", got)
		}
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed {
			t.Fatalf("second read must close: %+v", got)
		}
	})
}
