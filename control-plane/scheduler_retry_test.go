package main

import (
	"testing"
	"time"
)

// The retry seam (docs/log/38 ★7). A fire that never reached the Agent is a fire
// still in progress, not a verdict: the ledger stays put so a later tick re-attempts the
// SAME slot. Measured motivation: on ecs-ec2 the wake this fire itself started takes
// 65–131s and the old fixed budget dropped 3 of 5 consecutive mornings — for a daily cron,
// each drop cost the whole day.

// TestSchedulerRetriesUndeliveredFire: inside the window a retryable error leaves the row
// exactly as it was — same next_run, no status, no run history, no notification — and the
// next tick fires the same slot again. The eventual success is what lands in the ledger.
func TestSchedulerRetriesUndeliveredFire(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	mid := realMembership(t, st, ctx)
	slot := time.Date(2026, 8, 31, 0, 1, 0, 0, time.UTC)
	sc := Schedule{
		ID: "sch_retry", MembershipID: mid, TenantID: "default",
		SpecKind: "cron", Spec: "0 9 * * *", TZ: "Asia/Tokyo", Enabled: true, SpecLabel: "毎朝",
		NextRun: slot.Format(time.RFC3339), CreatedAt: nowTS(), UpdatedAt: nowTS(),
	}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatalf("create: %v", err)
	}

	ff := &fakeFirer{err: retryableFireErr(errTest("agent not ready: timed out waiting for agent"))}
	sched := newScheduler(st, ff, time.Minute)

	sched.tickAt(ctx, slot.Add(30*time.Second))
	got, _, _ := st.GetSchedule(ctx, "sch_retry")
	if got.NextRun != slot.Format(time.RFC3339) {
		t.Fatalf("next_run moved on a retryable failure: %q", got.NextRun)
	}
	if got.LastStatus != "" || got.LastRun != "" {
		t.Fatalf("ledger stamped mid-flight: status=%q last_run=%q", got.LastStatus, got.LastRun)
	}
	if runs, _ := st.ListScheduleRuns(ctx, "sch_retry", mid, 10); len(runs) != 0 {
		t.Fatalf("run history got %d rows for an in-flight fire, want 0", len(runs))
	}
	if n, _ := st.ListNotifications(ctx, mid, "2000-01-01T00:00:00Z", 50); len(n) != 0 {
		t.Fatalf("notified %d times for an in-flight fire, want 0", len(n))
	}

	// Still due: the next tick re-attempts the same slot, and this time it lands.
	ff.err = nil
	sched.tickAt(ctx, slot.Add(90*time.Second))
	if len(ff.fired) != 2 {
		t.Fatalf("fired %d times, want 2 (retry did not re-attempt)", len(ff.fired))
	}
	got, _, _ = st.GetSchedule(ctx, "sch_retry")
	if got.LastStatus != "fired" {
		t.Fatalf("last_status = %q, want fired", got.LastStatus)
	}
	if got.NextRun == slot.Format(time.RFC3339) || got.NextRun == "" {
		t.Fatalf("ledger did not advance after the successful retry: %q", got.NextRun)
	}
	runs, _ := st.ListScheduleRuns(ctx, "sch_retry", mid, 10)
	if len(runs) != 1 || runs[0].Status != "fired" {
		t.Fatalf("run history = %+v, want exactly one fired row", runs)
	}
	if n, _ := st.ListNotifications(ctx, mid, "2000-01-01T00:00:00Z", 50); len(n) != 0 {
		t.Fatalf("a fire that eventually succeeded notified %d times, want 0", len(n))
	}
}

