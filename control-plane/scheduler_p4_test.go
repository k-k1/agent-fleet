package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// realMembership creates a genuine membership row so notification inserts (which FK to
// membership) succeed, and returns its id.
func realMembership(t *testing.T, st *store.SQL, ctx context.Context) string {
	t.Helper()
	tn, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	id, err := st.UpsertIdentity(ctx, "sched@test", "sched-user", "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	m, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	return m.ID
}

func TestScheduleJitterDeterministic(t *testing.T) {
	old := scheduleJitterMax
	t.Cleanup(func() { scheduleJitterMax = old })

	scheduleJitterMax = 0
	if scheduleJitter("sch_1") != 0 {
		t.Error("jitter must be 0 when disabled")
	}

	scheduleJitterMax = 120 * time.Second
	a := scheduleJitter("sch_1")
	b := scheduleJitter("sch_1")
	if a != b {
		t.Fatalf("non-deterministic: %s vs %s", a, b)
	}
	if a < 0 || a > 120*time.Second {
		t.Fatalf("jitter %s out of [0,120s]", a)
	}
	// Different ids should (very likely) differ; at least one of a few must.
	diff := false
	for _, id := range []string{"sch_2", "sch_3", "sch_4", "sch_5"} {
		if scheduleJitter(id) != a {
			diff = true
		}
	}
	if !diff {
		t.Error("jitter identical across 5 distinct ids (hash suspicious)")
	}
	// Empty id => 0.
	if scheduleJitter("") != 0 {
		t.Error("empty id jitter must be 0")
	}
}

// TestJitterNotBakedIntoNextRun: jitter is a fire-time GATE (see tick), NOT baked into
// next_run — so the operator's read-back, next_run_local and {{time}} all show the nominal
// time the user asked for. jitterForSchedule exposes the deferral, cron-only.
func TestJitterNotBakedIntoNextRun(t *testing.T) {
	old := scheduleJitterMax
	t.Cleanup(func() { scheduleJitterMax = old })
	scheduleJitterMax = 120 * time.Second

	from := time.Date(2026, 7, 22, 6, 0, 0, 0, time.UTC)

	// cron: next_run is the EXACT nominal instant, no jitter mixed in.
	cron := store.Schedule{ID: "sch_jit", SpecKind: "cron", Spec: "0 9 * * *", TZ: "UTC"}
	got, err := initialNextRun(cron, from)
	if err != nil {
		t.Fatalf("cron initial: %v", err)
	}
	if want := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC).UTC().Format(time.RFC3339); got != want {
		t.Fatalf("cron next_run carries jitter: got %s want nominal %s (jitter must be a gate)", got, want)
	}
	// The deferral lives in jitterForSchedule: cron gets it, interval/once do not.
	if jitterForSchedule(cron) != scheduleJitter("sch_jit") {
		t.Fatalf("cron jitterForSchedule %s != scheduleJitter %s", jitterForSchedule(cron), scheduleJitter("sch_jit"))
	}

	// interval: no jitter, next_run = from + interval.
	iv := store.Schedule{ID: "sch_iv", SpecKind: "interval", Spec: "3600"}
	if jitterForSchedule(iv) != 0 {
		t.Fatalf("interval must not jitter")
	}
	if gotIv, _ := initialNextRun(iv, from); gotIv != from.Add(time.Hour).UTC().Format(time.RFC3339) {
		t.Fatalf("interval next_run wrong: %s", gotIv)
	}

	// once: exact instant, no jitter.
	once := store.Schedule{ID: "sch_once", SpecKind: "once", Spec: "2026-07-22T09:30:00Z"}
	if jitterForSchedule(once) != 0 {
		t.Fatalf("once must not jitter")
	}
	if gotOnce, _ := initialNextRun(once, from); gotOnce != "2026-07-22T09:30:00Z" {
		t.Fatalf("once jittered: %s", gotOnce)
	}
}

