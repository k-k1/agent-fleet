package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLiteStore(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	if err := st.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := st.migrate(ctx); err != nil { // idempotent
		t.Fatalf("migrate again: %v", err)
	}

	tn, err := st.EnsureDefaultTenant(ctx)
	if err != nil || tn.ID != "default" {
		t.Fatalf("tenant: %v %+v", err, tn)
	}

	// UpsertIdentity is idempotent on user_key; email updated when non-empty;
	// role upgrades but does not downgrade.
	i1, err := st.UpsertIdentity(ctx, "", "k1-kami-gmail-com", "")
	if err != nil {
		t.Fatalf("upsert1: %v", err)
	}
	i2, err := st.UpsertIdentity(ctx, "k1.kami@gmail.com", "k1-kami-gmail-com", "super_admin")
	if err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	if i1.ID != i2.ID {
		t.Fatalf("identity not idempotent: %s != %s", i1.ID, i2.ID)
	}
	if i2.Email != "k1.kami@gmail.com" || i2.Role != "super_admin" {
		t.Fatalf("identity not updated: %+v", i2)
	}

	// Membership: person joins two tenants.
	t2, err := st.CreateTenant(ctx, "security", "Security")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if _, err := st.EnsureMembership(ctx, i2.ID, tn.ID, "member"); err != nil {
		t.Fatalf("membership default: %v", err)
	}
	if _, err := st.EnsureMembership(ctx, i2.ID, t2.ID, "tenant_admin"); err != nil {
		t.Fatalf("membership security: %v", err)
	}
	ms, err := st.ListMemberships(ctx, i2.ID)
	if err != nil || len(ms) != 2 {
		t.Fatalf("memberships: %v n=%d", err, len(ms))
	}

	// GetTenantBySlug
	if got, ok, err := st.GetTenantBySlug(ctx, "security"); err != nil || !ok || got.ID != t2.ID {
		t.Fatalf("by slug: ok=%v err=%v %+v", ok, err, got)
	}

	// Workspace per membership.
	var defMem string
	for _, m := range ms {
		if m.TenantSlug == "default" {
			defMem = m.MembershipID
		}
	}
	if _, ok, err := st.GetWorkspaceByMembership(ctx, defMem); err != nil || ok {
		t.Fatalf("expected no workspace: ok=%v err=%v", ok, err)
	}
	ws := Workspace{
		ID: newID(), TenantID: tn.ID, MembershipID: defMem,
		ContainerName: "af-ws-k1-kami-gmail-com", Network: "af-net-k1-kami-gmail-com",
		DataDir: "/tmp/af-data/k1-kami-gmail-com", AgentPort: "7700",
		AgentToken: "tok", State: "stopped", CreatedAt: nowTS(),
	}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create ws: %v", err)
	}
	got, ok, err := st.GetWorkspaceByMembership(ctx, defMem)
	if err != nil || !ok || got.AgentPort != "7700" || got.ContainerName != ws.ContainerName {
		t.Fatalf("get ws: ok=%v err=%v %+v", ok, err, got)
	}
	if mx, err := st.MaxAgentPort(ctx); err != nil || mx != 7700 {
		t.Fatalf("maxport: %v %d", err, mx)
	}
}
