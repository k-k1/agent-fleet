package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

// MarkScheduleRunNotExecuted (#1257) rewrites only the first fired run of the session from the
// slot on, on both dialects (the LIKE and the ORDER BY … LIMIT subquery inside UPDATE).
func TestMarkScheduleRunNotExecuted(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, st *SQL) {
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		for i, r := range []ScheduleRun{
			{FiredAt: "2026-10-03T09:00:01Z", Status: "fired", Session: "s1"},
			{FiredAt: "2026-10-03T09:05:01Z", Status: "fired_rotated", Session: "s1"},
			{FiredAt: "2026-10-03T09:10:01Z", Status: "fired", Session: "s1"},
			{FiredAt: "2026-10-03T09:05:02Z", Status: "skipped_overlap", Session: "s2"},
		} {
			r.ID, r.ScheduleID, r.MembershipID = "r"+string(rune('a'+i)), "s", "m1"
			if err := st.AppendScheduleRun(ctx, r, 50); err != nil {
				t.Fatal(err)
			}
		}
		found, err := st.MarkScheduleRunNotExecuted(ctx, "s", "m1", "s1", "2026-10-03T09:05:00Z", "error:not executed", "archived")
		if err != nil || !found {
			t.Fatalf("mark = %v, %v", found, err)
		}
		if found, _ := st.MarkScheduleRunNotExecuted(ctx, "s", "m2", "s1", "2026-10-03T09:00:00Z", "x", "x"); found {
			t.Fatal("another membership's mark matched")
		}
		if found, _ := st.MarkScheduleRunNotExecuted(ctx, "s", "m1", "s2", "2026-10-03T09:00:00Z", "x", "x"); found {
			t.Fatal("a run that never fired was marked")
		}
		rows, err := st.ListScheduleRuns(ctx, "s", "m1", 50)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range rows {
			got = append(got, r.FiredAt[11:16]+"="+r.Status+"/"+r.Detail)
		}
		want := "09:10=fired/ 09:05=skipped_overlap/ 09:05=error:not executed/archived 09:00=fired/"
		if strings.Join(got, " ") != want {
			t.Fatalf("runs = %v, want %s", got, want)
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
