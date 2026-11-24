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
