package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// TestResolveWorkspaceMemBytes covers the clamp chain: unset → default (0), floor,
// per-tenant cap, and the deployment hard ceiling.
func TestResolveWorkspaceMemBytes(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "a@x.com", "a-x-com", "")
	mem, _ := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
	ws := store.Workspace{MembershipID: mem.ID, TenantID: tn.ID}

	m := &manager{store: st}

	// Unset per-user → 0 (deployment default applies at the factory).
	if got := m.resolveWorkspaceMemBytes(ctx, ws); got != 0 {
		t.Errorf("unset: got %d, want 0", got)
	}

	// Below the floor is raised to the floor.
	_ = st.PutUserLimit(ctx, mem.ID, store.UserQuota{MemLimit: 64 * mib})
	if got := m.resolveWorkspaceMemBytes(ctx, ws); got != memFloorBytes {
		t.Errorf("floor: got %d, want %d", got, memFloorBytes)
	}

	// A normal value passes through untouched.
	_ = st.PutUserLimit(ctx, mem.ID, store.UserQuota{MemLimit: 4 * gib})
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

// The CPU and disk axes go through the same two-stage clamp as memory (per-user value
// bounded by the tenant cap) and are INDEPENDENT of it — setting one must not disturb
// the others, which is the point of storing three numbers instead of a named size
// (ADR 0044 decision 1).
func TestResolveWorkspaceSizeCPUAndDisk(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "a@x.com", "a-x-com", "")
	mem, _ := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
	ws := store.Workspace{MembershipID: mem.ID, TenantID: tn.ID}
	m := &manager{store: st}

	if b, c, d := m.resolveWorkspaceSize(ctx, ws); b != 0 || c != 0 || d != 0 {
		t.Errorf("unset: got %d/%d/%d, want 0/0/0", b, c, d)
	}

	// All three set at once and none of them interfering with the others.
	_ = st.PutUserLimit(ctx, mem.ID, store.UserQuota{MemLimit: 8 * gib, CPULimit: 4096, DiskGB: 60})
	if b, c, d := m.resolveWorkspaceSize(ctx, ws); b != 8*gib || c != 4096 || d != 60 {
		t.Errorf("passthrough: got %d/%d/%d, want %d/4096/60", b, c, d, 8*gib)
	}

	// Each tenant cap clamps only its own axis.
	lj, _ := json.Marshal(tenantLimits{MaxWorkspaceCPU: 2048, MaxWorkspaceDiskGB: 30})
	_ = st.SetTenantLimits(ctx, tn.ID, string(lj))
	b, c, d := m.resolveWorkspaceSize(ctx, ws)
	if c != 2048 || d != 30 {
		t.Errorf("tenant caps: cpu=%d disk=%d, want 2048/30", c, d)
	}
	if b != 8*gib {
		t.Errorf("memory must be untouched by the cpu/disk caps: got %d, want %d", b, 8*gib)
	}
}
