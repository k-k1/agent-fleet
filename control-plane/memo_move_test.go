package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestMemoMoveKeepsSlotUntilReplacementFinishes(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 1}, 1)
		e.autoTick(time.Now())
		d := e.liveDeliveries(q)[0]
		agent.setRow(d.Session, map[string]any{"alive": false, "state": "limited"})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.LimitKinds != ",claude," {
			t.Fatalf("source limit: %+v", got)
		}
		e.pending("app", "impl", "next")
		agent.setRow(d.Session, map[string]any{"moving": "replacement", "state": ""})
		for i := 0; i < 3; i++ {
			e.autoTick(time.Now())
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || agent.createCount() != 1 {
			t.Fatalf("pending move lost its slot: %+v, creates=%d", got, agent.createCount())
		}

		agent.setRow(d.Session, map[string]any{"moving": "", "movedTo": "replacement", "state": "limited"})
		agent.mu.Lock()
		agent.rows = append(agent.rows, map[string]any{"name": "replacement", "kind": "codex", "alive": true, "state": "working", "progressAgeSec": 1})
		agent.mu.Unlock()
		for i := 0; i < 3; i++ {
			e.autoTick(time.Now())
		}
		got := e.delivery(d.ID)
		if got.State != store.DeliveryOpen || got.HeldBy != store.HeldMachine || got.HeldSession != "replacement" || got.LimitKinds != "" ||
			got.CreatedAt != d.CreatedAt || got.SentAt != d.SentAt || got.OpenedAt != d.OpenedAt || agent.createCount() != 1 {
			t.Fatalf("replacement must hold the original slot while clearing the source limit: %+v, creates=%d", got, agent.createCount())
		}
		if err := e.st.MemoTx(context.Background(), d.MembershipID, func(tx store.MemoTxOps) error {
			capacity, err := tx.MemoRunCapacity(q)
			if err != nil {
				return err
			}
			launches, err := tx.CountDeliveriesCreatedSince(q, store.DeliveryTS(time.Now().Add(-24*time.Hour)))
			if err == nil && (capacity.QueueOpen != 1 || capacity.Open != 1 || launches != 1) {
				t.Errorf("move charged %d queue slots, %d member slots and %d launches", capacity.QueueOpen, capacity.Open, launches)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}

		agent.setRow("replacement", map[string]any{"state": "limited", "alive": false})
		e.autoTick(time.Now())
		if got := e.delivery(d.ID); got.HeldBy != store.HeldLimit || got.LimitKinds != ",codex," {
			t.Fatalf("replacement limit: %+v", got)
		}
		agent.setRow("replacement", map[string]any{"state": "idle", "alive": true})
		e.autoTick(time.Now())
		if agent.createCount() != 1 {
			t.Fatal("the first clear read freed the slot")
		}
		e.autoTick(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean || agent.createCount() != 2 {
			t.Fatalf("completion must free exactly one slot: %+v, creates=%d", got, agent.createCount())
		}
		e.settleNow(time.Now())
		if len(agent.retires) != 1 || agent.retires[0] != "replacement" {
			t.Fatalf("retire addressed %v", agent.retires)
		}
		list := e.do("a", "GET", "/api/memo-queues/"+q+"/deliveries", nil)
		gotOriginal := false
		for _, row := range decode[[]memoDeliveryDTO](t, list) {
			if row.ID == d.ID {
				gotOriginal = row.Session == "replacement" && row.Outcome == store.DeliveryClean
			}
		}
		if !gotOriginal {
			t.Fatalf("history lost the continuing delivery: %s", list.Body.String())
		}
	})
}

func TestMemoMoveKeepsMovedChildThroughStoppedWorkspace(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setRow(d.Session, map[string]any{"alive": false})
		agent.child("review", d.Session, map[string]any{"alive": false, "state": "limited"})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.HeldSession != "review" || got.LimitKinds != ",claude," {
			t.Fatalf("child limit: %+v", got)
		}
		agent.setRow("review", map[string]any{"origin": "user", "movedTo": "first-review", "state": "limited"})
		agent.child("new-review", d.Session, map[string]any{"kind": "codex", "queueReplaces": []string{"review", "first-review"},
			"alive": false, "state": "limited"})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.HeldSession != "new-review" || got.LimitKinds != ",codex," {
			t.Fatalf("replacement child limit: %+v", got)
		}

		ctx := context.Background()
		ws, _, _ := e.st.GetWorkspaceByMembership(ctx, e.members["a"])
		rows := sessionRows([]sessionWire{{Name: d.Session, Origin: "queue"}, {
			Name: "new-review", Origin: "session", OriginSession: d.Session, QueueReplaces: []string{"review", "first-review"}, Carried: "question",
		}})
		if err := e.st.ReplaceSessions(ctx, ws.ID, rows); err != nil {
			t.Fatal(err)
		}
		e.setRuntime(agent, "stopped")
		for i := 0; i < 3; i++ {
			e.settleNow(time.Now())
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.LimitKinds != ",codex," || agent.retireCalls() != 0 {
			t.Fatalf("the stopped workspace lost its last hold: %+v", got)
		}
		e.setRuntime(agent, "")
		agent.setRow("new-review", map[string]any{"state": "question", "alive": true})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.HeldBy != store.HeldPerson || got.HeldSession != "new-review" || got.LimitKinds != "" {
			t.Fatalf("live report should replace the old hold: %+v", got)
		}
	})
}

