package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// scheduler_live_test.go — end-to-end verification of the scheduled-execution pipeline
// (docs/log/38 P5 pre-flight). Where scheduler_test.go drives sc.tick by hand and writes the
// store directly, these tests exercise the REAL wiring the fleet uses: the operator
// create/run_now HTTP handlers write the store, and the actual ticker goroutine (sc.run,
// not a hand-called tick) picks the row up and fires it. Only the side-effecting firer
// (wake + create_session) is stubbed, since a real workspace wake needs the full fleet.
// This is the closest deterministic proof-of-fire available without a running deployment.

// recordingFirer captures fires from the live ticker goroutine; guarded because run() and
// the test read it from different goroutines.
type recordingFirer struct {
	mu     sync.Mutex
	fired  []store.Schedule
	status string
}

func (f *recordingFirer) fire(_ context.Context, sch store.Schedule, _ time.Time) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fired = append(f.fired, sch)
	if f.status != "" {
		return f.status, "", nil
	}
	return "fired", "", nil
}

func (f *recordingFirer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fired)
}

func (f *recordingFirer) first() (store.Schedule, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.fired) == 0 {
		return store.Schedule{}, false
	}
	return f.fired[0], true
}

// waitFor polls cond up to d, so the live-goroutine tests are not flaky under load.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// TestLiveOperatorCreateThenTickerFires: an operator registers a due `once` schedule
// through the real create handler, and the real ticker goroutine fires it, advances the
// ledger, and appends a run — no hand-driven tick anywhere.
func TestLiveOperatorCreateThenTickerFires(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	api := newScheduleAPI(&manager{store: st})
	mv := store.MembershipView{MembershipID: "m1", TenantID: "default"}

	// Mark the scheduler enabled so create's response mirrors a real firing deployment
	// (no "scheduler disabled" warning). Restore after — it is a package global.
	prev := schedulerRunning
	schedulerRunning = true
	t.Cleanup(func() { schedulerRunning = prev })

	// A `once` whose instant is the current second is due immediately (jitter is 0 for
	// once). RFC3339 has second resolution, so "now" is the earliest imminent trigger.
	spec := time.Now().UTC().Format(time.RFC3339)
	body := `{"spec_kind":"once","spec":"` + spec + `","tz":"Asia/Tokyo","spec_label":"検証ワンショット","prompt":"日次レビュー {{date}} {{time}}"}`
	rec := doJSON(api.create, mv, "POST", body, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}
	var dto scheduleDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if dto.Warning != "" {
		t.Fatalf("unexpected disabled warning with scheduler enabled: %q", dto.Warning)
	}
	if dto.ID == "" {
		t.Fatal("create returned no id")
	}

	// Start the REAL ticker goroutine at a fast interval.
	ff := &recordingFirer{}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go newScheduler(st, ff, 30*time.Millisecond).run(runCtx)

	if !waitFor(2*time.Second, func() bool { return ff.count() >= 1 }) {
		t.Fatalf("ticker never fired the schedule (count=%d)", ff.count())
	}
	fired, _ := ff.first()
	if fired.ID != dto.ID {
		t.Fatalf("fired id=%q want %q", fired.ID, dto.ID)
	}

	// Ledger + history reflect the fire, and a spent `once` disabled itself.
	got, ok, err := st.GetSchedule(ctx, dto.ID)
	if err != nil || !ok {
		t.Fatalf("get after fire: err=%v ok=%v", err, ok)
	}
	if got.LastStatus != "fired" {
		t.Fatalf("last_status=%q want fired", got.LastStatus)
	}
	if got.Enabled || got.NextRun != "" {
		t.Fatalf("spent once not disabled: enabled=%v next_run=%q", got.Enabled, got.NextRun)
	}
	runs, err := st.ListScheduleRuns(ctx, dto.ID, mv.MembershipID, 50)
	if err != nil || len(runs) != 1 || runs[0].Status != "fired" {
		t.Fatalf("run history = %+v err=%v", runs, err)
	}

	// The ticker must not re-fire the now-disabled row.
	firstCount := ff.count()
	time.Sleep(150 * time.Millisecond)
	if ff.count() != firstCount {
		t.Fatalf("disabled schedule re-fired: %d -> %d", firstCount, ff.count())
	}
}

// TestLiveRunNowFiresThroughTicker: run_schedule_now on a not-yet-due interval schedule
// makes it due immediately and the real ticker fires it through the same path as a timed
// fire (ADR0021 decision 8). Proves the manual-fire affordance the operator uses to smoke
// test a schedule actually reaches the firer.
func TestLiveRunNowFiresThroughTicker(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	api := newScheduleAPI(&manager{store: st})
	mv := store.MembershipView{MembershipID: "m1", TenantID: "default"}
	prev := schedulerRunning
	schedulerRunning = true
	t.Cleanup(func() { schedulerRunning = prev })

	// interval=3600s: next_run is an hour out, so it is NOT due until run_now moves it.
	rec := doJSON(api.create, mv, "POST",
		`{"spec_kind":"interval","spec":"3600","prompt":"hourly check"}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}
	var dto scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)

	ff := &recordingFirer{}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go newScheduler(st, ff, 30*time.Millisecond).run(runCtx)

	// It must NOT fire on its own (next_run is an hour away).
	time.Sleep(200 * time.Millisecond)
	if ff.count() != 0 {
		t.Fatalf("interval fired before run_now: %d", ff.count())
	}

	// Operator hits run_now -> due immediately.
	rn := doJSON(api.runNow, mv, "POST", "", dto.ID)
	if rn.Code != http.StatusOK {
		t.Fatalf("run_now code=%d body=%s", rn.Code, rn.Body.String())
	}
	if !waitFor(2*time.Second, func() bool { return ff.count() >= 1 }) {
		t.Fatalf("run_now did not fire through the ticker (count=%d)", ff.count())
	}

	// interval stays enabled with a fresh future next_run after the fire.
	got, _, _ := st.GetSchedule(ctx, dto.ID)
	if !got.Enabled {
		t.Fatal("interval schedule should stay enabled after run_now")
	}
	if got.NextRun == "" || got.NextRun <= store.NowTS() {
		t.Fatalf("next_run not advanced to the future: %q", got.NextRun)
	}
}

// TestLeapDayCronResolves guards the P4.1 horizon fix: a Feb-29-only cron must resolve to
// a real leap-day instant instead of being rejected (a 1-year search horizon wrongly
// failed it). Covers the review finding that previously had no test.
func TestLeapDayCronResolves(t *testing.T) {
	utc := time.UTC
	// From mid-2026 (a non-leap year), the next 29 Feb is 2028-02-29.
	after := time.Date(2026, 7, 22, 0, 0, 0, 0, utc)
	got, err := nextCron("0 0 29 2 *", after, utc)
	if err != nil {
		t.Fatalf("leap-day cron rejected (horizon too small?): %v", err)
	}
	want := time.Date(2028, 2, 29, 0, 0, 0, 0, utc)
	if !got.Equal(want) {
		t.Fatalf("got %s want %s", got.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
	}
}
