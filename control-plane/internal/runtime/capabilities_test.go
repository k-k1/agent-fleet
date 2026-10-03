// Which adapter claims which optional interface. Behaviour branches on whether `rt.(X)`
// / `f.(X)` succeeds, so an adapter that stops claiming one still compiles and simply
// loses the feature in silence. The claiming direction is pinned at the declaration by
// `var _ X = (*T)(nil)`; the NOT-claiming direction cannot be written that way, and this
// is where it lives. It inspects the unexported adapter types, so it can only sit in the
// same package as the implementation.
package runtime

import (
	"context"
	"testing"
	"time"
)

// Which adapters stage <dataDir>/docs is a fact the start path reads off the type. If an
// ECS adapter ever claimed the marker it would copy megabytes onto the CP's disk that no
// task can read; if docker/native lost it, their bind mount would go empty.
func TestDocsMounterMarker(t *testing.T) {
	var _ DocsMounter = (*dockerRuntime)(nil)
	var _ DocsMounter = (*nativeRuntime)(nil)
	if _, ok := any((*ecsRuntime)(nil)).(DocsMounter); ok {
		t.Error("ecsRuntime has no host seam to mount from — it must not claim staged docs")
	}
	if _, ok := any((*ecsEC2Runtime)(nil)).(DocsMounter); ok {
		t.Error("ecsEC2Runtime has no host seam to mount from — it must not claim staged docs")
	}
}

// TestOnlyThePoolAdapterClaimsTheGoldenBake keeps the bake on the EC2 slot pool, the only
// deployment that seeds new homes from a shared snapshot; the other profiles have neither
// something to bake nor anywhere to keep it.
//
// The claiming direction is pinned by runtime_ecs_ec2_golden.go's
// `var _ GoldenBakePool = (*ecsEC2Factory)(nil)`. This is the reverse: if docker/native
// ever satisfied it by accident, goldenBakerFor would start a baker — a docker deployment
// that inexplicably bakes goldens, and no error anywhere.
func TestOnlyThePoolAdapterClaimsTheGoldenBake(t *testing.T) {
	for _, c := range []struct {
		name string
		f    RuntimeFactory
	}{
		{"dockerFactory", (*dockerFactory)(nil)},
		{"nativeFactory", (*nativeFactory)(nil)},
		{"ecsFactory", (*ecsFactory)(nil)},
	} {
		if _, ok := any(c.f).(GoldenBakePool); ok {
			t.Errorf("%s claims GoldenBakePool — it has no slot pool to seed a home from", c.name)
		}
	}
}

// The home ports (home_wipe.go). Claiming one is a promise the CP acts on: it stops the
// workspace and reports the home wiped. An adapter that claimed one it cannot keep would
// be the defect those ports exist to end — a success that removed nothing — so the
// adapters that must NOT claim are pinned here.
func TestHomePortsAreClaimedOnlyWhereTheHomeIsReachable(t *testing.T) {
	// Fargate's home is on EFS, which the CP reaches only through the task its stack
	// declares: it claims Wipe and Erase by type, behind the gate that asks for that task
	// (TestECSHomePortsFollowTheStack pins both answers of the gate).
	fargate := any((*ecsRuntime)(nil))
	if _, ok := fargate.(homePortsGate); !ok {
		t.Error("ecsRuntime claims the home ports without the gate; an older stack would offer buttons that always fail")
	}
	if _, ok := fargate.(homeBackupKeeper); ok {
		t.Error("ecsRuntime claims homeBackupKeeper, but Fargate keeps no copies of a home")
	}
	// The slot pool claims every port (home_wipe.go pins that direction). Its member's wipe
	// is kept by the Start that follows the request, not inside it: see
	// runtime_ecs_ec2_home_wipe.go, and the tests there that pin the order mount → wipe →
	// task.
	// Only the slot pool keeps backup copies.
	for _, rt := range []any{(*dockerRuntime)(nil), (*nativeRuntime)(nil)} {
		if _, ok := rt.(homeBackupKeeper); ok {
			t.Errorf("%T claims homeBackupKeeper, but it keeps no copies of a home", rt)
		}
	}
}

// The kubernetes profile's claims, both directions (ADR 0106 decision 11).
func TestKubernetesCapabilities(t *testing.T) {
	rt := any((*kubeRuntime)(nil))
	f := any((*kubeFactory)(nil))
	for name, ok := range map[string]bool{
		"TaskCounter": implements[TaskCounter](rt),
		"BootPhase":   implements[interface{ BootPhase() string }](rt),
		"Stale":       implements[interface{ Stale(context.Context) bool }](rt),
		"WipeHome":    implements[homeWiper](rt),
		"EraseHome":   implements[homeEraser](rt),
		"ResizeHome": implements[interface {
			ResizeHome(context.Context) (HomeResize, error)
		}](rt),
		"SizingProfile":  implements[interface{ SizingProfile() WorkspaceSizing }](f),
		"CostProfile":    implements[interface{ CostProfile() CostProfile }](f),
		"WorkspaceImage": implements[interface{ WorkspaceImage() string }](f),
	} {
		if !ok {
			t.Errorf("the kubernetes adapter does not claim %s", name)
		}
	}
	for name, ok := range map[string]bool{
		"DocsMounter":           implements[DocsMounter](rt),
		"MachineProfile":        implements[interface{ MachineProfile() WorkspaceMachine }](rt),
		"AcquireOperationFence": implements[runtimeOperationFencer](rt),
		"StartFencer":           implements[StartFencer](rt),
		"LaunchBudgeter":        implements[LaunchBudgeter](rt),
		"BeginHibernate":        implements[interface{ BeginHibernate(context.Context) error }](rt),
		"BackupHome": implements[interface {
			BackupHome(context.Context, time.Duration) error
		}](rt),
		"homeBackupKeeper": implements[homeBackupKeeper](rt),
		// The Start that carries the wipe out runs inside the handler's lease and leaves
		// nothing in the background, so no wipe can be asked for while one is half done.
		"homeWipeGate":   implements[homeWipeGate](rt),
		"GoldenBakePool": implements[GoldenBakePool](f),
	} {
		if ok {
			t.Errorf("the kubernetes adapter claims %s", name)
		}
	}
	if ops := HomeOperationsOf(&kubeFactory{cfg: &kubeConfig{}}); !ops.Wipe || !ops.Erase || ops.Backups {
		t.Errorf("HomeOperationsOf(kubernetes) = %+v, want wipe and erase, no backups", ops)
	}
}

func implements[T any](v any) bool { _, ok := v.(T); return ok }
