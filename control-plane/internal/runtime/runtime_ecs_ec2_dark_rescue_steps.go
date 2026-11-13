package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// The destructive half of rung L1 (ADR 0121 decision 2, steps 4-6). Each stage runs from the
// phase the marker is in, so a resumed rescue picks up where the last one stopped, and every
// stage re-reads the world instead of trusting the phase: a phase is a record of what this
// rescue did, not evidence of the box's present state.
//
// Every AWS side effect on the box is preceded by the phase that names it, written under the
// mount lock with the token verified under the claim lock nested inside (rescueGuarded), and
// by a clock check of both leases right before the call. Waits for the box (the ENI going,
// `stopped`) hold no lock.

// Bounds, in polls of rescuePollEvery. Variables only so a test can shorten them.
var (
	rescueENIPolls  = 15
	rescueStopPolls = 90
	rescuePollEvery = 2 * time.Second
)

// rescueSteps carries the rescue from whatever phase m is in to `detached`. It returns nil
// only with the home off a stopped box.
func (e *ecsEC2Runtime) rescueSteps(ctx context.Context, slot string, m *rescueMarker) error {
	if m.Phase.rank() <= rescuePhaseDeregistered.rank() {
		if err := e.rescueENIStage(ctx, slot, m); err != nil {
			return err
		}
	}
	if m.Phase.rank() < rescuePhaseStopped.rank() {
		if err := e.rescueStopStage(ctx, slot, m); err != nil {
			return err
		}
	}
	if m.Phase.rank() < rescuePhaseDetaching.rank() {
		// Rung L2, first trigger: the slot is out of the free list before its home is detached.
		if err := e.rescueRepeatStage(ctx, slot, m); err != nil {
			return err
		}
	}
	if m.Phase.rank() < rescuePhaseDetached.rank() {
		return e.rescueDetachStage(ctx, slot, m)
	}
	return nil
}

// advanceRescue moves the marker to phase `to` (never backwards) and clears blocked_since: a
// step that completes is progress, and the next bound starts afresh. Called under the
// guarded section. The lease is asked by the clock before the write.
func (e *ecsEC2Runtime) advanceRescue(ctx context.Context, slot string, m *rescueMarker, to rescuePhase) error {
	if to.rank() <= m.Phase.rank() {
		return nil
	}
	if err := rescueLeasesHeld(ctx); err != nil {
		return fmt.Errorf("%w: %w", errRescueAborted, err)
	}
	next := *m
	next.Phase, next.BlockedSince = to, time.Time{}
	if err := e.writeRescueMarker(ctx, slot, next); err != nil {
		return fmt.Errorf("%w: marker %s: %w", errRescueAborted, to, err)
	}
	*m = next
	return nil
}

