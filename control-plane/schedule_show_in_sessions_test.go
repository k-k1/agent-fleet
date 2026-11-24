package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// #5070: show_in_sessions defaults to false, is stored, read back, patched, and an unrelated
// patch leaves it alone. Every check reads the stored row, not just the response.
func TestScheduleShowInSessionsRoundTrip(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	api := newScheduleAPI(&manager{store: st})
	mv := store.MembershipView{MembershipID: "m1", TenantID: "default"}
	stored := func(id string) bool {
		t.Helper()
		got, ok, err := st.GetSchedule(ctx, id)
		if err != nil || !ok {
			t.Fatalf("read back %s: ok=%v err=%v", id, ok, err)
		}
		return got.ShowInSessions
	}

	var dto scheduleDTO
	rec := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x"}`, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != http.StatusCreated || dto.ShowInSessions || stored(dto.ID) {
		t.Fatalf("default: code=%d dto=%v stored=%v", rec.Code, dto.ShowInSessions, stored(dto.ID))
	}
	// false -> true through PATCH reaches the stored row.
	rec = doJSON(api.update, mv, "PATCH", `{"show_in_sessions":true}`, dto.ID)
	if rec.Code != http.StatusOK || !stored(dto.ID) {
		t.Fatalf("patch on: code=%d stored=%v", rec.Code, stored(dto.ID))
	}

	rec = doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x","show_in_sessions":true}`, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != http.StatusCreated || !dto.ShowInSessions || !stored(dto.ID) {
		t.Fatalf("create on: code=%d dto=%v stored=%v", rec.Code, dto.ShowInSessions, stored(dto.ID))
	}
	rec = doJSON(api.update, mv, "PATCH", `{"spec_label":"morning"}`, dto.ID)
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != http.StatusOK || !dto.ShowInSessions || !stored(dto.ID) {
		t.Fatalf("unrelated patch: code=%d dto=%v stored=%v", rec.Code, dto.ShowInSessions, stored(dto.ID))
	}
	rec = doJSON(api.update, mv, "PATCH", `{"show_in_sessions":false}`, dto.ID)
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != http.StatusOK || dto.ShowInSessions || stored(dto.ID) {
		t.Fatalf("patch off: code=%d dto=%v stored=%v", rec.Code, dto.ShowInSessions, stored(dto.ID))
	}
}

// #5070: a display-only change on an approved schedule through the token face does not put it
// back to approval pending, and next_run is kept. Together with a run-shape change the flag
// survives both the hold and the approval.
func TestScheduleShowInSessionsOnTokenFace(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	api.holdTokenWrites = true

	var d scheduleDTO
	_ = json.Unmarshal(doTokenJSON(api.create, mv, "POST", approvalBody, "").Body.Bytes(), &d)
	if ar := approveCurrent(api, mv, d.ID); ar.Code != http.StatusOK {
		t.Fatalf("approve %d %s", ar.Code, ar.Body.String())
	}
	// A fixed far-future next_run: a recomputed one would be now+3600s, whatever second it runs in.
	if err := api.store.SetScheduleEnabled(ctx, d.ID, mv.MembershipID, true, "2099-01-01T00:00:00Z", store.NowTS()); err != nil {
		t.Fatal(err)
	}
	before, _, _ := api.store.GetSchedule(ctx, d.ID)

	rr := doTokenJSON(api.update, mv, "PATCH", `{"show_in_sessions":true}`, d.ID)
	after, ok, err := api.store.GetSchedule(ctx, d.ID)
	if rr.Code != http.StatusOK || !ok || err != nil || !after.ShowInSessions || after.ApprovalPending || after.NextRun != before.NextRun || after.NextRun != "2099-01-01T00:00:00Z" {
		t.Fatalf("display-only patch: code=%d %s stored=%+v err=%v", rr.Code, rr.Body.String(), after, err)
	}

	rr = doTokenJSON(api.update, mv, "PATCH", `{"prompt":"changed","show_in_sessions":false}`, d.ID)
	held, _, _ := api.store.GetSchedule(ctx, d.ID)
	if rr.Code != http.StatusOK || !held.ApprovalPending || held.ShowInSessions || held.Prompt != "changed" {
		t.Fatalf("held patch: code=%d %s stored=%+v", rr.Code, rr.Body.String(), held)
	}
	if ar := approveCurrent(api, mv, d.ID); ar.Code != http.StatusOK {
		t.Fatalf("approve %d %s", ar.Code, ar.Body.String())
	}
	done, _, _ := api.store.GetSchedule(ctx, d.ID)
	if done.ApprovalPending || done.ShowInSessions {
		t.Fatalf("after approval: %+v", done)
	}
	rr = doTokenJSON(api.update, mv, "PATCH", `{"prompt":"again","show_in_sessions":true}`, d.ID)
	held, _, _ = api.store.GetSchedule(ctx, d.ID)
	if rr.Code != http.StatusOK || !held.ApprovalPending || !held.ShowInSessions {
		t.Fatalf("held patch on: code=%d stored=%+v", rr.Code, held)
	}
	_ = approveCurrent(api, mv, d.ID)
	done, _, _ = api.store.GetSchedule(ctx, d.ID)
	if done.ApprovalPending || !done.ShowInSessions {
		t.Fatalf("after second approval: %+v", done)
	}
}
