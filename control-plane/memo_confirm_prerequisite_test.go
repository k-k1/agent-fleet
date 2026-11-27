package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/memoafter"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestMemoConfirmPrerequisite(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		ctx := context.Background()
		now := time.Now()
		_, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 4, "batch": 2}, 0)
		pre := e.pending("app", "impl", "prerequisite")
		sibling := e.pending("app", "impl", "sibling in the same run")
		dependent := func(label, prerequisite string) memoDTO {
			t.Helper()
			rec := e.do("a", "POST", "/api/memos", afterMemo("app", "impl", label, prerequisite))
			if rec.Code != http.StatusCreated {
				t.Fatalf("create %s: %d %s", label, rec.Code, rec.Body.String())
			}
			return decode[memoDTO](t, rec)
		}
		held := dependent("held dependent", pre.ID)
		run := e.fired(q)
		if !sameIDs(run.MessageIDs, pre.ID, sibling.ID) {
			t.Fatalf("prerequisite run: %v", run.MessageIDs)
		}
		path := "/api/memos/" + held.ID + "/confirm-prerequisite"
		in := memoConfirmPrerequisiteIn{PrerequisiteID: pre.ID, DeliveryID: run.ID}
		press := func(who, path string, in memoConfirmPrerequisiteIn, want int, fetchSite ...string) {
			t.Helper()
			body, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", path, bytes.NewReader(body))
			req.Header.Set(inboxTestEmailHeader, who+"@example.com")
			req.Header.Set("Content-Type", "application/json")
			if len(fetchSite) != 0 {
				req.Header.Set("Sec-Fetch-Site", fetchSite[0])
			}
			r := httptest.NewRecorder()
			e.mux.ServeHTTP(r, req)
			if r.Code != want {
				t.Fatalf("confirm: %d %s, want %d", r.Code, r.Body.String(), want)
			}
		}
		press("a", path, in, http.StatusConflict) // claimed
		e.closeDeliveryAs(run.ID, store.DeliveryDeferred, "turn-failed", 5_000, now)
		press("a", path, in, http.StatusConflict) // unresolved hand-back
		e.closeNoPR(run.ID, "turn-failed", 5_000, now, store.AnswerOther)
		original := e.delivery(run.ID)
		runner := e.runner()
		runner.checkAfter(ctx, e.queueRow(q), now)
		pending := dependent("pending dependent", pre.ID)
		blocked := dependent("sibling dependent", sibling.ID)
		e.notReady(q, held.ID, memoNotReadyHeld)
		if sqlStats, goStats := e.statsOf(q); sqlStats.Eligible != 0 || goStats.Eligible != 0 {
			t.Fatalf("eligible before confirmation: SQL %+v, Go %+v", sqlStats, goStats)
		}
		if items := e.listed(held.ID).AfterStatus; len(items) != 1 || items[0].ConfirmDeliveryID != run.ID || items[0].Reason != memoafter.WhyBadFirstTurn {
			t.Fatalf("prerequisite before confirmation: %+v", items)
		}
		press("c", path, in, http.StatusNotFound)
		press("a", path, in, http.StatusForbidden, "same-site")
		press("a", "/api/memos/"+blocked.ID+"/confirm-prerequisite", in, http.StatusConflict)
		press("a", path, memoConfirmPrerequisiteIn{PrerequisiteID: pre.ID, DeliveryID: "stale"}, http.StatusConflict)
		if r := e.memoTokenDo("a", "POST", "/internal/memos/"+held.ID+"/confirm-prerequisite", in); r.Code != http.StatusNotFound {
			t.Fatalf("token mutation: %d %s", r.Code, r.Body.String())
		}
		press("a", path, in, http.StatusNoContent)
		press("a", path, in, http.StatusNoContent)
		for _, m := range []memoDTO{held, pending} {
			got := e.listed(m.ID)
			if !sameIDs(got.After, pre.ID) || len(got.AfterStatus) != 1 || got.AfterStatus[0].State != memoafter.Satisfied ||
				got.AfterStatus[0].Reason != memoafter.WhyConfirmed || got.AfterStatus[0].ConfirmDeliveryID != "" {
				t.Fatalf("after confirmation: %+v", got)
			}
		}
		if got := e.delivery(run.ID); !reflect.DeepEqual(got, original) {
			t.Fatalf("delivery history changed: got %+v, want %+v", got, original)
		}
		e.notReady(q, blocked.ID, memoNotReadyPrerequisites)
		e.notReady(q, held.ID, memoNotReadyHeld)
		sqlStats, goStats := e.statsOf(q)
		if sqlStats.Eligible != 1 || goStats.Eligible != 1 {
			t.Fatalf("eligible: SQL %+v, Go %+v", sqlStats, goStats)
		}
		if ds, _ := runner.dispatch(ctx, now, 1); len(ds) != 1 || !sameIDs(ds[0].MessageIDs, pending.ID) {
			t.Fatalf("auto-run after confirmation: %+v", ds)
		}
		if rec := e.release(held.ID, memoContentHash(e.memoRow(held.ID))); rec.Code != http.StatusOK {
			t.Fatalf("reapprove: %d %s", rec.Code, rec.Body.String())
		}
		if rec := e.fireMemo(q, held.ID); rec.Code != http.StatusAccepted || !sameIDs(decode[memoDeliveryDTO](t, rec).MessageIDs, held.ID) {
			t.Fatalf("run now: %d %s", rec.Code, rec.Body.String())
		}

		// A fresh run needs its own evidence, even on the same prerequisite memo.
		future := dependent("later dependent", pre.ID)
		if rec := e.do("a", "POST", "/api/memo-queues/"+q+"/deliveries/"+run.ID+"/return", nil); rec.Code != http.StatusOK {
			t.Fatalf("return: %d %s", rec.Code, rec.Body.String())
		}
		replay := decode[memoDeliveryDTO](t, e.fireMemo(q, pre.ID))
		if !sameIDs(replay.MessageIDs, pre.ID) {
			t.Fatalf("fresh run: %+v", replay)
		}
		e.closeNoPR(replay.ID, "turn-failed", 5_000, now, store.AnswerOther)
		press("a", "/api/memos/"+future.ID+"/confirm-prerequisite", in, http.StatusConflict)
		if items := e.listed(future.ID).AfterStatus; len(items) != 1 || items[0].State != memoafter.Failed || items[0].ConfirmDeliveryID != replay.ID {
			t.Fatalf("fresh run inherited confirmation: %+v", items)
		}
		if sqlStats, goStats := e.statsOf(q); sqlStats.Eligible != 1 || goStats.Eligible != 1 {
			t.Fatalf("only the returned sibling should be eligible: SQL %+v, Go %+v", sqlStats, goStats)
		}
	})
}
