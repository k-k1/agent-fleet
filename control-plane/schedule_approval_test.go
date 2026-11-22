package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// approveCurrent is the Console's approve: it sends the generation of the row it just read.
func approveCurrent(api scheduleAPI, mv store.MembershipView, id string) *httptest.ResponseRecorder {
	sch, _, _ := api.store.GetSchedule(context.Background(), id)
	return doJSON(api.approve, mv, "POST", fmt.Sprintf(`{"generation":%d}`, sch.Generation), id)
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
	if ar := approveCurrent(api, mv, d.ID); ar.Code != http.StatusOK {
		t.Fatalf("approve %d %s", ar.Code, ar.Body.String())
	}
	if got := dueIDs(t, api, ctx); len(got) != 1 || got[0] != d.ID {
		t.Fatalf("approved schedule not due: %v", got)
	}
	// A second approval changes nothing.
	if ar := approveCurrent(api, mv, d.ID); ar.Code != http.StatusConflict {
		t.Fatalf("repeat approve %d", ar.Code)
	}
}

func TestScheduleTokenEditPutsApprovedBackOnHold(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	api.holdTokenWrites = true
	var d scheduleDTO
	_ = json.Unmarshal(doTokenJSON(api.create, mv, "POST", approvalBody, "").Body.Bytes(), &d)
	approveCurrent(api, mv, d.ID)

	// An edit that changes nothing does not hold.
	if rr := doTokenJSON(api.update, mv, "PATCH", `{"prompt":"p"}`, d.ID); rr.Code != 200 || strings.Contains(rr.Body.String(), "approval_pending") {
		t.Fatalf("label edit held it: %d %s", rr.Code, rr.Body.String())
	}
	// The member's own edit of the prompt does not hold either.
	if rr := doJSON(api.update, mv, "PATCH", `{"prompt":"member edit"}`, d.ID); rr.Code != 200 || strings.Contains(rr.Body.String(), "approval_pending") {
		t.Fatalf("member edit held it: %d %s", rr.Code, rr.Body.String())
	}
	if len(dueIDs(t, api, ctx)) != 1 {
		t.Fatal("schedule should still be due")
	}
	for _, patch := range []string{`{"prompt":"new"}`, `{"spec":"7200"}`, `{"model":"m"}`, `{"spec_label":"x"}`,
		`{"silent":true}`, `{"wake_policy":"skip"}`, `{"overlap_policy":"queue"}`, `{"deliver_to":["notifications"]}`} {
		api2, ctx2, _ := newSchedAPITest(t)
		api2.holdTokenWrites = true
		var e scheduleDTO
		_ = json.Unmarshal(doTokenJSON(api2.create, mv, "POST", approvalBody, "").Body.Bytes(), &e)
		approveCurrent(api2, mv, e.ID)
		rr := doTokenJSON(api2.update, mv, "PATCH", patch, e.ID)
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"approval_pending":true`) {
			t.Fatalf("%s did not hold: %d %s", patch, rr.Code, rr.Body.String())
		}
		if got := dueIDs(t, api2, ctx2); len(got) != 0 {
			t.Fatalf("%s: held row is still due: %v", patch, got)
		}
		if ar := approveCurrent(api2, mv, e.ID); ar.Code != 200 {
			t.Fatalf("%s: re-approve %d %s", patch, ar.Code, ar.Body.String())
		}
		if got := dueIDs(t, api2, ctx2); len(got) != 1 {
			t.Fatalf("%s: approved row not due: %v", patch, got)
		}
	}
}

func TestScheduleTokenWritesCanBeLeftUnheld(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	api.holdTokenWrites = false
	rec := doTokenJSON(api.create, mv, "POST", approvalBody, "")
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "approval_pending") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(dueIDs(t, api, ctx)) != 1 {
		t.Fatal("default must stay today's behaviour")
	}
}

func TestScheduleHoldClearsPendingRunNow(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	api.holdTokenWrites = true
	var d scheduleDTO
	_ = json.Unmarshal(doTokenJSON(api.create, mv, "POST", approvalBody, "").Body.Bytes(), &d)
	approveCurrent(api, mv, d.ID)
	if rr := doJSON(api.runNow, mv, "POST", "", d.ID); rr.Code != 200 {
		t.Fatalf("run-now %d", rr.Code)
	}
	doTokenJSON(api.update, mv, "PATCH", `{"prompt":"changed"}`, d.ID)
	sch, _, _ := api.store.GetSchedule(ctx, d.ID)
	if !sch.ApprovalPending || sch.ManualFirePending {
		t.Fatalf("pending=%v manual=%v", sch.ApprovalPending, sch.ManualFirePending)
	}
}

func TestScheduleTokenSameTimingPatchKeepsNextRunAndApproval(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	api.holdTokenWrites = true
	var d scheduleDTO
	_ = json.Unmarshal(doTokenJSON(api.create, mv, "POST", approvalBody, "").Body.Bytes(), &d)
	approveCurrent(api, mv, d.ID)
	// A fixed far-future next_run: a recomputed one would be now+3600s, whatever second it runs in.
	if err := api.store.SetScheduleEnabled(ctx, d.ID, mv.MembershipID, true, "2099-01-01T00:00:00Z", store.NowTS()); err != nil {
		t.Fatal(err)
	}
	before, _, _ := api.store.GetSchedule(ctx, d.ID)
	rr := doTokenJSON(api.update, mv, "PATCH", `{"spec_kind":"interval","spec":"3600"}`, d.ID)
	if rr.Code != 200 {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	after, _, _ := api.store.GetSchedule(ctx, d.ID)
	if after.ApprovalPending || after.NextRun != before.NextRun {
		t.Fatalf("pending=%v next %s -> %s", after.ApprovalPending, before.NextRun, after.NextRun)
	}
}

func TestScheduleHoldIsOnByDefault(t *testing.T) {
	api, _, _ := newSchedAPITest(t)
	if !api.holdTokenWrites {
		t.Fatal("a session-written schedule must be held in production")
	}
}

// Approving the version the member looked at: an edit that landed after they opened the row bumps
// the generation, so the stale approval is refused and the new version stays held (#4834).
func TestScheduleApproveRefusesAVersionTheMemberDidNotSee(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	var d scheduleDTO
	_ = json.Unmarshal(doTokenJSON(api.create, mv, "POST", approvalBody, "").Body.Bytes(), &d)
	seen := d.Generation
	if rr := doTokenJSON(api.update, mv, "PATCH", `{"prompt":"swapped after the member looked"}`, d.ID); rr.Code != 200 {
		t.Fatalf("edit %d %s", rr.Code, rr.Body.String())
	}
	rr := doJSON(api.approve, mv, "POST", fmt.Sprintf(`{"generation":%d}`, seen), d.ID)
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "schedule_changed") {
		t.Fatalf("stale approve: %d %s", rr.Code, rr.Body.String())
	}
	if got := dueIDs(t, api, ctx); len(got) != 0 {
		t.Fatalf("unseen version became due: %v", got)
	}
	if rr := doJSON(api.approve, mv, "POST", "", d.ID); rr.Code != http.StatusBadRequest {
		t.Fatalf("approve without a generation: %d", rr.Code)
	}
	if rr := approveCurrent(api, mv, d.ID); rr.Code != 200 {
		t.Fatalf("approve of the current version: %d %s", rr.Code, rr.Body.String())
	}
}

// The store refuses an UpdateSchedule built from a row another write has since changed, so a PATCH
// that read an old row cannot write back a stale approval_pending / next_run (#4834).
func TestScheduleUpdateRefusesAStaleRead(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	var d scheduleDTO
	_ = json.Unmarshal(doTokenJSON(api.create, mv, "POST", approvalBody, "").Body.Bytes(), &d)
	stale, _, _ := api.store.GetSchedule(ctx, d.ID)
	if rr := approveCurrent(api, mv, d.ID); rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	stale.Prompt = "from the stale read"
	if err := api.store.UpdateSchedule(ctx, stale); !errors.Is(err, store.ErrScheduleChanged) {
		t.Fatalf("stale update: %v", err)
	}
	got, _, _ := api.store.GetSchedule(ctx, d.ID)
	if got.ApprovalPending || got.Prompt != "p" {
		t.Fatalf("stale write landed: %+v", got)
	}
	// Pause and resume move the generation too (approve racing resume).
	before := got.Generation
	if err := api.store.SetScheduleEnabled(ctx, d.ID, mv.MembershipID, false, "", store.NowTS()); err != nil {
		t.Fatal(err)
	}
	after, _, _ := api.store.GetSchedule(ctx, d.ID)
	if after.Generation != before+1 {
		t.Fatalf("pause did not bump the generation: %d -> %d", before, after.Generation)
	}
}

// The approve route through the real mux: the schedule token alone cannot reach it, a member
// cannot approve another member's schedule, and the owner can (#4631).
func TestScheduleApproveRouteThroughTheMux(t *testing.T) {
	e := newInboxEnv(t)
	ctx := context.Background()
	mid := e.members["a"]
	tok := mintScheduleToken(scheduleSignKey(e.mgr.tokenSigningRoot()), mid)

	body := strings.NewReader(approvalBody)
	req := httptest.NewRequest("POST", "/internal/schedules", body)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("token create %d %s", rec.Code, rec.Body.String())
	}
	d := decode[scheduleDTO](t, rec)
	if !d.ApprovalPending {
		t.Fatalf("token-created schedule is not held: %+v", d)
	}
	approve := fmt.Sprintf(`{"generation":%d}`, d.Generation)

	// The token alone: no approve on the internal face, and the member route wants a login.
	for _, path := range []string{"/internal/schedules/" + d.ID + "/approve", "/api/schedules/" + d.ID + "/approve"} {
		r := httptest.NewRequest("POST", path, strings.NewReader(approve))
		r.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		e.mux.ServeHTTP(rr, r)
		if rr.Code == http.StatusOK {
			t.Fatalf("%s approved with the token alone", path)
		}
	}
	// Another member of the tenant does not see it as theirs.
	if rr := e.do("c", "POST", "/api/schedules/"+d.ID+"/approve", map[string]any{"generation": d.Generation}); rr.Code != http.StatusNotFound {
		t.Fatalf("other member: %d %s", rr.Code, rr.Body.String())
	}
	if got, _, _ := e.st.GetSchedule(ctx, d.ID); !got.ApprovalPending {
		t.Fatal("refused approvals still cleared the hold")
	}
	if rr := e.do("a", "POST", "/api/schedules/"+d.ID+"/approve", map[string]any{"generation": d.Generation}); rr.Code != http.StatusOK {
		t.Fatalf("owner approve %d %s", rr.Code, rr.Body.String())
	}
	if got, _, _ := e.st.GetSchedule(ctx, d.ID); got.ApprovalPending {
		t.Fatal("owner approval did not clear the hold")
	}
}
