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
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// Rung L1 of ADR 0121: take ownership of a dark slot's home, write the rescue marker, end the
// occupant's Start, cancel the CP's own pending SSM commands, then (runtime_ecs_ec2_dark_rescue_steps.go)
// deregister, stop and detach, and resume and finalize after a restart.
//
// A slot whose service is up is never rescued. The Agent now reports its task id, but only an
// answer carries it, and the verdict that makes a service-up slot dark is a probe that got no
// answer, so there is nothing to prove with (ADR 0121, 2026-11-12 note on #3993). The check
// runs under the claim lock, in rescuePrepare.

// Bounds on waiting for a lock. They are variables only so a test can shorten them.
var (
	// rescueClaimBudget bounds the wait for the claim lock; rescueMountBudget the wait for
	// the mount lock, after which the rescue stops for this pass and the next one retries.
	rescueClaimBudget = 20 * time.Second
	rescueMountBudget = 30 * time.Second
	// rescueWedgeBudget bounds the wait for the per-instance finalizer lease.
	rescueWedgeBudget = 10 * time.Second
	// rescueTagBudget bounds a marker removal that must go out even when the pass's own
	// context has ended.
	rescueTagBudget = 15 * time.Second
)

// rescueStepHook is the crash matrix's seam: a test sets it, and a step that gets an error
// back returns at once, leaving the world as a dead process would. Nil in production.
var rescueStepHook func(ctx context.Context, step string) error

// errRescueCrash is what a test hook returns to stop a rescue where it stands.
var errRescueCrash = errors.New("test: process died here")

func rescueStep(ctx context.Context, step string) error {
	if rescueStepHook == nil {
		return nil
	}
	return rescueStepHook(ctx, step)
}

var (
	// errRescueStepAside: nothing was written; somebody acted since the decision, or the
	// slot is not one a rescue may take. The next pass decides again.
	errRescueStepAside = errors.New("rescue steps aside")
	// errRescueOvertaken: the rescue owned the home and no longer does (the Start count
	// moved, the marker or the home is gone). The caller finalizes: the rest is dropped,
	// the episode is kept.
	errRescueOvertaken = errors.New("rescue overtaken")
	// errRescueBusy: the mount lock was not had within its bound. The marker stays; the
	// next pass resumes.
	errRescueBusy = errors.New("rescue could not take the mount lock")
	// errRescueAborted: a lock, a read or a lease failed. The step did not run; the marker
	// stays and the resume scan retries. A doubt is never read as permission.
	errRescueAborted = errors.New("rescue step aborted")
	// errRescueBlocked: a step ran out of its bound (the task ENI did not go, the box did not
	// stop). Nothing further is done; the marker keeps its phase and carries blocked_since.
	errRescueBlocked = errors.New("rescue blocked")
	// errRescueStale: the marker on the box is not the one the caller holds (another replica
	// advanced it). The caller's copy has been refreshed; it stops for this pass.
	errRescueStale = errors.New("rescue marker moved on")
)

// darkDecision is what the dark scan knew when it decided to rescue: the Start count it
// read before the predicate's legs (g_d), the launch epoch and the home.
type darkDecision struct {
	slot   string
	vol    *ec2types.Volume
	launch time.Time
	gd     int64
	// wedged marks the wedged-slot release: the service may be up, which instead of
	// stepping aside must be shown to still be wedged under the claim lock (stillWedged),
	// and the marker carries the wedged kind.
	wedged      bool
	stillWedged func(context.Context) bool
	// operator marks the operator force-stop's token (marker kind `operator`).
	operator bool
}

// lockBounded is lockHome / lockSubject with a bound on the wait only: the context of the
// work under the lock has no deadline of its own. A wait that ran out, or ran out as the
// lock arrived, is an error and the lock is given back.
func (e *ecsEC2Runtime) lockBounded(ctx context.Context, which, subject string, budget time.Duration) (context.Context, func(), error) {
	wait, cancelWait := context.WithCancel(ctx)
	timer := time.AfterFunc(budget, cancelWait)
	lctx, unlock, err := e.lockSubject(wait, which, subject)
	if err != nil {
		timer.Stop()
		cancelWait()
		return nil, nil, err
	}
	if !timer.Stop() {
		unlock()
		cancelWait()
		return nil, nil, fmt.Errorf("%w: %s%s not had within %s", errHomeLockLost, which, subject, budget)
	}
	return lctx, func() { unlock(); cancelWait() }, nil
}

