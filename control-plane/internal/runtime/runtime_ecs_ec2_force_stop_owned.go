package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
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

// ErrSlotForceStopInProgress: the request ended (or its time ran out) after the force-stop had
// taken the rescue token. The work goes on to its own end and releases the token; this is the
// answer to the one who stopped waiting, not a failure.
var ErrSlotForceStopInProgress = errors.New("the force-stop is in progress; its result shows on the slot's next refresh")

type forceStopLateKey struct{}

// WithForceStopLate registers fn to receive the final answer of a force-stop whose caller
// stopped waiting (ErrSlotForceStopInProgress). The value survives the detaching of the
// context, which is the point: the audit outcome is written from here.
func WithForceStopLate(ctx context.Context, fn func(SlotForceStop, error)) context.Context {
	return context.WithValue(ctx, forceStopLateKey{}, fn)
}

// ForceStopLateFrom returns the callback WithForceStopLate registered on ctx, nil when none.
func ForceStopLateFrom(ctx context.Context) func(SlotForceStop, error) {
	fn, _ := ctx.Value(forceStopLateKey{}).(func(SlotForceStop, error))
	return fn
}

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
			return fmt.Errorf("%w (ECS: %v, SSM: %v, Agent probe or task read included); retry, or override", ErrSlotHealthUnknown, regErr, ssmErr)
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
		case cur.Phase.rank() >= rescuePhaseStopped.rank() && !cur.Wedged:
			// A dark rescue past the stop is finishing by itself. A wedged release has no
			// stop phase: its `detaching` is where a stuck umount sits, which is exactly
			// what an operator takes over.
			return res, fmt.Errorf("%w (it is past the stop, in phase %s)", ErrSlotRescueActive, cur.Phase)
		case cur.Operator && time.Since(cur.Began) < forceStopAdoptAfter:
			// Another operator request holds the token and is working (or has just ended).
			return res, fmt.Errorf("%w (an operator's force-stop is in progress)", ErrSlotRescueActive)
		case cur.BlockedSince.IsZero() && time.Since(cur.Began) < forceStopAdoptAfter:
			return res, ErrSlotRescueActive
		default:
			m, adopted = cur, true
		}
	}

	var ids []string
	if adopted {
		// Ownership moves to the operator under the claim lock, with the token verified fresh
		// there: the marker becomes the operator's (so no resume scan carries it on), the
		// service goes to 0 and the claim is dropped as a Stop does, and the set of the CP's
		// pending commands is fixed while the token still holds.
		var err error
		m, ids, err = occ.adoptRescue(ctx, cmds, id, m, ec2TagValue(inst.Tags, ec2TagRescue))
		if err != nil {
			return res, f.forceStopErr(ctx, ctx, id, err)
		}
	} else {
		// End the occupant's Start (desired 0, the claim dropped), then take the token on the
		// count that Stop produced.
		gen, err := occ.stopCounted(ctx)
		if err != nil {
			return res, fmt.Errorf("end the start of %s before stopping its slot: %w", res.Workspace, err)
		}
		m, ids, err = occ.rescuePrepare(ctx, cmds, darkDecision{slot: id, vol: home, launch: launch, gd: gen, operator: true})
		if err != nil {
			if now, cerr := occ.startGenNow(ctx); errors.Is(err, errRescueStepAside) && cerr == nil && now != gen {
				return res, ErrSlotStartedAgain
			}
			return res, fmt.Errorf("not stopping %s: %w (the occupant's start is ended)", id, err)
		}
	}
	// The token is taken. From here the work belongs to the CP, not to the request: a client
	// that goes away (a closed Console page, the ingress idle timeout) must not leave the box
	// deregistered but not stopped. The request's context only ends the wait for the answer.
	return f.forceStopDetached(ctx, func(dctx context.Context) (SlotForceStop, error) {
		return f.forceStopTokenHeld(dctx, bgCtx, cmds, inst, occ, m, ids, adopted, res)
	}, res)
}

