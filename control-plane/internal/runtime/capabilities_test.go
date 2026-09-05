// Which adapter claims which optional interface. Behaviour branches on whether `rt.(X)`
// / `f.(X)` succeeds, so an adapter that stops claiming one still compiles and simply
// loses the feature in silence. The claiming direction is pinned at the declaration by
// `var _ X = (*T)(nil)`; the NOT-claiming direction cannot be written that way, and this
// is where it lives. It inspects the unexported adapter types, so it can only sit in the
// same package as the implementation.
package runtime

import "testing"

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
