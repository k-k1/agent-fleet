package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

// The two-way rule: an identity linked to a tenant IdP holds no ACTIVE membership in another
// tenant, whichever side acts first.

func guardStores(t *testing.T) map[string]*SQL {
	t.Helper()
	out := map[string]*SQL{}
	sq, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sq.Close() })
	out["sqlite"] = sq
	if url, ok := pgtest.Schema(t); ok {
		pg, err := OpenPostgres(url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pg.Close() })
		out["postgres"] = pg
	}
	for _, s := range out {
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func salesLink(subject, email, key string) IdentityLink {
	return IdentityLink{Provider: "t:sales:oidc", Subject: subject, Email: email, FallbackKey: key, TenantSlug: "sales"}
}

func TestTenantIdPLinkedIdentityCannotBeInvitedByAnotherTenant(t *testing.T) {
	for name, st := range guardStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			sales, _ := st.CreateTenant(ctx, "sales", "Sales")
			legal, _ := st.CreateTenant(ctx, "legal", "Legal")
			id, _, err := st.LinkIdentity(ctx, salesLink("x", "victim@example.com", "victim-example-com"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.InviteMember(ctx, id.ID, legal.ID, "member"); !errors.Is(err, ErrIdentityTenantBound) {
				t.Fatalf("other tenant's invite = %v, want ErrIdentityTenantBound", err)
			}
			if _, err := st.InviteMember(ctx, id.ID, sales.ID, "member"); err != nil {
				t.Fatalf("own tenant's invite: %v", err)
			}
		})
	}
}

func TestRemovingTheOtherMembershipClearsTheLinkRefusal(t *testing.T) {
	for name, st := range guardStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st.CreateTenant(ctx, "sales", "Sales")
			legal, _ := st.CreateTenant(ctx, "legal", "Legal")
			ph, _ := st.InviteIdentity(ctx, "", "newbie-sales-example", legal.ID)
			inv, err := st.InviteMember(ctx, ph.ID, legal.ID, "member")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.LinkIdentity(ctx, salesLink("n", "newbie@sales.example", "newbie-sales-example")); !errors.Is(err, ErrClaimedByOtherTenant) {
				t.Fatalf("link while legal holds the person = %v, want ErrClaimedByOtherTenant", err)
			}
			if got, _, _ := st.GetIdentityByUserKey(ctx, "newbie-sales-example"); got.Email != "" {
				t.Errorf("refused link still wrote the email %q", got.Email)
			}
			if err := st.SetMembershipStatus(ctx, inv.Membership.ID, "inactive"); err != nil {
				t.Fatal(err)
			}
			got, _, err := st.LinkIdentity(ctx, salesLink("n", "newbie@sales.example", "newbie-sales-example"))
			if err != nil || got.ID != ph.ID || got.Email != "newbie@sales.example" {
				t.Fatalf("link after removal = %+v %v", got, err)
			}
		})
	}
}

// Whichever of link and invite runs, the end state never has both. On Postgres the link is
// parked between its guard read and its write (an uncommitted row on the same
// (provider, subject)), which is the window the row lock has to cover.
func TestLinkAndInviteNeverBothSucceed(t *testing.T) {
	for name, st := range guardStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st.CreateTenant(ctx, "sales", "Sales")
			legal, _ := st.CreateTenant(ctx, "legal", "Legal")
			dummy, _ := st.UpsertIdentity(ctx, "dummy@x.example", "dummy-x-example", "")
			ph, _ := st.InviteIdentity(ctx, "", "victim-example-com", legal.ID)
			var park func()
			if st.dialect == "postgres" {
				tx, err := st.db.DB.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO identity_provider(provider, subject, identity_id, email, realm,
					realm_claim, realm_subject, created_at, last_login_at) VALUES($1,$2,$3,'','','','',$4,'')`,
					"t:sales:oidc", "x", dummy.ID, NowTS()); err != nil {
					t.Fatal(err)
				}
				park = func() { _ = tx.Rollback() }
			}
			linkErr := make(chan error, 1)
			go func() {
				_, _, err := st.LinkIdentity(ctx, salesLink("x", "victim@example.com", "victim-example-com"))
				linkErr <- err
			}()
			time.Sleep(700 * time.Millisecond)
			inviteErr := make(chan error, 1)
			go func() {
				_, err := st.InviteMember(ctx, ph.ID, legal.ID, "tenant_admin")
				inviteErr <- err
			}()
			time.Sleep(700 * time.Millisecond)
			if park != nil {
				park()
			}
			le, ie := <-linkErr, <-inviteErr
			if le == nil && ie == nil {
				t.Fatal("link and invite both succeeded: tenant-IdP identity holds a second tenant's membership")
			}
			t.Logf("link=%v invite=%v", le, ie)
		})
	}
}

func TestSuspectsListsEveryMultiTenantIdentity(t *testing.T) {
	for name, st := range guardStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			sales, _ := st.CreateTenant(ctx, "sales", "Sales")
			legal, _ := st.CreateTenant(ctx, "legal", "Legal")
			ph, _ := st.InviteIdentity(ctx, "", "victim-example-com", legal.ID)
			st.InviteMember(ctx, ph.ID, legal.ID, "tenant_admin")
			st.InviteMember(ctx, ph.ID, sales.ID, "member")
			// Address written without an audit row (before the audit existed), then used.
			if _, err := st.db.ExecContext(ctx, `UPDATE identity SET email=? WHERE id=?`, "attacker@evil.example", ph.ID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.LinkIdentity(ctx, IdentityLink{Provider: "google", Subject: "a",
				Email: "attacker@evil.example", FallbackKey: "attacker-evil-example", EmailJoin: true}); err != nil {
				t.Fatal(err)
			}
			list, err := st.ListEmailSetSuspects(ctx)
			if err != nil || len(list) != 1 || list[0].UserKey != "victim-example-com" || len(list[0].Tenants) != 2 {
				t.Fatalf("list = %+v %v, want the identity even though it has a login and no audit row", list, err)
			}
		})
	}
}

// A tenant-IdP first login that creates the row itself still goes through the locked guard
// and writes the address only afterwards.
func TestTenantIdPFirstLoginCreatesTheRowAndWritesTheAddressAfterTheGuard(t *testing.T) {
	for name, st := range guardStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st.CreateTenant(ctx, "sales", "Sales")
			legal, _ := st.CreateTenant(ctx, "legal", "Legal")
			got, isNew, err := st.LinkIdentity(ctx, salesLink("a", "a@example.com", "a-example-com"))
			if err != nil || !isNew || got.Email != "a@example.com" {
				t.Fatalf("fresh link = %+v new=%v %v", got, isNew, err)
			}
			if _, err := st.InviteMember(ctx, got.ID, legal.ID, "member"); !errors.Is(err, ErrIdentityTenantBound) {
				t.Fatalf("invite after link = %v", err)
			}
			// A placeholder legal holds by the time the link starts: refused, address untouched.
			ph, _ := st.InviteIdentity(ctx, "", "b-example-com", legal.ID)
			if _, err := st.InviteMember(ctx, ph.ID, legal.ID, "tenant_admin"); err != nil {
				t.Fatal(err)
			}
			if _, isNew, err := st.LinkIdentity(ctx, salesLink("b", "b@example.com", "b-example-com")); !errors.Is(err, ErrClaimedByOtherTenant) || isNew {
				t.Fatalf("link onto legal's placeholder = new=%v %v", isNew, err)
			}
			if x, _, _ := st.GetIdentityByUserKey(ctx, "b-example-com"); x.Email != "" {
				t.Errorf("email written despite refusal: %q", x.Email)
			}
		})
	}
}
