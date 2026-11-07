package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

func TestNotificationEventIDScopeSQLite(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkNotificationEventIDScope(t, st)
}

func TestNotificationEventIDScopePostgres(t *testing.T) {
	url, ok := pgtest.Schema(t)
	if !ok {
		t.Skip("set AF_TEST_DATABASE_URL to run against Postgres")
	}
	st, err := OpenPostgres(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkNotificationEventIDScope(t, st)
}

// checkNotificationEventIDScope: the same event id is one row per membership (a repeat is
// still dropped), mark-seen touches only the caller's row, and rows written under the
// deployment-wide UNIQUE survive the migration that replaced it.
func checkNotificationEventIDScope(t *testing.T, st *SQL) {
	t.Helper()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var mems []string
	for _, e := range []string{"a@example.com", "b@example.com"} {
		id, err := st.UpsertIdentity(ctx, e, e, "")
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
		if err != nil {
			t.Fatal(err)
		}
		mems = append(mems, m.ID)
	}
	a, b := mems[0], mems[1]
	n := func(m string) Notification {
		return Notification{EventID: "evt-1", MembershipID: m, Kind: "k", Payload: "{}", CreatedAt: NowTS()}
	}
	for _, m := range []string{a, b, a} {
		if err := st.InsertNotification(ctx, n(m)); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []string{a, b} {
		if rows, err := st.ListNotifications(ctx, m, "", 50); err != nil || len(rows) != 1 {
			t.Fatalf("membership %s rows=%d err=%v", m, len(rows), err)
		}
	}
	if err := st.MarkNotificationsSeen(ctx, a, []string{"evt-1"}, NowTS()); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.CountUnseenNotifications(ctx, a, ""); c != 0 {
		t.Fatalf("a unseen=%d", c)
	}
	if c, _ := st.CountUnseenNotifications(ctx, b, ""); c != 1 {
		t.Fatalf("b unseen=%d, mark-seen crossed memberships", c)
	}

	// Upgrade path: put the old shape back with a row of each member, forget the migration,
	// run it again.
	if _, err := st.db.ExecContext(ctx, `DELETE FROM notification WHERE membership_id=?`, b); err != nil {
		t.Fatal(err)
	}
	legacy := map[string][]string{
		"sqlite":   {`DROP INDEX uq_notification_membership_event`, `CREATE UNIQUE INDEX legacy_event ON notification(event_id)`},
		"postgres": {`DROP INDEX uq_notification_membership_event`, `ALTER TABLE notification ADD CONSTRAINT notification_event_id_key UNIQUE (event_id)`},
	}[st.dialect]
	for _, q := range legacy {
		if _, err := st.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	old := n(b)
	old.EventID = "evt-old"
	if _, err := st.db.ExecContext(ctx, `INSERT INTO notification(event_id, membership_id, kind, payload, created_at) VALUES(?,?,?,?,?)`,
		old.EventID, b, "k", "{}", old.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version=?`, scopeMigrationVersion[st.dialect]); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.ListNotifications(ctx, a, "", 50); len(rows) != 1 || rows[0].EventID != "evt-1" || rows[0].SeenAt == "" {
		t.Fatalf("a's row after migration = %+v", rows)
	}
	if err := st.InsertNotification(ctx, n(b)); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.ListNotifications(ctx, b, "", 50); len(rows) != 2 {
		t.Fatalf("b rows after migration=%d, want the old row and the repeated id", len(rows))
	}
}

var scopeMigrationVersion = map[string]int{"sqlite": 125, "postgres": 110}
