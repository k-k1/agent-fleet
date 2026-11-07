package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

// dialects runs f on SQLite and, when AF_TEST_DATABASE_URL is set, on Postgres.
func dialects(t *testing.T, f func(t *testing.T, st *SQL, pg bool)) {
	t.Run("sqlite", func(t *testing.T) {
		st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		f(t, st, false)
	})
	t.Run("postgres", func(t *testing.T) {
		url, ok := pgtest.Schema(t)
		if !ok {
			t.Skip("set AF_TEST_DATABASE_URL to run against Postgres")
		}
		st, err := OpenPostgres(url)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		f(t, st, true)
	})
}

type revokeFixture struct {
	identity, membership, tenant string
}

func newRevokeFixture(t *testing.T, st *SQL) revokeFixture {
	t.Helper()
	ctx := context.Background()
	tn, err := st.CreateTenant(ctx, "sales", "Sales")
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertIdentity(ctx, "a@example.com", "a-example-com", "")
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	return revokeFixture{identity: id.ID, membership: m.ID, tenant: tn.ID}
}

func (f revokeFixture) addPAT(t *testing.T, st *SQL, id string) {
	t.Helper()
	if err := st.CreatePAT(context.Background(), PAT{ID: id, IdentityID: f.identity, MembershipID: f.membership,
		Scope: "read", Name: id, CreatedAt: NowTS()}, "hash-"+id); err != nil {
		t.Fatal(err)
	}
}

