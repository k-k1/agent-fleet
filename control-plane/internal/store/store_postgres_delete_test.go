package store

import (
	"context"
	"os"
	"testing"
)

// TestPostgresDeleteCascade runs the two irreversible deletes against a REAL Postgres.
//
// The SQLite test is not enough: the cascade is a list of DELETEs naming tables directly,
// and the two dialects do not have the same schema — `memo_category` is in
// migrations/0020 but has no counterpart in migrations-pg, so that feature does not run on
// Postgres at all. Off a real database the gap only shows up as an admin taking a 500
// halfway through an irreversible operation.
//
// Skipped unless AF_TEST_DATABASE_URL is set — see TestPostgresStore for the harness.
func TestPostgresDeleteCascade(t *testing.T) {
	url := os.Getenv("AF_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AF_TEST_DATABASE_URL to run the Postgres cascade test")
	}
	ctx := context.Background()
	st, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if _, err := st.db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tn, err := st.CreateTenant(ctx, "sales", "営業部")
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	ident, err := st.UpsertIdentity(ctx, "leaver@acme.co.jp", "leaver-acme-co-jp", "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	mem, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	if err := st.PutUserLimit(ctx, mem.ID, UserQuota{MaxSessions: 3}); err != nil {
		t.Fatalf("user limit: %v", err)
	}
	if err := st.AddUsage(ctx, mem.ID, tn.ID, "2026-07-01", 3600); err != nil {
		t.Fatalf("usage: %v", err)
	}
	if err := st.SetEngineMembersOnly(ctx, tn.ID, EngineAccessLLM, true); err != nil {
		t.Fatalf("engine access policy: %v", err)
	}
	if err := st.SetEngineGrant(ctx, tn.ID, mem.ID, EngineAccessLLM, true); err != nil {
		t.Fatalf("engine access grant: %v", err)
	}
	// The gateway's per-request read (a LEFT JOIN with a CASE) runs only here on Postgres.
	if one, err := st.GetMemberEngineAccess(ctx, tn.ID, mem.ID); err != nil || !one.Allows(mem.ID, EngineAccessLLM) {
		t.Errorf("granted member refused on postgres (%+v err=%v)", one, err)
	}
	if one, err := st.GetMemberEngineAccess(ctx, tn.ID, "M-other"); err != nil || one.Allows("M-other", EngineAccessLLM) {
		t.Errorf("ungranted member allowed on postgres (%+v err=%v)", one, err)
	}

	if err := st.DeleteMembership(ctx, mem.ID); err != nil {
		t.Fatalf("DeleteMembership on postgres: %v", err)
	}
	if _, ok, _ := st.GetMembershipByID(ctx, mem.ID); ok {
		t.Error("the membership survived")
	}
	if _, ok, err := st.GetUserLimit(ctx, mem.ID); err != nil || ok {
		t.Errorf("the quota survived (ok=%v err=%v)", ok, err)
	}
	if acc, err := st.GetEngineAccess(ctx, tn.ID); err != nil || len(acc.Grants) != 0 {
		t.Errorf("the engine grant survived (%+v err=%v)", acc.Grants, err)
	}
	if rows, err := st.ListUsage(ctx, tn.ID, "2026-07-01", "2026-07-01"); err != nil || len(rows) == 0 {
		t.Errorf("occupancy history was deleted: %+v %v", rows, err)
	}

	if err := st.DeleteTenant(ctx, tn.ID); err != nil {
		t.Fatalf("DeleteTenant on postgres: %v", err)
	}
	if _, ok, _ := st.GetTenantBySlug(ctx, "sales"); ok {
		t.Error("the tenant survived")
	}
	if acc, err := st.GetEngineAccess(ctx, tn.ID); err != nil || len(acc.MembersOnly) != 0 {
		t.Errorf("the engine access policy survived (%+v err=%v)", acc.MembersOnly, err)
	}
	if rows, err := st.ListUsage(ctx, tn.ID, "2026-07-01", "2026-07-01"); err != nil || len(rows) == 0 {
		t.Errorf("occupancy history was deleted with the tenant: %+v %v", rows, err)
	}
}
