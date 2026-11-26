package sessionx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorelay"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func queueMoveRows(t *testing.T) map[string]session.Session {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleListSessions(rec, httptest.NewRequest(http.MethodGet, "/sessions", nil))
	var response struct {
		Sessions []session.Session
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	rows := map[string]session.Session{}
	for _, row := range response.Sessions {
		rows[row.Name] = row
	}
	return rows
}

func TestQueueMoveContinueRootAfterUncertainCreate(t *testing.T) {
	r := moveRunEnv(t, "queue-origin")
	src := r.src
	src.Origin, src.QueueDelivery, src.QueueCategoryID = session.OriginQueue, "d1", "q1"
	writeMeta(t, src)
	r.create = func(req CreateReq) (int, []byte) {
		createFakeTarget(req)
		return http.StatusGatewayTimeout, nil
	}
	move := decodeMove(t, postMove(t, src.Name, "", `{"kind":"claude"}`))
	middle := move.Error.Target
	if move.Error.Code != "move_create_uncertain" || middle == "" {
		t.Fatalf("expected uncertain create: %+v", move)
	}
	rows := queueMoveRows(t)
	if rows[src.Name].QueueDelivery != "d1" || rows[src.Name].Moving != middle || rows[middle].QueueDelivery != "" ||
		memorelay.QueueDeliveryOf(middle) != "" {
		t.Fatalf("a provisional target acquired the delivery: %+v", rows)
	}
	rec := postMove(t, src.Name, "/retry", "")
	if rec.Code != http.StatusOK || len(r.creates) != 1 {
		t.Fatalf("retry %d with %d creates: %s", rec.Code, len(r.creates), rec.Body.String())
	}
	r.create = nil
	target := mustMove(t, middle, session.KindCodex).Target.Name
	t.Cleanup(func() { lifegate.Reopen(middle) })
	if rec := postMove(t, src.Name, "/reopen", ""); rec.Code != http.StatusOK {
		t.Fatalf("reopen: %d %s", rec.Code, rec.Body.String())
	}
	session.RemoveMeta(middle)
	rows = queueMoveRows(t)
	owner := rows[target]
	if owner.Origin != session.OriginQueue || owner.QueueDelivery != "d1" || owner.QueueCategoryID != "q1" ||
		!reflect.DeepEqual(owner.QueueReplaces, []string{src.Name, middle}) ||
		rows[src.Name].QueueDelivery != "" || memorelay.QueueDeliveryOf(src.Name) != "" || memorelay.QueueDeliveryOf(target) != "d1" {
		t.Fatalf("want one owner after the second move: source=%+v target=%+v", rows[src.Name], owner)
	}
}

func TestQueueMoveCancelLeavesOwnerAndRecoveryKeepsCommittedEdge(t *testing.T) {
	r := moveRunEnv(t, "queue-recover")
	src := r.src
	src.Origin, src.QueueDelivery = session.OriginQueue, "d2"
	writeMeta(t, src)
	r.create = func(CreateReq) (int, []byte) { return 0, nil }
	move := decodeMove(t, postMove(t, src.Name, "", `{"kind":"claude"}`))
	if move.Error.Code != "move_create_uncertain" {
		t.Fatalf("expected uncertain create: %+v", move)
	}
	if rec := postMove(t, src.Name, "/cancel", ""); rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	if row := queueMoveRows(t)[src.Name]; row.Moving != "" || row.QueueDelivery != "d2" || memorelay.QueueDeliveryOf(src.Name) != "d2" {
		t.Fatalf("cancel lost the original owner: %+v", row)
	}

	committed := commitOnly(t, r, src.Name)
	target := committed.Target
	if row := queueMoveRows(t)[target]; row.QueueDelivery != "d2" {
		t.Fatalf("the commit was invisible until cleanup: %+v", row)
	}
	lifegate.ResetForTest()
	if n := RecoverMoves(); n != 1 {
		t.Fatalf("recovered %d moves", n)
	}
	session.RemoveMeta(src.Name)
	if row := queueMoveRows(t)[target]; row.QueueDelivery != "d2" || memorelay.QueueDeliveryOf(target) != "d2" {
		t.Fatalf("recovery did not make ownership durable: %+v", row)
	}
}