func (f revokeFixture) addGrant(t *testing.T, st *SQL, id string) {
	t.Helper()
	now := time.Now().Unix()
	if err := st.CreateMCPOAuthGrant(context.Background(), MCPOAuthGrant{ID: id, ClientID: "c", IdentityID: f.identity,
		MembershipID: f.membership, TenantID: f.tenant, Scope: "read", Resource: "r", CreatedAt: now,
		IdleExpiresAt: now + 100, AbsExpiresAt: now + 100}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertMCPOAuthToken(context.Background(), MCPOAuthToken{Hash: "tok-" + id, Kind: MCPOAuthAccessKind,
		GrantID: id, ClientID: "c", IdentityID: f.identity, ExpiresAt: now + 100}); err != nil {
		t.Fatal(err)
	}
}

func auditCount(t *testing.T, st *SQL, tenantID, action string) int {
	t.Helper()
	logs, err := st.ListAuditByTenant(context.Background(), tenantID, 500)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range logs {
		if l.Action == action {
			n++
		}
	}
	return n
}

// Two sweeps that both read a credential as live must still record ONE revocation: only the
// update that changed the row claims it. On Postgres the sweeps are held at the update by a
// row lock so both have read the credential as live first.
func TestConcurrentBearerRevocationAuditsOncePerCredential(t *testing.T) {
	dialects(t, func(t *testing.T, st *SQL, pg bool) {
		ctx := context.Background()
		f := newRevokeFixture(t, st)
		f.addPAT(t, st, "p1")
		f.addGrant(t, st, "g1")
		set, err := st.LiveBearersOfIdentity(ctx, f.identity)
		if err != nil {
			t.Fatal(err)
		}
		var hold *sqlTx
		if pg {
			tx, err := st.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			hold = tx
			for _, q := range []string{`UPDATE pat SET name=name WHERE id='p1'`, `UPDATE mcp_oauth_grant SET revoked_at=revoked_at WHERE id='g1'`} {
				if _, err := hold.ExecContext(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
		}
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := st.RevokeBearerSet(ctx, f.identity, "r", set); err != nil {
					t.Error(err)
				}
			}()
		}
		if hold != nil {
			time.Sleep(500 * time.Millisecond)
			if err := hold.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		wg.Wait()
		if n := auditCount(t, st, f.tenant, "pat.revoke"); n != 1 {
			t.Errorf("pat.revoke rows = %d, want 1", n)
		}
		if n := auditCount(t, st, f.tenant, "mcp.oauth.grant"); n != 1 {
			t.Errorf("mcp.oauth.grant rows = %d, want 1", n)
		}
	})
}

// A revocation that cannot be recorded is not applied: the PAT, the grant, the grant's tokens
// and the rows written before the failure all roll back.
func TestRevokeBearerSetRollsBackWhenTheAuditInsertFails(t *testing.T) {
	dialects(t, func(t *testing.T, st *SQL, pg bool) {
		ctx := context.Background()
		f := newRevokeFixture(t, st)
		f.addPAT(t, st, "p1")
		f.addGrant(t, st, "g1")
		ddl := []string{`CREATE TRIGGER audit_fail BEFORE INSERT ON audit_log WHEN NEW.action='mcp.oauth.grant'
			BEGIN SELECT RAISE(ABORT,'audit down'); END`}
		if pg {
			ddl = []string{
				`CREATE FUNCTION audit_fail() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'audit down'; END $$ LANGUAGE plpgsql`,
				`CREATE TRIGGER audit_fail BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='mcp.oauth.grant') EXECUTE FUNCTION audit_fail()`,
			}
		}
		for _, q := range ddl {
			if _, err := st.db.ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		set, err := st.LiveBearersOfIdentity(ctx, f.identity)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.RevokeBearerSet(ctx, f.identity, "r", set); err == nil {
			t.Fatal("RevokeBearerSet succeeded although its audit row could not be written")
		}
		after, err := st.LiveBearersOfIdentity(ctx, f.identity)
		if err != nil {
			t.Fatal(err)
		}
		if len(after.PATs) != 1 || len(after.Grants) != 1 {
			t.Errorf("live after the failure = %+v, want the PAT and the grant untouched", after)
		}
		var tokens int
		if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_oauth_token WHERE grant_id='g1'`).Scan(&tokens); err != nil || tokens != 1 {
			t.Errorf("grant tokens = %d (%v), want 1", tokens, err)
		}
		if n := auditCount(t, st, f.tenant, "pat.revoke"); n != 0 {
			t.Errorf("pat.revoke rows = %d after the rollback, want 0", n)
		}
	})
}

// ChangeMembershipRole reports the role it overwrote, and two concurrent changes report old roles
// consistent with one serial order.
func TestChangeMembershipRoleReportsTheOverwrittenRole(t *testing.T) {
	dialects(t, func(t *testing.T, st *SQL, pg bool) {
		ctx := context.Background()
		f := newRevokeFixture(t, st)
		if old, ok, err := st.ChangeMembershipRole(ctx, f.membership, "tenant_admin"); err != nil || !ok || old != "member" {
			t.Fatalf("first change = %q %v %v, want member", old, ok, err)
		}
		if old, ok, err := st.ChangeMembershipRole(ctx, f.membership, "member"); err != nil || !ok || old != "tenant_admin" {
			t.Fatalf("second change = %q %v %v, want tenant_admin", old, ok, err)
		}
		if _, ok, err := st.ChangeMembershipRole(ctx, "nope", "member"); err != nil || ok {
			t.Errorf("unknown membership = %v %v, want not found", ok, err)
		}
		// Two concurrent changes to different roles: one saw "member", the other the first's result.
		var wg sync.WaitGroup
		olds := make([]string, 2)
		for i, role := range []string{"tenant_admin", "member"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				olds[i], _, _ = st.ChangeMembershipRole(ctx, f.membership, role)
			}()
		}
		wg.Wait()
		final, _, _ := st.GetMembership(ctx, f.identity, f.tenant)
		// The role starts as "member" (the sequence above ends there). Run B first and both
		// overwrite "member" and the final role is A's; run A first and B overwrote A's role.
		if olds[0] != "member" {
			t.Errorf("tenant_admin change reports old=%q, want member", olds[0])
		}
		want := map[string]string{"member": "tenant_admin", "tenant_admin": "member"}[olds[1]]
		if want == "" || final.Role != want {
			t.Errorf("olds=%v final=%q: the reported old roles do not match the order the changes ran in", olds, final.Role)
		}
	})
}