// TestJitterGateDefersCronFire: a due cron schedule is held back until now reaches
// next_run + its jitter (spreads aligned 09:00 wakes), then fires; a once with a due slot
// fires immediately (no jitter) even when jitter is enabled.
func TestJitterGateDefersCronFire(t *testing.T) {
	old := scheduleJitterMax
	t.Cleanup(func() { scheduleJitterMax = old })
	scheduleJitterMax = 120 * time.Second

	// Find an id whose jitter is a meaningful window so the deferral is observable.
	id, j := "", time.Duration(0)
	for i := 0; i < 50; i++ {
		cand := fmt.Sprintf("sch_gate%d", i)
		if d := scheduleJitter(cand); d >= 60*time.Second {
			id, j = cand, d
			break
		}
	}
	if id == "" {
		t.Skip("no id with jitter>=60s found")
	}

	st, ctx := newSchedTestStore(t)
	T0 := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	cron := store.Schedule{
		ID: id, MembershipID: "m1", TenantID: "default", SpecKind: "cron", Spec: "0 9 * * *",
		TZ: "UTC", Enabled: true, NextRun: T0.Format(time.RFC3339), CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
	}
	if err := st.CreateSchedule(ctx, cron); err != nil {
		t.Fatalf("create cron: %v", err)
	}

	ff := &fakeFirer{}
	// Within the jitter window (now < T0+j): due by SQL, but the gate holds it.
	newScheduler(st, ff, time.Minute).tickAt(ctx, T0.Add(j-30*time.Second))
	if len(ff.fired) != 0 {
		t.Fatalf("cron fired inside jitter window: %v", ff.fired)
	}
	// At the window edge (now == T0+j): fires.
	newScheduler(st, ff, time.Minute).tickAt(ctx, T0.Add(j))
	if len(ff.fired) != 1 || ff.fired[0] != id {
		t.Fatalf("cron not fired after jitter window: %v", ff.fired)
	}

	// once: no jitter — a due once fires on the first tick even with jitter enabled.
	once := store.Schedule{
		ID: "sch_once_gate", MembershipID: "m1", TenantID: "default", SpecKind: "once",
		Spec: T0.Format(time.RFC3339), TZ: "UTC", Enabled: true,
		NextRun: T0.Format(time.RFC3339), CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
	}
	if err := st.CreateSchedule(ctx, once); err != nil {
		t.Fatalf("create once: %v", err)
	}
	ff2 := &fakeFirer{}
	newScheduler(st, ff2, time.Minute).tickAt(ctx, T0.Add(time.Second))
	if len(ff2.fired) != 1 || ff2.fired[0] != "sch_once_gate" {
		t.Fatalf("once not fired immediately (jitter must not gate once): %v", ff2.fired)
	}
}

func TestScheduleNotifyStatus(t *testing.T) {
	notify := []string{"error:boom", "error", "skipped_quota", "skipped_rate_limited",
		"skipped_membership_inactive", "skipped_target_missing", "skipped_overlap"}
	quiet := []string{"fired", "fired_noop", "skipped_stopped", ""}
	for _, s := range notify {
		if !scheduleNotifyStatus(s) {
			t.Errorf("status %q should notify", s)
		}
	}
	for _, s := range quiet {
		if scheduleNotifyStatus(s) {
			t.Errorf("status %q should NOT notify", s)
		}
	}
}

// TestSchedulerTickNotifiesFailure: a firer error produces an "error:" status, a run-
// history row, AND a notification-center entry (★3 — unattended failures aren't silent).
func TestSchedulerTickNotifiesFailure(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	mid := realMembership(t, st, ctx)
	sc := store.Schedule{
		ID: "sch_f", MembershipID: mid, TenantID: "default",
		SpecKind: "cron", Spec: "*/5 * * * *", TZ: "UTC", Enabled: true, SpecLabel: "毎朝レビュー",
		NextRun: "2000-01-01T00:00:00Z", CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
	}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatalf("create: %v", err)
	}
	ff := &fakeFirer{err: errTest("wake failed")}
	newScheduler(st, ff, time.Minute).tick(ctx)

	got, _, _ := st.GetSchedule(ctx, "sch_f")
	if got.LastStatus == "" || got.LastStatus[:5] != "error" {
		t.Fatalf("last_status = %q, want error:...", got.LastStatus)
	}
	notifs, err := st.ListNotifications(ctx, mid, "2000-01-01T00:00:00Z", 50)
	if err != nil {
		t.Fatalf("list notifs: %v", err)
	}
	if len(notifs) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifs))
	}
	if notifs[0].Kind != "schedule-failed" || notifs[0].TargetID != "sch_f" || notifs[0].DisplayName != "毎朝レビュー" {
		t.Fatalf("bad notification: %+v", notifs[0])
	}

	// Re-firing the SAME slot must not double-notify (deterministic EventID dedup).
	sc.NextRun = "2000-01-01T00:00:00Z"
	_ = st.RecordScheduleFire(ctx, sc.ID, "", "", "2000-01-01T00:00:00Z", true, store.NowTS())
	newScheduler(st, ff, time.Minute).tick(ctx)
	notifs2, _ := st.ListNotifications(ctx, mid, "2000-01-01T00:00:00Z", 50)
	if len(notifs2) != 1 {
		t.Fatalf("re-fire same slot double-notified: %d", len(notifs2))
	}
}

// TestSchedulerTickSuccessNoNotify: a successful fire reports itself via report_to, so
// the scheduler must NOT also raise a failure notification.
func TestSchedulerTickSuccessNoNotify(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	sc := store.Schedule{
		ID: "sch_ok", MembershipID: "m1", TenantID: "default",
		SpecKind: "cron", Spec: "*/5 * * * *", TZ: "UTC", Enabled: true,
		NextRun: "2000-01-01T00:00:00Z", CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
	}
	_ = st.CreateSchedule(ctx, sc)
	newScheduler(st, &fakeFirer{}, time.Minute).tick(ctx) // fakeFirer returns "fired"

	notifs, _ := st.ListNotifications(ctx, "m1", "2000-01-01T00:00:00Z", 50)
	if len(notifs) != 0 {
		t.Fatalf("success raised %d notifications, want 0", len(notifs))
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