// rescueDarkSlot is the entry from the dark scan for a slot that has been dark for darkAfter.
func (f *ecsEC2Factory) rescueDarkSlot(ctx context.Context, d darkDecision) {
	if !f.pool.darkActs() {
		return
	}
	rt := f.rescueRuntime(d.vol)
	cmds, ok := f.ssmc.(ssmSlotCommands)
	if rt == nil || !ok {
		return
	}
	m, ids, err := rt.rescuePrepare(ctx, cmds, d)
	if err != nil {
		if !errors.Is(err, errRescueStepAside) {
			log.Printf("ecs-ec2 sweep: rescue of %s on %s not started: %v", rt.base.name, d.slot, err)
		}
		return
	}
	log.Printf("ecs-ec2 sweep: rescuing %s from the dark slot %s (marker %s)", rt.base.name, d.slot, m)
	rt.rescueContinue(ctx, cmds, d.slot, m, ids)
}

// rescueRuntime is the runtime the rescue acts through; its waits use the factory's sleeper
// when one is set, as the probe runtime's do.
func (f *ecsEC2Factory) rescueRuntime(vol *ec2types.Volume) *ecsEC2Runtime {
	rt := f.runtimeForVolume(vol)
	if rt != nil && f.sleep != nil {
		rt.sleep = f.sleep
	}
	return rt
}

// rescueStartCount is g_d: read before the predicate's legs, so a Start that lands while
// they are read is one the locked step in rescuePrepare sees as a moved count.
func (f *ecsEC2Factory) rescueStartCount(ctx context.Context, vol *ec2types.Volume) (int64, bool) {
	rt := f.runtimeForVolume(vol)
	if rt == nil {
		return 0, false
	}
	g, err := rt.startGenNow(ctx)
	if err != nil {
		log.Printf("ecs-ec2 sweep: rescue: start count of %s unreadable: %v", rt.base.name, err)
		return 0, false
	}
	return g, true
}