func TestMemoMoveStopsAndRecordsUnobservedReplacement(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setRow(d.Session, map[string]any{"origin": "user", "alive": false, "movedTo": "latest"})
		agent.mu.Lock()
		agent.rows = append(agent.rows, map[string]any{"name": "latest", "kind": "codex", "origin": "queue", "queueReplaces": []string{d.Session},
			"alive": true, "state": "working", "progressAgeSec": 1})
		agent.mu.Unlock()
		rec := e.do("a", "POST", "/api/memo-queues/"+d.QueueID+"/deliveries/"+d.ID+"/end", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("end: %d %s", rec.Code, rec.Body.String())
		}
		e.settleNow(time.Now())
		got := e.delivery(d.ID)
		if got.Outcome != store.DeliveryEnded || got.RetirePending || got.CurrentSession() != "latest" ||
			len(agent.retires) != 1 || agent.retires[0] != "latest" || agent.retireBody(0) != "" {
			t.Fatalf("end did not reach the active owner: %+v, retires=%v", got, agent.retires)
		}
	})
}

func TestMemoMoveStoppedMirrorCannotCountClearReadForNewOwner(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.IdleTicks != 1 {
			t.Fatalf("expected one clear read: %+v", got)
		}
		ctx := context.Background()
		ws, _, _ := e.st.GetWorkspaceByMembership(ctx, e.members["a"])
		rows := sessionRows([]sessionWire{{Name: d.Session, QueueSuccessor: "latest"}, {
			Name: "latest", Origin: "queue", QueueReplaces: []string{d.Session},
		}})
		if err := e.st.ReplaceSessions(ctx, ws.ID, rows); err != nil {
			t.Fatal(err)
		}
		e.setRuntime(agent, "stopped")
		for i := 0; i < 3; i++ {
			e.settleNow(time.Now())
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
			t.Fatalf("the stopped mirror completed a different owner's clear read: %+v", got)
		}
	})
}

func TestMemoMoveKeepsOverdueDeadlineAndRetiresReplacement(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(map[string]any{"maxTaskHours": 1})
		agent.setRow(d.Session, map[string]any{"alive": false, "movedTo": "overdue-owner"})
		agent.mu.Lock()
		agent.rows = append(agent.rows, map[string]any{"name": "overdue-owner", "kind": "codex", "origin": "queue",
			"queueReplaces": []string{d.Session}, "alive": false, "state": "limited"})
		agent.mu.Unlock()
		e.settleNow(time.Now().Add(2 * time.Hour))
		if got := e.delivery(d.ID); got.OverdueAt == "" || got.OpenedAt != d.OpenedAt ||
			len(agent.retires) != 1 || agent.retires[0] != "overdue-owner" {
			t.Fatalf("move reset the deadline or retired the wrong session: %+v, retires=%v", got, agent.retires)
		}
	})
}

