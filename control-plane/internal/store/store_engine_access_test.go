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
	// DeleteTenant with a live membership still holding a grant, and an orphan grant whose
	// membership is already gone (the roster-check/insert race): both must go.
	must(st.SetEngineGrant(ctx, tn.ID, a, EngineAccessLLM, true))
	must(st.SetEngineGrant(ctx, tn.ID, "M-vanished", EngineAccessLLM, true))
	if err := st.SetMembershipStatus(ctx, a, "removed"); err != nil {
		t.Fatal(err)
	}
	must(st.DeleteTenant(ctx, tn.ID))
	if n := countRows(t, st, "engine_access_policy"); n != 0 {
		t.Errorf("a deleted tenant left %d policy row(s)", n)
	}
	if n := countRows(t, st, "engine_access_grant"); n != 0 {
		t.Errorf("a deleted tenant left %d grant row(s)", n)
	}
}

func TestGetMemberEngineAccessMatchesTheWholeTenantRead(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const tid = "T-1"
	for _, c := range []struct {
		role string
		mo   bool
	}{{EngineAccessLLM, true}, {EngineAccessImage, false}} {
		if err := st.SetEngineMembersOnly(ctx, tid, c.role, c.mo); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range [][2]string{{"M-a", EngineAccessLLM}, {"M-a", EngineAccessImage}, {"M-b", EngineAccessImage}} {
		if err := st.SetEngineGrant(ctx, tid, g[0], g[1], true); err != nil {
			t.Fatal(err)
		}
	}
	whole, err := st.GetEngineAccess(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	for _, mid := range []string{"M-a", "M-b", "M-none"} {
		one, err := st.GetMemberEngineAccess(ctx, tid, mid)
		if err != nil {
			t.Fatal(err)
		}
		for _, role := range []string{EngineAccessLLM, EngineAccessImage} {
			if one.Allows(mid, role) != whole.Allows(mid, role) {
				t.Errorf("%s/%s: narrow read says %v, whole read says %v", mid, role, one.Allows(mid, role), whole.Allows(mid, role))
			}
		}
	}
	// Positive control: the fixture really does separate a and b on llm.
	if !whole.Allows("M-a", EngineAccessLLM) || whole.Allows("M-b", EngineAccessLLM) {
		t.Fatalf("fixture: %+v", whole)
	}
}
