package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestNotificationOriginRoundTripSQLite(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, _ := st.EnsureDefaultTenant(ctx)
	id, _ := st.UpsertIdentity(ctx, "a@example.com", "a@example.com", "")
	m, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(ev, root string, child bool) Notification {
		return Notification{EventID: ev, MembershipID: m.ID, Kind: "k", Payload: "{}", CreatedAt: NowTS(), OriginRoot: root, OriginChild: child}
	}
	for _, n := range []Notification{mk("e1", "autorun", true), mk("e2", "", false)} {
		if err := st.InsertNotification(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.ListNotifications(ctx, m.ID, "", 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	for _, r := range rows {
		switch r.EventID {
		case "e1":
			if r.OriginRoot != "autorun" || !r.OriginChild {
				t.Errorf("e1 = %+v", r)
			}
		case "e2":
			// No origin set reads as the member's own, not a child.
			if r.OriginRoot != "own" || r.OriginChild {
				t.Errorf("e2 = %+v", r)
			}
		}
	}
}

// A save that fails on the origins column must leave the kinds as they were: both lists are one
// statement, so a failure of either half cannot commit the other.
func TestPutPushPrefsIsOneStatementSQLite(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, _ := st.EnsureDefaultTenant(ctx)
	id, _ := st.UpsertIdentity(ctx, "a@example.com", "a@example.com", "")
	m, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutPushPrefs(ctx, m.ID, []string{"question"}, &[]string{"schedule"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `CREATE TRIGGER fail_origins BEFORE UPDATE OF off_origins ON push_pref
		BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutPushPrefs(ctx, m.ID, []string{"answer-ready"}, &[]string{"autorun"}); err == nil {
		t.Fatal("the injected failure did not surface")
	}
	k, o, _ := st.GetPushPrefs(ctx, m.ID)
	if len(k) != 1 || k[0] != "question" || len(o) != 1 || o[0] != "schedule" {
		t.Fatalf("a failed save changed the stored prefs: %v %v", k, o)
	}
	// The injection is limited to the origins column: a kinds-only save still works.
	if k, _, err := st.PutPushPrefs(ctx, m.ID, []string{"answer-ready"}, nil); err != nil || len(k) != 1 || k[0] != "answer-ready" {
		t.Fatalf("kinds-only save: %v %v", k, err)
	}
}
