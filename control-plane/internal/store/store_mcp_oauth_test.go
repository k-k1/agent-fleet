package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// Both dialects: a refresh token is spent by one conditional UPDATE and its RowsAffected is
// the whole guarantee, so concurrent refreshes produce one winner.
func TestMCPOAuthRefreshIsSpentExactlyOnce(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		if err := st.InsertMCPOAuthToken(ctx, MCPOAuthToken{Hash: "r1", Kind: MCPOAuthRefresh, GrantID: "g1",
			ClientID: "c", IdentityID: "i", ExpiresAt: 2000}); err != nil {
			t.Fatal(err)
		}
		var ok, replay, other atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, state, err := st.ConsumeMCPOAuthToken(ctx, "r1", MCPOAuthRefresh, 1000)
				switch {
				case err != nil:
					other.Add(1)
				case state == MCPOAuthSpendOK:
					ok.Add(1)
				case state == MCPOAuthSpendReplayed:
					replay.Add(1)
				default:
					other.Add(1)
				}
			}()
		}
		wg.Wait()
		if ok.Load() != 1 || replay.Load() != 7 || other.Load() != 0 {
			t.Errorf("%s: ok=%d replayed=%d other=%d, want 1/7/0", name, ok.Load(), replay.Load(), other.Load())
		}
		// A refresh token is not a code, and an expired one is unknown.
		if _, state, _ := st.ConsumeMCPOAuthToken(ctx, "r1", MCPOAuthCode, 1000); state != MCPOAuthSpendUnknown {
			t.Errorf("%s: a refresh token redeemed as a code: %v", name, state)
		}
		if _, state, _ := st.ConsumeMCPOAuthToken(ctx, "r1", MCPOAuthRefresh, 2000); state != MCPOAuthSpendUnknown {
			t.Errorf("%s: an expired token was %v, want unknown", name, state)
		}
	}
}

// An access token is live only while its grant is: revoked, past the absolute lifetime and idle
// grants all stop resolving, and revoking deletes the tokens.
func TestMCPOAuthAccessFollowsGrant(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		g := MCPOAuthGrant{ID: "g1", ClientID: "c", IdentityID: "i", MembershipID: "m", Scope: "read",
			Resource: "https://x/mcp", CodeHash: "code1", CreatedAt: 100, IdleExpiresAt: 500, AbsExpiresAt: 900}
		if err := st.CreateMCPOAuthGrant(ctx, g); err != nil {
			t.Fatal(err)
		}
		if err := st.InsertMCPOAuthToken(ctx, MCPOAuthToken{Hash: "a1", Kind: MCPOAuthAccessKind, GrantID: "g1",
			ClientID: "c", IdentityID: "i", MembershipID: "m", Resource: "https://x/mcp", ExpiresAt: 1000}); err != nil {
			t.Fatal(err)
		}
		if a, ok, err := st.GetMCPOAuthAccess(ctx, "a1", 200); err != nil || !ok || a.GrantID != "g1" || a.Resource != "https://x/mcp" {
			t.Fatalf("%s: live token: %+v %v %v", name, a, ok, err)
		}
		if _, ok, _ := st.GetMCPOAuthAccess(ctx, "a1", 500); ok {
			t.Errorf("%s: an idle grant still resolved", name)
		}
		// Use pushes the idle deadline, but never past the absolute one.
		if err := st.TouchMCPOAuthGrant(ctx, "g1", 400, 300); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := st.GetMCPOAuthGrant(ctx, "g1"); got.IdleExpiresAt != 700 || got.LastUsedAt != 400 {
			t.Errorf("%s: touched grant idle=%d used=%d, want 700/400", name, got.IdleExpiresAt, got.LastUsedAt)
		}
		if err := st.TouchMCPOAuthGrant(ctx, "g1", 800, 300); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := st.GetMCPOAuthGrant(ctx, "g1"); got.IdleExpiresAt != 900 {
			t.Errorf("%s: idle deadline %d passed the absolute 900", name, got.IdleExpiresAt)
		}
		if _, ok, _ := st.GetMCPOAuthAccess(ctx, "a1", 900); ok {
			t.Errorf("%s: a grant past its absolute lifetime still resolved", name)
		}
		ids, err := st.RevokeMCPOAuthGrantsByCode(ctx, "code1", "code replayed", 450)
		if err != nil || len(ids) != 1 || ids[0] != "g1" {
			t.Fatalf("%s: revoke by code: %v %v", name, ids, err)
		}
		if _, ok, _ := st.GetMCPOAuthAccess(ctx, "a1", 450); ok {
			t.Errorf("%s: a revoked grant still resolved", name)
		}
		if _, ok, _ := st.GetMCPOAuthTokenByHash(ctx, "a1", 450); ok {
			t.Errorf("%s: revoking left the token row", name)
		}
		if again, _ := st.RevokeMCPOAuthGrant(ctx, "g1", "x", 460); again {
			t.Errorf("%s: a second revoke reported a change", name)
		}
	}
}

