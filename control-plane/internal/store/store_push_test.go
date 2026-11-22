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
	checkPushPrefs(t, st, tn.ID, a, b)
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
	// A stale result for the previous owner touches nothing.
	if err := st.TouchPushSubscription(ctx, a, ep, "2026-01-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if rows, _ = st.ListPushSubscriptions(ctx, b); rows[0].LastOKAt != "" {
		t.Fatalf("a stale touch reached the new owner's row: %+v", rows)
	}
	if err := st.TouchPushSubscription(ctx, b, ep, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if rows, _ = st.ListPushSubscriptions(ctx, b); rows[0].LastOKAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("last_ok_at not stored: %+v", rows)
	}
	if ok, err := st.DeletePushSubscription(ctx, b, rows[0].ID); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	_ = st.UpsertPushSubscription(ctx, sub)
	// A stale result for the previous owner must not delete the row that moved away from them.
	_ = st.DeletePushSubscriptionByEndpoint(ctx, a, ep)
	if rows, _ = st.ListPushSubscriptions(ctx, b); len(rows) != 1 {
		t.Fatalf("a stale delete removed the new owner's row: %+v", rows)
	}
	// Another member who knows the endpoint but not its keys cannot release it.
	if err := st.ReleasePushEndpoint(ctx, ep, "other", "other"); err != nil {
		t.Fatal(err)
	}
	if rows, _ = st.ListPushSubscriptions(ctx, b); len(rows) != 1 {
		t.Fatalf("a release with foreign keys removed the row: %+v", rows)
	}
	if err := st.ReleasePushEndpoint(ctx, ep, sub.P256dh, sub.Auth); err != nil {
		t.Fatal(err)
	}
	if rows, _ = st.ListPushSubscriptions(ctx, b); len(rows) != 0 {
		t.Fatalf("release left %+v", rows)
	}
	_ = st.UpsertPushSubscription(ctx, sub)
	if err := st.DeletePushSubscriptionByEndpoint(ctx, b, ep); err != nil {
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

// checkPushPrefs covers the part 3 tables: per-member opt-out, the tenant text switch and the
// active-membership check the sender relies on.
func checkPushPrefs(t *testing.T, st *SQL, tenantID, a, b string) {
	t.Helper()
	ctx := context.Background()
	if off, err := st.GetPushOffKinds(ctx, a); err != nil || len(off) != 0 {
		t.Fatalf("default off=%v err=%v", off, err)
	}
	if err := st.PutPushOffKinds(ctx, a, []string{"answer-ready"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutPushOffKinds(ctx, a, []string{"answer-ready", "question"}); err != nil { // replaces
		t.Fatal(err)
	}
	if off, _ := st.GetPushOffKinds(ctx, a); len(off) != 2 {
		t.Fatalf("off=%v", off)
	}
	if off, _ := st.GetPushOffKinds(ctx, b); len(off) != 0 {
		t.Fatalf("one member's opt-out leaked to another: %v", off)
	}
	active, inc, err := st.PushDeliveryContext(ctx, a)
	if err != nil || !active || inc {
		t.Fatalf("default context active=%v inc=%v err=%v", active, inc, err)
	}
	if err := st.PutTenantPushIncludeText(ctx, tenantID, true, "admin"); err != nil {
		t.Fatal(err)
	}
	if on, _ := st.GetTenantPushIncludeText(ctx, tenantID); !on {
		t.Fatal("setting not stored")
	}
	if _, inc, _ := st.PushDeliveryContext(ctx, a); !inc {
		t.Fatal("tenant setting not seen by the sender")
	}
	if err := st.PutTenantPushIncludeText(ctx, tenantID, false, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, inc, _ := st.PushDeliveryContext(ctx, a); inc {
		t.Fatal("switching it off did not take effect")
	}
	if err := st.SetMembershipStatus(ctx, b, "inactive"); err != nil {
		t.Fatal(err)
	}
	if active, _, _ := st.PushDeliveryContext(ctx, b); active {
		t.Fatal("a deactivated membership still counts as active")
	}
	if err := st.SetMembershipStatus(ctx, b, "active"); err != nil {
		t.Fatal(err)
	}
}
