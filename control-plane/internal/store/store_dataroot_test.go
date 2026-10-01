package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

func TestDataRootNamesSQLite(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkDataRootNames(t, st)
}

func TestDataRootNamesPostgres(t *testing.T) {
	url, ok := pgtest.Schema(t)
	if !ok {
		t.Skip("set AF_TEST_DATABASE_URL to run against Postgres")
	}
	st, err := OpenPostgres(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkDataRootNames(t, st)
}

// checkDataRootNames: a tenant slug and a default-tenant user key are sibling directories
// under the data root (issue #1214), so the store refuses a new one that lands on a
// reserved name or on the other side's existing name — and leaves existing rows alone.
func checkDataRootNames(t *testing.T, st *SQL) {
	t.Helper()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	def, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	alice, err := st.UpsertIdentity(ctx, "alice@example.com", "alice-example-com", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureMembership(ctx, alice.ID, def.ID, "member"); err != nil {
		t.Fatalf("an ordinary default-tenant member: %v", err)
	}

	// Tenant side.
	for _, slug := range []string{"git", "drawio-stencils", "shared"} {
		if _, err := st.CreateTenant(ctx, slug, slug); !errors.Is(err, ErrDataRootNameReserved) {
			t.Errorf("CreateTenant(%q) = %v, want ErrDataRootNameReserved", slug, err)
		}
	}
	if _, err := st.CreateTenant(ctx, "alice-example-com", "x"); !errors.Is(err, ErrDataRootNameTaken) {
		t.Errorf("CreateTenant over a default member's home = %v, want ErrDataRootNameTaken", err)
	}
	sales, err := st.CreateTenant(ctx, "sales", "Sales")
	if err != nil {
		t.Fatalf("an ordinary tenant: %v", err)
	}

	// Member side: the user key is the home's directory name.
	for _, key := range []string{"git", "Git", "sales", "SALES"} {
		id, err := st.UpsertIdentity(ctx, "", key, "")
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.EnsureMembership(ctx, id.ID, def.ID, "member")
		if !errors.Is(err, ErrDataRootNameReserved) && !errors.Is(err, ErrDataRootNameTaken) {
			t.Errorf("default-tenant membership for key %q = %v, want a data-root refusal", key, err)
		}
		// Another tenant nests the home under its slug: no collision there.
		if _, err := st.EnsureMembership(ctx, id.ID, sales.ID, "member"); err != nil {
			t.Errorf("membership of %q in a non-default tenant: %v", key, err)
		}
	}

	// A row that predates the check is returned as it is, never refused: a running
	// deployment keeps its member. DataRootCollisions reports it instead.
	legacy, err := st.UpsertIdentity(ctx, "", "drawio-stencils", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO membership(id, identity_id, tenant_id, role, status, created_at) VALUES(?, ?, ?, 'member', 'active', ?)`,
		NewID(), legacy.ID, def.ID, NowTS()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO tenant(id, slug, name, status, limits, isolation, created_at) VALUES(?, 'git', 'git', 'active', '{}', 'shared', ?)`,
		NewID(), NowTS()); err != nil {
		t.Fatal(err)
	}
	carol, err := st.UpsertIdentity(ctx, "", "carol", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureMembership(ctx, carol.ID, def.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO tenant(id, slug, name, status, limits, isolation, created_at) VALUES(?, 'carol', 'carol', 'active', '{}', 'shared', ?)`,
		NewID(), NowTS()); err != nil {
		t.Fatal(err)
	}
	if m, err := st.EnsureMembership(ctx, legacy.ID, def.ID, "member"); err != nil || m.IdentityID != legacy.ID {
		t.Fatalf("an existing membership must be returned unchanged: %+v %v", m, err)
	}
	cs, err := st.DataRootCollisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(cs, "\n")
	for _, want := range []string{`tenant "git"`, `member "drawio-stencils"`, `tenant "carol"`} {
		if !strings.Contains(got, want) {
			t.Errorf("DataRootCollisions misses %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sales") || strings.Contains(got, "alice") {
		t.Errorf("DataRootCollisions reports a name that does not collide:\n%s", got)
	}
}
