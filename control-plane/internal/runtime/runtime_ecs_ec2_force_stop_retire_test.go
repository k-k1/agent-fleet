package runtime

import (
	"context"
	"errors"
	"strings"
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

// The reservation must already be on the box when the stop goes out: the token is overtaken by
// any Start as soon as the guarded stop returns, and a Start that read the slot unreserved
// would wake a box that never registers again.
func TestForceStopReservesTheDeregisteredSlotBeforeTheStopIsSent(t *testing.T) {
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	var atStop string
	var stopsAtHook int
	hookAt("stopping", func() {
		atStop = ec2TagValue(h.ec2.instances["i-wedge"].Tags, ec2TagSlotReplace)
		stopsAtHook = stopsOnBox(h)
	})
	defer func() { rescueStepHook = nil }()
	if _, err := h.factory().ForceStopSlot(context.Background(), "i-wedge", false); err != nil {
		t.Fatalf("ForceStopSlot: %v", err)
	}
	if atStop == "" || stopsAtHook != 0 {
		t.Fatalf("reservation %q with %d stops sent when the stop was about to go out; want it set first", atStop, stopsAtHook)
	}
}

// #4098: an ENI that needs longer than the old 15-poll window (about 30 s) to go must not make
// the detached force-stop give up half done; it waits for the ENI and stops the box.
func TestForceStopWaitsForAnENIThatReleasesAfterTheOldWindow(t *testing.T) {
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	h.ci.stickyENI = true // the ENI outlives the deregistration until the sleep hook drops it
	f := h.factory()
	sleeps := 0
	f.sleep = func(c context.Context, d time.Duration) error {
		sleeps++
		if sleeps == rescueENIPolls+5 { // past the old cap
			h.ec2.dropTaskENIs("i-wedge")
		}
		return sleepCtx(c, time.Millisecond)
	}
	res, err := f.ForceStopSlot(context.Background(), "i-wedge", false)
	if err != nil || !res.Deregistered || !res.Retired || stopsOnBox(h) != 1 {
		t.Fatalf("ForceStopSlot = %+v, %v; stops %v (sleeps %d)", res, err, h.ec2.stopForce, sleeps)
	}
	if sleeps <= rescueENIPolls {
		t.Fatalf("only %d polls: the test did not outlast the old window", sleeps)
	}
	if slotMarker(h) != "" {
		t.Fatalf("marker %q left behind", slotMarker(h))
	}
}

// An ENI that never goes: the force-stop gives up, but never leaves the slot deregistered and
// unreserved, and the answer says the next Start replaces it.
func TestForceStopGiveUpAfterTheDeregistrationLeavesTheSlotReserved(t *testing.T) {
	old := forceStopENIBudget
	forceStopENIBudget = 300 * time.Millisecond
	defer func() { forceStopENIBudget = old }()
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	h.ci.stickyENI = true
	f := h.factory()
	f.sleep = func(c context.Context, d time.Duration) error { return sleepCtx(c, 10*time.Millisecond) }
	res, err := f.ForceStopSlot(context.Background(), "i-wedge", false)
	if !errors.Is(err, ErrSlotTaskENIHeld) || !strings.Contains(err.Error(), "reserved for replacement") {
		t.Fatalf("err = %v, want ErrSlotTaskENIHeld saying the slot is reserved", err)
	}
	if !res.Deregistered || !res.Retired || stopsOnBox(h) != 0 {
		t.Fatalf("res %+v stops %v", res, h.ec2.stopForce)
	}
	if ec2TagValue(h.ec2.instances["i-wedge"].Tags, ec2TagSlotReplace) == "" {
		t.Fatal("deregistered, given up and not reserved")
	}
	if slotMarker(h) != "" {
		t.Fatalf("marker %q left behind", slotMarker(h))
	}
}

// The deregistration went out but a lease lapsed while its answer was in flight, so the
// guarded section ends in an error. The box is deregistered all the same: it must still be
// reserved, and no stop may follow.
func TestForceStopReservesWhenALeaseLapsesDuringTheDeregistration(t *testing.T) {
	// Only the claim lease is checked when the section ends; the mount lease is asked before
	// the call (rescueGuarded), so a lapse of it after the call is not an error to react to.
	for _, which := range []string{"claim"} {
		t.Run(which, func(t *testing.T) {
			h := wedgedSlot(t)
			h.ec2.giveTaskENI("i-wedge")
			var gctx context.Context
			rescueStepHook = func(c context.Context, s string) error {
				if s == "deregistering" {
					gctx = c
				}
				return nil
			}
			defer func() { rescueStepHook = nil }()
			h.ci.onDeregister = func() { markGuard(gctx, which) }
			res, err := h.factory().ForceStopSlot(context.Background(), "i-wedge", false)
			if err == nil {
				t.Fatalf("ForceStopSlot succeeded after the lease lapsed: %+v", res)
			}
			if len(h.ci.deregistered) != 1 {
				t.Fatalf("deregistered %v, want the call to have gone out", h.ci.deregistered)
			}
			if ec2TagValue(h.ec2.instances["i-wedge"].Tags, ec2TagSlotReplace) == "" || !res.Retired {
				t.Fatalf("deregistered but not reserved (res %+v, err %v)", res, err)
			}
			if stopsOnBox(h) != 0 {
				t.Fatalf("stopped after the lease lapsed: %v", h.ec2.stopForce)
			}
		})
	}
}

// A container instance an earlier, half-done attempt already deregistered is not in the
// cluster list, so the deregistration sends nothing; with its task ENI still attached, a
// repeated force-stop that gives up must still leave the slot reserved.
func TestForceStopReservesASlotAnEarlierAttemptAlreadyDeregistered(t *testing.T) {
	old := forceStopENIBudget
	forceStopENIBudget = 300 * time.Millisecond
	defer func() { forceStopENIBudget = old }()
	h := wedgedSlot(t)
	delete(h.ci.registered, "i-wedge") // not listed: nothing left to deregister
	h.ec2.giveTaskENI("i-wedge")
	h.ci.stickyENI = true
	f := h.factory()
	f.sleep = func(c context.Context, d time.Duration) error { return sleepCtx(c, 10*time.Millisecond) }
	res, err := f.ForceStopSlot(context.Background(), "i-wedge", false)
	if !errors.Is(err, ErrSlotTaskENIHeld) {
		t.Fatalf("err = %v, want the ENI give-up", err)
	}
	if len(h.ci.deregistered) != 0 {
		t.Fatalf("deregistered %v: the fixture was meant to have nothing to deregister", h.ci.deregistered)
	}
	if !res.Retired || ec2TagValue(h.ec2.instances["i-wedge"].Tags, ec2TagSlotReplace) == "" {
		t.Fatalf("an already-deregistered slot with its ENI held was left unreserved (res %+v)", res)
	}
}
