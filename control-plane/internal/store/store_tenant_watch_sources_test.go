package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

func TestTenantWatchSourcesSQLite(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkTenantWatchSources(t, st)
}

func TestTenantWatchSourcesPostgres(t *testing.T) {
	url, ok := pgtest.Schema(t)
	if !ok {
		t.Skip("set AF_TEST_DATABASE_URL to run against Postgres")
	}
	st, err := OpenPostgres(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkTenantWatchSources(t, st)
}

// checkTenantWatchSources: a tenant starts with the switch off, the switch is per tenant, and an unknown
// tenant is an error so a gate built on it fails closed.
func checkTenantWatchSources(t *testing.T, st *SQL) {
	t.Helper()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ := st.CreateTenant(ctx, "sales", "Sales")
	b, _ := st.CreateTenant(ctx, "ops", "Ops")
	if on, err := st.GetTenantWatchSources(ctx, a.ID); err != nil || on {
		t.Fatalf("new tenant = %v %v, want off", on, err)
	}
	if err := st.SetTenantWatchSources(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if on, _ := st.GetTenantWatchSources(ctx, a.ID); !on {
		t.Error("switch on did not stick")
	}
	if on, _ := st.GetTenantWatchSources(ctx, b.ID); on {
		t.Error("another tenant's switch changed")
	}
	if _, err := st.GetTenantWatchSources(ctx, "nope"); err == nil {
		t.Error("an unknown tenant must be an error")
	}
}
