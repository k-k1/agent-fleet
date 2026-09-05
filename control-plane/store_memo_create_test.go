package main

// This test stays on the main side (ADR 0067 / CP-STORE) because memoCreateFor is an
// API-layer helper in memo.go, not a store one: covering it from inside internal/store
// would drag memo.go across the seam.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// A new memo lands at the end of its repo/category group even when the API omits
// position — the zero value must not be taken as "first".
func TestMemoCreateForAppendsToItsGroup(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	tn, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "a@x.com", "a-x-com", "")
	mem, _ := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")

	first := store.Memo{ID: store.NewID(), MembershipID: mem.ID, Repo: "repo-a", Category: "frontend",
		Kind: "text", Body: "tighten padding", Position: 0, CreatedAt: store.NowTS()}
	if err := st.CreateMemo(ctx, first); err != nil {
		t.Fatalf("create: %v", err)
	}

	created, aerr := memoCreateFor(ctx, st, store.MembershipView{MembershipID: mem.ID}, memoDTO{
		Repo: "repo-a", Category: "frontend", Kind: "text", Body: "add at bottom",
	})
	if aerr != nil || created.Position != 1 {
		t.Fatalf("new memo position: err=%v memo=%+v", aerr, created)
	}
}
