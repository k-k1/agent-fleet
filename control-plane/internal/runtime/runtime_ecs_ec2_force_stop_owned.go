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

// The operator force-stop on the rescue's ownership and guarded steps (ADR 0121 decision 4,
// #3966). For a slot that holds a home it takes the same token the dark-slot rescue and the
// wedged release take (the af-rescue marker, the Start count bumped under the claim lock),
// cancels the CP's pending commands from the fixed set, runs the same ENI stage (deregister,
// verify the task ENI is gone, both leases asked before every call), and only then sends the
// stop, in the same guarded section. Unlike the rescue it does not detach the home and does
// not wait for `stopped`: the member's next Start wakes this same slot, and the reply has to
// fit one request. It is an operator action and is not behind the sweeper's action settings.

// forceStopAdoptAfter is how old a marker that is not blocked must be before an operator may
// take it over: a rescue that is merely slow is left to finish, one nobody carries on
// (a CP that restarted with the action switched off) is not a reason to refuse for good.
var forceStopAdoptAfter = 10 * time.Minute

// forceStopHealth is the health gate. The slot has to meet the dark predicate (both
// cluster legs silent, the Agent does not answer) or the wedged predicate (ECS agent
// connected, a task stuck stopping, or the Agent dropping its probe while ECS says HEALTHY),
// both as of now and without the sweeper's waiting time; a slot still inside the boot grace
// meets neither. Otherwise only the operator's override passes. A signal that cannot be read
// refuses, because "when in doubt, do not stop it".
func (f *ecsEC2Factory) forceStopHealth(ctx context.Context, cmds ssmSlotCommands, probe *ecsEC2Runtime,
	inst *ec2types.Instance, home *ec2types.Volume, override bool, res *SlotForceStop) error {
	id := aws.ToString(inst.InstanceId)
	registered, regErr := probe.registeredSlots(ctx)
	online, ssmErr := ssmOnline(ctx, cmds, id)
	res.AgentConnected, res.SSMOnline = registered[id], online
	unknown, predicate := false, ""
	switch {
	case regErr != nil || ssmErr != nil:
		unknown = true
	case inst.LaunchTime != nil && time.Since(*inst.LaunchTime) < f.pool.darkBootGrace:
		// booting: neither dark nor shown healthy
	case !res.AgentConnected && !res.SSMOnline:
		leg := AgentProbeFailed // no home, no service: no Agent to ask
		if home != nil {
			leg = f.darkAgentLeg(ctx, f.newDarkProbePass(), id, home)
		}
		switch leg {
		case AgentProbeFailed:
			predicate = "dark"
		case AgentProbeUnknown:
			unknown = true
		}
	case res.AgentConnected && home != nil:
		switch f.wedgedVerdictFor(ctx, f.newDarkProbePass(), id, home, time.Now()).state {
		case wedgedYes:
			predicate = "wedged"
		case wedgedUnknown:
			unknown = true
		}
	}
	if !override {
		if unknown {
			return fmt.Errorf("could not tell whether the slot is dark or wedged (ECS: %v, SSM: %v); retry, or override", regErr, ssmErr)
		}
		if predicate == "" {
			return ErrSlotHealthy
		}
	}
	res.Predicate = predicate
	res.OverrodeHealthy = override && predicate == ""
	return nil
}