// rescuePrepare is decision 2 step 1 and decision 4's ownership step, under the claim lock
// alone: require the count still to be g_d and bump it to g_r, write the marker, end any
// Start as a Stop does, and fix the set of the CP's pending SSM commands. Nothing here waits
// for the mount lock.
//
// The marker goes out before the count moves, naming the g_r the bump will produce: under
// the claim lock nothing else can bump it, and a marker written after the bump would leave a
// window in which the count says "somebody owns this" and nothing says who.
func (e *ecsEC2Runtime) rescuePrepare(ctx context.Context, cmds ssmSlotCommands, d darkDecision) (rescueMarker, []string, error) {
	var m rescueMarker
	lctx, unlock, err := e.lockBounded(ctx, homeLockClaim, e.base.name, rescueClaimBudget)
	if err != nil {
		return m, nil, fmt.Errorf("%w: %w", errRescueAborted, err)
	}
	defer unlock()
	guard := homeGuardOf(lctx)

	g, err := e.startGenNow(lctx)
	if err != nil {
		return m, nil, fmt.Errorf("%w: start count: %w", errRescueAborted, err)
	}
	if g != d.gd {
		return m, nil, fmt.Errorf("%w: the start count moved (%d, decided at %d)", errRescueStepAside, g, d.gd)
	}
	vol, err := e.homeVolume(lctx)
	if err != nil {
		return m, nil, fmt.Errorf("%w: home: %w", errRescueAborted, err)
	}
	volumeID := aws.ToString(d.vol.VolumeId)
	if vol == nil || aws.ToString(vol.VolumeId) != volumeID || attachedInstance(vol) != d.slot {
		return m, nil, fmt.Errorf("%w: the home is no longer on %s", errRescueStepAside, d.slot)
	}
	if e.claimLive(vol) {
		return m, nil, fmt.Errorf("%w: a Start holds a live claim", errRescueStepAside)
	}
	svc, exists, err := e.base.describeService(lctx)
	if err != nil {
		return m, nil, fmt.Errorf("%w: service: %w", errRescueAborted, err)
	}
	if d.wedged {
		if d.stillWedged == nil || !d.stillWedged(lctx) {
			return m, nil, fmt.Errorf("%w: the slot no longer reads as wedged", errRescueStepAside)
		}
	} else if exists && svc.DesiredCount > 0 {
		// A probe through the shared alias cannot say which task answered, so a service that
		// is up is not rescued: the container may be serving its member.
		return m, nil, fmt.Errorf("%w: the service is up and a failed probe carries no task identity", errRescueStepAside)
	}
	inst, err := e.slotInstance(lctx, d.slot)
	if err != nil {
		return m, nil, fmt.Errorf("%w: slot: %w", errRescueAborted, err)
	}
	if inst == nil || inst.State == nil || inst.State.Name != ec2types.InstanceStateNameRunning ||
		inst.LaunchTime == nil || !inst.LaunchTime.UTC().Truncate(time.Second).Equal(d.launch) ||
		ec2TagValue(inst.Tags, EC2TagRole) != ec2RoleSlot {
		return m, nil, fmt.Errorf("%w: the box is not the running slot the decision saw", errRescueStepAside)
	}
	if v := ec2TagValue(inst.Tags, ec2TagRescue); v != "" {
		return m, nil, fmt.Errorf("%w: a rescue marker is already on the box", errRescueStepAside)
	}
	if err := guard.check(); err != nil {
		return m, nil, fmt.Errorf("%w: %w", errRescueAborted, err)
	}

	m = rescueMarker{Volume: volumeID, Epoch: d.launch, Phase: rescuePhasePreparing, Gen: g + 1, Began: e.now().UTC().Truncate(time.Second), Wedged: d.wedged, Operator: d.operator}
	preparing := m.String()
	if err := e.writeRescueMarker(lctx, d.slot, m); err != nil {
		e.dropRescueMarker(ctx, d.slot, preparing)
		return rescueMarker{}, nil, fmt.Errorf("%w: marker: %w", errRescueAborted, err)
	}
	if err := rescueStep(lctx, "marked"); err != nil {
		return rescueMarker{}, nil, err
	}
	// Asked by the clock before the bump, not after it: a lease that lapsed during the marker
	// write means another CP may have counted a Start since, and a bump now would overtake it.
	if err := guard.check(); err != nil {
		e.dropRescueMarker(ctx, d.slot, preparing)
		return rescueMarker{}, nil, fmt.Errorf("%w: %w", errRescueAborted, err)
	}
	gr, err := e.bumpStartCount(lctx)
	if err == nil && gr != m.Gen {
		err = fmt.Errorf("the count is %d, expected %d", gr, m.Gen)
	}
	if err != nil {
		e.dropRescueMarker(ctx, d.slot, preparing)
		return rescueMarker{}, nil, fmt.Errorf("%w: bump: %w", errRescueAborted, err)
	}
	// The bump is confirmed: only now is the marker one a resume may continue.
	m.Phase = rescuePhaseBegun
	if err := guard.check(); err == nil {
		err = e.writeRescueMarker(lctx, d.slot, m)
		if err != nil {
			e.dropRescueMarker(ctx, d.slot, preparing)
			e.dropRescueMarker(ctx, d.slot, m.String())
			return rescueMarker{}, nil, fmt.Errorf("%w: marker: %w", errRescueAborted, err)
		}
	} else {
		e.dropRescueMarker(ctx, d.slot, preparing)
		return rescueMarker{}, nil, fmt.Errorf("%w: %w", errRescueAborted, err)
	}
	if err := rescueStep(lctx, "counted"); err != nil {
		return rescueMarker{}, nil, err
	}
	// Every write below acts on state a newer Start may own once the lease is gone, so each
	// is preceded by the clock, never by the lock context alone. A lapse leaves the marker:
	// the resume scan decides by the count.
	lapsed := func(what string) error {
		if err := guard.check(); err != nil {
			return fmt.Errorf("%w: %s: %w", errRescueAborted, what, err)
		}
		return nil
	}
	if err := lapsed("after the marker advanced"); err != nil {
		return rescueMarker{}, nil, err
	}

	// End the Start the way a Stop does. The service is already at desired 0 (checked above,
	// and a Start that could raise it needs the claim lock we hold); the bump overtook any
	// Start that was converging. The dormancy clock is deliberately not restarted.
	e.clearBlockedPhase()
	e.setPhase("")
	if vol, err := e.homeVolume(lctx); err != nil {
		return rescueMarker{}, nil, fmt.Errorf("%w: home: %w", errRescueAborted, err)
	} else if vol != nil && ec2TagValue(vol.Tags, EC2TagClaim) != "" {
		// dropClaimOnStop reads and then deletes with no check between: a claim written by a
		// Start counted under a newer lease must survive.
		if err := lapsed("before dropping the claim"); err != nil {
			return rescueMarker{}, nil, err
		}
		e.unclaim(lctx, aws.ToString(vol.VolumeId))
	}
	if err := rescueStep(lctx, "ended-start"); err != nil {
		return rescueMarker{}, nil, err
	}

	ids, err := listSlotVolumeCommandIDs(lctx, cmds, d.slot)
	if err != nil {
		e.dropRescueMarker(ctx, d.slot, m.String())
		return rescueMarker{}, nil, fmt.Errorf("%w: list commands: %w", errRescueAborted, err)
	}
	if err := rescueStep(lctx, "listed"); err != nil {
		return rescueMarker{}, nil, err
	}
	// The fixed set is only trustworthy if the lease held through the listing: a command a
	// newer Start sent would otherwise be in it.
	if err := lapsed("after listing the commands"); err != nil {
		return rescueMarker{}, nil, err
	}
	return m, ids, nil
}