func TestQueueMoveRetireUsesContinuingTree(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "launch", Origin: session.OriginQueue, QueueDelivery: "d3", MovedTo: "replacement", Archived: true})
	writeMeta(t, session.Meta{Name: "replacement"})
	writeMeta(t, session.Meta{Name: "review-old", Origin: session.OriginSession, OriginSession: "launch", MovedTo: "review"})
	writeMeta(t, session.Meta{Name: "review"})
	writeMeta(t, session.Meta{Name: "grandchild", Origin: session.OriginSession, OriginSession: "review-old"})
	writeMeta(t, session.Meta{Name: "other", Origin: session.OriginUser, OriginSession: "replacement"})
	for _, name := range []string{"replacement", "review", "grandchild", "other"} {
		r.hold(t, name)
	}
	rows := queueMoveRows(t)
	if rows["review"].Origin != session.OriginSession || rows["review"].OriginSession != "replacement" ||
		rows["grandchild"].OriginSession != "review" || memorelay.QueueDeliveryOf("review") != "" {
		t.Fatalf("moved child lost its parent or inherited the root's authority: %+v", rows)
	}
	if status, out := postRetire(t, "replacement"); status != http.StatusOK || !out.Complete ||
		retiredNames(out.Sessions) != "replacement,review,grandchild" {
		t.Fatalf("retire status %d, reports %+v", status, out)
	}
	if mustMeta(t, "review-old").RetiredAt != "" || mustMeta(t, "other").RetiredAt != "" {
		t.Fatal("retired the replaced child or an unrelated session")
	}
}

func TestQueueMoveReopenDoesNotRestoreDeliveryAfterTargetIsGone(t *testing.T) {
	r := moveRunEnv(t, "queue-reopen")
	src := r.src
	src.Origin, src.QueueDelivery = session.OriginQueue, "d4"
	writeMeta(t, src)
	target := mustMove(t, src.Name, session.KindClaude).Target.Name
	session.RemoveMeta(target)
	if rec := postMove(t, src.Name, "/reopen", ""); rec.Code != http.StatusOK {
		t.Fatalf("reopen: %d %s", rec.Code, rec.Body.String())
	}
	row := queueMoveRows(t)[src.Name]
	if row.MovedTo != "" || row.QueueSuccessor != target || memorelay.QueueDeliveryOf(src.Name) != "" {
		t.Fatalf("the reopened source regained the delivery: %+v", row)
	}
}

func TestQueueMoveRetriesAnUnwrittenContinuation(t *testing.T) {
	r := moveRunEnv(t, "queue-write")
	src := r.src
	src.Origin, src.QueueDelivery = session.OriginQueue, "d5"
	writeMeta(t, src)
	blockWrite := true
	writeMoveTargetMeta = func(m session.Meta) error {
		if blockWrite {
			return errors.New("disk full")
		}
		return session.WriteMetaChecked(m)
	}
	target := mustMove(t, src.Name, session.KindClaude).Target.Name
	if _, ok := moveRecords.Read(src.Name); !ok || memorelay.QueueDeliveryOf(target) != "d5" {
		t.Fatal("a committed move lost ownership or its recovery witness when the target write failed")
	}
	blockWrite = false
	if rec := postMove(t, src.Name, "/retry", ""); rec.Code != http.StatusOK || len(r.creates) != 1 {
		t.Fatalf("retry: %d %s, creates=%d", rec.Code, rec.Body.String(), len(r.creates))
	}
	session.RemoveMeta(src.Name)
	if memorelay.QueueDeliveryOf(target) != "d5" {
		t.Fatal("the retry did not save the queue lineage on the target")
	}
}
