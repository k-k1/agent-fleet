package store

import (
	"context"
	"testing"
)

// #2709: the generation check and the write are one transaction, and a bump withdraws the flags in
// the same one, on SQLite and (when AF_TEST_DATABASE_URL is set) Postgres.
func TestWorkItemCommentGeneration(t *testing.T) {
	for name, st := range ssmStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			tn, _ := st.CreateTenant(ctx, "sales", "Sales")
			ident, _ := st.UpsertIdentity(ctx, "a@example.com", "a-example-com", "")
			mem, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
			if err != nil {
				t.Fatal(err)
			}
			row := func(id string) WorkItem {
				return WorkItem{ID: id, MembershipID: mem.ID, QueryID: "q", Provider: "bitbucket", Kind: "pr",
					Key: "acme/web#" + id, FetchedAt: "2026-10-25T00:00:00Z", CanComment: true}
			}
			can := func() []bool {
				items, err := st.ListWorkItems(ctx, mem.ID)
				if err != nil {
					t.Fatal(err)
				}
				var out []bool
				for _, it := range items {
					out = append(out, it.CanComment)
				}
				return out
			}
			g0, err := st.WorkItemCommentGen(ctx, mem.ID)
			if err != nil || g0 != 0 {
				t.Fatalf("initial gen = %d err=%v", g0, err)
			}
			// Same generation: the flag is stored.
			if err := st.ReplaceWorkItemsAt(ctx, mem.ID, []string{"q"}, []WorkItem{row("1")}, g0); err != nil {
				t.Fatal(err)
			}
			if got := can(); len(got) != 1 || !got[0] {
				t.Fatalf("flag at the current generation = %v", got)
			}
			// A bump withdraws it and moves the generation.
			if err := st.BumpWorkItemCommentGen(ctx, mem.ID); err != nil {
				t.Fatal(err)
			}
			if got := can(); len(got) != 1 || got[0] {
				t.Fatalf("flag after a bump = %v", got)
			}
			if g1, _ := st.WorkItemCommentGen(ctx, mem.ID); g1 != g0+1 {
				t.Fatalf("gen after a bump = %d", g1)
			}
			// A writer that began under the old generation stores its rows without the flag.
			if err := st.ReplaceWorkItemsAt(ctx, mem.ID, []string{"q"}, []WorkItem{row("2")}, g0); err != nil {
				t.Fatal(err)
			}
			if got := can(); len(got) != 1 || got[0] {
				t.Fatalf("a stale writer restored the flag: %v", got)
			}
		})
	}
}
