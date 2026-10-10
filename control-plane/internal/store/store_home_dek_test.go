package store

import (
	"context"
	"testing"
)

func TestHomeDEKInsertNeverReplaces(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		ws := homeOpWorkspace(t, st)
		first, err := st.InsertHomeDEK(ctx, HomeDEK{MembershipID: ws.MembershipID, Ciphertext: "sealed-1", KeyRef: ws.TenantID})
		if err != nil || first.Ciphertext != "sealed-1" || first.Scheme != HomeDEKMigrating {
			t.Fatalf("%s: first insert = %+v, %v", name, first, err)
		}
		// A second start that raced the first gets the stored key back, not its own.
		second, err := st.InsertHomeDEK(ctx, HomeDEK{MembershipID: ws.MembershipID, Ciphertext: "sealed-2", KeyRef: ws.TenantID})
		if err != nil || second.Ciphertext != "sealed-1" {
			t.Fatalf("%s: second insert = %+v, %v; want the first key kept", name, second, err)
		}
		// Deleting the workspace row leaves the home's key, and so does finishing a Destroy.
		if err := st.DeleteWorkspace(ctx, ws.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := st.GetHomeDEK(ctx, ws.MembershipID); err != nil || !ok {
			t.Fatalf("%s: the home key went with the workspace row (ok=%v, err=%v)", name, ok, err)
		}
		ws2 := homeOpWorkspace(t, st)
		if _, err := st.InsertHomeDEK(ctx, HomeDEK{MembershipID: ws2.MembershipID, Ciphertext: "sealed", KeyRef: ws2.TenantID}); err != nil {
			t.Fatal(err)
		}
		op := HomeOperation{ID: NewID(), WorkspaceID: ws2.ID, MembershipID: ws2.MembershipID, Kind: HomeOpDestroy, Op: "destroy"}
		if err := st.InsertHomeOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		if ok, err := st.FinishHomeOperation(ctx, op.ID, HomeOperationFinish{DeleteWorkspace: true}); err != nil || !ok {
			t.Fatalf("%s: finish = %v, %v", name, ok, err)
		}
		if _, ok, err := st.GetHomeDEK(ctx, ws2.MembershipID); err != nil || !ok {
			t.Fatalf("%s: a finished Destroy dropped the home key (ok=%v, err=%v)", name, ok, err)
		}
	}
}

func TestCountHomeDEKs(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		with := homeOpWorkspace(t, st)
		homeOpWorkspace(t, st) // a second home with no key yet
		if _, err := st.InsertHomeDEK(ctx, HomeDEK{MembershipID: with.MembershipID, Ciphertext: "sealed", KeyRef: with.TenantID}); err != nil {
			t.Fatal(err)
		}
		c, err := st.CountHomeDEKs(ctx)
		if err != nil || c != (HomeDEKCounts{Migrating: 1, WithoutKey: 1}) {
			t.Fatalf("%s: counts = %+v, %v", name, c, err)
		}
	}
}
