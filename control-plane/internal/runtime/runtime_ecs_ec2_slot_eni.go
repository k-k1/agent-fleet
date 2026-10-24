package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// slotENIReleasePolls caps releaseSlotTaskENI's polls (2 s apart). The sleeps are only a
// floor on the time the wait takes: the AWS calls between them add to it, so the wall-clock
// bound is the context deadline ForceStopSlot passes (forceStopENIBudget), not this count.
const slotENIReleasePolls = 15

// ErrSlotTaskENIHeld: the slot still carries a task ENI and ECS did not release it in time.
// Stopping now would bring the box back multi-ENI, without an auto-assigned public IPv4
// (ADR 0045 decision 3-3), so the stop is refused. Retrying is safe and usually enough.
var ErrSlotTaskENIHeld = errors.New("the slot still holds a task network interface")

// ErrSlotStartedAgain: a Start was accepted for the slot's workspace after the force-stop
// ended the previous one. The slot is that Start's now, so nothing more is done to it.
var ErrSlotStartedAgain = errors.New("the workspace was started again while its slot was being stopped; the slot was left alone")

// releaseSlotTaskENI is the first half of stopping a slot whose ECS agent may be gone:
// when a task ENI is attached, force-deregister the container instance (ECS cannot stop a
// task whose agent is disconnected, so it keeps the task and its ENI until it is told to
// let go) and wait, bounded, for the ENI to disappear. Only when it returns nil may the
// caller StopInstances. deregistered reports whether a deregistration was attempted.
//
// Never deregisters a slot with no task ENI: it has nothing to release, and a
// deregistered box only re-registers after its agent restarts.
//
// Free of any Start accounting itself, unlike abandonLostSlot: the caller (an operator's
// force-stop, and the dark-slot rescue of ADR 0121 once it exists) says what fences the
// one destructive call, the deregistration, through guard. A guard that refuses (false)
// ends the sequence with ErrSlotStartedAgain before anything is deregistered; nil runs
// the call unguarded. The wait is not guarded and does not hold any lock.
func (e *ecsEC2Runtime) releaseSlotTaskENI(ctx context.Context, instanceID string, polls int,
	guard func(context.Context, func(context.Context)) bool) (deregistered bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		return false, fmt.Errorf("could not tell whether %s holds a task network interface: %w", instanceID, err)
	}
	if !instancesHoldTaskENI(out) {
		return false, nil
	}
	log.Printf("ecs-ec2: slot %s holds a task ENI; force-deregistering its container instance before it is stopped", instanceID)
	// A failed deregistration is not decisive by itself: the wait below is what says
	// whether the ENI went. Its message is kept for the refusal.
	deregistered = true
	var derr error
	dereg := func(c context.Context) { derr = e.deregisterContainerInstance(c, instanceID) }
	if guard == nil {
		dereg(ctx)
	} else if !guard(ctx, dereg) {
		if ctx.Err() != nil {
			return false, ctx.Err() // out of time before the lock: not a Start's doing
		}
		return false, ErrSlotStartedAgain
	}
	if derr != nil {
		log.Printf("ecs-ec2: %v", derr)
	}
	if werr := e.waitTaskENIsGoneWithin(ctx, instanceID, polls); werr != nil {
		if ctx.Err() != nil {
			return deregistered, werr
		}
		why := "ECS did not release it in time"
		if derr != nil {
			why += " (" + derr.Error() + ")"
		}
		return deregistered, fmt.Errorf("%w: %s", ErrSlotTaskENIHeld, why)
	}
	return deregistered, nil
}
