package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestResolveWorkspaceMemBytes covers the clamp chain: unset → default (0), floor,
// per-tenant cap, and the deployment hard ceiling.
func TestResolveWorkspaceMemBytes(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "a@x.com", "a-x-com", "")
	mem, _ := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
	ws := Workspace{MembershipID: mem.ID, TenantID: tn.ID}

	m := &manager{store: st}

	// Unset per-user → 0 (deployment default applies at the factory).
	if got := m.resolveWorkspaceMemBytes(ctx, ws); got != 0 {
		t.Errorf("unset: got %d, want 0", got)
	}

	// Below the floor is raised to the floor.
	_ = st.PutUserLimit(ctx, mem.ID, 0, 0, 64*mib)
	if got := m.resolveWorkspaceMemBytes(ctx, ws); got != memFloorBytes {
		t.Errorf("floor: got %d, want %d", got, memFloorBytes)
	}

	// A normal value passes through untouched.
	_ = st.PutUserLimit(ctx, mem.ID, 0, 0, 4*gib)
	if got := m.resolveWorkspaceMemBytes(ctx, ws); got != 4*gib {
		t.Errorf("passthrough: got %d, want %d", got, 4*gib)
	}

	// Tenant cap clamps a larger request down.
	lj, _ := json.Marshal(tenantLimits{MaxWorkspaceMem: 2 * gib})
	_ = st.SetTenantLimits(ctx, tn.ID, string(lj))
	if got := m.resolveWorkspaceMemBytes(ctx, ws); got != 2*gib {
		t.Errorf("tenant cap: got %d, want %d", got, 2*gib)
	}

	// The deployment hard ceiling clamps below the tenant cap.
	m.memMaxBytes = 1 * gib
	if got := m.resolveWorkspaceMemBytes(ctx, ws); got != 1*gib {
		t.Errorf("host ceiling: got %d, want %d", got, 1*gib)
	}
}