func TestMemoMoveContinuationRequiresCurrentProducer(t *testing.T) {
	cases := []struct {
		name, producer, launch string
		admit                  bool
	}{
		{"legacy source", "launch", "", false},
		{"unrelated session", "other", "launch", false},
		{"replacement", "replacement", "launch", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eachDialect(t, func(t *testing.T, e *inboxEnv) {
				q := e.contQueue("impl", 4, nil)
				root := e.contRoot("impl", contRef)
				d := e.contRun(q, "launch", store.DeliveryOpen, root.ID)
				if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
					_, _, err := tx.ObserveDelivery(d, store.DeliveryObservation{Gen: 1, RunSession: "replacement", RunMoveKey: "replacement"})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				body := contMemo("impl", "part two", contRef)
				body["producer"], body["producerOrigin"] = c.producer, "queue"
				body["continuation"], body["queueDelivery"] = true, d
				if c.launch != "" {
					body["queueLaunch"] = c.launch
				}
				rec := e.memoTokenDo("a", "POST", "/internal/memos", body)
				if rec.Code != http.StatusCreated {
					t.Fatalf("continuation: %d %s", rec.Code, rec.Body.String())
				}
				row := e.memoRow(decode[memoDTO](t, rec).ID)
				if c.admit {
					if row.State != store.MemoStatePending || row.ChainID != root.ID || row.Producer != "replacement" {
						t.Fatalf("the observed replacement should continue the original delivery: %+v", row)
					}
				} else if row.State != store.MemoStateHeld || row.ChainID != "" {
					t.Fatalf("unverified producer inherited the release: %+v", row)
				}
			})
		})
	}
}

func TestMemoMoveKeepsMemberAtCapacity(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": store.DefaultMemoRunLimit + 1}, store.DefaultMemoRunLimit+1)
		var live []store.MemoDelivery
		for i := 0; i < store.DefaultMemoRunLimit && len(live) < store.DefaultMemoRunLimit; i++ {
			e.autoTick(time.Now())
			live = e.liveDeliveries(q)
			for _, d := range live {
				agent.setRow(d.Session, map[string]any{"state": "working", "progressAgeSec": 1})
			}
		}
		if len(live) != store.DefaultMemoRunLimit {
			t.Fatalf("expected the member cap, got %d deliveries", len(live))
		}
		agent.setRow(live[0].Session, map[string]any{"alive": false, "state": "", "movedTo": "continued"})
		agent.mu.Lock()
		agent.rows = append(agent.rows, map[string]any{"name": "continued", "kind": "codex", "alive": true, "state": "working", "progressAgeSec": 1})
		agent.mu.Unlock()
		for i := 0; i < 3; i++ {
			e.autoTick(time.Now())
		}
		if n := agent.createCount(); n != store.DefaultMemoRunLimit {
			t.Fatalf("member started %d runs with a busy replacement", n)
		}
		agent.setRow("continued", map[string]any{"state": "idle"})
		e.autoTick(time.Now())
		e.autoTick(time.Now())
		if n := agent.createCount(); n != store.DefaultMemoRunLimit+1 {
			t.Fatalf("member started %d runs after one slot became free", n)
		}
	})
}

func TestMemoMoveListGapDoesNotAbortTheRun(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.dropRow(d.Session)
		ctx := context.Background()
		r := e.runner()
		view := r.view(ctx, map[string]*memoMemberView{}, d.MembershipID)
		if view == nil || !view.liveOK || len(view.live) != 0 {
			t.Fatalf("expected a temporary list gap: %+v", view)
		}
		agent.mu.Lock()
		agent.rows = append(agent.rows, map[string]any{"name": "target", "kind": "codex", "origin": "queue", "queueReplaces": []string{d.Session},
			"alive": true, "state": "working"})
		agent.mu.Unlock()
		r.closeIfDoneReported(ctx, view, d, time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
			t.Fatalf("a list gap aborted the delivery despite the surviving replacement: %+v", got)
		}
	})
}

