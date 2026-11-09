package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestWatchIngressStoreBoundsAndRunningOnly(t *testing.T) {
	limitBoardStores(t, watchIngressStoreCase)
}

func watchIngressStoreCase(t *testing.T, st *SQL) {
	ctx := context.Background()
	tn, err := st.CreateTenant(ctx, "wt", "wt")
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	var rows []WatchSubjectRow
	for i := 1; i <= 3; i++ {
		rows = append(rows, WatchSubjectRow{"github", "github:o/r#" + strconv.Itoa(i)})
	}
	if err := st.ReplaceWatchSubjects(ctx, "m1", tn.ID, rows, exp, 2); !errors.Is(err, ErrWatchLimit) {
		t.Fatalf("3 rows over a max of 2: %v", err)
	}
	if err := st.ReplaceWatchSubjects(ctx, "m1", tn.ID, rows[:2], exp, 2); err != nil {
		t.Fatal(err)
	}
	// Only a membership whose workspace is running is a target, and only inside the asked tenant.
	tn2, ms := limitMembers(t, st, "up", "down")
	for i, m := range ms {
		state := []string{"running", "stopped"}[i]
		w := Workspace{ID: NewID(), TenantID: tn2.ID, MembershipID: m.ID, ContainerName: "c" + state, Network: "n", DataDir: "d" + state,
			AgentPort: "1", AgentToken: "t", State: state, CreatedAt: NowTS()}
		if err := st.CreateWorkspace(ctx, w); err != nil {
			t.Fatal(err)
		}
		if err := st.ReplaceWatchSubjects(ctx, m.ID, tn2.ID, rows[:1], exp, 2); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.WatchTargets(ctx, rows[0].Subject, "", NowTS(), 10)
	if err != nil || len(got) != 1 || got[0].MembershipID != ms[0].ID {
		t.Fatalf("targets = %v %v, want only the running workspace", got, err)
	}
	if got, _ := st.WatchTargets(ctx, rows[0].Subject, tn.ID, NowTS(), 10); len(got) != 0 {
		t.Fatalf("another tenant's workspace is a target: %v", got)
	}
	for i := 0; i < 2; i++ {
		h := WatchHook{ID: "h" + strconv.Itoa(i), TenantID: tn.ID, MembershipID: "m1", Source: "github", CreatedAt: NowTS()}
		err := st.CreateWatchHook(ctx, h, 1)
		if (i == 0) != (err == nil) || (i == 1 && !errors.Is(err, ErrWatchLimit)) {
			t.Fatalf("hook %d with a max of 1: %v", i, err)
		}
	}
}

// A deleted membership leaves no hook nobody can delete and no registration behind.
func TestDeleteMembershipRemovesItsWatchRows(t *testing.T) {
	limitBoardStores(t, func(t *testing.T, st *SQL) {
		ctx := context.Background()
		tn, ms := limitMembers(t, st, "gone")
		exp := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
		if err := st.ReplaceWatchSubjects(ctx, ms[0].ID, tn.ID, []WatchSubjectRow{{"github", "github:o/r#1"}}, exp, 5); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateWatchHook(ctx, WatchHook{ID: "hk", TenantID: tn.ID, MembershipID: ms[0].ID, Source: "github", CreatedAt: NowTS()}, 5); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteMembership(ctx, ms[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := st.GetWatchHook(ctx, "hk"); ok {
			t.Fatal("the hook of a deleted membership is still there")
		}
		var n int
		if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM watch_subject WHERE membership_id=?`, ms[0].ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("registrations left: %d %v", n, err)
		}
	})
}
