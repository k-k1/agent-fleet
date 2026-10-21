package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

// A Stop that lands while the background half of a Start is still converging must win:
// the goroutine may not raise desiredCount afterwards, the claim must not hold the
// workspace at `starting`, and no phase may be left behind.
func TestECSEC2StopWhileStartConvergesWins(t *testing.T) {
	ctx := context.Background()
	h := memberWipeHarness(t)
	defer h.rt.setPhase("")
	h.ci.registered["i-hot"] = false // not registered yet: the Start goes to the background
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.deferred) != 1 {
		t.Fatalf("want a background Start, deferred=%d", len(h.deferred))
	}
	if got := h.rt.State(ctx); got != "starting" {
		t.Fatalf("State while converging = %q, want starting", got)
	}

	if err := h.rt.Stop(ctx); err != nil {
		t.Fatalf("Stop while starting: %v", err)
	}
	if got := h.rt.State(ctx); got != "stopped" {
		t.Errorf("State right after Stop = %q, want stopped (the claim must not outlive it)", got)
	}
	if ph := h.rt.BootPhase(); ph != "" {
		t.Errorf("phase left behind by the stop: %q", ph)
	}

	// The slot registers and the goroutine carries on: it must find itself overtaken.
	h.ci.registered["i-hot"] = true
	h.runDeferred(ctx)
	if scaledUp(h) {
		t.Error("the overtaken Start raised desiredCount after the Stop")
	}
	if got := h.rt.State(ctx); got != "stopped" {
		t.Errorf("State after the overtaken Start ended = %q, want stopped", got)
	}
	if ph := h.rt.BootPhase(); ph != "" {
		t.Errorf("phase left behind by the overtaken start: %q", ph)
	}
	// A fresh Start afterwards is unaffected by the earlier one's count.
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start after Stop: %v", err)
	}
	h.runDeferred(ctx)
	if !scaledUp(h) {
		t.Error("a Start after the Stop did not scale up")
	}
}

// An overtaken Start that wakes up after a Stop and a restart must not tear down what the
// restart is using: its slot-lost cleanup (stop the slot, detach the home) is skipped.
func TestECSEC2OvertakenStartDoesNotAbandonTheRestartsSlot(t *testing.T) {
	ctx := context.Background()
	h := memberWipeHarness(t)
	defer h.rt.setPhase("")
	h.ci.registered["i-hot"] = false
	h.rt.pool.slotLostAfter = time.Second
	if err := h.rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	once := false
	h.rt.sleep = func(context.Context, time.Duration) error {
		if !once {
			once = true
			if err := h.rt.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			h.ci.registered["i-hot"] = true
			if err := h.rt.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if !scaledUp(h) {
				t.Fatal("positive control: the restart did not launch")
			}
		}
		return nil
	}
	h.runDeferred(ctx)
	if got := attachedInstance(h.ec2.volumes["vol-1"]); got != "i-hot" {
		t.Errorf("the overtaken Start detached the restart's home: attachment=%q", got)
	}
	if got := h.ec2.instances["i-hot"].State.Name; got != "running" {
		t.Errorf("the overtaken Start stopped the restart's slot: %s", got)
	}
}

// ecsAfterRegister runs a hook once, right after RegisterTaskDefinition returns.
type ecsAfterRegister struct {
	ecsAPI
	hook func()
}

func (w *ecsAfterRegister) RegisterTaskDefinition(ctx context.Context, in *ecs.RegisterTaskDefinitionInput, opt ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error) {
	out, err := w.ecsAPI.RegisterTaskDefinition(ctx, in, opt...)
	if f := w.hook; f != nil {
		w.hook = nil
		f()
	}
	return out, err
}

// The pre-point of the service at a new revision is a service update like the scale-up: an
// overtaken Start must not put the old revision back over the restart's.
func TestECSEC2OvertakenStartDoesNotRepointTheRestartsService(t *testing.T) {
	ctx := context.Background()
	h := memberWipeHarness(t)
	defer h.rt.setPhase("")
	h.ci.registered["i-hot"] = false
	if err := h.rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	w := &ecsAfterRegister{ecsAPI: h.ecs}
	h.rt.base.ecs = w
	var fresh string
	w.hook = func() {
		if err := h.rt.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		h.ci.registered["i-hot"] = true
		if err := h.rt.Start(ctx); err != nil {
			t.Fatal(err)
		}
		fresh = aws.ToString(h.ecs.services[h.rt.base.name].TaskDefinition)
		if !scaledUp(h) {
			t.Fatal("positive control: the restart did not launch")
		}
	}
	h.runDeferred(ctx)
	if got := aws.ToString(h.ecs.services[h.rt.base.name].TaskDefinition); got != fresh {
		t.Errorf("the overtaken Start reverted the restart's task definition: got %s want %s", got, fresh)
	}
}