func TestMemoMoveUnbookedLimitHoldsSlotAndDispatch(t *testing.T) {
	cases := []struct {
		name string
		row  map[string]any
		hold string
	}{
		{"stopped window limit", map[string]any{"alive": false, "stoppedLimit": stateLimited}, store.HeldLimit},
		{"stopped spend limit", map[string]any{"alive": false, "stoppedLimit": stateSpendLimit}, store.HeldPerson},
		{"live spend limit", map[string]any{"alive": true, "state": stateSpendLimit}, store.HeldPerson},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
				agent, d := e.openReported(nil)
				same := e.queueWith("app", "same", map[string]any{"enabled": true, "agentKind": "codex"})
				e.pending("app", "same", "same kind")
				e.queueWith("app", "other", map[string]any{"enabled": true, "agentKind": "claude"})
				e.pending("app", "other", "other kind")
				agent.setRow(d.Session, map[string]any{"alive": false, "movedTo": "replacement"})
				agent.child("replacement", "", map[string]any{"origin": "queue", "kind": "codex", "queueReplaces": []string{d.Session}, "state": ""})
				agent.setRow("replacement", c.row)
				for i := 0; i < 3; i++ {
					e.autoTick(time.Now())
				}
				got := e.delivery(d.ID)
				if got.State != store.DeliveryOpen || got.HeldBy != c.hold || got.HeldSession != "replacement" ||
					got.LimitKinds != ",codex," || len(e.allDeliveries(same)) != 0 || agent.createCount() != 2 {
					t.Fatalf("want the original slot, a same-kind hold and one other-kind launch: %+v, creates=%d", got, agent.createCount())
				}

				agent.setRow("replacement", map[string]any{"alive": false, "state": "", "stoppedLimit": ""})
				e.autoTick(time.Now())
				if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.LimitKinds != "" || len(e.allDeliveries(same)) != 1 {
					t.Fatalf("clear evidence should allow dispatch while the original run settles: %+v", got)
				}
				e.autoTick(time.Now())
				if got := e.delivery(d.ID); got.Outcome != store.DeliveryClean {
					t.Fatalf("clear replacement should finish through the normal close: %+v", got)
				}
			})
		})
	}
}

func TestMemoMoveHandBackCannotCrossReplacement(t *testing.T) {
	for _, closed := range []bool{false, true} {
		name := "before close"
		if closed {
			name = "before recheck"
		}
		t.Run(name, func(t *testing.T) {
			eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
				agent, d, mid := e.openDeferrable(nil)
				rec := e.deferCall(d.ID, map[string]any{"session": d.Session, "reason": "later"})
				if rec.Code != http.StatusOK {
					t.Fatalf("defer: %d %s", rec.Code, rec.Body.String())
				}
				if closed {
					agent.mu.Lock()
					agent.retireFail = 1
					agent.mu.Unlock()
					e.settleTwice()
					if got := e.delivery(d.ID); got.Outcome != store.DeliveryDeferred || !got.RetirePending {
						t.Fatalf("expected deferred close waiting for retire: %+v", got)
					}
				}
				agent.setRow(d.Session, map[string]any{"alive": false, "movedTo": "replacement"})
				agent.child("replacement", "", map[string]any{"origin": "queue", "queueReplaces": []string{d.Session}})
				if !closed {
					e.settleNow(time.Now())
					for _, name := range []string{d.Session, "other"} {
						rec := e.deferCall(d.ID, map[string]any{"session": name, "cancel": true})
						if rec.Code != http.StatusConflict {
							t.Errorf("%s cancelled a request for the continuing run: %d", name, rec.Code)
						}
					}
				}
				e.settleTwice()
				got := e.delivery(d.ID)
				if got.Outcome != store.DeliveryClean || got.DeferResult != store.DeferDroppedReleased ||
					e.memoRow(mid).State != store.MemoStateDelivered {
					t.Fatalf("replacement inherited a stale return request: %+v", got)
				}
			})
		})
	}
}