// forceStopDetached runs fn on a context the CP owns and returns its answer, or, when ctx
// ends first, ErrSlotForceStopInProgress with what is known so far; fn is never interrupted
// by that. When the caller gave up, the final answer goes to the ForceStopLate callback on
// ctx (the audit outcome) or, with none, to the log.
//
// The detached context carries no deadline of the request's; it has its own forceStopBudget
// from the moment the token was taken. It is not tied to the process: like every other
// background step of the pool (backgroundWithin) it ends on its budget or with the process,
// and a process that dies mid-way leaves the operator's marker, which ages out and is taken
// over by the next press (forceStopAdoptAfter).
func (f *ecsEC2Factory) forceStopDetached(ctx context.Context, fn func(context.Context) (SlotForceStop, error),
	partial SlotForceStop) (SlotForceStop, error) {
	type outcome struct {
		res SlotForceStop
		err error
	}
	late := ForceStopLateFrom(ctx)
	var (
		mu       sync.Mutex
		gone     bool
		finished bool
	)
	done := make(chan outcome, 1)
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), forceStopBudget)
	go func() {
		defer cancel()
		// This goroutine is outside net/http's recovery: a panic here would end the whole CP
		// and leave no audit outcome. It becomes the error of the one answer instead.
		res, err := func() (res SlotForceStop, err error) {
			defer func() {
				if r := recover(); r != nil {
					res, err = partial, fmt.Errorf("force-stop of %s panicked: %v", partial.InstanceID, r)
				}
			}()
			return fn(dctx)
		}()
		mu.Lock()
		finished = true
		abandoned := gone
		mu.Unlock()
		if !abandoned {
			done <- outcome{res, err}
			return
		}
		if late != nil {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("ecs-ec2: force-stop of slot %s: outcome callback panicked: %v", res.InstanceID, r)
					}
				}()
				late(res, err)
			}()
			return
		}
		log.Printf("ecs-ec2: force-stop of slot %s finished after its request ended: %v (%+v)", res.InstanceID, err, res)
	}()
	select {
	case o := <-done:
		return o.res, o.err
	case <-ctx.Done():
	}
	mu.Lock()
	if finished {
		mu.Unlock()
		o := <-done
		return o.res, o.err
	}
	gone = true
	mu.Unlock()
	return partial, ErrSlotForceStopInProgress
}