// TestSchedulerRetryWindowIsBounded: past scheduleRetryWindow the same retryable error is
// final — recorded, notified and advanced. A workspace that can never come up must stop
// costing a wake every tick and must surface as a failure.
func TestSchedulerRetryWindowIsBounded(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	mid := realMembership(t, st, ctx)
	slot := time.Date(2026, 8, 31, 0, 1, 0, 0, time.UTC)
	sc := Schedule{
		ID: "sch_giveup", MembershipID: mid, TenantID: "default",
		SpecKind: "cron", Spec: "0 9 * * *", TZ: "UTC", Enabled: true, SpecLabel: "毎朝",
		NextRun: slot.Format(time.RFC3339), CreatedAt: nowTS(), UpdatedAt: nowTS(),
	}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatalf("create: %v", err)
	}
	ff := &fakeFirer{err: retryableFireErr(errTest("agent not ready"))}
	newScheduler(st, ff, time.Minute).tickAt(ctx, slot.Add(scheduleRetryWindow))

	got, _, _ := st.GetSchedule(ctx, "sch_giveup")
	if got.LastStatus == "" || got.LastStatus[:5] != "error" {
		t.Fatalf("last_status = %q, want error:...", got.LastStatus)
	}
	if got.NextRun == slot.Format(time.RFC3339) {
		t.Fatal("ledger stuck on the abandoned slot — it would re-fire forever")
	}
	if n, _ := st.ListNotifications(ctx, mid, "2000-01-01T00:00:00Z", 50); len(n) != 1 {
		t.Fatalf("notifications = %d, want 1 (the give-up must not be silent)", len(n))
	}
}

// TestSchedulerNonRetryableErrorIsFinal: only the firer may mark an error retryable, and
// it does so ONLY where nothing was delivered. A plain error inside the window is final —
// re-attempting it could deliver the prompt twice (assistant/reuse have no per-slot key).
func TestSchedulerNonRetryableErrorIsFinal(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	mid := realMembership(t, st, ctx)
	slot := time.Date(2026, 8, 31, 0, 1, 0, 0, time.UTC)
	sc := Schedule{
		ID: "sch_final", MembershipID: mid, TenantID: "default",
		SpecKind: "cron", Spec: "0 9 * * *", TZ: "UTC", Enabled: true,
		NextRun: slot.Format(time.RFC3339), CreatedAt: nowTS(), UpdatedAt: nowTS(),
	}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatalf("create: %v", err)
	}
	ff := &fakeFirer{err: errTest("inject: agent create_session 500")}
	newScheduler(st, ff, time.Minute).tickAt(ctx, slot.Add(30*time.Second))

	got, _, _ := st.GetSchedule(ctx, "sch_final")
	if got.LastStatus == "" || got.LastStatus[:5] != "error" {
		t.Fatalf("last_status = %q, want error:... (a post-delivery error must not retry)", got.LastStatus)
	}
	if got.NextRun == slot.Format(time.RFC3339) {
		t.Fatal("a non-retryable error left the slot due")
	}
}

// TestScheduleWakeBudgets pins the two budgets apart. The Agent-boot wait must be the same
// window the platform already grants a boot to call itself "starting" (agentBootBudget) —
// anything smaller silently means "schedules do not fire on substrates that boot slower
// than this". The per-session input-readiness wait must NOT follow it up: that one runs
// against a container that is already answering.
func TestScheduleWakeBudgets(t *testing.T) {
	f := newWakeFirer(nil, 5*time.Minute, agentBootBudget)
	if f.readyTimeout != agentBootBudget {
		t.Fatalf("readyTimeout = %v, want %v", f.readyTimeout, agentBootBudget)
	}
	if f.sessionReadyTimeout != scheduleSessionReadyWait {
		t.Fatalf("sessionReadyTimeout = %v, want %v", f.sessionReadyTimeout, scheduleSessionReadyWait)
	}
	if f.sessionReadyTimeout >= f.readyTimeout {
		t.Fatalf("session readiness wait %v must stay under the boot budget %v",
			f.sessionReadyTimeout, f.readyTimeout)
	}
	// The measured ecs-ec2 wake distribution (docs/log/64: ~110s for a sleeping slot,
	// ~135s to grow the pool, 131s observed end to end on acrt) must fit inside the
	// budget — that is the whole point of the number.
	if agentBootBudget < 150*time.Second {
		t.Fatalf("boot budget %v is below the measured ecs-ec2 wake times", agentBootBudget)
	}
}