func TestMemoMoveHandoverNeedsAttentionUntilDeadline(t *testing.T) {
	for _, state := range []string{"failed", "unknown"} {
		t.Run(state, func(t *testing.T) {
			eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
				agent, d := e.openReported(map[string]any{"maxTaskHours": 1})
				agent.setRow(d.Session, map[string]any{"alive": false, "movedTo": "replacement"})
				agent.child("replacement", "", map[string]any{"origin": "queue", "queueReplaces": []string{d.Session},
					"alive": false, "state": "", "initialPromptState": state})
				e.settleTwice()
				if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.HeldBy != store.HeldPerson {
					t.Fatalf("handover should wait for the member: %+v", got)
				}
				later := time.Now().Add(2 * time.Hour)
				e.settleNow(later)
				e.settleNow(later)
				if got := e.delivery(d.ID); got.Outcome != store.DeliveryClean {
					t.Fatalf("handover held a slot past the delivery deadline: %+v", got)
				}
			})
		})
	}
}

func TestMemoMoveCurrentOwnerCanRequestHandBack(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d, mid := e.openDeferrable(nil)
		agent.setRow(d.Session, map[string]any{"alive": false, "movedTo": "replacement"})
		agent.child("replacement", "", map[string]any{"origin": "queue", "queueReplaces": []string{d.Session}, "state": "working"})
		e.settleNow(time.Now())
		rec := e.deferCall(d.ID, map[string]any{"session": "replacement", "reason": "later"})
		if rec.Code != http.StatusOK {
			t.Fatalf("current producer: %d %s", rec.Code, rec.Body.String())
		}
		agent.setRow("replacement", map[string]any{"state": "idle"})
		e.settleTwice()
		got := e.delivery(d.ID)
		if got.Outcome != store.DeliveryDeferred || got.DeferResult != store.DeferApplied ||
			e.memoRow(mid).State != store.MemoStatePending {
			t.Fatalf("the current owner's request did not settle: %+v", got)
		}
	})
}

func TestMemoMoveHandBackIsTiedToChildOwnership(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d, mid := e.openDeferrable(nil)
		agent.child("review", d.Session, nil)
		rec := e.deferCall(d.ID, map[string]any{"session": d.Session, "reason": "later"})
		if rec.Code != http.StatusOK {
			t.Fatalf("defer: %d %s", rec.Code, rec.Body.String())
		}
		agent.setRow("review", map[string]any{"alive": false, "movedTo": "review-next"})
		agent.child("review-next", d.Session, map[string]any{"queueReplaces": []string{"review"}})
		e.settleTwice()
		e.settleNow(time.Now())
		got := e.delivery(d.ID)
		if got.Outcome != store.DeliveryClean || got.DeferResult != store.DeferDroppedReleased ||
			e.memoRow(mid).State != store.MemoStateDelivered {
			t.Fatalf("a changed child inherited a stale return request: %+v", got)
		}
	})
}

func TestMemoMoveMirrorLimitVetoesAStaleClearReport(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setRow(d.Session, map[string]any{"alive": false, "movedTo": "replacement"})
		agent.child("replacement", "", map[string]any{"origin": "queue", "queueReplaces": []string{d.Session}, "alive": false})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.HeldBy != "" || got.IdleTicks != 1 || got.CurrentSession() != "replacement" {
			t.Fatalf("expected a clear observation of the replacement: %+v", got)
		}
		ctx := context.Background()
		ws, _, _ := e.st.GetWorkspaceByMembership(ctx, e.members["a"])
		rows := sessionRows([]sessionWire{{Name: "replacement", Origin: "queue", QueueReplaces: []string{d.Session}, StoppedLimit: stateLimited}})
		if err := e.st.ReplaceSessions(ctx, ws.ID, rows); err != nil {
			t.Fatal(err)
		}
		e.setRuntime(agent, "stopped")
		e.settleTwice()
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
			t.Fatalf("a stale clear report freed the limited replacement's slot: %+v", got)
		}
	})
}
