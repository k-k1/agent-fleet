package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return loc
}

func TestNextCronBasic(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		name  string
		spec  string
		after time.Time
		want  time.Time
	}{
		{
			name:  "daily 9am same day",
			spec:  "0 9 * * *",
			after: time.Date(2026, 7, 22, 6, 0, 0, 0, utc),
			want:  time.Date(2026, 7, 22, 9, 0, 0, 0, utc),
		},
		{
			name:  "daily 9am rolls to next day when past",
			spec:  "0 9 * * *",
			after: time.Date(2026, 7, 22, 9, 0, 0, 0, utc), // strictly after => next day
			want:  time.Date(2026, 7, 23, 9, 0, 0, 0, utc),
		},
		{
			name:  "every 15 minutes step",
			spec:  "*/15 * * * *",
			after: time.Date(2026, 7, 22, 9, 7, 0, 0, utc),
			want:  time.Date(2026, 7, 22, 9, 15, 0, 0, utc),
		},
		{
			name:  "weekday range mon-fri 18:00 skips weekend",
			spec:  "0 18 * * 1-5",
			after: time.Date(2026, 7, 24, 19, 0, 0, 0, utc), // Fri 19:00 -> next is Mon
			want:  time.Date(2026, 7, 27, 18, 0, 0, 0, utc), // Mon
		},
		{
			name:  "day-of-month specific",
			spec:  "0 0 1 * *",
			after: time.Date(2026, 7, 22, 0, 0, 0, 0, utc),
			want:  time.Date(2026, 8, 1, 0, 0, 0, 0, utc),
		},
		{
			name:  "dom OR dow both restricted",
			spec:  "0 12 13 * 5", // noon on the 13th OR any Friday
			after: time.Date(2026, 7, 22, 0, 0, 0, 0, utc),
			want:  time.Date(2026, 7, 24, 12, 0, 0, 0, utc), // Fri the 24th comes before the 13th of Aug
		},
		{
			name:  "list of hours",
			spec:  "0 9,17 * * *",
			after: time.Date(2026, 7, 22, 10, 0, 0, 0, utc),
			want:  time.Date(2026, 7, 22, 17, 0, 0, 0, utc),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextCron(tc.spec, tc.after, utc)
			if err != nil {
				t.Fatalf("nextCron: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("got %s want %s", got.UTC().Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}

func TestNextCronTZ(t *testing.T) {
	tokyo := mustLoc(t, "Asia/Tokyo")
	// 09:00 JST daily. Reference is 06:00 UTC (=15:00 JST) so the next 09:00 JST is
	// the following calendar day, i.e. 2026-07-23 09:00 JST = 2026-07-23 00:00 UTC.
	got, err := nextCron("0 9 * * *", time.Date(2026, 7, 22, 6, 0, 0, 0, time.UTC), tokyo)
	if err != nil {
		t.Fatalf("nextCron: %v", err)
	}
	want := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %s want %s", got.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestNextCronDSTFallBack: on the fall-back day 01:30 local occurs twice; a daily
// 01:30 cron must fire only once, then advance to the next day.
func TestNextCronDSTFallBack(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// US DST ends 2025-11-02: 02:00 EDT falls back to 01:00 EST.
	// Reference just before midnight local the night before.
	after := time.Date(2025, 11, 2, 0, 0, 0, 0, ny)
	first, err := nextCron("30 1 * * *", after, ny)
	if err != nil {
		t.Fatalf("nextCron first: %v", err)
	}
	// First 01:30 is EDT (UTC-4) => 05:30 UTC.
	if want := time.Date(2025, 11, 2, 5, 30, 0, 0, time.UTC); !first.Equal(want) {
		t.Fatalf("first fire got %s want %s", first.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
	}
	// Advancing from the first fire must NOT return the second 01:30 (06:30 UTC, EST)
	// but jump to the next day's 01:30 EST (2025-11-03 06:30 UTC).
	next, err := nextCron("30 1 * * *", first, ny)
	if err != nil {
		t.Fatalf("nextCron next: %v", err)
	}
	if want := time.Date(2025, 11, 3, 6, 30, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next fire got %s want %s (fall-back double fire not suppressed)", next.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestNextCronDSTSpringForward: on the spring-forward day 02:30 local does not exist;
// a daily 02:30 cron must skip that day and fire the next.
func TestNextCronDSTSpringForward(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// US DST begins 2025-03-09: 02:00 EST springs to 03:00 EDT, so 02:30 is skipped.
	after := time.Date(2025, 3, 9, 0, 0, 0, 0, ny)
	got, err := nextCron("30 2 * * *", after, ny)
	if err != nil {
		t.Fatalf("nextCron: %v", err)
	}
	// Next real 02:30 is 2025-03-10 02:30 EDT (UTC-4) => 06:30 UTC.
	if want := time.Date(2025, 3, 10, 6, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %s want %s (spring-forward gap not skipped)", got.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestParseCronErrors(t *testing.T) {
	bad := []string{
		"",            // empty
		"* * * *",     // 4 fields
		"* * * * * *", // 6 fields
		"60 * * * *",  // minute out of range
		"* 24 * * *",  // hour out of range
		"* * 0 * *",   // dom below 1
		"* * * 13 *",  // month out of range
		"*/0 * * * *", // zero step
		"5-1 * * * *", // inverted range
		"a * * * *",   // non-numeric
	}
	for _, spec := range bad {
		if _, err := parseCron(spec); err == nil {
			t.Errorf("parseCron(%q) = nil error, want error", spec)
		}
	}
}

func TestIntervalAndOnce(t *testing.T) {
	from := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)

	// interval: floor enforced, otherwise from+seconds.
	if _, _, err := advanceNextRun(Schedule{SpecKind: "interval", Spec: "30"}, from); err == nil {
		t.Error("interval 30s should be rejected (below floor)")
	}
	got, keep, err := advanceNextRun(Schedule{SpecKind: "interval", Spec: "3600"}, from)
	if err != nil || !keep {
		t.Fatalf("interval advance: err=%v keep=%v", err, keep)
	}
	if want := from.Add(time.Hour).Format(time.RFC3339); got != want {
		t.Fatalf("interval next got %s want %s", got, want)
	}

	// once: initial is the absolute instant, advance is spent (disable).
	iso := "2026-07-22T09:30:00Z"
	init, err := initialNextRun(Schedule{SpecKind: "once", Spec: iso}, from)
	if err != nil || init != iso {
		t.Fatalf("once initial got %q err %v", init, err)
	}
	next, keep, err := advanceNextRun(Schedule{SpecKind: "once", Spec: iso}, from)
	if err != nil {
		t.Fatalf("once advance: %v", err)
	}
	if keep || next != "" {
		t.Fatalf("once advance should disable: keep=%v next=%q", keep, next)
	}

	// once with a bad spec surfaces an error at initial computation.
	if _, err := initialNextRun(Schedule{SpecKind: "once", Spec: "not-a-time"}, from); err == nil {
		t.Error("once with bad spec should error")
	}
}

func TestInitialNextRunCron(t *testing.T) {
	// At creation the first fire must include a match at exactly `from` when it lines
	// up (initialNextRun searches from just before `from`).
	from := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	got, err := initialNextRun(Schedule{SpecKind: "cron", Spec: "0 9 * * *", TZ: "UTC"}, from)
	if err != nil {
		t.Fatalf("initialNextRun: %v", err)
	}
	if want := from.Format(time.RFC3339); got != want {
		t.Fatalf("cron initial got %s want %s", got, want)
	}
}

// fakeFirer records the schedules it was asked to fire.
type fakeFirer struct {
	mu     sync.Mutex // tickAt fires due schedules concurrently
	fired  []string
	status string
	err    error
}

func (f *fakeFirer) fire(_ context.Context, sch Schedule, _ time.Time) (string, string, error) {
	f.mu.Lock()
	f.fired = append(f.fired, sch.ID)
	f.mu.Unlock()
	if f.status == "" {
		return "fired", "", f.err
	}
	return f.status, "", f.err
}

func newSchedTestStore(t *testing.T) (*sqlStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st, ctx
}

func TestScheduleStoreCRUD(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	sc := Schedule{
		ID: "sch_1", MembershipID: "m1", TenantID: "default", OwnerConv: "conv1",
		SpecKind: "cron", Spec: "0 9 * * *", SpecLabel: "毎朝9時", TZ: "Asia/Tokyo",
		WakePolicy: "wake", SessionMode: "new", AgentKind: "claude",
		Repo: "agent-fleet", NewBranch: true, Prompt: "review PRs",
		OverlapPolicy: "skip", Enabled: true,
		NextRun: "2026-07-23T00:00:00Z", CreatedAt: nowTS(), UpdatedAt: nowTS(),
	}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, ok, err := st.GetSchedule(ctx, "sch_1")
	if err != nil || !ok {
		t.Fatalf("get: %v ok=%v", err, ok)
	}
	if got.SpecLabel != "毎朝9時" || !got.NewBranch || !got.Enabled || got.TZ != "Asia/Tokyo" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	list, err := st.ListSchedules(ctx, "m1")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v n=%d", err, len(list))
	}
	// Another member sees nothing.
	if other, _ := st.ListSchedules(ctx, "m2"); len(other) != 0 {
		t.Fatalf("cross-member leak: %d", len(other))
	}

	// Enabled toggle clears/sets next_run.
	if err := st.SetScheduleEnabled(ctx, "sch_1", "m1", false, "", nowTS()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, _, _ = st.GetSchedule(ctx, "sch_1")
	if got.Enabled || got.NextRun != "" {
		t.Fatalf("disable not applied: %+v", got)
	}

	// Delete is membership-scoped: a foreign member cannot delete.
	if err := st.DeleteSchedule(ctx, "sch_1", "m2"); err != nil {
		t.Fatalf("delete wrong member errored: %v", err)
	}
	if _, ok, _ := st.GetSchedule(ctx, "sch_1"); !ok {
		t.Fatal("foreign delete removed the row")
	}
	if err := st.DeleteSchedule(ctx, "sch_1", "m1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := st.GetSchedule(ctx, "sch_1"); ok {
		t.Fatal("row still present after owner delete")
	}
}

// TestMarkManualFirePending checks the run-now provenance flag: MarkManualFirePending sets
// next_run + manual_fire_pending, and the fire ledger (RecordScheduleFire) clears the flag
// so a subsequent automatic fire is not mis-tagged as manual (docs/log/38).
func TestMarkManualFirePending(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	sc := Schedule{
		ID: "sch_1", MembershipID: "m1", TenantID: "default", SpecKind: "interval", Spec: "3600",
		TZ: "UTC", Enabled: true, NextRun: "2999-01-01T00:00:00Z", CreatedAt: nowTS(), UpdatedAt: nowTS(),
	}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got, _, _ := st.GetSchedule(ctx, "sch_1"); got.ManualFirePending {
		t.Fatal("manual_fire_pending should default off")
	}
	if err := st.MarkManualFirePending(ctx, "sch_1", "m1", "2026-07-23T00:00:00Z", nowTS()); err != nil {
		t.Fatalf("mark: %v", err)
	}
	got, _, _ := st.GetSchedule(ctx, "sch_1")
	if !got.ManualFirePending || got.NextRun != "2026-07-23T00:00:00Z" {
		t.Fatalf("mark not applied: pending=%v next=%q", got.ManualFirePending, got.NextRun)
	}
	// A foreign member cannot flag someone else's schedule.
	if err := st.MarkManualFirePending(ctx, "sch_1", "m2", "2020-01-01T00:00:00Z", nowTS()); err != nil {
		t.Fatalf("cross-member mark errored: %v", err)
	}
	if got, _, _ := st.GetSchedule(ctx, "sch_1"); got.NextRun != "2026-07-23T00:00:00Z" {
		t.Fatal("cross-member mark mutated the row")
	}
	// A fire clears the flag.
	if err := st.RecordScheduleFire(ctx, "sch_1", nowTS(), "fired", "2999-01-01T00:00:00Z", true, nowTS()); err != nil {
		t.Fatalf("record fire: %v", err)
	}
	if got, _, _ := st.GetSchedule(ctx, "sch_1"); got.ManualFirePending {
		t.Fatal("manual_fire_pending should be cleared after a fire")
	}
}

// TestScheduleRunSessionTrigger round-trips the run-history columns added for the Console:
// the session a fire drove and whether it was a manual or scheduled fire.
func TestScheduleRunSessionTrigger(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	runs := []ScheduleRun{
		{ID: newID(), ScheduleID: "s", MembershipID: "m1", FiredAt: "2026-07-23T09:00:00Z", Status: "fired", Session: "sess-a", Trigger: "scheduled"},
		{ID: newID(), ScheduleID: "s", MembershipID: "m1", FiredAt: "2026-07-23T09:05:00Z", Status: "fired", Session: "sess-b", Trigger: "manual"},
	}
	for _, r := range runs {
		if err := st.AppendScheduleRun(ctx, r, 50); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	got, err := st.ListScheduleRuns(ctx, "s", "m1", 50)
	if err != nil || len(got) != 2 {
		t.Fatalf("list: %v n=%d", err, len(got))
	}
	// Newest first: the manual run.
	if got[0].Session != "sess-b" || got[0].Trigger != "manual" {
		t.Fatalf("row0 = %+v", got[0])
	}
	if got[1].Session != "sess-a" || got[1].Trigger != "scheduled" {
		t.Fatalf("row1 = %+v", got[1])
	}
}

func TestListDueSchedules(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	base := Schedule{MembershipID: "m1", TenantID: "default", SpecKind: "cron", Spec: "0 9 * * *", TZ: "UTC", Enabled: true, CreatedAt: nowTS(), UpdatedAt: nowTS()}
	// due (past next_run)
	due := base
	due.ID, due.NextRun = "sch_due", "2026-07-22T09:00:00Z"
	// future
	future := base
	future.ID, future.NextRun = "sch_future", "2999-01-01T00:00:00Z"
	// disabled but past
	disabled := base
	disabled.ID, disabled.NextRun, disabled.Enabled = "sch_disabled", "2026-07-22T09:00:00Z", false
	// enabled but empty next_run (paused) must never be due
	paused := base
	paused.ID, paused.NextRun = "sch_paused", ""
	for _, s := range []Schedule{due, future, disabled, paused} {
		if err := st.CreateSchedule(ctx, s); err != nil {
			t.Fatalf("create %s: %v", s.ID, err)
		}
	}
	got, err := st.ListDueSchedules(ctx, "2026-07-22T12:00:00Z")
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(got) != 1 || got[0].ID != "sch_due" {
		ids := make([]string, len(got))
		for i, s := range got {
			ids[i] = s.ID
		}
		t.Fatalf("due set = %v, want [sch_due]", ids)
	}
}

// TestSchedulerTickAdvances drives the full tick: a due cron schedule fires once and
// its next_run advances to a future instant so it is not due again on the next tick.
func TestSchedulerTickAdvances(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	// next_run in the past so it is due now.
	sc := Schedule{
		ID: "sch_tick", MembershipID: "m1", TenantID: "default",
		SpecKind: "cron", Spec: "*/5 * * * *", TZ: "UTC", Enabled: true,
		NextRun: "2000-01-01T00:00:00Z", CreatedAt: nowTS(), UpdatedAt: nowTS(),
	}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatalf("create: %v", err)
	}
	ff := &fakeFirer{}
	sched := newScheduler(st, ff, time.Minute)
	sched.tick(ctx)

	if len(ff.fired) != 1 || ff.fired[0] != "sch_tick" {
		t.Fatalf("firer calls = %v, want [sch_tick]", ff.fired)
	}
	got, _, _ := st.GetSchedule(ctx, "sch_tick")
	if got.LastStatus != "fired" {
		t.Fatalf("last_status = %q, want fired", got.LastStatus)
	}
	if got.NextRun == "" || got.NextRun <= nowTS() {
		t.Fatalf("next_run not advanced to the future: %q", got.NextRun)
	}
	if !got.Enabled {
		t.Fatal("cron schedule should stay enabled")
	}
	// Second tick: no longer due, firer not called again.
	sched.tick(ctx)
	if len(ff.fired) != 1 {
		t.Fatalf("re-fired: %v", ff.fired)
	}
}

// TestSchedulerTickMembershipInactiveDisables: a schedule whose owner membership is gone
// is unreachable from every surface (the Console lists by membership, so does the run
// history, and so does the failure notification). Left enabled it would no-op on every
// slot forever where nobody can see it — so the first such outcome disables the row, and
// the ledger keeps the reason for whoever restores access.
func TestSchedulerTickMembershipInactiveDisables(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	sc := Schedule{
		ID: "sch_orphan", MembershipID: "m_gone", TenantID: "default",
		SpecKind: "cron", Spec: "*/5 * * * *", TZ: "UTC", Enabled: true,
		NextRun: "2000-01-01T00:00:00Z", CreatedAt: nowTS(), UpdatedAt: nowTS(),
	}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatalf("create: %v", err)
	}
	ff := &fakeFirer{status: statusMembershipInactive}
	sched := newScheduler(st, ff, time.Minute)
	sched.tick(ctx)

	got, _, _ := st.GetSchedule(ctx, "sch_orphan")
	if got.Enabled || got.NextRun != "" {
		t.Fatalf("orphan not disabled: enabled=%v next_run=%q", got.Enabled, got.NextRun)
	}
	if got.LastStatus != statusMembershipInactive {
		t.Fatalf("last_status = %q, want %q", got.LastStatus, statusMembershipInactive)
	}
	// The reason survives in the history for whoever restores access.
	runs, err := st.ListScheduleRuns(ctx, "sch_orphan", "m_gone", 10)
	if err != nil || len(runs) != 1 || runs[0].Status != statusMembershipInactive {
		t.Fatalf("run history = %+v (err %v), want one %s row", runs, err, statusMembershipInactive)
	}
	// No further ticking: the invisible row stops consuming slots (and stops appending
	// run/notification rows nobody can read).
	sched.tick(ctx)
	if len(ff.fired) != 1 {
		t.Fatalf("orphan re-fired: %v", ff.fired)
	}
}

// TestSchedulerTickOnceDisables: a once schedule fires and then disables itself.
func TestSchedulerTickOnceDisables(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	sc := Schedule{
		ID: "sch_once", MembershipID: "m1", TenantID: "default",
		SpecKind: "once", Spec: "2000-01-01T00:00:00Z", TZ: "UTC", Enabled: true,
		NextRun: "2000-01-01T00:00:00Z", CreatedAt: nowTS(), UpdatedAt: nowTS(),
	}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatalf("create: %v", err)
	}
	ff := &fakeFirer{}
	sched := newScheduler(st, ff, time.Minute)
	sched.tick(ctx)

	got, _, _ := st.GetSchedule(ctx, "sch_once")
	if got.Enabled || got.NextRun != "" {
		t.Fatalf("once not disabled after fire: enabled=%v next_run=%q", got.Enabled, got.NextRun)
	}
	// Not due anymore.
	sched.tick(ctx)
	if len(ff.fired) != 1 {
		t.Fatalf("once re-fired: %v", ff.fired)
	}
}