// bumpStartCount is beginStart's increment, for a caller that already holds the claim lock.
func (e *ecsEC2Runtime) bumpStartCount(lctx context.Context) (int64, error) {
	if e.pool.quarantines() {
		cur, err := e.startGenNow(lctx)
		if err == nil {
			err = e.carryQuarantine(lctx, cur+1) // before the bump: see carryQuarantine
		}
		if err != nil {
			return 0, err
		}
	}
	if e.leases == nil {
		return e.generation().Add(1), nil
	}
	gen, err := e.leases.BumpCPCounter(lctx, homeStartGen+e.base.name)
	if err = homeGuardOf(lctx).outcome(err); err != nil {
		return 0, err
	}
	return gen, nil
}

// slotInstance reads one instance of this pool by id; nil when it is not there.
func (e *ecsEC2Runtime) slotInstance(ctx context.Context, instanceID string) (*ec2types.Instance, error) {
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, e.pool.pool),
			{Name: aws.String("instance-id"), Values: []string{instanceID}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		return nil, err
	}
	for _, r := range out.Reservations {
		for i := range r.Instances {
			if aws.ToString(r.Instances[i].InstanceId) == instanceID {
				return &r.Instances[i], nil
			}
		}
	}
	return nil, nil
}

func (e *ecsEC2Runtime) writeRescueMarker(ctx context.Context, instanceID string, m rescueMarker) error {
	_, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{instanceID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagRescue), Value: aws.String(m.String())}},
	})
	return err
}

// dropRescueMarker removes the marker only while it still carries exactly value: EC2 deletes
// a tag given a value only if the value matches, so a late-resuming old rescue can never
// delete a newer rescue's marker. It runs on its own deadline: it is the cleanup after a
// failure, and the failure may be the pass's own context ending.
func (e *ecsEC2Runtime) dropRescueMarker(ctx context.Context, instanceID, value string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rescueTagBudget)
	defer cancel()
	_, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{instanceID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagRescue), Value: aws.String(value)}},
	})
	if err != nil {
		log.Printf("ecs-ec2: removing the rescue marker on %s failed (the resume scan will): %v", instanceID, err)
	}
	return err
}

// listSlotVolumeCommandIDs lists the CP's own unfinished commands on the box. Unlike
// cancelSlotVolumeCommands a failed read is an error: the set is fixed from this answer, so
// a partial one would be a rescue that cancelled some of what holds the mount lock.
func listSlotVolumeCommandIDs(ctx context.Context, c ssmSlotCommands, instanceID string) ([]string, error) {
	var ids []string
	for _, status := range []string{"Pending", "InProgress"} {
		var next *string
		for {
			out, err := c.ListCommands(ctx, &ssm.ListCommandsInput{
				InstanceId: aws.String(instanceID),
				Filters:    []ssmtypes.CommandFilter{{Key: ssmtypes.CommandFilterKeyStatus, Value: aws.String(status)}},
				NextToken:  next,
			})
			if err != nil {
				return nil, err
			}
			for _, cmd := range out.Commands {
				if aws.ToString(cmd.Comment) == slotVolumeCommandComment {
					ids = append(ids, aws.ToString(cmd.CommandId))
				}
			}
			if next = out.NextToken; next == nil {
				break
			}
		}
	}
	return ids, nil
}

