package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

// MarkScheduleRunNotExecuted (#1257) rewrites exactly the run of that session and slot, once:
// a repeated report finds the run it marked and changes nothing, and never moves on to the
// session's next fired run. On both dialects.
func TestMarkScheduleRunNotExecuted(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, st *SQL) {
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		for i, r := range []ScheduleRun{
			{FiredAt: "2026-10-03T09:00:01Z", Slot: "2026-10-03T09:00:00Z", Status: "fired", Session: "s1"},
			{FiredAt: "2026-10-03T09:05:01Z", Slot: "2026-10-03T09:05:00Z", Status: "fired_rotated", Session: "s1"},
			{FiredAt: "2026-10-03T09:10:01Z", Slot: "2026-10-03T09:10:00Z", Status: "fired", Session: "s1"},
			{FiredAt: "2026-10-03T09:05:02Z", Slot: "2026-10-03T09:05:00Z", Status: "skipped_overlap", Session: "s2"},
			{FiredAt: "2026-10-03T08:55:01Z", Status: "fired", Session: "s1"}, // recorded before slots were
		} {
			r.ID, r.ScheduleID, r.MembershipID = "r"+string(rune('a'+i)), "s", "m1"
			if err := st.AppendScheduleRun(ctx, r, 50); err != nil {
				t.Fatal(err)
			}
		}
		mark := func(mid, session, slot string) (bool, bool) {
			t.Helper()
			found, changed, err := st.MarkScheduleRunNotExecuted(ctx, "s", mid, session, slot, "error:not executed", "archived")
			if err != nil {
				t.Fatal(err)
			}
			return found, changed
		}
		if f, c := mark("m1", "s1", "2026-10-03T09:05:00Z"); !f || !c {
			t.Fatalf("first report = %v, %v", f, c)
		}
		if f, c := mark("m1", "s1", "2026-10-03T09:05:00Z"); !f || c {
			t.Fatalf("repeated report = %v, %v, want found and unchanged", f, c)
		}
		if f, _ := mark("m2", "s1", "2026-10-03T09:00:00Z"); f {
			t.Fatal("another membership's report matched")
		}
		if f, c := mark("m1", "s2", "2026-10-03T09:05:00Z"); !f || c {
			t.Fatalf("a run that never fired = %v, %v", f, c)
		}
		if f, _ := mark("m1", "s1", "2026-10-03T08:55:00Z"); f {
			t.Fatal("a run with no slot matched")
		}
		if f, _ := mark("m1", "s1", ""); f {
			t.Fatal("an empty slot matched")
		}
		rows, err := st.ListScheduleRuns(ctx, "s", "m1", 50)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range rows {
			got = append(got, r.FiredAt[11:16]+"="+r.Status+"/"+r.Detail)
		}
		want := "09:10=fired/ 09:05=skipped_overlap/ 09:05=error:not executed/archived 09:00=fired/ 08:55=fired/"
		if strings.Join(got, " ") != want {
			t.Fatalf("runs = %v, want %s", got, want)
		}
		if rows[0].Slot != "2026-10-03T09:10:00Z" {
			t.Fatalf("slot not round-tripped: %+v", rows[0])
		}
	}
	t.Run("sqlite", func(t *testing.T) {
		st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		run(t, st)
	})
	t.Run("postgres", func(t *testing.T) {
		url, ok := pgtest.Schema(t)
		if !ok {
			t.Skip("set AF_TEST_DATABASE_URL to run against Postgres")
		}
		st, err := OpenPostgres(url)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		run(t, st)
	})
}

// #1560: the delivery columns round-trip, and a silent mark changes only a plainly fired run.
func TestScheduleDeliveryAndSilentRun(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, st *SQL) {
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		sc := Schedule{ID: "s", MembershipID: "m1", TenantID: "default", SpecKind: "cron", Spec: "0 9 * * *",
			Prompt: "x", Report: true, DeliverTo: "notifications,slack", Silent: true, Enabled: true}
		if err := st.CreateSchedule(ctx, sc); err != nil {
			t.Fatal(err)
		}
		got, _, err := st.GetSchedule(ctx, "s")
		if err != nil || got.DeliverTo != "notifications,slack" || !got.Silent {
			t.Fatalf("created: deliver_to=%q silent=%v err=%v", got.DeliverTo, got.Silent, err)
		}
		sc.DeliverTo, sc.Silent = "", false
		if err := st.UpdateSchedule(ctx, sc); err != nil {
			t.Fatal(err)
		}
		if got, _, _ = st.GetSchedule(ctx, "s"); got.DeliverTo != "" || got.Silent {
			t.Fatalf("updated: deliver_to=%q silent=%v", got.DeliverTo, got.Silent)
		}
		for i, r := range []ScheduleRun{
			{FiredAt: "2026-10-03T09:00:01Z", Slot: "2026-10-03T09:00:00Z", Status: "fired", Session: "s1"},
			{FiredAt: "2026-10-03T09:05:01Z", Slot: "2026-10-03T09:05:00Z", Status: "fired_rotated", Session: "s1"},
			{FiredAt: "2026-10-03T09:10:01Z", Slot: "2026-10-03T09:10:00Z", Status: "error:not executed", Session: "s1"},
		} {
			r.ID, r.ScheduleID, r.MembershipID = "r"+string(rune('a'+i)), "s", "m1"
			if err := st.AppendScheduleRun(ctx, r, 50); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct {
			mid, slot      string
			found, changed bool
		}{
			{"m1", "2026-10-03T09:00:00Z", true, true},
			{"m1", "2026-10-03T09:00:00Z", true, false}, // repeated
			{"m1", "2026-10-03T09:05:00Z", true, false}, // a rotation fire is not a plain fire
			{"m1", "2026-10-03T09:10:00Z", true, false}, // a failure is never overwritten
			{"m2", "2026-10-03T09:00:00Z", false, false},
			{"m1", "", false, false},
		} {
			f, c, err := st.MarkScheduleRunSilent(ctx, "s", tc.mid, "s1", tc.slot)
			if err != nil || f != tc.found || c != tc.changed {
				t.Fatalf("%+v: found=%v changed=%v err=%v", tc, f, c, err)
			}
		}
		rows, _ := st.ListScheduleRuns(ctx, "s", "m1", 50)
		var got2 []string
		for _, r := range rows {
			got2 = append(got2, r.FiredAt[11:16]+"="+r.Status)
		}
		if want := "09:10=error:not executed 09:05=fired_rotated 09:00=fired_silent"; strings.Join(got2, " ") != want {
			t.Fatalf("runs = %v, want %s", got2, want)
		}
	}
	t.Run("sqlite", func(t *testing.T) {
		st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		run(t, st)
	})
	t.Run("postgres", func(t *testing.T) {
		url, ok := pgtest.Schema(t)
		if !ok {
			t.Skip("set AF_TEST_DATABASE_URL to run against Postgres")
		}
		st, err := OpenPostgres(url)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		run(t, st)
	})
}
