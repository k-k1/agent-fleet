package store

import (
	"context"
	"testing"
)

// The git token epoch (issue #1199) starts at 0 on an existing row, so the upgrade keeps
// every token valid; a bump moves it by one per call, reaches an inactive membership too,
// and the read side only answers for an active one, which is what the git face trusts.
func TestGitTokenEpochBumpAndRead(t *testing.T) {
	for name, st := range ssmStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			tn, err := st.CreateTenant(ctx, "sales", "Sales")
			if err != nil {
				t.Fatal(err)
			}
			ident, err := st.UpsertIdentity(ctx, "a@example.com", "a-example-com", "")
			if err != nil {
				t.Fatal(err)
			}
			mem, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
			if err != nil {
				t.Fatal(err)
			}
			if e, ok, err := st.GitTokenEpoch(ctx, mem.ID); err != nil || !ok || e != 0 {
				t.Fatalf("new membership epoch = %d ok=%v err=%v, want 0", e, ok, err)
			}
			for want := int64(1); want <= 2; want++ {
				if e, ok, err := st.BumpGitTokenEpoch(ctx, mem.ID); err != nil || !ok || e != want {
					t.Fatalf("bump = %d ok=%v err=%v, want %d", e, ok, err, want)
				}
			}
			if e, _, _ := st.GitTokenEpoch(ctx, mem.ID); e != 2 {
				t.Fatalf("epoch after two bumps = %d, want 2", e)
			}
			if err := st.SetMembershipStatus(ctx, mem.ID, "inactive"); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := st.GitTokenEpoch(ctx, mem.ID); err != nil || ok {
				t.Fatalf("inactive membership read ok=%v err=%v, want not found", ok, err)
			}
			if e, ok, err := st.BumpGitTokenEpoch(ctx, mem.ID); err != nil || !ok || e != 3 {
				t.Fatalf("bump of an inactive membership = %d ok=%v err=%v, want 3", e, ok, err)
			}
			if _, ok, err := st.BumpGitTokenEpoch(ctx, "no-such-membership"); err != nil || ok {
				t.Fatalf("bump of a missing membership ok=%v err=%v, want not found", ok, err)
			}
		})
	}
}