// rescueContinue runs what follows the preparation: cancel exactly the fixed set with no
// lock held, prove the rescue still owns the home, then the steps of decision 2 (steps 4-6,
// rescueSteps). The outcome decides the audit event and what is left on the box.
func (e *ecsEC2Runtime) rescueContinue(ctx context.Context, cmds ssmSlotCommands, slot string, m rescueMarker, ids []string) {
	if err := rescueStep(ctx, "before-cancel"); err != nil {
		return
	}
	cancelled, failed := cancelCommandIDs(ctx, cmds, slot, ids)
	if cancelled+failed > 0 {
		log.Printf("ecs-ec2 sweep: rescue of %s: cancelled %d pending commands on %s (%d failed)", e.base.name, cancelled, slot, failed)
	}
	if err := rescueStep(ctx, "cancelled"); err != nil {
		return
	}
	err := e.rescueGuarded(ctx, &m, slot, func(ctx context.Context) error {
		return rescueStep(ctx, "guarded")
	})
	blockedBefore := m.BlockedSince
	if err == nil {
		if m.Wedged {
			err = e.wedgedReleaseSteps(ctx, cmds, e.serviceTasks, slot, &m)
		} else {
			err = e.rescueSteps(ctx, slot, &m)
		}
	}
	switch {
	case err == nil:
		log.Printf("ecs-ec2 sweep: rescued %s from the slot %s: the home is detached (wedged release: %t)", e.base.name, slot, m.Wedged)
		e.finalizeRescue(ctx, slot, m.String(), m)
		e.reportEpisode(ctx, slot, m, "rescued")
	case errors.Is(err, errRescueBlocked):
		log.Printf("ecs-ec2 sweep: rescue of %s on %s is blocked (phase %s): %v", e.base.name, slot, m.Phase, err)
		if blockedBefore.IsZero() || !blockedBefore.Equal(m.BlockedSince) {
			e.reportEpisode(ctx, slot, m, "blocked")
		}
		// Rung L2, second trigger. The blocked rescue keeps being retried after it.
		e.rescueBlockedQuarantine(ctx, slot, &m)
	case errors.Is(err, errRescueOvertaken):
		log.Printf("ecs-ec2 sweep: rescue of %s on %s overtaken: %v", e.base.name, slot, err)
		e.finalizeRescue(ctx, slot, m.String(), m)
	case errors.Is(err, errRescueCrash):
	default:
		log.Printf("ecs-ec2 sweep: rescue of %s on %s stopped for this pass: %v", e.base.name, slot, err)
	}
}

// reportEpisode routes the outcome of a marker's episode to the audit callback of its ladder:
// a dark rescue's is SlotDark, a wedged release's is SlotWedged ("rescued" is its "released").
func (e *ecsEC2Runtime) reportEpisode(ctx context.Context, slot string, m rescueMarker, action string) {
	if m.Wedged {
		if action == "rescued" {
			action = "released"
		}
		if e.onSlotWedged != nil {
			e.onSlotWedged(ctx, SlotWedged{Slot: slot, Volume: m.Volume, Since: m.Began, Workspace: e.base.name,
				MembershipID: e.base.membershipID, Action: action})
		}
		return
	}
	e.reportRescue(ctx, SlotDark{Slot: slot, Volume: m.Volume, Since: m.Began, Workspace: e.base.name, MembershipID: e.base.membershipID}, action)
}

// reportRescue hands a rescue event to the audit callback. Nothing here can fail the rescue.
func (e *ecsEC2Runtime) reportRescue(ctx context.Context, ev SlotDark, action string) {
	ev.Action = action
	if e.onSlotDark != nil {
		e.onSlotDark(ctx, ev)
	}
}

func (f *ecsEC2Factory) reportRescue(ctx context.Context, ev SlotDark, action string) {
	ev.Action = action
	if f.onSlotDark != nil {
		f.onSlotDark(ctx, ev)
	}
}

// cancelCommandIDs cancels exactly ids. A command that cannot be cancelled is counted: the
// mount lock bound is what then keeps the rescue from waiting on it for good.
func cancelCommandIDs(ctx context.Context, c ssmSlotCommands, instanceID string, ids []string) (cancelled, failed int) {
	for _, id := range ids {
		if _, err := c.CancelCommand(ctx, &ssm.CancelCommandInput{CommandId: aws.String(id), InstanceIds: []string{instanceID}}); err != nil {
			log.Printf("ecs-ec2: cancelling SSM command %s on %s: %v", id, instanceID, err)
			failed++
			continue
		}
		cancelled++
	}
	return cancelled, failed
}

