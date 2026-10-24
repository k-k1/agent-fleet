package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// slotENIReleasePolls bounds releaseSlotTaskENI's wait for the task ENI to go (2 s per
// poll, so 30 s). It has to fit inside a request: the admin force-stop runs it on the
// handler's thread, behind a 60 s idle ingress timeout, after health checks and before
// StopInstances.
const slotENIReleasePolls = 15

// ErrSlotTaskENIHeld: the slot still carries a task ENI and ECS did not release it in time.
// Stopping now would bring the box back multi-ENI, without an auto-assigned public IPv4
// (ADR 0045 decision 3-3), so the stop is refused. Retrying is safe and usually enough.
var ErrSlotTaskENIHeld = errors.New("the slot still holds a task network interface")

// releaseSlotTaskENI is the first half of stopping a slot whose ECS agent may be gone:
// when a task ENI is attached, force-deregister the container instance (ECS cannot stop a
// task whose agent is disconnected, so it keeps the task and its ENI until it is told to
// let go) and wait, bounded, for the ENI to disappear. Only when it returns nil may the
// caller StopInstances. deregistered reports whether a deregistration was attempted.
//
// Never deregisters a slot with no task ENI: it has nothing to release, and a
// deregistered box only re-registers after its agent restarts.
//
// Deliberately free of any Start ownership check, unlike abandonLostSlot: the caller
// (an operator's force-stop, and the dark-slot rescue of ADR 0121 once it exists) has
// already ended or fenced the occupant's Start.
func (e *ecsEC2Runtime) releaseSlotTaskENI(ctx context.Context, instanceID string, polls int) (deregistered bool, err error) {
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		return false, fmt.Errorf("could not tell whether %s holds a task network interface: %w", instanceID, err)
	}
	if !instancesHoldTaskENI(out) {
		return false, nil
	}
	log.Printf("ecs-ec2: slot %s holds a task ENI; force-deregistering its container instance before it is stopped", instanceID)
	deregistered = true
	// A failed deregistration is not decisive by itself: the wait below is what says
	// whether the ENI went. Its message is kept for the refusal.
	derr := e.deregisterContainerInstance(ctx, instanceID)
	if derr != nil {
		log.Printf("ecs-ec2: %v", derr)
	}
	if werr := e.waitTaskENIsGoneWithin(ctx, instanceID, polls); werr != nil {
		if ctx.Err() != nil {
			return deregistered, werr
		}
		why := fmt.Sprintf("ECS did not release it within %s", time.Duration(polls)*2*time.Second)
		if derr != nil {
			why += " (" + derr.Error() + ")"
		}
		return deregistered, fmt.Errorf("%w: %s", ErrSlotTaskENIHeld, why)
	}
	return deregistered, nil
}
