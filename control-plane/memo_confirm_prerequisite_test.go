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

func (e *deliveryEnv) confirmPrerequisiteRequest(who, path string, in memoConfirmPrerequisiteIn, fetchSite string) *httptest.ResponseRecorder {
	e.t.Helper()
	body, err := json.Marshal(in)
	if err != nil {
		e.t.Fatal(err)
	}
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set(inboxTestEmailHeader, who+"@example.com")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", fetchSite)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

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
			site := "same-origin"
			if len(fetchSite) != 0 {
				site = fetchSite[0]
			}
			if r := e.confirmPrerequisiteRequest(who, path, in, site); r.Code != want {
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

func TestMemoConfirmPrerequisiteDeliveryOutcomes(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.useQueueAgent("")
		now := time.Now()
		for _, tc := range []struct {
			name, outcome string
			overdue       bool
			want          int
		}{
			{name: "wake", outcome: store.DeliveryWake, want: http.StatusConflict},
			{name: "aborted", outcome: store.DeliveryAborted, want: http.StatusNoContent},
			{name: "unknown", outcome: store.DeliveryUnknown, want: http.StatusNoContent},
			{name: "overdue", outcome: store.DeliveryClean, overdue: true, want: http.StatusNoContent},
		} {
			q := e.queueWith("app", tc.name, nil)
			pre := e.pending("app", tc.name, "prerequisite")
			dep := decode[memoDTO](t, e.do("a", "POST", "/api/memos", afterMemo("app", tc.name, "dependent", pre.ID)))
			run := e.fired(q)
			e.closeDeliveryAs(run.ID, tc.outcome, "clean", 90_000, now)
			if tc.overdue {
				e.setPRs(run.ID, store.PRStateMerged)
				if _, err := e.st.DB().ExecContext(context.Background(),
					`UPDATE memo_delivery SET overdue_at=?, retire_gen=3 WHERE id=?`, store.DeliveryTS(now), run.ID); err != nil {
					t.Fatal(err)
				}
			}
			if sqlSide, goSide := e.statsOf(q); sqlSide.Eligible != 0 || goSide.Eligible != 0 {
				t.Fatalf("%s should require confirmation: SQL %+v, Go %+v", tc.name, sqlSide, goSide)
			}
			in := memoConfirmPrerequisiteIn{PrerequisiteID: pre.ID, DeliveryID: run.ID}
			path := "/api/memos/" + dep.ID + "/confirm-prerequisite"
			if r := e.confirmPrerequisiteRequest("a", path, in, "same-origin"); r.Code != tc.want {
				t.Fatalf("%s confirmation: %d %s, want %d", tc.name, r.Code, r.Body.String(), tc.want)
			}
			wantEligible := 1
			if tc.outcome == store.DeliveryWake {
				wantEligible = 0
				// A wake cannot deliver a memo. Even a stale marker on such a row cannot satisfy it.
				if _, err := e.st.DB().ExecContext(context.Background(),
					`UPDATE memo SET confirmed_delivery_id=? WHERE id=?`, run.ID, pre.ID); err != nil {
					t.Fatal(err)
				}
			}
			if sqlSide, goSide := e.statsOf(q); sqlSide.Eligible != wantEligible || goSide.Eligible != wantEligible {
				t.Fatalf("%s after confirmation, want %d eligible: SQL %+v, Go %+v", tc.name, wantEligible, sqlSide, goSide)
			}
		}
	})
}