type rescueMountGuardKey struct{}

// rescueMountGuard is the guard of the mount lock a guarded section runs under; homeGuardOf
// on the same context is the claim lock's. They are taken at different times and renewed and
// lapse independently, so a destructive call asks both (rescueLeasesHeld).
func rescueMountGuard(ctx context.Context) *homeLockGuard {
	g, _ := ctx.Value(rescueMountGuardKey{}).(*homeLockGuard)
	return g
}

// rescueLeasesHeld asks the clock of both leases of a guarded section. Right before every AWS
// side effect, never earlier: whatever waits between the check and the call is the risk.
func rescueLeasesHeld(ctx context.Context) error {
	if err := rescueMountGuard(ctx).check(); err != nil {
		return err
	}
	return homeGuardOf(ctx).check()
}

// rescueGuarded runs fn only while this rescue still owns the home, under the mount lock
// with the token re-verified under the claim lock nested inside it (decision 4, lock order).
// A lock not taken, a count not readable and a lease that lapsed abort the step: the
// unguarded fallback of ifOwned is deliberately not used.
func (e *ecsEC2Runtime) rescueGuarded(ctx context.Context, m *rescueMarker, instanceID string, fn func(context.Context) error) error {
	mctx, unlockMount, err := e.lockBounded(ctx, homeLockMount, e.base.name, rescueMountBudget)
	if err != nil {
		return fmt.Errorf("%w: %w", errRescueBusy, err)
	}
	defer unlockMount()
	cctx, unlockClaim, err := e.lockBounded(mctx, homeLockClaim, e.base.name, rescueClaimBudget)
	if err != nil {
		return fmt.Errorf("%w: %w", errRescueAborted, err)
	}
	defer unlockClaim()
	cctx = context.WithValue(cctx, rescueMountGuardKey{}, homeGuardOf(mctx))
	cur, err := e.rescueOwned(cctx, *m, instanceID)
	if err != nil {
		return err
	}
	// Two replicas can resume one marker. The phase and blocked_since the caller holds are a
	// snapshot; if the box's marker has moved on (or the caller's write never landed), the
	// caller is working from the past: adopt what the box says and stop, so the next decision is
	// made on it. Without this a stale `begun` would write over a `deregistering` that carries
	// blocked_since, step backwards, and block and audit a second time.
	if cur.String() != m.String() {
		*m = cur
		return fmt.Errorf("%w: the marker is now %s", errRescueStale, cur)
	}
	// Asked here, not left to the contexts: their end is a timer's callback, which a
	// goroutine resumed past the deadline can outrun.
	if err := homeGuardOf(mctx).check(); err != nil {
		return fmt.Errorf("%w: %w", errRescueAborted, err)
	}
	if err := homeGuardOf(cctx).check(); err != nil {
		return fmt.Errorf("%w: %w", errRescueAborted, err)
	}
	err = fn(cctx)
	if lost := homeGuardOf(cctx).check(); lost != nil && err == nil {
		err = lost
	}
	return err
}

// rescueOwned is the token check of decision 4: the count is exactly g_r, the box carries
// this rescue's marker in this launch epoch, and the home is still on the box.
func (e *ecsEC2Runtime) rescueOwned(ctx context.Context, m rescueMarker, instanceID string) (rescueMarker, error) {
	g, err := e.startGenNow(ctx)
	if err != nil {
		return rescueMarker{}, fmt.Errorf("%w: start count: %w", errRescueAborted, err)
	}
	if g != m.Gen {
		return rescueMarker{}, fmt.Errorf("%w: the start count is %d, the rescue holds %d", errRescueOvertaken, g, m.Gen)
	}
	inst, err := e.slotInstance(ctx, instanceID)
	if err != nil {
		return rescueMarker{}, fmt.Errorf("%w: slot: %w", errRescueAborted, err)
	}
	if inst == nil {
		return rescueMarker{}, fmt.Errorf("%w: the box is gone", errRescueOvertaken)
	}
	cur, ok := parseRescueMarker(ec2TagValue(inst.Tags, ec2TagRescue))
	if !ok || !cur.sameRescue(m) {
		return rescueMarker{}, fmt.Errorf("%w: the box does not carry this rescue's marker", errRescueOvertaken)
	}
	if inst.LaunchTime == nil || !inst.LaunchTime.UTC().Truncate(time.Second).Equal(m.Epoch) {
		return rescueMarker{}, fmt.Errorf("%w: the box was started again", errRescueOvertaken)
	}
	vol, err := e.homeVolume(ctx)
	if err != nil {
		return rescueMarker{}, fmt.Errorf("%w: home: %w", errRescueAborted, err)
	}
	if vol == nil || aws.ToString(vol.VolumeId) != m.Volume || attachedInstance(vol) != instanceID {
		return rescueMarker{}, fmt.Errorf("%w: the home is no longer on the box", errRescueOvertaken)
	}
	return cur, nil
}

