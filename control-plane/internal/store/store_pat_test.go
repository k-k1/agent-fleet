package store

import (
	"context"
	"testing"
)

// Both dialects: a token issued before PATs had a resource reads back as a /mcp token, and a
// memory token keeps its binding through create, lookup and list (#2764).
func TestPATResourceDefaultsToMCPAndRoundTrips(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		id, err := st.UpsertIdentity(ctx, "pat@example.com", "pat-example-com", "")
		if err != nil {
			t.Fatalf("%s: identity: %v", name, err)
		}
		// The INSERT an older Control Plane ran, without the column.
		if _, err := st.db.ExecContext(ctx, `INSERT INTO pat(id, identity_id, token_hash, scope, name, created_at)
			VALUES(?, ?, ?, ?, ?, ?)`, "legacy", id.ID, "h-legacy", "write", "old", NowTS()); err != nil {
			t.Fatalf("%s: legacy insert: %v", name, err)
		}
		if err := st.CreatePAT(ctx, PAT{ID: "plain", IdentityID: id.ID, Scope: "read", CreatedAt: NowTS()}, "h-plain"); err != nil {
			t.Fatalf("%s: create: %v", name, err)
		}
		if err := st.CreatePAT(ctx, PAT{ID: "mem", IdentityID: id.ID, Scope: "read", CreatedAt: NowTS(), Resource: PATResourceMemory}, "h-mem"); err != nil {
			t.Fatalf("%s: create memory: %v", name, err)
		}
		for hash, want := range map[string]string{"h-legacy": PATResourceMCP, "h-plain": PATResourceMCP, "h-mem": PATResourceMemory} {
			if p, ok, err := st.GetPATByHash(ctx, hash); err != nil || !ok || p.Resource != want {
				t.Fatalf("%s: %s = %+v ok=%v err=%v, want resource %q", name, hash, p, ok, err, want)
			}
		}
		ps, err := st.ListPATsByIdentity(ctx, id.ID)
		if err != nil || len(ps) != 3 {
			t.Fatalf("%s: list: %v %+v", name, err, ps)
		}
		for _, p := range ps {
			if want := map[string]string{"legacy": PATResourceMCP, "plain": PATResourceMCP, "mem": PATResourceMemory}[p.ID]; p.Resource != want {
				t.Fatalf("%s: listed %s with resource %q, want %q", name, p.ID, p.Resource, want)
			}
		}
	}
}
