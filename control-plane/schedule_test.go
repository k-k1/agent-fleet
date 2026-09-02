package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func newSchedAPITest(t *testing.T) (scheduleAPI, context.Context, store.MembershipView) {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api := newScheduleAPI(&manager{store: st})
	return api, ctx, store.MembershipView{MembershipID: "m1", TenantID: "default"}
}

// doJSON invokes a bare handler with a JSON body and optional path id.
func doJSON(h func(http.ResponseWriter, *http.Request, store.MembershipView), mv store.MembershipView, method, body, id string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, "/", strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, "/", nil)
	}
	if id != "" {
		r.SetPathValue("id", id)
	}
	rec := httptest.NewRecorder()
	h(rec, r, mv)
	return rec
}

func TestScheduleCreateAndList(t *testing.T) {
	api, _, mv := newSchedAPITest(t)

	rec := doJSON(api.create, mv, "POST",
		`{"spec_kind":"cron","spec":"0 9 * * *","tz":"Asia/Tokyo","spec_label":"毎朝9時","prompt":"review PRs"}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}
	var dto scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if dto.ID == "" || !strings.HasPrefix(dto.ID, "sch_") {
		t.Fatalf("bad id %q", dto.ID)
	}
	if !dto.Enabled {
		t.Error("new schedule should be enabled")
	}
	if dto.NextRun == "" || dto.NextRunLocal == "" {
		t.Errorf("next_run/next_run_local empty: %+v", dto)
	}
	if !strings.HasSuffix(dto.NextRunLocal, "JST") {
		t.Errorf("next_run_local not in tz: %q", dto.NextRunLocal)
	}
	if dto.Report {
		t.Error("report should default to false (報告しない)")
	}

	// List returns it; another member sees nothing.
	if lr := doJSON(api.list, mv, "GET", "", ""); lr.Code != 200 {
		t.Fatalf("list code=%d", lr.Code)
	} else {
		var got []scheduleDTO
		_ = json.Unmarshal(lr.Body.Bytes(), &got)
		if len(got) != 1 {
			t.Fatalf("list n=%d", len(got))
		}
	}
	other := doJSON(api.list, store.MembershipView{MembershipID: "m2"}, "GET", "", "")
	var got2 []scheduleDTO
	_ = json.Unmarshal(other.Body.Bytes(), &got2)
	if len(got2) != 0 {
		t.Fatalf("cross-member leak: %d", len(got2))
	}
}

func TestScheduleCreateValidation(t *testing.T) {
	api, _, mv := newSchedAPITest(t)
	bad := []string{
		`{"spec_kind":"cron","spec":"nope","prompt":"x"}`,                         // bad cron
		`{"spec_kind":"interval","spec":"10","prompt":"x"}`,                       // below floor
		`{"spec_kind":"once","spec":"not-a-time","prompt":"x"}`,                   // bad once
		`{"spec_kind":"weekly","spec":"x","prompt":"x"}`,                          // bad kind
		`{"spec_kind":"cron","spec":"0 9 * * *","prompt":""}`,                     // missing prompt
		`{"spec_kind":"cron","spec":"0 9 * * *","tz":"Mars/Phobos","prompt":"x"}`, // bad tz
		`{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x","wake_policy":"bogus"}`,
	}
	for _, b := range bad {
		rec := doJSON(api.create, mv, "POST", b, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("create %s -> code %d, want 400 (%s)", b, rec.Code, rec.Body.String())
		}
	}
}

func TestScheduleUpdatePatch(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	rec := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","tz":"UTC","prompt":"old"}`, "")
	var dto scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	origNext := dto.NextRun

	// Patch only the prompt: next_run must NOT change (timing unchanged).
	up := doJSON(api.update, mv, "PATCH", `{"prompt":"new"}`, dto.ID)
	if up.Code != 200 {
		t.Fatalf("update code=%d body=%s", up.Code, up.Body.String())
	}
	sch, _, _ := api.store.GetSchedule(ctx, dto.ID)
	if sch.Prompt != "new" {
		t.Errorf("prompt not updated: %q", sch.Prompt)
	}
	if sch.NextRun != origNext {
		t.Errorf("next_run changed on prompt-only patch: %q -> %q", origNext, sch.NextRun)
	}

	// Patch the spec: next_run recomputed.
	up2 := doJSON(api.update, mv, "PATCH", `{"spec":"30 10 * * *"}`, dto.ID)
	if up2.Code != 200 {
		t.Fatalf("update2 code=%d body=%s", up2.Code, up2.Body.String())
	}
	sch2, _, _ := api.store.GetSchedule(ctx, dto.ID)
	if sch2.NextRun == origNext {
		t.Error("next_run not recomputed after spec change")
	}
	if !strings.Contains(sch2.NextRun, "T10:30") {
		t.Errorf("recomputed next_run %q does not reflect new spec 30 10 * * *", sch2.NextRun)
	}

	// Patch report on/off: round-trips through the store, and an unrelated patch
	// leaves it alone (nil pointer = unchanged).
	if up3 := doJSON(api.update, mv, "PATCH", `{"report":true}`, dto.ID); up3.Code != 200 {
		t.Fatalf("update3 code=%d body=%s", up3.Code, up3.Body.String())
	}
	sch3, _, _ := api.store.GetSchedule(ctx, dto.ID)
	if !sch3.Report {
		t.Error("report=true patch not persisted")
	}
	_ = doJSON(api.update, mv, "PATCH", `{"prompt":"newer"}`, dto.ID)
	sch4, _, _ := api.store.GetSchedule(ctx, dto.ID)
	if !sch4.Report {
		t.Error("unrelated patch reset report")
	}
	if up5 := doJSON(api.update, mv, "PATCH", `{"report":false}`, dto.ID); up5.Code != 200 {
		t.Fatalf("update5 code=%d body=%s", up5.Code, up5.Body.String())
	}
	sch5, _, _ := api.store.GetSchedule(ctx, dto.ID)
	if sch5.Report {
		t.Error("report=false patch not persisted")
	}
}

func TestSchedulePauseResumeRunNow(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	rec := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","tz":"UTC","prompt":"x"}`, "")
	var dto scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)

	// run_now while enabled: next_run set to ~now.
	if rn := doJSON(api.runNow, mv, "POST", "", dto.ID); rn.Code != 200 {
		t.Fatalf("run_now code=%d body=%s", rn.Code, rn.Body.String())
	}
	sch, _, _ := api.store.GetSchedule(ctx, dto.ID)
	if nt, err := time.Parse(time.RFC3339, sch.NextRun); err != nil || time.Since(nt) > 5*time.Second {
		t.Errorf("run_now did not set next_run to ~now: %q", sch.NextRun)
	}

	// pause: disabled + next_run cleared.
	if p := doJSON(api.pause, mv, "POST", "", dto.ID); p.Code != 200 {
		t.Fatalf("pause code=%d", p.Code)
	}
	sch, _, _ = api.store.GetSchedule(ctx, dto.ID)
	if sch.Enabled || sch.NextRun != "" {
		t.Errorf("pause not applied: enabled=%v next=%q", sch.Enabled, sch.NextRun)
	}

	// run_now on a paused schedule -> 409.
	if rn := doJSON(api.runNow, mv, "POST", "", dto.ID); rn.Code != http.StatusConflict {
		t.Errorf("run_now on paused -> %d, want 409", rn.Code)
	}

	// resume: enabled + next_run recomputed.
	if rs := doJSON(api.resume, mv, "POST", "", dto.ID); rs.Code != 200 {
		t.Fatalf("resume code=%d", rs.Code)
	}
	sch, _, _ = api.store.GetSchedule(ctx, dto.ID)
	if !sch.Enabled || sch.NextRun == "" {
		t.Errorf("resume not applied: enabled=%v next=%q", sch.Enabled, sch.NextRun)
	}
}