func TestMCPOAuthClientCapAndPrune(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		for i, id := range []string{"c1", "c2"} {
			if err := st.CreateMCPOAuthClient(ctx, MCPOAuthClient{ClientID: id, RedirectURIs: "[]", CreatedAt: int64(100 + i)}, 2); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		if err := st.CreateMCPOAuthClient(ctx, MCPOAuthClient{ClientID: "c3", RedirectURIs: "[]", CreatedAt: 1}, 2); !errors.Is(err, ErrMCPOAuthClientCap) {
			t.Errorf("%s: third client over a cap of 2: %v", name, err)
		}
		// c1 has a grant, c2 has none: only c2 goes once both are older than the cutoff.
		if err := st.CreateMCPOAuthGrant(ctx, MCPOAuthGrant{ID: "g", ClientID: "c1", IdentityID: "i", MembershipID: "m",
			Scope: "read", Resource: "r", IdleExpiresAt: 9999, AbsExpiresAt: 9999}); err != nil {
			t.Fatal(err)
		}
		if err := st.PruneMCPOAuth(ctx, 1000, 500, 0); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := st.GetMCPOAuthClient(ctx, "c1"); !ok {
			t.Errorf("%s: a client with a grant was pruned", name)
		}
		if _, ok, _ := st.GetMCPOAuthClient(ctx, "c2"); ok {
			t.Errorf("%s: a client with no grant survived the prune", name)
		}
	}
}

// The registration cap holds under concurrent registrations on every dialect.
func TestMCPOAuthClientCapHoldsUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		if name == "postgres" {
			// Widen the window between the count and the insert so unserialised registrations overlap.
			for _, q := range []string{
				`CREATE FUNCTION mcp_oauth_slow() RETURNS trigger AS $$ BEGIN PERFORM pg_sleep(0.3); RETURN NEW; END $$ LANGUAGE plpgsql`,
				`CREATE TRIGGER mcp_oauth_slow BEFORE INSERT ON mcp_oauth_client FOR EACH ROW EXECUTE FUNCTION mcp_oauth_slow()`,
			} {
				if _, err := st.db.ExecContext(ctx, q); err != nil {
					t.Fatalf("postgres: %v", err)
				}
			}
		}
		var ok, capped, other atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				err := st.CreateMCPOAuthClient(ctx, MCPOAuthClient{ClientID: "c" + string(rune('a'+i)), RedirectURIs: "[]"}, 1)
				switch {
				case err == nil:
					ok.Add(1)
				case errors.Is(err, ErrMCPOAuthClientCap):
					capped.Add(1)
				default:
					other.Add(1)
				}
			}(i)
		}
		wg.Wait()
		if ok.Load() != 1 || capped.Load() != 7 || other.Load() != 0 {
			t.Errorf("%s: ok=%d capped=%d other=%d, want 1/7/0", name, ok.Load(), capped.Load(), other.Load())
		}
	}
}

