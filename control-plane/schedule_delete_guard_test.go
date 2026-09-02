package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func newGuardTestStore(t *testing.T) (*store.SQL, context.Context) {
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
	return st, ctx
}

func mustCreateSchedule(t *testing.T, ctx context.Context, st *store.SQL, sc store.Schedule) {
	t.Helper()
	sc.SpecKind = "interval"
	sc.Spec = "3600"
	sc.CreatedAt = store.NowTS()
	sc.UpdatedAt = sc.CreatedAt
	if sc.ID == "" {
		sc.ID = store.NewID()
	}
	if sc.TenantID == "" {
		sc.TenantID = "default"
	}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
}

func TestScheduleGuardErrBlocksRepoInUseByEnabledSchedule(t *testing.T) {
	st, ctx := newGuardTestStore(t)
	mustCreateSchedule(t, ctx, st, store.Schedule{
		MembershipID: "m1", SpecLabel: "毎朝レビュー",
		Repo: "/home/dev/repos/agent-fleet@schedule-cli-release-watch", Enabled: true,
	})

	err := scheduleGuardErr(ctx, st, "m1", "agent-fleet@schedule-cli-release-watch", "")
	if err == nil {
		t.Fatal("expected the delete to be refused, got nil error")
	}
	if !strings.Contains(err.Error(), "毎朝レビュー") {
		t.Errorf("error %q should name the schedule that references the repo", err.Error())
	}
}

func TestScheduleGuardErrBlocksSessionInUseByReuseTarget(t *testing.T) {
	st, ctx := newGuardTestStore(t)
	mustCreateSchedule(t, ctx, st, store.Schedule{
		MembershipID: "m1", SpecLabel: "cli-watch", ReuseTarget: "sess-pinned", Enabled: true,
	})

	if err := scheduleGuardErr(ctx, st, "m1", "", "sess-pinned"); err == nil {
		t.Fatal("expected the delete to be refused for a session pinned via reuse_target")
	}
}

func TestScheduleGuardErrBlocksSessionInUseByReuseSession(t *testing.T) {
	st, ctx := newGuardTestStore(t)
	mustCreateSchedule(t, ctx, st, store.Schedule{
		MembershipID: "m1", SpecLabel: "cli-watch", ReuseSession: "sess-live", Enabled: true,
	})

	if err := scheduleGuardErr(ctx, st, "m1", "", "sess-live"); err == nil {
		t.Fatal("expected the delete to be refused for the current reuse_session")
	}
}

func TestScheduleGuardErrAllowsWhenScheduleDisabled(t *testing.T) {
	st, ctx := newGuardTestStore(t)
	mustCreateSchedule(t, ctx, st, store.Schedule{
		MembershipID: "m1", Repo: "/home/dev/repos/myrepo", ReuseTarget: "sess1", Enabled: false,
	})

	if err := scheduleGuardErr(ctx, st, "m1", "myrepo", ""); err != nil {
		t.Errorf("disabled schedule must not block repo delete, got %v", err)
	}
	if err := scheduleGuardErr(ctx, st, "m1", "", "sess1"); err != nil {
		t.Errorf("disabled schedule must not block session delete, got %v", err)
	}
}

func TestScheduleGuardErrAllowsUnrelatedTarget(t *testing.T) {
	st, ctx := newGuardTestStore(t)
	mustCreateSchedule(t, ctx, st, store.Schedule{
		MembershipID: "m1", Repo: "/home/dev/repos/other-repo", ReuseTarget: "other-sess", Enabled: true,
	})

	if err := scheduleGuardErr(ctx, st, "m1", "myrepo", ""); err != nil {
		t.Errorf("unrelated repo name must not be blocked, got %v", err)
	}
	if err := scheduleGuardErr(ctx, st, "m1", "", "sess1"); err != nil {
		t.Errorf("unrelated session name must not be blocked, got %v", err)
	}
}

func TestScheduleGuardErrIsScopedToMembership(t *testing.T) {
	st, ctx := newGuardTestStore(t)
	mustCreateSchedule(t, ctx, st, store.Schedule{
		MembershipID: "m2", Repo: "/home/dev/repos/myrepo", Enabled: true,
	})

	if err := scheduleGuardErr(ctx, st, "m1", "myrepo", ""); err != nil {
		t.Errorf("another membership's schedule must not block this member's delete, got %v", err)
	}
}

func TestScheduleDeleteGuardMatchesOnlyExactRepoAndSessionDeleteRoutes(t *testing.T) {
	st, ctx := newGuardTestStore(t)
	mustCreateSchedule(t, ctx, st, store.Schedule{
		MembershipID: "m1", SpecLabel: "watch", Repo: "/home/dev/repos/myrepo",
		ReuseTarget: "sess1", Enabled: true,
	})

	newReq := func(method, path, name string) *http.Request {
		r := httptest.NewRequest(method, path, nil)
		r.SetPathValue("name", name)
		return r
	}

	cases := []struct {
		name    string
		req     *http.Request
		wantErr bool
	}{
		{"DELETE repo in use", newReq(http.MethodDelete, "/api/repos/myrepo", "myrepo"), true},
		{"DELETE session in use", newReq(http.MethodDelete, "/api/sessions/sess1", "sess1"), true},
		{"DELETE unrelated repo", newReq(http.MethodDelete, "/api/repos/other", "other"), false},
		{"DELETE repo branch sub-route is not the repo delete", newReq(http.MethodDelete, "/api/repos/myrepo/branch", "myrepo"), false},
		{"GET is never guarded", newReq(http.MethodGet, "/api/repos/myrepo", "myrepo"), false},
		{"POST checkout is never guarded", newReq(http.MethodPost, "/api/repos/myrepo/checkout", "myrepo"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := scheduleDeleteGuard(ctx, st, "m1", tc.req)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}