// TestResumeSpentOnceRejected: resuming a `once` whose instant has passed would re-fire it
// immediately; it must be rejected so the operator creates a fresh schedule instead.
func TestResumeSpentOnceRejected(t *testing.T) {
	api, _, mv := newSchedAPITest(t)
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	rec := doJSON(api.create, mv, "POST",
		`{"spec_kind":"once","spec":"`+past+`","prompt":"x"}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create once code=%d body=%s", rec.Code, rec.Body.String())
	}
	var dto scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if p := doJSON(api.pause, mv, "POST", "", dto.ID); p.Code != 200 {
		t.Fatalf("pause code=%d", p.Code)
	}
	rs := doJSON(api.resume, mv, "POST", "", dto.ID)
	if rs.Code != http.StatusBadRequest {
		t.Fatalf("resume of spent once = %d, want 400 (body=%s)", rs.Code, rs.Body.String())
	}
	// A future once, by contrast, resumes fine.
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	rec2 := doJSON(api.create, mv, "POST", `{"spec_kind":"once","spec":"`+future+`","prompt":"x"}`, "")
	var dto2 scheduleDTO
	_ = json.Unmarshal(rec2.Body.Bytes(), &dto2)
	_ = doJSON(api.pause, mv, "POST", "", dto2.ID)
	if rs2 := doJSON(api.resume, mv, "POST", "", dto2.ID); rs2.Code != 200 {
		t.Fatalf("resume of future once = %d, want 200", rs2.Code)
	}
}

// TestSchedulerDisabledWarning: create/run_now surface a warning when the scheduler
// goroutine is not running, so an operator is not misled into thinking a stored schedule
// will fire on a deployment that never runs it.
func TestSchedulerDisabledWarning(t *testing.T) {
	api, _, mv := newSchedAPITest(t)
	old := schedulerRunning
	t.Cleanup(func() { schedulerRunning = old })

	schedulerRunning = false
	rec := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","tz":"UTC","prompt":"x"}`, "")
	var d scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if d.Warning == "" {
		t.Fatal("create with scheduler disabled must return a warning")
	}
	if rn := doJSON(api.runNow, mv, "POST", "", d.ID); rn.Code == 200 {
		var dr scheduleDTO
		_ = json.Unmarshal(rn.Body.Bytes(), &dr)
		if dr.Warning == "" {
			t.Error("run_now with scheduler disabled must return a warning")
		}
	}

	schedulerRunning = true
	rec2 := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","tz":"UTC","prompt":"x"}`, "")
	var d2 scheduleDTO
	_ = json.Unmarshal(rec2.Body.Bytes(), &d2)
	if d2.Warning != "" {
		t.Fatalf("create with scheduler enabled must not warn: %q", d2.Warning)
	}
}

