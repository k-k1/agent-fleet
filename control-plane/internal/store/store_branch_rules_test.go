package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

func TestTenantBranchRulesSQLite(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkTenantBranchRules(t, st)
}

func TestTenantBranchRulesPostgres(t *testing.T) {
	url, ok := pgtest.Schema(t)
	if !ok {
		t.Skip("set AF_TEST_DATABASE_URL to run against Postgres")
	}
	st, err := OpenPostgres(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkTenantBranchRules(t, st)
}

// checkTenantBranchRules: a missing row is "no rules", a save replaces the list, each tenant
// sees only its own, and deleting the tenant takes its row.
func checkTenantBranchRules(t *testing.T, st *SQL) {
	t.Helper()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateTenant(ctx, "sales", "Sales")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateTenant(ctx, "ops", "Ops")
	if err != nil {
		t.Fatal(err)
	}
	if r, ok, err := st.GetTenantBranchRules(ctx, a.ID); err != nil || ok || r.Rules != "" {
		t.Fatalf("no row = %+v %v %v", r, ok, err)
	}
	for _, rules := range []string{`[{"match":"*","base":"develop"}]`, `[{"match":"*","name":"{prefix}{key}"}]`} {
		if err := st.PutTenantBranchRules(ctx, TenantBranchRules{TenantID: a.ID, Rules: rules, UpdatedBy: "I1", UpdatedAt: NowTS()}); err != nil {
			t.Fatal(err)
		}
	}
	if r, ok, err := st.GetTenantBranchRules(ctx, a.ID); err != nil || !ok || r.Rules != `[{"match":"*","name":"{prefix}{key}"}]` || r.UpdatedBy != "I1" {
		t.Fatalf("after two saves = %+v %v %v", r, ok, err)
	}
	if _, ok, _ := st.GetTenantBranchRules(ctx, b.ID); ok {
		t.Error("another tenant sees the rules")
	}
	if err := st.DeleteTenant(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.GetTenantBranchRules(ctx, a.ID); ok {
		t.Error("a deleted tenant left its rules")
	}
}
