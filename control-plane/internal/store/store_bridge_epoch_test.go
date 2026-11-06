package store

import (
	"context"
	"testing"
)

func bridgeMembership(t *testing.T, st Store, email string) Membership {
	t.Helper()
	ctx := context.Background()
	tn, err := st.CreateTenant(ctx, "t-"+email[:1], "T")
	if err != nil {
		t.Fatal(err)
	}
	ident, err := st.UpsertIdentity(ctx, email, email[:1]+"-example-com", "")
	if err != nil {
		t.Fatal(err)
	}
	mem, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	return mem
}

// The bridge epoch (issue #3452) starts at 0, so the upgrade keeps every token valid; a bump
// moves it (and the git epoch) by one, reaches an inactive membership, and the read side only
// answers for an active one.
func TestBridgeEpochBumpAndRead(t *testing.T) {
	for name, st := range ssmStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			mem := bridgeMembership(t, st, "a@example.com")
			if e, ok, err := st.BridgeEpoch(ctx, mem.ID); err != nil || !ok || e != 0 {
				t.Fatalf("new membership epoch = %d ok=%v err=%v, want 0", e, ok, err)
			}
			if e, ok, err := st.BumpBridgeEpoch(ctx, mem.ID); err != nil || !ok || e != 1 {
				t.Fatalf("bump = %d ok=%v err=%v, want 1", e, ok, err)
			}
			if g, _, _ := st.GitTokenEpoch(ctx, mem.ID); g != 1 {
				t.Fatalf("a bridge rotation must advance the git epoch too, got %d", g)
			}
			if err := st.SetMembershipStatus(ctx, mem.ID, "inactive"); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := st.BridgeEpoch(ctx, mem.ID); err != nil || ok {
				t.Fatalf("inactive membership read ok=%v err=%v, want not found", ok, err)
			}
			if _, ok, err := st.BumpBridgeEpoch(ctx, "no-such-membership"); err != nil || ok {
				t.Fatalf("bump of a missing membership ok=%v err=%v", ok, err)
			}
		})
	}
}

// Removal and re-activation each move both epochs once; repeating the same status does not.
func TestBridgeEpochAdvancesOnRemovalAndReactivation(t *testing.T) {
	for name, st := range ssmStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			mem := bridgeMembership(t, st, "b@example.com")
			b0, _, _ := st.BridgeEpoch(ctx, mem.ID)
			g0, _, _ := st.GitTokenEpoch(ctx, mem.ID)
			for _, status := range []string{"inactive", "inactive", "active", "active"} {
				if err := st.SetMembershipStatus(ctx, mem.ID, status); err != nil {
					t.Fatal(err)
				}
			}
			b1, _, _ := st.BridgeEpoch(ctx, mem.ID)
			g1, _, _ := st.GitTokenEpoch(ctx, mem.ID)
			if b1 != b0+2 {
				t.Errorf("bridge epoch %d -> %d, want +2 (one removal, one re-activation)", b0, b1)
			}
			if g1 != g0+2 {
				t.Errorf("git epoch %d -> %d, want +2", g0, g1)
			}
		})
	}
}

func TestBumpAllBridgeEpochsAndStartedMarker(t *testing.T) {
	for name, st := range ssmStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			a := bridgeMembership(t, st, "c@example.com")
			b := bridgeMembership(t, st, "d@example.com")
			if err := st.SetMembershipStatus(ctx, b.ID, "inactive"); err != nil { // epoch 1, inactive
				t.Fatal(err)
			}
			n, err := st.BumpAllBridgeEpochs(ctx)
			if c, err := st.CountMemberships(ctx); err != nil || c != n {
				t.Errorf("CountMemberships = %d (%v), want what the bump moved: %d", c, err, n)
			}
			if err != nil || n < 2 {
				t.Fatalf("BumpAll moved %d rows err=%v, want at least both memberships (inactive included)", n, err)
			}
			if e, _, _ := st.BridgeEpoch(ctx, a.ID); e != 1 {
				t.Errorf("active membership epoch = %d, want 1", e)
			}
			if _, cur, _, _ := st.BridgeEpochStarted(ctx, b.ID); cur != 2 {
				t.Errorf("inactive membership epoch = %d, want 2", cur)
			}
			if err := st.SetBridgeEpochStarted(ctx, a.ID, 1); err != nil {
				t.Fatal(err)
			}
			if started, cur, ok, _ := st.BridgeEpochStarted(ctx, a.ID); !ok || started != 1 || cur != 1 {
				t.Errorf("started/current = %d/%d", started, cur)
			}
		})
	}
}