// rescueBlock records that a step ran out of its bound: blocked_since is written the first
// time and kept, so rescueBlockedAfter (the next rung) counts from it. It is a guarded write;
// when it cannot be made the step simply stops.
func (e *ecsEC2Runtime) rescueBlock(ctx context.Context, slot string, m *rescueMarker, why string) error {
	if m.BlockedSince.IsZero() {
		next := *m
		next.BlockedSince = e.now().UTC().Truncate(time.Second)
		err := e.rescueGuarded(ctx, m, slot, func(c context.Context) error {
			if err := rescueLeasesHeld(c); err != nil {
				return fmt.Errorf("%w: %w", errRescueAborted, err)
			}
			if err := e.writeRescueMarker(c, slot, next); err != nil {
				return fmt.Errorf("%w: blocked_since: %w", errRescueAborted, err)
			}
			*m = next
			return nil
		})
		if err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: %s", errRescueBlocked, why)
}

func (e *ecsEC2Runtime) pollPause(ctx context.Context) error {
	return e.sleep(ctx, rescuePollEvery)
}

// readSlot reads the box. A box that is gone or whose LaunchTime is not the rescue's epoch is
// overtaken (an operator started it again), a failed read aborts.
func (e *ecsEC2Runtime) readRescueBox(ctx context.Context, slot string, m *rescueMarker) (*ec2types.Instance, error) {
	inst, err := e.slotInstance(ctx, slot)
	if err != nil {
		return nil, fmt.Errorf("%w: slot: %w", errRescueAborted, err)
	}
	if inst == nil || inst.State == nil {
		return nil, fmt.Errorf("%w: the box is gone", errRescueOvertaken)
	}
	if inst.LaunchTime == nil || !inst.LaunchTime.UTC().Truncate(time.Second).Equal(m.Epoch) {
		return nil, fmt.Errorf("%w: the box was started again", errRescueOvertaken)
	}
	return inst, nil
}

// rescueENIStage is step 4: force-deregister the container instance when a task ENI is
// attached (so ECS lets go of it) and verify, bounded, that the ENI is physically gone.
// Deregistration is assumed to do nothing else. A box with no task ENI is not deregistered.
func (e *ecsEC2Runtime) rescueENIStage(ctx context.Context, slot string, m *rescueMarker) error {
	// A context that has already ended (the force-stop's ENI deadline, spent by the steps before
	// it) must not be left to the lock wait below: with a free lock and a done context a select
	// picks either, and the deregistration would sometimes go out.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", errRescueAborted, err)
	}
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{slot}})
	if err != nil {
		return fmt.Errorf("%w: ENIs of %s: %w", errRescueAborted, slot, err)
	}
	if instancesHoldTaskENI(out) {
		err := e.rescueGuarded(ctx, m, slot, func(c context.Context) error {
			if err := e.advanceRescue(c, slot, m, rescuePhaseDeregistering); err != nil {
				return err
			}
			if err := rescueStep(c, "deregistering"); err != nil {
				return err
			}
			if err := rescueLeasesHeld(c); err != nil {
				return fmt.Errorf("%w: %w", errRescueAborted, err)
			}
			// A failed call is not decisive: the wait below says whether the ENI went.
			if derr := e.deregisterContainerInstanceChecked(c, slot, func() error {
				if err := rescueLeasesHeld(c); err != nil {
					return fmt.Errorf("%w: %w", errRescueAborted, err)
				}
				return nil
			}); derr != nil {
				log.Printf("ecs-ec2: rescue of %s: %v", e.base.name, derr)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err := rescueStep(ctx, "deregistered-call"); err != nil {
			return err
		}
		if err := e.waitTaskENIsGoneWithin(ctx, slot, rescueENIPolls); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("%w: %w", errRescueAborted, ctx.Err())
			}
			return e.rescueBlock(ctx, slot, m, "the task network interface is still attached")
		}
	}
	err = e.rescueGuarded(ctx, m, slot, func(c context.Context) error {
		return e.advanceRescue(c, slot, m, rescuePhaseDeregistered)
	})
	if err != nil {
		return err
	}
	return rescueStep(ctx, "deregistered")
}

