package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

func TestPushSubscriptionsSQLite(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkPushSubscriptions(t, st)
}

func TestPushSubscriptionsPostgres(t *testing.T) {
	url, ok := pgtest.Schema(t)
	if !ok {
		t.Skip("set AF_TEST_DATABASE_URL to run against Postgres")
	}
	st, err := OpenPostgres(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkPushSubscriptions(t, st)
}

func checkPushSubscriptions(t *testing.T, st *SQL) {
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
	const ep = "https://fcm.googleapis.com/fcm/send/one"
	sub := PushSubscription{MembershipID: a, Endpoint: ep, P256dh: "k1", Auth: "a1", UserAgent: "ua"}
	for i := 0; i < 2; i++ { // a repeat is one row
		if err := st.UpsertPushSubscription(ctx, sub); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.ListPushSubscriptions(ctx, a)
	if err != nil || len(rows) != 1 || rows[0].ID != PushSubscriptionID(ep) || rows[0].P256dh != "k1" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}

	// The same browser signing in as another member takes the endpoint over.
	sub.MembershipID, sub.P256dh = b, "k2"
	if err := st.UpsertPushSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.ListPushSubscriptions(ctx, a); len(rows) != 0 {
		t.Fatalf("previous member still holds the endpoint: %+v", rows)
	}
	rows, _ = st.ListPushSubscriptions(ctx, b)
	if len(rows) != 1 || rows[0].P256dh != "k2" {
		t.Fatalf("rows=%+v", rows)
	}

	// Removal is scoped to the caller's membership.
	if ok, err := st.DeletePushSubscription(ctx, a, rows[0].ID); err != nil || ok {
		t.Fatalf("another member removed it: ok=%v err=%v", ok, err)
	}
	if err := st.TouchPushSubscription(ctx, ep, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if rows, _ = st.ListPushSubscriptions(ctx, b); rows[0].LastOKAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("last_ok_at not stored: %+v", rows)
	}
	if ok, err := st.DeletePushSubscription(ctx, b, rows[0].ID); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	_ = st.UpsertPushSubscription(ctx, sub)
	if err := st.DeletePushSubscriptionByEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	if rows, _ = st.ListPushSubscriptions(ctx, b); len(rows) != 0 {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestNotificationHookFiresOncePerRow(t *testing.T) {
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
	m, _ := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	var got []string
	st.SetNotificationHook(func(n Notification) { got = append(got, n.EventID) })
	n := Notification{EventID: "e1", MembershipID: m.ID, Kind: "question", Payload: "{}", CreatedAt: NowTS()}
	for i := 0; i < 3; i++ { // the Agent re-sends until acked
		if err := st.InsertNotification(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 {
		t.Fatalf("hook fired %d times for one row", len(got))
	}
}