func TestMCPOAuthTenantDisablePersistsRevocation(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		tn, err := st.EnsureDefaultTenant(ctx)
		if err != nil {
			t.Fatal(err)
		}
		other, err := st.CreateTenant(ctx, "other", "Other")
		if err != nil {
			t.Fatal(err)
		}
		ident, err := st.UpsertIdentity(ctx, "", "owner", "")
		if err != nil {
			t.Fatal(err)
		}
		member, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
		if err != nil {
			t.Fatal(err)
		}
		if on, err := st.GetTenantMCPOAuth(ctx, tn.ID); err != nil || !on {
			t.Fatalf("%s default: %v %v", name, on, err)
		}
		code := MCPOAuthToken{Hash: "code", Kind: MCPOAuthCode, IdentityID: ident.ID, MembershipID: member.ID, ExpiresAt: 2000}
		if err := st.InsertMCPOAuthToken(ctx, code); err != nil {
			t.Fatal(err)
		}
		grant := MCPOAuthGrant{ID: "grant", IdentityID: ident.ID, MembershipID: member.ID, TenantID: tn.ID, CodeHash: "code", CreatedAt: 1, IdleExpiresAt: 2000, AbsExpiresAt: 3000}
		if err := st.CreateMCPOAuthGrant(ctx, grant); err != nil {
			t.Fatal(err)
		}
		at := MCPOAuthToken{Hash: "access", Kind: MCPOAuthAccessKind, GrantID: grant.ID, ExpiresAt: 2000}
		if err := st.InsertMCPOAuthToken(ctx, at); err != nil {
			t.Fatal(err)
		}
		if err := st.SetTenantMCPOAuth(ctx, tn.ID, false, ident.ID, 1000); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := st.GetMCPOAuthAccess(ctx, "access", 1001); err != nil || ok {
			t.Fatalf("%s disabled access: %v %v", name, ok, err)
		}
		if on, err := st.GetTenantMCPOAuth(ctx, other.ID); err != nil || !on {
			t.Fatalf("%s other tenant: %v %v", name, on, err)
		}
		var count int
		if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action='mcp.oauth.grant' AND target='grant' AND detail='revoked reason=tenant-disabled' AND tenant_id=? AND actor_id=?`, tn.ID, ident.ID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s audit: %d %v", name, count, err)
		}
		if err := st.SetTenantMCPOAuth(ctx, tn.ID, true, ident.ID, 1002); err != nil {
			t.Fatal(err)
		}
		// An access/refresh insert already in flight may finish after disabling and re-enabling.
		at.Hash = "late-access"
		if err := st.InsertMCPOAuthToken(ctx, at); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := st.GetMCPOAuthAccess(ctx, at.Hash, 1003); err != nil || ok {
			t.Fatalf("%s late access: %v %v", name, ok, err)
		}
		// A code exchange that read the code before disabling must also see the durable mark.
		grant.ID = "late-grant"
		if err := st.CreateMCPOAuthGrant(ctx, grant); !errors.Is(err, ErrMCPOAuthDisabled) {
			t.Fatalf("%s late grant: %v", name, err)
		}
		code.Hash = "new-code"
		if err := st.InsertMCPOAuthToken(ctx, code); err != nil {
			t.Fatal(err)
		}
		grant.CodeHash = code.Hash
		if err := st.CreateMCPOAuthGrant(ctx, grant); err != nil {
			t.Fatalf("%s new connection: %v", name, err)
		}

		// Access resolution independently enforces policy, even without a revocation sweep.
		at.Hash, at.GrantID = "new-access", grant.ID
		if err := st.InsertMCPOAuthToken(ctx, at); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.ExecContext(ctx, `UPDATE tenant SET mcp_oauth_disabled=1 WHERE id=?`, tn.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := st.GetMCPOAuthAccess(ctx, at.Hash, 1003); err != nil || ok {
			t.Fatalf("%s policy gate: %v %v", name, ok, err)
		}
		if _, err := st.db.ExecContext(ctx, `UPDATE tenant SET mcp_oauth_disabled=0 WHERE id=?`, tn.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := st.GetMCPOAuthAccess(ctx, at.Hash, 1003); err != nil || !ok {
			t.Fatalf("%s live access: %v %v", name, ok, err)
		}

		// A failed audit rolls the policy and revocations back together.
		if _, err := st.db.ExecContext(ctx, `DROP TABLE audit_log`); err != nil {
			t.Fatal(err)
		}
		if err := st.SetTenantMCPOAuth(ctx, tn.ID, false, ident.ID, 1004); err == nil {
			t.Fatalf("%s disable succeeded without audit", name)
		}
		if on, err := st.GetTenantMCPOAuth(ctx, tn.ID); err != nil || !on {
			t.Fatalf("%s partial policy change: %v %v", name, on, err)
		}
	}
}