// rescueStopStage is step 5: only with the ENI gone, a graceful StopInstances (Force only on
// a stop that did not finish within its bound, never on an instance started again since),
// then verify `stopped`.
func (e *ecsEC2Runtime) rescueStopStage(ctx context.Context, slot string, m *rescueMarker) error {
	inst, err := e.readRescueBox(ctx, slot, m)
	if err != nil {
		return err
	}
	if inst.State.Name != ec2types.InstanceStateNameStopped {
		// Force is for a stop that was already sent, did not finish within its bound (that is
		// what blocked_since records), and is still not finished.
		force := inst.State.Name == ec2types.InstanceStateNameStopping &&
			m.Phase == rescuePhaseStopping && !m.BlockedSince.IsZero()
		err := e.rescueGuarded(ctx, m, slot, func(c context.Context) error {
			// Under the locks, right before the call: the box must still be this epoch's.
			cur, err := e.readRescueBox(c, slot, m)
			if err != nil {
				return err
			}
			if cur.State.Name == ec2types.InstanceStateNameStopped {
				return nil
			}
			if err := e.advanceRescue(c, slot, m, rescuePhaseStopping); err != nil {
				return err
			}
			if err := rescueStep(c, "stopping"); err != nil {
				return err
			}
			if err := rescueLeasesHeld(c); err != nil {
				return fmt.Errorf("%w: %w", errRescueAborted, err)
			}
			if _, err := e.ec2.StopInstances(c, &ec2.StopInstancesInput{
				InstanceIds: []string{slot}, Force: aws.Bool(force),
			}); err != nil {
				return fmt.Errorf("%w: stop %s: %w", errRescueAborted, slot, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err := rescueStep(ctx, "stop-sent"); err != nil {
			return err
		}
		stopped, err := e.waitRescueStopped(ctx, slot, m)
		if err != nil {
			return err
		}
		if !stopped {
			return e.rescueBlock(ctx, slot, m, "the instance did not reach stopped")
		}
	}
	err = e.rescueGuarded(ctx, m, slot, func(c context.Context) error {
		return e.advanceRescue(c, slot, m, rescuePhaseStopped)
	})
	if err != nil {
		return err
	}
	return rescueStep(ctx, "stopped")
}

// waitRescueStopped polls, bounded, until EC2 says `stopped` for this launch epoch.
func (e *ecsEC2Runtime) waitRescueStopped(ctx context.Context, slot string, m *rescueMarker) (bool, error) {
	for i := 0; i < rescueStopPolls; i++ {
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("%w: %w", errRescueAborted, err)
		}
		inst, err := e.readRescueBox(ctx, slot, m)
		if err == nil && inst.State.Name == ec2types.InstanceStateNameStopped {
			return true, nil
		}
		if err != nil && !errors.Is(err, errRescueAborted) {
			return false, err // overtaken
		}
		if err := e.pollPause(ctx); err != nil {
			return false, fmt.Errorf("%w: %w", errRescueAborted, err)
		}
	}
	return false, nil
}

// rescueDetachStage is step 6: with the box stopped (asked of EC2 immediately before, under
// the locks), the `detaching` phase and an ordinary detach of the home, then `detached`. The
// pool bookkeeping a release owes follows outside the locks.
func (e *ecsEC2Runtime) rescueDetachStage(ctx context.Context, slot string, m *rescueMarker) error {
	detached := false
	err := e.rescueGuarded(ctx, m, slot, func(c context.Context) error {
		cur, err := e.readRescueBox(c, slot, m)
		if err != nil {
			return err
		}
		if cur.State.Name != ec2types.InstanceStateNameStopped {
			// Never detach from a box that is not stopped: an operator may have started it.
			return fmt.Errorf("%w: %s is %s, not stopped", errRescueAborted, slot, cur.State.Name)
		}
		if err := e.advanceRescue(c, slot, m, rescuePhaseDetaching); err != nil {
			return err
		}
		if err := rescueStep(c, "detaching"); err != nil {
			return err
		}
		if err := rescueLeasesHeld(c); err != nil {
			return fmt.Errorf("%w: %w", errRescueAborted, err)
		}
		if _, err := e.ec2.DetachVolume(c, &ec2.DetachVolumeInput{
			VolumeId: aws.String(m.Volume), InstanceId: aws.String(slot),
		}); err != nil {
			return fmt.Errorf("%w: detach %s: %w", errRescueAborted, m.Volume, err)
		}
		detached = true
		// The bookkeeping a release owes runs here, with both locks still held and the leases
		// still shown to be ours: once they are released a Start that was waiting may have
		// attached the home and set its owner and idle tags, and these writes would undo them.
		// A lapsed lease skips it; the sweeper's owner-tag and free-slot repairs cover it.
		if err := rescueLeasesHeld(c); err == nil {
			e.afterDetach(c, m.Volume, slot, func() error { return rescueLeasesHeld(c) })
		} else {
			log.Printf("ecs-ec2: rescue of %s detached %s but its lease lapsed; leaving the bookkeeping to the sweeper: %v", e.base.name, m.Volume, err)
		}
		return e.advanceRescue(c, slot, m, rescuePhaseDetached)
	})
	if detached {
		if err := rescueStep(ctx, "detached"); err != nil {
			return err
		}
	}
	if err != nil && detached {
		log.Printf("ecs-ec2: rescue of %s detached %s but could not record it (the resume scan will): %v", e.base.name, m.Volume, err)
		return nil
	}
	return err
}
