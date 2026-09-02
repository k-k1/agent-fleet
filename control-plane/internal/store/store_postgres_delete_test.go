package store

import (
	"context"
	"os"
	"testing"
)

// TestPostgresDeleteCascade runs the two irreversible deletes against a REAL Postgres.
//
// ★ なぜ SQLite のテストだけでは足りないか: cascade は表名を直接並べた DELETE の列で、
// **2 つの方言でスキーマが一致していない**。`memo_category` は migrations/0020 にあって
// migrations-pg には無い（この機能自体が Postgres で動いていない、既存の取りこぼし）。
// 実 DB へ通さない限り、この差は「本番の管理者が取り消せない操作の途中で 500 を踏む」
// という形でしか現れない。
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

	if err := st.DeleteMembership(ctx, mem.ID); err != nil {
		t.Fatalf("DeleteMembership on postgres: %v", err)
	}
	if _, ok, _ := st.GetMembershipByID(ctx, mem.ID); ok {
		t.Error("the membership survived")
	}
	if _, ok, err := st.GetUserLimit(ctx, mem.ID); err != nil || ok {
		t.Errorf("the quota survived (ok=%v err=%v)", ok, err)
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
	if rows, err := st.ListUsage(ctx, tn.ID, "2026-07-01", "2026-07-01"); err != nil || len(rows) == 0 {
		t.Errorf("occupancy history was deleted with the tenant: %+v %v", rows, err)
	}
}
