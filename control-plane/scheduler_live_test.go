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
// the test read it from different goroutines. `fires` carries one notification per fire;
// it is buffered and the send is non-blocking, so a fire is never held up by the fact
// that nobody is waiting.
type recordingFirer struct {
	mu     sync.Mutex
	fired  []store.Schedule
	status string
	fires  chan struct{}
}

func newRecordingFirer() *recordingFirer {
	return &recordingFirer{fires: make(chan struct{}, 16)}
}

func (f *recordingFirer) fire(_ context.Context, sch store.Schedule, _ time.Time) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fired = append(f.fired, sch)
	if f.fires != nil {
		select {
		case f.fires <- struct{}{}:
		default: // nobody waiting / buffer full — fired still keeps the record
		}
	}
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

// waitFire waits for a fire BY EVENT, not by polling a fixed budget. How many times a
// 30 ms ticker turns inside a fixed window is a function of how busy the machine is, so a
// poll loop cannot tell "late" apart from "never fired".
//
// The deadline is not slack. It does not slow the green path down — this returns the
// instant a fire arrives — and it exists only to ASSERT that nothing happened, so a
// generous one does not weaken the check while a tight one lies.
//
// What it guarantees is that the FIRER WAS CALLED, and nothing about the ledger. The
// notification is sent from inside fire, whereas the ledger advance (RecordScheduleFire)
// and the run-history append (AppendScheduleRun) run in the ticker goroutine AFTER the
// firer returns (runOne in scheduler.go), with advanceNextRun and a SQLite write in
// between. Reading the ledger straight after this returns can therefore find it not yet
// advanced: measured at roughly 1 iteration in 200 on 8 cores (`last_status="" want
// fired`). It is not a waiting problem, so a longer deadline does not fix it — go through
// waitLedger before reading the ledger.
func (f *recordingFirer) waitFire(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-f.fires:
	case <-time.After(d):
		t.Fatalf("no fire after waiting %v (count=%d)", d, f.count())
	}
}

// liveFireDeadline is the ceiling before declaring that no fire happened. Generous on
// purpose: another session running a build on this shared host delays a fire, it never
// stops one from happening.
const liveFireDeadline = 30 * time.Second

// waitLedger waits until the ledger has finished writing the fire (cond becomes true) and
// returns that row.
//
// Use it PAIRED with waitFire. waitFire only guarantees the firer call, while the ledger
// advance runs afterwards in another goroutine (the ticker), so there is always a window
// between the two; treating "it fired" as "the ledger has advanced" breaks under
// contention (see waitFire).
//
// This is not a sleep to buy time. It returns the moment cond is true, so the green path
// is no slower, and the deadline exists only to ASSERT that nothing advanced — the same
// reasoning as waitFire.
//
// cond must test ONLY whether the ledger advanced, never the expected value itself.
// Conditioning on `LastStatus == "fired"`, for instance, makes a scheduler that wrote
// `error:...` wait out the whole deadline and then fail under the FALSE headline "it did
// not advance". Wait on "advanced" and leave the value judgement to the caller's
// assertion, which then fails with its own wording.
func waitLedger(t *testing.T, ctx context.Context, st *store.SQL, id string, d time.Duration, cond func(store.Schedule) bool) store.Schedule {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		got, ok, err := st.GetSchedule(ctx, id)
		if err != nil {
			t.Fatalf("failed to read the ledger (%s): %v", id, err)
		}
		if !ok {
			t.Fatalf("schedule %s disappeared from the ledger", id)
		}
		if cond(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ledger did not advance after waiting %v: last_status=%q next_run=%q enabled=%v",
				d, got.LastStatus, got.NextRun, got.Enabled)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitRuns waits until the run history has reached n rows and returns them.
// AppendScheduleRun runs even later than RecordScheduleFire and is best-effort, so having
// waited for the ledger to advance is not enough to know it has been written.
func waitRuns(t *testing.T, ctx context.Context, st *store.SQL, id, membershipID string, n int, d time.Duration) []store.ScheduleRun {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		runs, err := st.ListScheduleRuns(ctx, id, membershipID, 50)
		if err != nil {
			t.Fatalf("failed to read the run history (%s): %v", id, err)
		}
		if len(runs) >= n {
			return runs
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited %v and the run history still did not reach %d rows: %+v", d, n, runs)
		}
		time.Sleep(time.Millisecond)
	}
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
	ff := newRecordingFirer()
	sc := newScheduler(st, ff, 30*time.Millisecond)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go sc.run(runCtx)

	ff.waitFire(t, liveFireDeadline)
	fired, _ := ff.first()
	if fired.ID != dto.ID {
		t.Fatalf("fired id=%q want %q", fired.ID, dto.ID)
	}

	// Ledger + history reflect the fire, and a spent `once` disabled itself.
	// The ledger advances after the firer call, so waitFire alone is not enough (see
	// waitLedger).
	got := waitLedger(t, ctx, st, dto.ID, liveFireDeadline, func(s store.Schedule) bool {
		return s.LastStatus != "" // wait on "did it advance"; the value is judged below
	})
	if got.LastStatus != "fired" {
		t.Fatalf("last_status=%q want fired", got.LastStatus)
	}
	if got.Enabled || got.NextRun != "" {
		t.Fatalf("spent once not disabled: enabled=%v next_run=%q", got.Enabled, got.NextRun)
	}
	runs := waitRuns(t, ctx, st, dto.ID, mv.MembershipID, 1, liveFireDeadline)
	if len(runs) != 1 || runs[0].Status != "fired" {
		t.Fatalf("run history = %+v", runs)
	}

	// The ticker must not re-fire the now-disabled row.
	//
	// Sleeping and then counting would pass even if no tick ever ran during the sleep,
	// so a check that tested nothing is indistinguishable from a real pass — and the
	// busier the host, the likelier that is, i.e. it stops working exactly when it
	// matters. Stop the loop and step tick synchronously, twice.
	cancel()
	firstCount := ff.count()
	sc.tickAt(ctx, time.Now().UTC())
	sc.tickAt(ctx, time.Now().UTC())
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

	ff := newRecordingFirer()
	sc := newScheduler(st, ff, 30*time.Millisecond)

	// It must NOT fire on its own (next_run is an hour away).
	// Do not sleep and assume the ticker probably turned — step tick synchronously
	// once. A sleep goes green even if the ticker never turned during it, and nobody
	// can tell that apart from the check actually holding.
	sc.tickAt(ctx, time.Now().UTC())
	if ff.count() != 0 {
		t.Fatalf("interval fired before run_now: %d", ff.count())
	}

	// The subject of the test: have the real ticker pick run_now up.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go sc.run(runCtx)

	// Operator hits run_now -> due immediately.
	rn := doJSON(api.runNow, mv, "POST", "", dto.ID)
	if rn.Code != http.StatusOK {
		t.Fatalf("run_now code=%d body=%s", rn.Code, rn.Body.String())
	}
	ff.waitFire(t, liveFireDeadline)

	// interval stays enabled with a fresh future next_run after the fire.
	// This breaks too without waiting for the ledger to advance: run_now resets
	// next_run to now (schedule.go:371-372), so a read taken before the advance still
	// sees a next_run in the past and the "moved into the future" check below is false.
	// Returning from waitFire settles nothing here.
	got := waitLedger(t, ctx, st, dto.ID, liveFireDeadline, func(s store.Schedule) bool {
		return s.LastStatus != ""
	})
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
