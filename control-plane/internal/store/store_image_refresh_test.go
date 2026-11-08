package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestClaimImageRefreshIsOncePerTarget(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	id, _ := st.UpsertIdentity(ctx, "r@example.com", "r", "")
	m, _ := st.EnsureMembership(ctx, id.ID, tenant.ID, "member")
	ws := Workspace{ID: NewID(), TenantID: tenant.ID, MembershipID: m.ID, ContainerName: "r", Network: "n",
		DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	claim := func(target string) bool {
		ok, err := st.ClaimImageRefresh(ctx, ws.ID, target, NowTS())
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !claim("a") {
		t.Fatal("first claim lost")
	}
	if claim("a") {
		t.Fatal("same image claimed twice")
	}
	if !claim("b") {
		t.Fatal("a new image must be claimable")
	}
	if got, ok, _ := st.GetImageRefreshTarget(ctx, ws.ID); !ok || got != "b" {
		t.Fatalf("target = %q %v", got, ok)
	}
}
