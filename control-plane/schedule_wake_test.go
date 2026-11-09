package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func withScheduler(t *testing.T, on bool) {
	t.Helper()
	prev := schedulerRunning
	schedulerRunning = on
	t.Cleanup(func() { schedulerRunning = prev })
}

func wakeAt(d time.Duration) string {
	return `{"at":"` + time.Now().UTC().Add(d).Format(time.RFC3339) + `"}`
}

func createWakeID(t *testing.T, api scheduleAPI, mv store.MembershipView, d time.Duration) string {
	t.Helper()
	rec := doJSON(api.createWake, mv, "POST", wakeAt(d), "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("createWake -> %d %s", rec.Code, rec.Body.String())
	}
	var out struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.ID == "" {
		t.Fatalf("no id in %s", rec.Body.String())
	}
	return out.ID
}

// ADR 0133 §7: where the scheduler does not run, a wake is refused and nothing is stored.
func TestCreateWakeRefusedWithoutScheduler(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	withScheduler(t, false)
	rec := doJSON(api.createWake, mv, "POST", wakeAt(time.Hour), "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "scheduler_disabled") {
		t.Fatalf("createWake without a scheduler -> %d %s, want 409 scheduler_disabled", rec.Code, rec.Body.String())
	}
	if rows, _ := api.store.ListSchedules(ctx, mv.MembershipID); len(rows) != 0 {
		t.Fatalf("a refused wake left %d rows", len(rows))
	}
}

func TestCreateWakeValidatesInstant(t *testing.T) {
	api, _, mv := newSchedAPITest(t)
	withScheduler(t, true)
	for name, body := range map[string]string{
		"past":   wakeAt(-time.Minute),
		"far":    wakeAt(40 * 24 * time.Hour),
		"edge":   wakeAt(30*24*time.Hour + time.Hour), // the engine's own limit is 30 days
		"empty":  `{}`,
		"junk":   `{"at":"tomorrow"}`,
		"notobj": `[]`,
	} {
		if rec := doJSON(api.createWake, mv, "POST", body, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s -> %d, want 400", name, rec.Code)
		}
	}
}

// The hidden target must not reach the member: not the list, and no by-id handler.
func TestWakeTargetIsHiddenFromTheScheduleAPI(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	withScheduler(t, true)
	id := createWakeID(t, api, mv, time.Hour)
	if rows, _ := api.store.ListSchedules(ctx, mv.MembershipID); len(rows) != 1 {
		t.Fatalf("the wake row should be stored, got %d rows", len(rows))
	}

	visible := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"p"}`, "")
	if visible.Code != http.StatusCreated {
		t.Fatalf("create -> %d", visible.Code)
	}
	lr := doJSON(api.list, mv, "GET", "", "")
	var got []scheduleDTO
	_ = json.Unmarshal(lr.Body.Bytes(), &got)
	if len(got) != 1 || got[0].ID == id || strings.Contains(lr.Body.String(), id) {
		t.Fatalf("list leaks the wake target: %s", lr.Body.String())
	}

	handlers := map[string]func(http.ResponseWriter, *http.Request, store.MembershipView){
		"update": api.update, "delete": api.delete, "pause": api.pause, "resume": api.resume,
		"runNow": api.runNow, "runs": api.runs, "runNotExecuted": api.runNotExecuted, "runSilent": api.runSilent,
	}
	for name, h := range handlers {
		if rec := doJSON(h, mv, "POST", `{"prompt":"x","session":"s","slot":"2026-01-01T00:00:00Z"}`, id); rec.Code != http.StatusNotFound {
			t.Errorf("%s on a wake id -> %d, want 404", name, rec.Code)
		}
	}
	if row, ok, _ := api.store.GetSchedule(ctx, id); !ok || !row.Enabled {
		t.Fatal("a refused by-id call changed the wake row")
	}
}

// A member cannot make or convert a schedule into the hidden target.
func TestScheduleAPIRefusesWakeSessionMode(t *testing.T) {
	api, _, mv := newSchedAPITest(t)
	rec := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"p","session_mode":"wake"}`, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create with session_mode wake -> %d %s", rec.Code, rec.Body.String())
	}
	ok := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"p"}`, "")
	var dto scheduleDTO
	_ = json.Unmarshal(ok.Body.Bytes(), &dto)
	if up := doJSON(api.update, mv, "PATCH", `{"session_mode":"wake"}`, dto.ID); up.Code != http.StatusBadRequest {
		t.Fatalf("patching a schedule into wake -> %d %s", up.Code, up.Body.String())
	}
}

func TestDeleteWakeIsIdempotentAndScoped(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	withScheduler(t, true)
	id := createWakeID(t, api, mv, time.Hour)

	other := store.MembershipView{MembershipID: "m2", TenantID: "default"}
	if rec := doJSON(api.deleteWake, other, "DELETE", "", id); rec.Code != http.StatusNotFound {
		t.Fatalf("another member deleting the wake -> %d, want 404", rec.Code)
	}
	if rec := doJSON(api.deleteWake, mv, "DELETE", "", id); rec.Code != http.StatusOK {
		t.Fatalf("delete -> %d", rec.Code)
	}
	if rec := doJSON(api.deleteWake, mv, "DELETE", "", id); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete -> %d, want 404 (the Agent reads it as already gone)", rec.Code)
	}

	// The wake route cannot be used to delete an ordinary schedule.
	vis := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"p"}`, "")
	var dto scheduleDTO
	_ = json.Unmarshal(vis.Body.Bytes(), &dto)
	if rec := doJSON(api.deleteWake, mv, "DELETE", "", dto.ID); rec.Code != http.StatusNotFound {
		t.Fatalf("deleteWake on an ordinary schedule -> %d, want 404", rec.Code)
	}
	if _, ok, _ := api.store.GetSchedule(ctx, dto.ID); !ok {
		t.Fatal("deleteWake removed an ordinary schedule")
	}
}

