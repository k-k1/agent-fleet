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
			queue, member, err := tx.CountLiveDeliveries(q)
			if err != nil {
				return err
			}
			launches, err := tx.CountDeliveriesCreatedSince(q, store.DeliveryTS(time.Now().Add(-24*time.Hour)))
			if err == nil && (queue != 1 || member != 1 || launches != 1) {
				t.Errorf("move charged %d queue slots, %d member slots and %d launches", queue, member, launches)
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

func TestMemoMoveContinuationClaimsOriginalDelivery(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("impl", 4, nil)
		root := e.contRoot("impl", contRef)
		d := e.contRun(q, "launch", store.DeliveryOpen, root.ID)
		body := contMemo("impl", "part two", contRef)
		body["producer"], body["producerOrigin"] = "replacement", "queue"
		body["continuation"], body["queueDelivery"], body["queueLaunch"] = true, d, "launch"
		rec := e.memoTokenDo("a", "POST", "/internal/memos", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("continuation: %d %s", rec.Code, rec.Body.String())
		}
		row := e.memoRow(decode[memoDTO](t, rec).ID)
		if row.State != store.MemoStatePending || row.ChainID != root.ID || row.Producer != "replacement" {
			t.Fatalf("the launch and the current producer should serve different purposes: %+v", row)
		}
	})
}

func TestMemoMoveKeepsMemberAtCapacity(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": memoQueueMemberLimit + 1}, memoQueueMemberLimit+1)
		var live []store.MemoDelivery
		for i := 0; i < memoQueueMemberLimit && len(live) < memoQueueMemberLimit; i++ {
			e.autoTick(time.Now())
			live = e.liveDeliveries(q)
			for _, d := range live {
				agent.setRow(d.Session, map[string]any{"state": "working", "progressAgeSec": 1})
			}
		}
		if len(live) != memoQueueMemberLimit {
			t.Fatalf("expected the member cap, got %d deliveries", len(live))
		}
		agent.setRow(live[0].Session, map[string]any{"alive": false, "state": "", "movedTo": "continued"})
		agent.mu.Lock()
		agent.rows = append(agent.rows, map[string]any{"name": "continued", "kind": "codex", "alive": true, "state": "working", "progressAgeSec": 1})
		agent.mu.Unlock()
		for i := 0; i < 3; i++ {
			e.autoTick(time.Now())
		}
		if n := agent.createCount(); n != memoQueueMemberLimit {
			t.Fatalf("member started %d runs with a busy replacement", n)
		}
		agent.setRow("continued", map[string]any{"state": "idle"})
		e.autoTick(time.Now())
		e.autoTick(time.Now())
		if n := agent.createCount(); n != memoQueueMemberLimit+1 {
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