func TestScheduleDeleteOwnership(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	rec := doJSON(api.create, mv, "POST", `{"spec_kind":"interval","spec":"3600","prompt":"x"}`, "")
	var dto scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)

	// Foreign member gets 404 and the row survives.
	if d := doJSON(api.delete, store.MembershipView{MembershipID: "m2"}, "DELETE", "", dto.ID); d.Code != http.StatusNotFound {
		t.Errorf("foreign delete -> %d, want 404", d.Code)
	}
	if _, ok, _ := api.store.GetSchedule(ctx, dto.ID); !ok {
		t.Fatal("foreign delete removed the row")
	}
	// Owner deletes.
	if d := doJSON(api.delete, mv, "DELETE", "", dto.ID); d.Code != 200 {
		t.Fatalf("delete code=%d", d.Code)
	}
	if _, ok, _ := api.store.GetSchedule(ctx, dto.ID); ok {
		t.Fatal("row present after owner delete")
	}
}

func TestScheduleRunsHistory(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	rec := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"*/5 * * * *","tz":"UTC","prompt":"x"}`, "")
	var dto scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)

	// Seed a couple of runs directly.
	_ = api.store.AppendScheduleRun(ctx, store.ScheduleRun{ID: store.NewID(), ScheduleID: dto.ID, MembershipID: "m1", FiredAt: "2026-07-22T09:00:00Z", Status: "fired"}, 50)
	_ = api.store.AppendScheduleRun(ctx, store.ScheduleRun{ID: store.NewID(), ScheduleID: dto.ID, MembershipID: "m1", FiredAt: "2026-07-22T09:05:00Z", Status: "skipped_stopped"}, 50)

	r := doJSON(api.runs, mv, "GET", "", dto.ID)
	if r.Code != 200 {
		t.Fatalf("runs code=%d", r.Code)
	}
	var out struct {
		Runs []map[string]any `json:"runs"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &out)
	if len(out.Runs) != 2 {
		t.Fatalf("runs n=%d, want 2", len(out.Runs))
	}
	// Newest first.
	if out.Runs[0]["fired_at"] != "2026-07-22T09:05:00Z" {
		t.Errorf("runs not newest-first: %v", out.Runs[0])
	}
}

func TestAppendScheduleRunTrims(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Append 5 runs but keep only 3.
	for i := 0; i < 5; i++ {
		firedAt := time.Date(2026, 7, 22, 9, i, 0, 0, time.UTC).Format(time.RFC3339)
		if err := st.AppendScheduleRun(ctx, store.ScheduleRun{ID: store.NewID(), ScheduleID: "s", MembershipID: "m1", FiredAt: firedAt, Status: "fired"}, 3); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	rows, err := st.ListScheduleRuns(ctx, "s", "m1", 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("kept %d rows, want 3 (trim failed)", len(rows))
	}
	// The 3 kept are the newest (09:04, 09:03, 09:02).
	if rows[0].FiredAt != "2026-07-22T09:04:00Z" {
		t.Errorf("newest kept = %q, want 09:04", rows[0].FiredAt)
	}
}

func TestScheduleTokenRoundTrip(t *testing.T) {
	key := scheduleSignKey([]byte("master"))
	tok := mintScheduleToken(key, "m-123")
	mid, ok := verifyScheduleToken(key, tok)
	if !ok || mid != "m-123" {
		t.Fatalf("round trip: ok=%v mid=%q", ok, mid)
	}
	// Tampered tag rejected.
	if _, ok := verifyScheduleToken(key, tok+"x"); ok {
		t.Error("tampered token accepted")
	}
	// Wrong key rejected.
	if _, ok := verifyScheduleToken(scheduleSignKey([]byte("other")), tok); ok {
		t.Error("token from a different key accepted")
	}
	// A memo token must not verify as a schedule token (distinct sign labels).
	memoTok := mintMemoToken(memoSignKey([]byte("master")), "m-123")
	if _, ok := verifyScheduleToken(key, memoTok); ok {
		t.Error("memo token accepted as schedule token")
	}
}