func TestCreateWakeLimit(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	withScheduler(t, true)
	for i := 0; i < maxWakesPerMembership; i++ {
		s := store.Schedule{ID: "w" + string(rune('A'+i%26)) + time.Duration(i).String(), MembershipID: mv.MembershipID, TenantID: "default",
			SpecKind: "once", Spec: "2099-01-01T00:00:00Z", TZ: "UTC", SessionMode: scheduleModeWake, Enabled: true,
			NextRun: "2099-01-01T00:00:00Z", CreatedAt: store.NowTS(), UpdatedAt: store.NowTS()}
		if err := api.store.CreateSchedule(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if rec := doJSON(api.createWake, mv, "POST", wakeAt(time.Hour), ""); rec.Code != http.StatusConflict {
		t.Fatalf("wake past the limit -> %d, want 409", rec.Code)
	}
}

// A due wake row is fired once and removed, leaves no run history and notifies nobody, even
// when the fire failed: the member never saw the row.
func TestSchedulerFiresWakeOnceAndDeletesIt(t *testing.T) {
	for _, status := range []string{"", statusWakeRunning, "error: boom", "skipped_quota"} {
		st, ctx := newSchedTestStore(t)
		sc := store.Schedule{
			ID: "sch_w", MembershipID: "m1", TenantID: "default", SpecKind: "once", Spec: "2000-01-01T00:00:00Z",
			TZ: "UTC", SessionMode: scheduleModeWake, WakePolicy: "wake", Enabled: true,
			NextRun: "2000-01-01T00:00:00Z", CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
		}
		if err := st.CreateSchedule(ctx, sc); err != nil {
			t.Fatal(err)
		}
		ff := &fakeFirer{status: status}
		sched := newScheduler(st, ff, time.Minute)
		sched.tick(ctx)
		sched.tick(ctx)
		if len(ff.fired) != 1 {
			t.Fatalf("status %q: fired %v, want exactly once", status, ff.fired)
		}
		if _, ok, _ := st.GetSchedule(ctx, "sch_w"); ok {
			t.Fatalf("status %q: the spent wake row was not deleted", status)
		}
		if runs, _ := st.ListScheduleRuns(ctx, "sch_w", "m1", 10); len(runs) != 0 {
			t.Fatalf("status %q: wake fire wrote run history: %+v", status, runs)
		}
	}
}

// The firer must not judge a wake row by the kind or route a session schedule carries, nor
// start a session for it: the row of a removed member is refused as inactive, not as a bad kind.
func TestWakeFireSkipsSessionChecks(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	f := &wakeFirer{mgr: &manager{store: st}}
	status, session, err := f.fire(ctx, store.Schedule{ID: "s", MembershipID: "m_gone", SessionMode: scheduleModeWake,
		WakePolicy: "wake", AgentKind: "no-such-kind", Route: "vertex", RouteConnection: "c1"}, time.Now())
	if err != nil || status != statusMembershipInactive || session != "" {
		t.Fatalf("fire = %q, %q, %v; want %q with no error", status, session, err, statusMembershipInactive)
	}
}