// finalizeRescue is the finalizer of decision 4. It touches tags only, so it needs no count
// or epoch match. A marker still at `begun` took no action on the box and is just removed;
// any later phase may have acted on it, so it is an episode: it gets its own record
// (recordEpisode, under the per-instance lease), and then the marker (matched on its exact
// value, raw) is removed. A lease not had, a read or a write
// that fails leaves the marker for the next pass.
func (e *ecsEC2Runtime) finalizeRescue(ctx context.Context, instanceID, raw string, m rescueMarker) {
	if m.Phase != rescuePhaseBegun && m.Phase != rescuePhasePreparing {
		ctx, unlock, err := e.lockBounded(ctx, homeLockWedge, instanceID, rescueWedgeBudget)
		if err != nil {
			log.Printf("ecs-ec2: finalizing the rescue of %s: no lease, the marker stays: %v", instanceID, err)
			return
		}
		defer unlock()
		inst, err := e.slotInstance(ctx, instanceID)
		if err != nil {
			log.Printf("ecs-ec2: finalizing the rescue of %s: reading the box: %v", instanceID, err)
			return
		}
		if inst == nil {
			return // gone, and its tags with it
		}
		// The clock decides, not the lease's timer callback.
		if err := homeGuardOf(ctx).check(); err != nil {
			log.Printf("ecs-ec2: finalizing the rescue of %s: lease lapsed, the marker stays: %v", instanceID, err)
			return
		}
		if err := e.recordEpisode(ctx, instanceID, inst.Tags, m); err != nil {
			log.Printf("ecs-ec2: recording the episode on %s failed, the marker stays: %v", instanceID, err)
			return
		}
	}
	if m.Wedged {
		// The stamp's clock described the episode that is over; left in place, the next home
		// to land on this box would meet an expired wait.
		e.clearWedgedStampOn(ctx, instanceID)
	}
	e.dropRescueMarker(ctx, instanceID, raw)
}

// resumeRescues is the resume scan: every marker on a box of this pool, whatever the box's
// state. A marker that still matches (same home on the box, same launch epoch, count still
// g_r) and whose action is still `rescue` continues; any other is finalized. The match is
// read under the claim lock, so a rescue that is still preparing on another replica is
// waited for rather than mistaken for one whose count never moved.
func (f *ecsEC2Factory) resumeRescues(ctx context.Context) {
	probe := f.probeRuntime()
	insts, err := probe.rescueMarkedInstances(ctx)
	if err != nil {
		log.Printf("ecs-ec2 sweep: rescue resume scan skipped, cannot read the markers: %v", err)
		return
	}
	cmds, _ := f.ssmc.(ssmSlotCommands)
	for _, inst := range insts {
		id := aws.ToString(inst.InstanceId)
		raw := ec2TagValue(inst.Tags, ec2TagRescue)
		m, ok := parseRescueMarker(raw)
		if !ok {
			log.Printf("ecs-ec2 sweep: the rescue marker on %s does not parse (%q); leaving it, it still fences its home", id, raw)
			continue
		}
		f.resumeRescue(ctx, probe, cmds, inst, raw, m)
	}
}

