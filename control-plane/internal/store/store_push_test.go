package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// A send falls back to GetPushSubscription when the member's status cannot be read, so the row
// read must not wait on the membership table. With membership locked ACCESS EXCLUSIVE in another
// transaction (as DDL would hold it; a row lock does not block a plain SELECT), the read with the
// status runs out its deadline while the row alone answers.
func TestPushRowReadIgnoresMembershipLockPostgres(t *testing.T) {
	url, ok := pgtest.Schema(t)
	if !ok {
		t.Skip("set AF_TEST_DATABASE_URL to run against Postgres")
	}
	st, err := OpenPostgres(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertIdentity(ctx, "a@example.com", "a@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	const ep = "https://fcm.googleapis.com/fcm/send/locked"
	if err := st.UpsertPushSubscription(ctx, PushSubscription{MembershipID: m.ID, Endpoint: ep, P256dh: "k", Auth: "a"}); err != nil {
		t.Fatal(err)
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `LOCK TABLE membership IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	_, _, _, err = st.GetPushSendTarget(sctx, m.ID, ep, false)
	cancel()
	if err == nil {
		t.Fatal("the read with the status did not wait on the locked membership table")
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	got, held, err := st.GetPushSubscription(rctx, m.ID, ep)
	if err != nil || !held || got.Endpoint != ep {
		t.Fatalf("row read under the membership lock: %+v %v %v", got, held, err)
	}
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
	checkPushPrefs(t, st, tn.ID, tn.Slug, a, b)
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
	checkPushCap(t, st, a, b)
}

// checkPushCap covers AddPushSubscription: the cap, the refresh of a held endpoint at the cap, a
// move from another member, GetPushSubscription, and concurrent registrations that must not
// overshoot.
func checkPushCap(t *testing.T, st *SQL, a, b string) {
	t.Helper()
	ctx := context.Background()
	const max = 3
	add := func(mid, ep string) bool {
		t.Helper()
		ok, err := st.AddPushSubscription(ctx, PushSubscription{MembershipID: mid, Endpoint: ep, P256dh: "k", Auth: "a"}, max)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	ep := func(i int) string { return fmt.Sprintf("https://fcm.googleapis.com/fcm/send/cap-%d", i) }
	for i := 0; i < max; i++ {
		if !add(a, ep(i)) {
			t.Fatalf("device %d refused under the cap", i)
		}
	}
	if add(a, ep(max)) {
		t.Fatal("a device over the cap was stored")
	}
	if !add(a, ep(0)) {
		t.Fatal("re-registering a held endpoint at the cap was refused")
	}
	// A member already past the cap (rows written before it was atomic) can still refresh a device.
	if err := st.UpsertPushSubscription(ctx, PushSubscription{MembershipID: a, Endpoint: ep(99), P256dh: "k", Auth: "a"}); err != nil {
		t.Fatal(err)
	}
	if !add(a, ep(1)) {
		t.Fatal("refreshing a held endpoint past the cap was refused")
	}
	if _, err := st.DeletePushSubscription(ctx, a, PushSubscriptionID(ep(99))); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := st.GetPushSubscription(ctx, a, ep(0)); err != nil || !ok || got.Endpoint != ep(0) || got.MembershipID != a {
		t.Fatalf("get: %+v %v %v", got, ok, err)
	}
	// Moving one of a's endpoints to b frees a slot for a and is itself under b's cap.
	if !add(b, ep(0)) {
		t.Fatal("b could not take over an endpoint")
	}
	if _, ok, err := st.GetPushSubscription(ctx, a, ep(0)); err != nil || ok {
		t.Fatalf("a still holds the moved endpoint: %v %v", ok, err)
	}
	if _, _, ok, err := st.GetPushSendTarget(ctx, a, ep(0), true); err != nil || ok {
		t.Fatalf("a still holds the moved endpoint: %v %v", ok, err)
	}
	if !add(a, ep(max)) {
		t.Fatal("the slot freed by the move was not reusable")
	}

	// Concurrent registrations of one member: exactly max of them are stored. A few rounds, since
	// one round of a non-atomic count can happen to run in order.
	for _, rows := range [][]PushSubscription{mustList(t, st, a), mustList(t, st, b)} {
		for _, r := range rows {
			if _, err := st.DeletePushSubscription(ctx, r.MembershipID, r.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	for round := 0; round < 5; round++ {
		var wg sync.WaitGroup
		var stored atomic.Int32
		errs := make(chan error, 16)
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				ok, err := st.AddPushSubscription(ctx, PushSubscription{MembershipID: a,
					Endpoint: fmt.Sprintf("https://fcm.googleapis.com/fcm/send/race-%d-%d", round, i), P256dh: "k", Auth: "a"}, max)
				if err != nil {
					errs <- err
					return
				}
				if ok {
					stored.Add(1)
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		rows := mustList(t, st, a)
		if len(rows) != max || stored.Load() != max {
			t.Fatalf("round %d: concurrent registrations stored %d rows (%d reported), want %d", round, len(rows), stored.Load(), max)
		}
		for _, r := range rows {
			if _, err := st.DeletePushSubscription(ctx, a, r.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func mustList(t *testing.T, st *SQL, mid string) []PushSubscription {
	t.Helper()
	rows, err := st.ListPushSubscriptions(context.Background(), mid)
	if err != nil {
		t.Fatal(err)
	}
	return rows
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
func checkPushPrefs(t *testing.T, st *SQL, tenantID, slug, a, b string) {
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
	if o, err := st.GetPushOffOrigins(ctx, a); err != nil || len(o) != 0 {
		t.Fatalf("default origins=%v err=%v", o, err)
	}
	if err := st.PutPushOffOrigins(ctx, a, []string{"autorun-child"}); err != nil {
		t.Fatal(err)
	}
	if o, _ := st.GetPushOffOrigins(ctx, a); len(o) != 1 || o[0] != "autorun-child" {
		t.Fatalf("origins=%v", o)
	}
	// Saving one list leaves the other alone.
	if off, _ := st.GetPushOffKinds(ctx, a); len(off) != 2 {
		t.Fatalf("saving origins changed the kinds: %v", off)
	}
	if err := st.PutPushOffKinds(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	if o, _ := st.GetPushOffOrigins(ctx, a); len(o) != 1 {
		t.Fatalf("saving kinds changed the origins: %v", o)
	}
	if err := st.PutPushOffKinds(ctx, a, []string{"answer-ready", "question"}); err != nil {
		t.Fatal(err)
	}
	dc, err := st.PushDeliveryContext(ctx, a)
	if err != nil || !dc.Active || dc.IncludeText {
		t.Fatalf("default context=%+v err=%v", dc, err)
	}
	if slug == "" || dc.TenantSlug != slug {
		t.Fatalf("tenant slug = %q, want %q", dc.TenantSlug, slug)
	}
	if err := st.PutTenantPushIncludeText(ctx, tenantID, slug, true, "admin"); err != nil {
		t.Fatal(err)
	}
	if on, _ := st.GetTenantPushIncludeText(ctx, tenantID); !on {
		t.Fatal("setting not stored")
	}
	if dc, _ := st.PushDeliveryContext(ctx, a); !dc.IncludeText {
		t.Fatal("tenant setting not seen by the sender")
	}
	if err := st.PutTenantPushIncludeText(ctx, tenantID, slug, false, "admin"); err != nil {
		t.Fatal(err)
	}
	if dc, _ := st.PushDeliveryContext(ctx, a); dc.IncludeText {
		t.Fatal("switching it off did not take effect")
	}
	if err := st.SetMembershipStatus(ctx, b, "inactive"); err != nil {
		t.Fatal(err)
	}
	if dc, _ := st.PushDeliveryContext(ctx, b); dc.Active || dc.TenantSlug != "" {
		t.Fatal("a deactivated membership still counts as active")
	}
	if err := st.SetMembershipStatus(ctx, b, "active"); err != nil {
		t.Fatal(err)
	}

	// The send-time read answers the same context with the subscription, in one statement.
	sub := PushSubscription{MembershipID: a, Endpoint: "https://fcm.googleapis.com/fcm/send/ctx", P256dh: "k", Auth: "x"}
	if err := st.UpsertPushSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	get := func() PushDelivery {
		t.Helper()
		got, dc, ok, err := st.GetPushSendTarget(ctx, a, sub.Endpoint, true)
		if err != nil || !ok || got.Endpoint != sub.Endpoint {
			t.Fatalf("get: %+v %v %v", got, ok, err)
		}
		// Without the setting the same read still answers the member's status.
		_, alone, ok, err := st.GetPushSendTarget(ctx, a, sub.Endpoint, false)
		if err != nil || !ok || alone.IncludeText || alone.Active != dc.Active {
			t.Fatalf("read without the setting: %+v %v %v (with it: %+v)", alone, ok, err, dc)
		}
		return dc
	}
	if dc := get(); !dc.Active || dc.IncludeText {
		t.Fatalf("default send context = %+v", dc)
	}
	if err := st.PutTenantPushIncludeText(ctx, tenantID, slug, true, "admin"); err != nil {
		t.Fatal(err)
	}
	if dc := get(); !dc.IncludeText {
		t.Fatal("the send-time read missed the tenant setting")
	}
	if err := st.SetMembershipStatus(ctx, a, "inactive"); err != nil {
		t.Fatal(err)
	}
	if dc := get(); dc.Active {
		t.Fatal("the send-time read counts a deactivated membership as active")
	}
	if err := st.SetMembershipStatus(ctx, a, "active"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTenantPushIncludeText(ctx, tenantID, slug, false, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeletePushSubscription(ctx, a, PushSubscriptionID(sub.Endpoint)); err != nil {
		t.Fatal(err)
	}
}

// The text switch and its audit row are one write: a change that cannot be recorded is not
// applied, and one that is applied has exactly one row naming the new value.
func TestPutTenantPushIncludeTextIsAudited(t *testing.T) {
	dialects(t, func(t *testing.T, st *SQL, pg bool) {
		ctx := context.Background()
		tn, err := st.EnsureDefaultTenant(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.PutTenantPushIncludeText(ctx, tn.ID, tn.Slug, true, "admin"); err != nil {
			t.Fatal(err)
		}
		logs, err := st.ListAuditByTenant(ctx, tn.ID, 50)
		if err != nil {
			t.Fatal(err)
		}
		var rows []AuditLog
		for _, l := range logs {
			if l.Action == "tenant.push_settings" {
				rows = append(rows, l)
			}
		}
		if len(rows) != 1 || rows[0].Detail != "include_text=true" || rows[0].Target != tn.Slug || rows[0].ActorID != "admin" {
			t.Fatalf("audit rows = %+v", rows)
		}

		ddl := []string{`CREATE TRIGGER audit_fail BEFORE INSERT ON audit_log WHEN NEW.action='tenant.push_settings'
			BEGIN SELECT RAISE(ABORT,'audit down'); END`}
		if pg {
			ddl = []string{
				`CREATE FUNCTION audit_fail() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'audit down'; END $$ LANGUAGE plpgsql`,
				`CREATE TRIGGER audit_fail BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='tenant.push_settings') EXECUTE FUNCTION audit_fail()`,
			}
		}
		for _, q := range ddl {
			if _, err := st.db.ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.PutTenantPushIncludeText(ctx, tn.ID, tn.Slug, false, "admin"); err == nil {
			t.Fatal("the setting was written although its audit row could not be")
		}
		if on, err := st.GetTenantPushIncludeText(ctx, tn.ID); err != nil || !on {
			t.Fatalf("a change without its audit row stayed applied: on=%v err=%v", on, err)
		}
	})
}