// forceStopOwned runs the owned half for a slot with a home. A marker already on the box is
// the rescue's: refused unless it is blocked, or has outlived forceStopAdoptAfter, in which
// case the operator continues it with the same token (its count is not bumped again, which
// would overtake it).
func (f *ecsEC2Factory) forceStopOwned(ctx, bgCtx context.Context, started time.Time, cmds ssmSlotCommands,
	inst *ec2types.Instance, home *ec2types.Volume, occ *ecsEC2Runtime, res SlotForceStop) (SlotForceStop, error) {
	id := aws.ToString(inst.InstanceId)
	volumeID := aws.ToString(home.VolumeId)
	launch := aws.ToTime(inst.LaunchTime).UTC().Truncate(time.Second)

	var m rescueMarker
	adopted := false
	if raw := ec2TagValue(inst.Tags, ec2TagRescue); raw != "" {
		cur, ok := parseRescueMarker(raw)
		switch {
		case !ok || cur.Volume != volumeID || !cur.Epoch.Equal(launch):
			return res, fmt.Errorf("%w (the marker %q is not one this action can take over)", ErrSlotRescueActive, raw)
		case cur.Phase == rescuePhasePreparing:
			if time.Since(cur.Began) < forceStopAdoptAfter {
				return res, ErrSlotRescueActive
			}
			// Never resumable, only finalized: nothing was acted on.
			if err := occ.dropRescueMarker(ctx, id, raw); err != nil {
				return res, fmt.Errorf("%w: could not remove the stale marker: %w", ErrSlotRescueActive, err)
			}
		case cur.Phase.rank() >= rescuePhaseStopped.rank():
			return res, fmt.Errorf("%w (it is past the stop, in phase %s)", ErrSlotRescueActive, cur.Phase)
		case cur.BlockedSince.IsZero() && time.Since(cur.Began) < forceStopAdoptAfter:
			return res, ErrSlotRescueActive
		default:
			m, adopted = cur, true
		}
	}

	var ids []string
	if !adopted {
		// End the occupant's Start (desired 0, the claim dropped), then take the token on the
		// count that Stop produced.
		gen, err := occ.stopCounted(ctx)
		if err != nil {
			return res, fmt.Errorf("end the start of %s before stopping its slot: %w", res.Workspace, err)
		}
		m, ids, err = occ.rescuePrepare(ctx, cmds, darkDecision{slot: id, vol: home, launch: launch, gd: gen})
		if err != nil {
			if now, cerr := occ.startGenNow(ctx); errors.Is(err, errRescueStepAside) && cerr == nil && now != gen {
				return res, ErrSlotStartedAgain
			}
			return res, fmt.Errorf("not stopping %s: %w (the occupant's start is ended)", id, err)
		}
	}
	if err := rescueStep(ctx, "prepared"); err != nil {
		return res, err
	}
	// What a failed attempt leaves behind: a marker the sweeper's resume scan would carry on
	// to a detach, which the operator did not ask for. An adopted marker is the rescue's own
	// and stays.
	giveUp := func() {
		if adopted {
			return
		}
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*rescueWedgeBudget)
		defer cancel()
		occ.finalizeRescue(fctx, id, m.String(), m)
	}

	if adopted {
		res.CancelledCommands, res.CancelFailed = cancelSlotVolumeCommands(ctx, cmds, id)
	} else {
		res.CancelledCommands, res.CancelFailed = cancelCommandIDs(ctx, cmds, id, ids)
	}

	// The task ENI, on the rescue's stage: the override never skips it.
	eniDeadline := started.Add(forceStopENIBudget)
	if limit := started.Add(forceStopBudget - forceStopStopReserve); limit.Before(eniDeadline) {
		eniDeadline = limit
	}
	eniCtx, cancelENI := context.WithDeadline(ctx, eniDeadline)
	defer cancelENI()
	if m.Phase.rank() <= rescuePhaseDeregistered.rank() {
		if out, err := f.ec2.DescribeInstances(eniCtx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}}); err == nil {
			res.Deregistered = instancesHoldTaskENI(out)
		}
		if err := occ.rescueENIStage(eniCtx, id, &m); err != nil {
			giveUp()
			return res, f.forceStopErr(ctx, eniCtx, id, err)
		}
	}

	// The stop, in the same guarded section as every destructive step.
	res.Forced = inst.State.Name == ec2types.InstanceStateNameStopping
	if err := occ.rescueStopSend(ctx, id, &m, res.Forced); err != nil {
		giveUp()
		return res, f.forceStopErr(ctx, ctx, id, err)
	}
	if !res.Forced {
		res.ForceAfterSec = int(slotForceStopGrace.Seconds())
		probe := f.probeRuntime()
		probe.bg(bgCtx, func(c context.Context) { probe.forceStopAfterGrace(c, id, aws.ToTime(inst.LaunchTime)) })
	}
	// Done with the token: the episode is recorded and the marker goes, as after a rescue's
	// last step. The home stays where it is.
	fctx, cancelFin := context.WithTimeout(context.WithoutCancel(ctx), 3*rescueWedgeBudget)
	occ.finalizeRescue(fctx, id, m.String(), m)
	cancelFin()
	infoCtx, cancelInfo := context.WithTimeout(ctx, 3*time.Second)
	res.StoppingTask, _ = f.probeRuntime().stoppingTaskOn(infoCtx, id)
	cancelInfo()
	log.Printf("ecs-ec2: force-stop of slot %s sent under the rescue token (forced=%v, adopted=%v, cancelled %d commands, %d failed, stopping task %q)",
		id, res.Forced, adopted, res.CancelledCommands, res.CancelFailed, res.StoppingTask)
	return res, nil
}

// forceStopErr maps a rescue-step error onto the errors the API and the Console know.
func (f *ecsEC2Factory) forceStopErr(ctx, stageCtx context.Context, id string, err error) error {
	switch {
	case errors.Is(err, errRescueOvertaken):
		return ErrSlotStartedAgain
	case errors.Is(err, errRescueBlocked):
		return fmt.Errorf("not stopping %s: %w: %v (its commands are already cancelled and the occupant's start is ended)", id, ErrSlotTaskENIHeld, err)
	case ctx.Err() == nil && stageCtx.Err() != nil:
		return fmt.Errorf("not stopping %s: %w: gave up at the time limit of this request (its commands are already cancelled and the occupant's start is ended)", id, ErrSlotTaskENIHeld)
	}
	return fmt.Errorf("not stopping %s: %w", id, err)
}

// rescueStopSend sends the stop inside a guarded section: the token is verified, the phase
// that names the call is on the box, both leases are asked, then StopInstances. It does not
// wait for `stopped`: the caller does not detach.
func (e *ecsEC2Runtime) rescueStopSend(ctx context.Context, slot string, m *rescueMarker, force bool) error {
	return e.rescueGuarded(ctx, m, slot, func(c context.Context) error {
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
		if _, err := e.ec2.StopInstances(c, &ec2.StopInstancesInput{InstanceIds: []string{slot}, Force: aws.Bool(force)}); err != nil {
			return fmt.Errorf("%w: stop %s: %w", errRescueAborted, slot, err)
		}
		return nil
	})
}