func (f *ecsEC2Factory) resumeRescue(ctx context.Context, probe *ecsEC2Runtime, cmds ssmSlotCommands, inst ec2types.Instance, raw string, m rescueMarker) {
	id := aws.ToString(inst.InstanceId)
	if m.Operator && time.Since(m.Began) < forceStopAdoptAfter {
		// An operator's force-stop is in progress (or just ended): its handler owns the token,
		// and finalizing it here would drop the fence under it. One that has aged is a crashed
		// handler's, and falls through to be finalized, never continued.
		return
	}
	// Filtered, not looked up by id: a volume that is gone answers an error by id, which
	// would read as "could not ask".
	vols, err := f.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{{Name: aws.String("volume-id"), Values: []string{m.Volume}}},
	})
	if err != nil {
		log.Printf("ecs-ec2 sweep: resume of the rescue on %s: reading %s: %v", id, m.Volume, err)
		return
	}
	var vol *ec2types.Volume
	for i := range vols.Volumes {
		if s := vols.Volumes[i].State; s != ec2types.VolumeStateDeleting && s != ec2types.VolumeStateDeleted {
			vol = &vols.Volumes[i]
		}
	}
	var rt *ecsEC2Runtime
	if vol != nil {
		rt = f.rescueRuntime(vol)
	}
	if rt == nil {
		// The home is gone, or it carries no owner the sweeper can lock for: no count to
		// compare, so nothing of the rescue can continue.
		probe.finalizeRescue(ctx, id, raw, m)
		return
	}
	var ids []string
	var count int64
	stillOnBox := false
	strict, err := func() (bool, error) {
		lctx, unlock, err := rt.lockBounded(ctx, homeLockClaim, rt.base.name, rescueClaimBudget)
		if err != nil {
			return false, err
		}
		defer unlock()
		g, err := rt.startGenNow(lctx)
		if err != nil {
			return false, err
		}
		count, stillOnBox = g, attachedInstance(vol) == id
		strict := stillOnBox && inst.LaunchTime != nil &&
			inst.LaunchTime.UTC().Truncate(time.Second).Equal(m.Epoch) && g == m.Gen
		// Only a rescue this build can continue lists commands again, under the claim lock
		// with the count at g_r, and cancels only what the listing returns.
		if strict && f.canContinue(m) && m.Phase == rescuePhaseBegun && cmds != nil {
			ids, err = listSlotVolumeCommandIDs(lctx, cmds, id)
			if err != nil {
				return false, err
			}
		}
		return strict, homeGuardOf(lctx).check()
	}()
	if err != nil {
		log.Printf("ecs-ec2 sweep: resume of the rescue on %s: %v; the marker stays", id, err)
		return
	}
	if !strict || !f.canContinue(m) || m.Phase == rescuePhasePreparing || m.Phase == rescuePhaseDetached || cmds == nil {
		// Not a rescue this pass can carry on (overtaken, the action is no longer `rescue`, a
		// marker whose bump was never confirmed, or one already detached): record the
		// episode, drop the rest. `detaching` with the home gone is complete when the count
		// is still g_r: nobody else could have moved it while the marker existed (decision 3,
		// rule 2), so it was this rescue.
		if (m.Phase == rescuePhaseDetaching || m.Phase == rescuePhaseDetached) && !stillOnBox && count == m.Gen {
			rt.reportEpisode(ctx, id, m, "rescued")
		}
		rt.finalizeRescue(ctx, id, raw, m)
		return
	}
	log.Printf("ecs-ec2 sweep: resuming the rescue of %s on %s (phase %s)", rt.base.name, id, m.Phase)
	rt.rescueContinue(ctx, cmds, id, m, ids)
}

// canContinue is whether this build may carry a marker on: the action of the ladder that
// wrote it must still be the one that acts.
func (f *ecsEC2Factory) canContinue(m rescueMarker) bool {
	if m.Operator {
		return false // the operator's handler carries it; the resume scan never does
	}
	if m.Wedged {
		return f.pool.wedgedAction == wedgedActionRelease
	}
	return f.pool.darkActs()
}

// rescueMarkedInstances lists the boxes of this pool that carry a marker, in any live state.
func (e *ecsEC2Runtime) rescueMarkedInstances(ctx context.Context) ([]ec2types.Instance, error) {
	var out []ec2types.Instance
	var next *string
	for {
		res, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
			Filters: []ec2types.Filter{
				tagFilter(EC2TagPool, e.pool.pool),
				{Name: aws.String("tag-key"), Values: []string{ec2TagRescue}},
				{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
			},
			NextToken: next,
		})
		if err != nil {
			return nil, err
		}
		for _, r := range res.Reservations {
			out = append(out, r.Instances...)
		}
		if next = res.NextToken; next == nil || *next == "" {
			return out, nil
		}
	}
}
