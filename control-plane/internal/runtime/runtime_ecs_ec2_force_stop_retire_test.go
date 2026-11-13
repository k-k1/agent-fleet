package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// #4086: a force-stop that deregistered the container instance leaves a slot whose ECS agent
// never re-registers, so the next Start must not be handed it. The slot is reserved for
// replacement, and the occupant's next Start takes the replacement path.

func TestForceStopOfADeregisteredSlotRetiresItAndTheNextStartMovesTheHome(t *testing.T) {
	ctx := context.Background()
	slotReplaceSeen.reset()
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	res, err := h.factory().ForceStopSlot(ctx, "i-wedge", false)
	if err != nil || !res.Deregistered || !res.Retired {
		t.Fatalf("ForceStopSlot = %+v, %v; want deregistered and retired", res, err)
	}
	if ec2TagValue(h.ec2.instances["i-wedge"].Tags, ec2TagSlotReplace) == "" {
		t.Fatal("the deregistered slot carries no replacement reservation")
	}
	if attachedInstance(h.ec2.volumes["vol-1"]) != "i-wedge" {
		t.Fatal("the force-stop itself must leave the home where it is")
	}

	// The next Start: never woken on the dead slot; a new one is launched and the home moves.
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if callIndex(h, "StartInstances i-wedge") >= 0 {
		t.Fatalf("the deregistered slot was woken: %v", h.ec2.calls)
	}
	run, detach := callIndex(h, "RunInstances"), callIndex(h, "DetachVolume vol-1")
	if run < 0 || detach < 0 || run > detach {
		t.Fatalf("calls = %v, want a new slot launched, then the home detached", h.ec2.calls)
	}
	if callIndex(h, "DeleteVolume") >= 0 {
		t.Fatalf("a home was deleted: %v", h.ec2.calls)
	}
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); got != "i-new1" {
		t.Fatalf("claim = %q, want the new slot i-new1", got)
	}
	h.ec2.instances["i-new1"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}
	h.ci.registered["i-new1"] = true
	h.runDeferred(ctx)
	if got := attachedInstance(h.ec2.volumes["vol-1"]); got != "i-new1" {
		t.Fatalf("home attached to %q, want i-new1 (once, and only there)", got)
	}
}

// A force-stop that did not deregister (no task ENI) keeps today's behaviour: the slot is not
// reserved and the next Start wakes it.
func TestForceStopWithoutDeregistrationDoesNotRetireTheSlot(t *testing.T) {
	h := wedgedSlot(t)
	res, err := h.factory().ForceStopSlot(context.Background(), "i-wedge", false)
	if err != nil || res.Deregistered || res.Retired {
		t.Fatalf("ForceStopSlot = %+v, %v", res, err)
	}
	if ec2TagValue(h.ec2.instances["i-wedge"].Tags, ec2TagSlotReplace) != "" {
		t.Fatal("a slot that was not deregistered must not be reserved")
	}
}

// The path of a slot with no home gets the same treatment.
func TestForceStopOfADeregisteredSlotWithoutAHomeRetiresIt(t *testing.T) {
	h := newEC2Harness(t)
	inst := h.ec2.addSlot("i-bare", "ap-northeast-1a", "m7i.large", true, false)
	inst.LaunchTime = aws.Time(time.Now().Add(-2 * time.Hour))
	h.ci.registered["i-bare"] = false
	h.ssmc.online = map[string]bool{"i-bare": false}
	h.ssmc.box = "i-bare"
	h.ec2.giveTaskENI("i-bare")
	res, err := h.factory().ForceStopSlot(context.Background(), "i-bare", true)
	if err != nil || !res.Deregistered || !res.Retired {
		t.Fatalf("ForceStopSlot = %+v, %v", res, err)
	}
	if ec2TagValue(h.ec2.instances["i-bare"].Tags, ec2TagSlotReplace) == "" {
		t.Fatal("no reservation on the deregistered homeless slot")
	}
}