// forceStopTokenHeld is the rest of the owned force-stop, after the token is taken, on the
// CP's own context. Whatever way it ends the token is released (finalizeRescue, or giveUp
// for a failure) and the answer is returned for the audit.
func (f *ecsEC2Factory) forceStopTokenHeld(ctx, bgCtx context.Context, cmds ssmSlotCommands, inst *ec2types.Instance,
	occ *ecsEC2Runtime, m rescueMarker, ids []string, adopted bool, res SlotForceStop) (out SlotForceStop, err error) {
	id := aws.ToString(inst.InstanceId)
	started := time.Now()
	// What a failed attempt leaves behind: an operator marker nobody carries on. Removed, so
	// it does not fence the home until it ages out.
	giveUp := func() {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*rescueWedgeBudget)
		defer cancel()
		occ.finalizeRescue(fctx, id, m.String(), m)
	}
	// A panic must not strand the token: release it (bounded) and answer with an error.
	defer func() {
		if r := recover(); r != nil {
			giveUp()
			out, err = res, fmt.Errorf("force-stop of %s panicked: %v", id, r)
		}
	}()
	if err := rescueStep(ctx, "prepared"); err != nil {
		return res, err
	}

	res.CancelledCommands, res.CancelFailed = cancelCommandIDs(ctx, cmds, id, ids)

	// The task ENI, on the rescue's stage: the override never skips it.
	eniDeadline := started.Add(forceStopENIBudget)
	if limit := started.Add(forceStopBudget - forceStopStopReserve); limit.Before(eniDeadline) {
		eniDeadline = limit
	}
	eniCtx, cancelENI := context.WithDeadline(ctx, eniDeadline)
	defer cancelENI()
	// A taken-over marker (a wedged release in `detaching`, say) is past the phases the ENI stage runs in, but its
	// slot is a running box that may well hold a task ENI: the gate is not skipped for it.
	if m.Phase.rank() <= rescuePhaseDeregistered.rank() || adopted {
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
	case errors.Is(err, errRescueStale):
		return fmt.Errorf("%w (the marker moved on)", ErrSlotRescueActive)
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

// adoptRescue moves a blocked or aged marker to the operator, under the claim lock with
// everything the token stands on read fresh there: the count is still the marker's g_r, the
// box carries exactly this marker in this launch epoch, and the home is on it. Then, still
// under the lock and each step preceded by the clock, it rewrites the marker as the operator's
// (the resume scan never continues that kind), puts the service at 0 and drops the claim as a
// Stop does, and fixes the set of the CP's pending commands, so nothing is cancelled that a
// newer Start sent. The count is not bumped: that would overtake the token being taken over.
func (e *ecsEC2Runtime) adoptRescue(ctx context.Context, cmds ssmSlotCommands, slot string, cur rescueMarker, raw string) (rescueMarker, []string, error) {
	lctx, unlock, err := e.lockBounded(ctx, homeLockClaim, e.base.name, rescueClaimBudget)
	if err != nil {
		return rescueMarker{}, nil, fmt.Errorf("%w: %w", errRescueAborted, err)
	}
	defer unlock()
	guard := homeGuardOf(lctx)
	lapsed := func(what string) error {
		if err := guard.check(); err != nil {
			return fmt.Errorf("%w: %s: %w", errRescueAborted, what, err)
		}
		return nil
	}
	g, err := e.startGenNow(lctx)
	if err != nil {
		return rescueMarker{}, nil, fmt.Errorf("%w: start count: %w", errRescueAborted, err)
	}
	if g != cur.Gen {
		return rescueMarker{}, nil, fmt.Errorf("%w: the start count is %d, the marker holds %d", errRescueOvertaken, g, cur.Gen)
	}
	inst, err := e.slotInstance(lctx, slot)
	if err != nil {
		return rescueMarker{}, nil, fmt.Errorf("%w: slot: %w", errRescueAborted, err)
	}
	if inst == nil || inst.LaunchTime == nil || !inst.LaunchTime.UTC().Truncate(time.Second).Equal(cur.Epoch) {
		return rescueMarker{}, nil, fmt.Errorf("%w: the box is not the one the marker names", errRescueOvertaken)
	}
	if ec2TagValue(inst.Tags, ec2TagRescue) != raw {
		return rescueMarker{}, nil, fmt.Errorf("%w: the marker changed", errRescueStale)
	}
	vol, err := e.homeVolume(lctx)
	if err != nil {
		return rescueMarker{}, nil, fmt.Errorf("%w: home: %w", errRescueAborted, err)
	}
	if vol == nil || aws.ToString(vol.VolumeId) != cur.Volume || attachedInstance(vol) != slot {
		return rescueMarker{}, nil, fmt.Errorf("%w: the home is no longer on the box", errRescueOvertaken)
	}
	if err := lapsed("before the marker moves"); err != nil {
		return rescueMarker{}, nil, err
	}
	// A new attempt of the operator's: its own clock, so a resume scan reads it as young and a
	// second press cannot tell it blocked, and the marker string no longer matches the one any
	// other handler or the original rescue holds (their guarded steps find it moved).
	m := cur
	m.Wedged, m.Operator = false, true
	m.Began, m.BlockedSince = e.now().UTC().Truncate(time.Second), time.Time{}
	if err := e.writeRescueMarker(lctx, slot, m); err != nil {
		return rescueMarker{}, nil, fmt.Errorf("%w: marker: %w", errRescueAborted, err)
	}
	if svc, ok, err := e.base.describeService(lctx); err != nil {
		return rescueMarker{}, nil, fmt.Errorf("%w: service: %w", errRescueAborted, err)
	} else if ok && svc.DesiredCount > 0 {
		if err := lapsed("before scaling the service down"); err != nil {
			return rescueMarker{}, nil, err
		}
		if _, err := e.base.ecs.UpdateService(lctx, &ecs.UpdateServiceInput{
			Cluster: aws.String(e.base.cfg.cluster), Service: aws.String(e.base.name), DesiredCount: aws.Int32(0),
		}); err != nil {
			return rescueMarker{}, nil, fmt.Errorf("%w: scale %s to 0: %w", errRescueAborted, e.base.name, err)
		}
	}
	e.clearBlockedPhase()
	e.setPhase("")
	if ec2TagValue(vol.Tags, EC2TagClaim) != "" {
		if err := lapsed("before dropping the claim"); err != nil {
			return rescueMarker{}, nil, err
		}
		e.unclaim(lctx, aws.ToString(vol.VolumeId))
	}
	ids, err := listSlotVolumeCommandIDs(lctx, cmds, slot)
	if err != nil {
		return rescueMarker{}, nil, fmt.Errorf("%w: list commands: %w", errRescueAborted, err)
	}
	if err := lapsed("after listing the commands"); err != nil {
		return rescueMarker{}, nil, err
	}
	// Nothing can count a Start while the claim lock is held; a count that moved anyway is
	// another process that bypassed it, and its commands are not ours to cancel.
	if g2, err := e.startGenNow(lctx); err != nil {
		return rescueMarker{}, nil, fmt.Errorf("%w: start count: %w", errRescueAborted, err)
	} else if g2 != cur.Gen {
		return rescueMarker{}, nil, fmt.Errorf("%w: the start count moved to %d while the commands were listed", errRescueOvertaken, g2)
	}
	return m, ids, nil
}
