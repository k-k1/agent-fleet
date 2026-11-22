package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// doTokenJSON is doJSON on the schedule-token face (what a session process uses).
func doTokenJSON(h func(http.ResponseWriter, *http.Request, store.MembershipView), mv store.MembershipView, method, body, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/", strings.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), scheduleTokenFaceKey{}, true))
	if id != "" {
		r.SetPathValue("id", id)
	}
	rec := httptest.NewRecorder()
	h(rec, r, mv)
	return rec
}

func dueIDs(t *testing.T, api scheduleAPI, ctx context.Context) []string {
	t.Helper()
	rows, err := api.store.ListDueSchedules(ctx, time.Now().UTC().Add(400*24*time.Hour).Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range rows {
		ids = append(ids, s.ID)
	}
	return ids
}

const approvalBody = `{"spec_kind":"interval","spec":"3600","prompt":"p"}`

func TestScheduleTokenCreateIsHeldUntilApproved(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	api.holdTokenWrites = true

	rec := doTokenJSON(api.create, mv, "POST", approvalBody, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	var d scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if !d.ApprovalPending || d.Warning != scheduleHeldNotice {
		t.Fatalf("not reported as held: %+v", d)
	}
	if got := dueIDs(t, api, ctx); len(got) != 0 {
		t.Fatalf("held schedule is due: %v", got)
	}
	// run-now is refused while held.
	if rr := doTokenJSON(api.runNow, mv, "POST", "", d.ID); rr.Code != http.StatusConflict {
		t.Fatalf("run-now on held: %d", rr.Code)
	}
	// The member approves on the Console face (no token mark).
	if ar := doJSON(api.approve, mv, "POST", "", d.ID); ar.Code != http.StatusOK {
		t.Fatalf("approve %d %s", ar.Code, ar.Body.String())
	}
	if got := dueIDs(t, api, ctx); len(got) != 1 || got[0] != d.ID {
		t.Fatalf("approved schedule not due: %v", got)
	}
	// A second approval changes nothing.
	if ar := doJSON(api.approve, mv, "POST", "", d.ID); ar.Code != http.StatusConflict {
		t.Fatalf("repeat approve %d", ar.Code)
	}
}

func TestScheduleTokenEditPutsApprovedBackOnHold(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	api.holdTokenWrites = true
	var d scheduleDTO
	_ = json.Unmarshal(doTokenJSON(api.create, mv, "POST", approvalBody, "").Body.Bytes(), &d)
	doJSON(api.approve, mv, "POST", "", d.ID)

	// A label edit does not change what runs.
	if rr := doTokenJSON(api.update, mv, "PATCH", `{"spec_label":"x"}`, d.ID); rr.Code != 200 || strings.Contains(rr.Body.String(), "approval_pending") {
		t.Fatalf("label edit held it: %d %s", rr.Code, rr.Body.String())
	}
	// The member's own edit of the prompt does not hold either.
	if rr := doJSON(api.update, mv, "PATCH", `{"prompt":"member edit"}`, d.ID); rr.Code != 200 || strings.Contains(rr.Body.String(), "approval_pending") {
		t.Fatalf("member edit held it: %d %s", rr.Code, rr.Body.String())
	}
	if len(dueIDs(t, api, ctx)) != 1 {
		t.Fatal("schedule should still be due")
	}
	for _, patch := range []string{`{"prompt":"new"}`, `{"spec":"7200"}`, `{"model":"m"}`} {
		api2, _, _ := newSchedAPITest(t)
		api2.holdTokenWrites = true
		var e scheduleDTO
		_ = json.Unmarshal(doTokenJSON(api2.create, mv, "POST", approvalBody, "").Body.Bytes(), &e)
		doJSON(api2.approve, mv, "POST", "", e.ID)
		rr := doTokenJSON(api2.update, mv, "PATCH", patch, e.ID)
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"approval_pending":true`) {
			t.Fatalf("%s did not hold: %d %s", patch, rr.Code, rr.Body.String())
		}
	}
}

func TestScheduleTokenWritesAreNotHeldByDefault(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	rec := doTokenJSON(api.create, mv, "POST", approvalBody, "")
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "approval_pending") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(dueIDs(t, api, ctx)) != 1 {
		t.Fatal("default must stay today's behaviour")
	}
}
