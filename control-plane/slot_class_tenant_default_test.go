package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// capturingFactory remembers the workspace record of the last runtime it was asked to
// build, and serves the sizing profile (sizingProfiler) the class chain reads.
type capturingFactory struct {
	sizing runtime.WorkspaceSizing
	got    *runtime.Workspace
}

func (f capturingFactory) New(ws runtime.Workspace, _ runtime.SecretKeys, _ []string) runtime.Runtime {
	*f.got = ws
	return resizableStub{}
}

func (f capturingFactory) SizingProfile() runtime.WorkspaceSizing { return f.sizing }

// resizableStub satisfies homeResizer so resizeHomeByMembership reaches the factory's output.
type resizableStub struct{ runtime.Runtime }

func (resizableStub) ResizeHome(context.Context) (runtime.HomeResize, error) {
	return runtime.HomeResize{}, nil
}

var tenantDefaultClassSizing = runtime.WorkspaceSizing{
	Runtime: "ecs-ec2", DefaultSlotClass: "standard",
	SlotClasses: []runtime.WorkspaceSlotClass{
		{ID: "standard", Label: "S", Arch: "x86_64"},
		{ID: "arm", Label: "A", Arch: "arm64"},
	},
}

// A tenant default class must reach every runtime build that precedes a start. An unattended
// start (the scheduler's wake) and a home resize used to resolve the size axes only, so the
// record carried SlotClass "" and ecs-ec2 placed the member on the deployment default class.
func TestRuntimeBuildsCarryTheTenantDefaultClass(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, mv, _ := networkFixture(t)
	var got runtime.Workspace
	mgr.rtFactory = capturingFactory{sizing: tenantDefaultClassSizing, got: &got}
	lj, _ := json.Marshal(tenantLimits{SlotClass: "arm"})
	if err := st.SetTenantLimits(ctx, tn.ID, string(lj)); err != nil {
		t.Fatal(err)
	}
	ws := store.Workspace{ID: store.NewID(), TenantID: tn.ID, MembershipID: mv.MembershipID,
		ContainerName: "af-yamada", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t",
		State: "stopped", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	t.Run("unattended start", func(t *testing.T) {
		got = runtime.Workspace{}
		if _, err := mgr.runtimeForUnattended(ctx, &resolved{ws: ws}); err != nil {
			t.Fatal(err)
		}
		if got.SlotClass != "arm" {
			t.Fatalf("SlotClass = %q, want the tenant default \"arm\"", got.SlotClass)
		}
	})

	t.Run("home resize", func(t *testing.T) {
		got = runtime.Workspace{}
		if _, err := mgr.resizeHomeByMembership(ctx, mv.MembershipID); err != nil {
			t.Fatal(err)
		}
		if got.SlotClass != "arm" {
			t.Fatalf("SlotClass = %q, want the tenant default \"arm\"", got.SlotClass)
		}
	})
}

// The roster reports the class a member's next start lands on next to the stored one, so a
// member who follows the tenant default is not drawn as the deployment default box.
func TestAdminMembersReportTheEffectiveSlotClass(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, mv, _ := networkFixture(t)
	var got runtime.Workspace
	mgr.rtFactory = capturingFactory{sizing: tenantDefaultClassSizing, got: &got}
	lj, _ := json.Marshal(tenantLimits{SlotClass: "arm"})
	if err := st.SetTenantLimits(ctx, tn.ID, string(lj)); err != nil {
		t.Fatal(err)
	}

	// No stored limits at all: this is the member the stored value cannot describe.
	rows := memberRows(t, mgr, "boss@acme.co.jp")
	if eff := rows["yamada-acme-co-jp"]["slot_class_effective"]; eff != "arm" {
		t.Fatalf("no stored limits: slot_class_effective = %v, want the tenant default", eff)
	}

	// An explicit per-member class wins, and the stored value stays as stored.
	if err := st.PutUserLimit(ctx, mv.MembershipID, store.UserQuota{SlotClass: "standard"}); err != nil {
		t.Fatal(err)
	}
	row := memberRows(t, mgr, "boss@acme.co.jp")["yamada-acme-co-jp"]
	// The "follow the tenant default" target does not depend on what is stored.
	if row["slot_class_default"] != "arm" {
		t.Fatalf("explicit class: slot_class_default = %v, want the tenant default", row["slot_class_default"])
	}
	if row["slot_class"] != "standard" || row["slot_class_effective"] != "standard" {
		t.Fatalf("explicit class: slot_class=%v effective=%v", row["slot_class"], row["slot_class_effective"])
	}

	// Stored limits with no class: the stored value is "", the effective one is the tenant's.
	if err := st.PutUserLimit(ctx, mv.MembershipID, store.UserQuota{MaxSessions: 3}); err != nil {
		t.Fatal(err)
	}
	row = memberRows(t, mgr, "boss@acme.co.jp")["yamada-acme-co-jp"]
	if row["slot_class"] != "" || row["slot_class_effective"] != "arm" {
		t.Fatalf("follow tenant: slot_class=%v effective=%v", row["slot_class"], row["slot_class_effective"])
	}
}
