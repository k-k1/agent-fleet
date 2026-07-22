package main

import (
	"context"
	"testing"
	"time"
)

// realMembership creates a genuine membership row so notification inserts (which FK to
// membership) succeed, and returns its id.
func realMembership(t *testing.T, st *sqlStore, ctx context.Context) string {
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

func TestJitterAppliedToCronOnly(t *testing.T) {
	old := scheduleJitterMax
	t.Cleanup(func() { scheduleJitterMax = old })
	scheduleJitterMax = 120 * time.Second

	from := time.Date(2026, 7, 22, 6, 0, 0, 0, time.UTC)

	// cron: next_run = base 09:00 + deterministic jitter, so the seconds are non-zero
	// for this id (and within the window).
	cron := Schedule{ID: "sch_jit", SpecKind: "cron", Spec: "0 9 * * *", TZ: "UTC"}
	got, err := initialNextRun(cron, from)
	if err != nil {
		t.Fatalf("cron initial: %v", err)
	}
	ts, _ := time.Parse(time.RFC3339, got)
	base := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	off := ts.Sub(base)
	if off < 0 || off > 120*time.Second {
		t.Fatalf("cron next_run %s not within jitter window of 09:00", got)
	}
	if off != scheduleJitter("sch_jit") {
		t.Fatalf("cron jitter %s != scheduleJitter %s", off, scheduleJitter("sch_jit"))
	}

	// interval: jitter must NOT be applied (would drift each period).
	iv := Schedule{ID: "sch_iv", SpecKind: "interval", Spec: "3600"}
	gotIv, _ := initialNextRun(iv, from)
	if want := from.Add(time.Hour).UTC().Format(time.RFC3339); gotIv != want {
		t.Fatalf("interval jittered: got %s want %s", gotIv, want)
	}

	// once: exact instant, never jittered.
	once := Schedule{ID: "sch_once", SpecKind: "once", Spec: "2026-07-22T09:30:00Z"}
	gotOnce, _ := initialNextRun(once, from)
	if gotOnce != "2026-07-22T09:30:00Z" {
		t.Fatalf("once jittered: %s", gotOnce)
	}
}

func TestScheduleNotifyStatus(t *testing.T) {
	notify := []string{"error:boom", "error", "skipped_quota", "skipped_rate_limited", "skipped_membership_inactive"}
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
	sc := Schedule{
		ID: "sch_f", MembershipID: mid, TenantID: "default",
		SpecKind: "cron", Spec: "*/5 * * * *", TZ: "UTC", Enabled: true, SpecLabel: "毎朝レビュー",
		NextRun: "2000-01-01T00:00:00Z", CreatedAt: nowTS(), UpdatedAt: nowTS(),
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
	_ = st.RecordScheduleFire(ctx, sc.ID, "", "", "2000-01-01T00:00:00Z", true, nowTS())
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
	sc := Schedule{
		ID: "sch_ok", MembershipID: "m1", TenantID: "default",
		SpecKind: "cron", Spec: "*/5 * * * *", TZ: "UTC", Enabled: true,
		NextRun: "2000-01-01T00:00:00Z", CreatedAt: nowTS(), UpdatedAt: nowTS(),
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
