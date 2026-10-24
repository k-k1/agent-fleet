package store

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Both dialects: the state is deleted by one DELETE ... RETURNING, so of any number of
// concurrent callbacks exactly one gets it.
func TestMCPClientOAuthStateIsTakenExactlyOnce(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		in := MCPClientOAuthState{
			IdentityID: "i1", MembershipID: "m1", TenantID: "t1", WorkspaceID: "w1",
			ServerID: "mcp-1", ServerName: "hosted", Issuer: "https://as.example.com", Registration: "cimd",
			ExpiresAt: 2000,
		}
		if err := st.PutMCPClientOAuthState(ctx, "state-aaaaaaaaaaaaaaaa", in, 1000); err != nil {
			t.Fatal(err)
		}
		var won, lost, bad atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, ok, expired, err := st.TakeMCPClientOAuthState(ctx, "state-aaaaaaaaaaaaaaaa", 1500)
				switch {
				case err != nil || expired:
					bad.Add(1)
				case ok && got.IdentityID == "i1" && got.MembershipID == "m1" && got.WorkspaceID == "w1" && got.Issuer == in.Issuer:
					won.Add(1)
				case !ok:
					lost.Add(1)
				default:
					bad.Add(1)
				}
			}()
		}
		wg.Wait()
		if won.Load() != 1 || lost.Load() != 7 || bad.Load() != 0 {
			t.Errorf("%s: won=%d lost=%d bad=%d, want 1/7/0", name, won.Load(), lost.Load(), bad.Load())
		}
	}
}

// An expired state is deleted when it is presented and reported as expired, not as unknown.
func TestMCPClientOAuthExpiredStateIsSpentAndReported(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		if err := st.PutMCPClientOAuthState(ctx, "state-bbbbbbbbbbbbbbbb", MCPClientOAuthState{IdentityID: "i", MembershipID: "m", ServerID: "s", ExpiresAt: 1100}, 1000); err != nil {
			t.Fatal(err)
		}
		got, ok, expired, err := st.TakeMCPClientOAuthState(ctx, "state-bbbbbbbbbbbbbbbb", 1100)
		if err != nil || ok || !expired || got.IdentityID != "i" {
			t.Errorf("%s: ok=%v expired=%v err=%v got=%+v", name, ok, expired, err, got)
		}
		if _, ok, expired, _ := st.TakeMCPClientOAuthState(ctx, "state-bbbbbbbbbbbbbbbb", 1100); ok || expired {
			t.Errorf("%s: the expired row must be gone after one presentation", name)
		}
		if _, ok, expired, err := st.TakeMCPClientOAuthState(ctx, "never-issued-state-xxxx", 1100); ok || expired || err != nil {
			t.Errorf("%s: unknown state: ok=%v expired=%v err=%v", name, ok, expired, err)
		}
	}
}

// The raw state is never stored, and a new insert purges the rows already expired.
func TestMCPClientOAuthStateIsStoredHashedAndPurged(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		raw := "state-cccccccccccccccc"
		if err := st.PutMCPClientOAuthState(ctx, "state-old-dddddddddddd", MCPClientOAuthState{IdentityID: "i", MembershipID: "m", ServerID: "s", ExpiresAt: 1100}, 1000); err != nil {
			t.Fatal(err)
		}
		if err := st.PutMCPClientOAuthState(ctx, raw, MCPClientOAuthState{IdentityID: "i", MembershipID: "m", ServerID: "s", ExpiresAt: 5000}, 2000); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_client_oauth_state`).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s: rows = %d (%v), want the expired one purged", name, n, err)
		}
		var h string
		if err := st.db.QueryRowContext(ctx, `SELECT state_hash FROM mcp_client_oauth_state`).Scan(&h); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(h, raw) || h != mcpClientOAuthHash(raw) || len(h) != 64 {
			t.Errorf("%s: stored %q", name, h)
		}
	}
}
