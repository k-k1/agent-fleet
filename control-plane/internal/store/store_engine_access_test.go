package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestEngineAccessRoundTripAndCascade(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, err := st.CreateTenant(ctx, "sales", "Sales")
	if err != nil {
		t.Fatal(err)
	}
	var mids []string
	for _, email := range []string{"a@example.com", "b@example.com"} {
		id, err := st.UpsertIdentity(ctx, email, sanitizeUser(email), "")
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
		if err != nil {
			t.Fatal(err)
		}
		mids = append(mids, m.ID)
	}
	a, b := mids[0], mids[1]

	acc, err := st.GetEngineAccess(ctx, tn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !acc.Allows(a, EngineAccessLLM) || !acc.Allows(b, EngineAccessImage) {
		t.Fatalf("a tenant nobody configured must be open to every member: %+v", acc)
	}

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetEngineMembersOnly(ctx, tn.ID, EngineAccessImage, true))
	must(st.SetEngineMembersOnly(ctx, tn.ID, EngineAccessImage, true)) // idempotent
	must(st.SetEngineGrant(ctx, tn.ID, a, EngineAccessImage, true))
	must(st.SetEngineGrant(ctx, tn.ID, a, EngineAccessImage, true))
	if err := st.SetEngineGrant(ctx, tn.ID, a, "chat", true); err == nil {
		t.Error("an unknown role was stored")
	}

	acc, err = st.GetEngineAccess(ctx, tn.ID)
	must(err)
	if !acc.Allows(a, EngineAccessImage) || acc.Allows(b, EngineAccessImage) {
		t.Errorf("image is members-only with only a granted: %+v", acc)
	}
	if !acc.Allows(b, EngineAccessLLM) {
		t.Error("restricting image must leave llm open")
	}

	// Switching the restriction off keeps the list, so switching it back restores it.
	must(st.SetEngineMembersOnly(ctx, tn.ID, EngineAccessImage, false))
	acc, _ = st.GetEngineAccess(ctx, tn.ID)
	if !acc.Allows(b, EngineAccessImage) || !acc.Grants[a][EngineAccessImage] {
		t.Errorf("open mode: %+v", acc)
	}
	must(st.SetEngineMembersOnly(ctx, tn.ID, EngineAccessImage, true))

	must(st.SetEngineGrant(ctx, tn.ID, a, EngineAccessImage, false))
	acc, _ = st.GetEngineAccess(ctx, tn.ID)
	if acc.Allows(a, EngineAccessImage) {
		t.Error("revoked grant still allows")
	}

	must(st.SetEngineGrant(ctx, tn.ID, b, EngineAccessLLM, true))
	must(st.DeleteMembership(ctx, b))
	if n := countRows(t, st, "engine_access_grant"); n != 0 {
		t.Errorf("a deleted membership left %d grant row(s)", n)
	}
	must(st.SetEngineGrant(ctx, tn.ID, a, EngineAccessLLM, true))
	must(st.DeleteMembership(ctx, a))
	must(st.DeleteTenant(ctx, tn.ID))
	if n := countRows(t, st, "engine_access_policy"); n != 0 {
		t.Errorf("a deleted tenant left %d policy row(s)", n)
	}
}
