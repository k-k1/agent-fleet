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
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// The action of the wedged-slot ladder (ADR 0121, amendment of 2026-11-12): once a slot's
// stamp is `detected`, release its home the way a Stop's background release does (SSM umount,
// task gone, detach, slot left in service). It is the L1 rescue with a different last half:
// the same ownership token (the af-rescue marker with the `wedged` kind, written and verified
// by rescuePrepare / rescueGuarded), the same resume scan and finalizer, but it never
// deregisters the container instance or stops the box, because its agents answer.
//
// It goes through a guarded direct path, not releaseSlotSince with asRescue. releaseSlotSince
// takes the mount lock itself and reads the count with no claim lock, and it waits for the
// tasks with no bound before the umount; the guarded path holds mount-outer / claim-inner and
// asks the token and both leases before every destructive call, and bounds every wait.
//
// A slot whose SSM is not Online is not released and not taken over: the umount cannot run,
// and the dark scan never sees a slot whose ECS agent is connected. It is reported once as
// blocked (reason ssm_offline) and left alone.

// Bounds of the wedged release. Variables only so a test can shorten them.
var (
	// wedgedUmountBudget bounds the SSM umount, which runs under the mount lock; past it the
	// command is cancelled and the release is blocked until the next pass.
	wedgedUmountBudget = 3 * time.Minute
	// wedgedTaskPolls x rescuePollEvery bounds the wait for every task of the service to be
	// STOPPED after the umount (the incident's task stopped about 75 s after it).
	wedgedTaskPolls = 60
)

const wedgedReasonSSMOffline = "ssm_offline"

// wedgedDecision is what the wedged scan knew when it decided to act: the slot, the Start
// count read before the verdict's legs (g_d), the launch epoch and the verdict's signal.
type wedgedDecision struct {
	slot   string
	vol    *ec2types.Volume
	launch time.Time
	gd     int64
	signal string
	pass   *darkProbePass
}

// releaseWedgedSlot is the entry from the wedged scan for a slot whose stamp is `detected`.
func (f *ecsEC2Factory) releaseWedgedSlot(ctx context.Context, w wedgedDecision) {
	rt := f.rescueRuntime(w.vol)
	cmds, ok := f.ssmc.(ssmSlotCommands)
	if rt == nil || !ok {
		return
	}
	online, err := ssmOnline(ctx, cmds, w.slot)
	if err != nil {
		log.Printf("ecs-ec2 sweep: wedged release of %s: SSM state of %s unknown: %v", rt.base.name, w.slot, err)
		return
	}
	if !online {
		if prev, _ := f.wedgedBlocked.Swap(w.slot, w.launch.Format(time.RFC3339)); prev != w.launch.Format(time.RFC3339) {
			log.Printf("ecs-ec2 sweep: wedged slot %s cannot be released: SSM is not online, so the home cannot be unmounted", w.slot)
			f.reportWedged(ctx, SlotWedged{Slot: w.slot, Volume: aws.ToString(w.vol.VolumeId), Since: f.rescueNow(rt),
				Signal: w.signal, Reason: wedgedReasonSSMOffline, Workspace: rt.base.name, MembershipID: rt.base.membershipID}, "blocked")
		}
		return
	}
	f.wedgedBlocked.Delete(w.slot)
	d := darkDecision{slot: w.slot, vol: w.vol, launch: w.launch, gd: w.gd, wedged: true,
		// A fresh pass: the verdict that decided may be older than the wait for the lock, and
		// a cached probe is not a second look.
		stillWedged: func(c context.Context) bool {
			return f.wedgedVerdictFor(c, f.newDarkProbePass(), w.slot, w.vol, time.Now()).state == wedgedYes
		}}
	m, ids, err := rt.rescuePrepare(ctx, cmds, d)
	if err != nil {
		if !errors.Is(err, errRescueStepAside) {
			log.Printf("ecs-ec2 sweep: wedged release of %s on %s not started: %v", rt.base.name, w.slot, err)
		}
		return
	}
	log.Printf("ecs-ec2 sweep: releasing the home of %s from the wedged slot %s (marker %s)", rt.base.name, w.slot, m)
	f.rescueContinue(ctx, rt, cmds, w.slot, m, ids)
}

func (f *ecsEC2Factory) rescueNow(rt *ecsEC2Runtime) time.Time {
	return rt.now().UTC().Truncate(time.Second)
}

// serviceTasksFn reads every task of a service, running, stopping and recently stopped
// (ecsEC2Factory.serviceTasks); ok is false when any read failed.
type serviceTasksFn func(ctx context.Context, service string) ([]ecstypes.Task, bool)

// wedgedReleaseSteps carries a wedged marker from `begun` to `detached`: the service is put
// at desired 0, the home is unmounted through SSM, the tasks are waited out, the home is
// detached. All of it runs in one guarded section, so the mount lock spans the umount and the
// detach as releaseSlotSince's does; the waits inside are bounded, and one that runs out
// leaves the marker blocked and the home attached and unmounted (a Start mounts it again).
func (e *ecsEC2Runtime) wedgedReleaseSteps(ctx context.Context, cmds ssmSlotCommands, tasks serviceTasksFn, slot string, m *rescueMarker) error {
	if m.Phase.rank() >= rescuePhaseDetached.rank() {
		return nil
	}
	var stuck string
	detached := false
	err := e.rescueGuarded(ctx, m, slot, func(c context.Context) error {
		cur, err := e.readRescueBox(c, slot, m)
		if err != nil {
			return err
		}
		running := cur.State.Name == ec2types.InstanceStateNameRunning
		if !running && cur.State.Name != ec2types.InstanceStateNameStopped {
			return fmt.Errorf("%w: %s is %s", errRescueAborted, slot, cur.State.Name)
		}
		if err := e.advanceRescue(c, slot, m, rescuePhaseDetaching); err != nil {
			return err
		}
		if err := rescueStep(c, "detaching"); err != nil {
			return err
		}
		// The service goes to 0 as a Stop's does; the count the rescue bumped already
		// overtook any Start that was converging.
		if svc, ok, err := e.base.describeService(c); err != nil {
			return fmt.Errorf("%w: service: %w", errRescueAborted, err)
		} else if ok && svc.DesiredCount > 0 {
			if err := rescueLeasesHeld(c); err != nil {
				return fmt.Errorf("%w: %w", errRescueAborted, err)
			}
			if _, err := e.base.ecs.UpdateService(c, &ecs.UpdateServiceInput{
				Cluster: aws.String(e.base.cfg.cluster), Service: aws.String(e.base.name), DesiredCount: aws.Int32(0),
			}); err != nil {
				return fmt.Errorf("%w: scale %s to 0: %w", errRescueAborted, e.base.name, err)
			}
		}
		if err := rescueStep(c, "service-stopped"); err != nil {
			return err
		}
		if running {
			if stuck, err = e.wedgedUmount(c, cmds, slot); err != nil || stuck != "" {
				return err
			}
		}
		if stuck, err = e.wedgedWaitTasksStopped(c, tasks); err != nil || stuck != "" {
			return err
		}
		// After the waits, before the detach: a Start that landed moves the count and the
		// token no longer holds; the home stays attached and unmounted for it.
		if _, err := e.rescueOwned(c, *m, slot); err != nil {
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
		if err := rescueLeasesHeld(c); err == nil {
			e.afterDetach(c, m.Volume, slot, func() error { return rescueLeasesHeld(c) })
		} else {
			log.Printf("ecs-ec2: wedged release of %s detached %s but its lease lapsed; leaving the bookkeeping to the sweeper: %v", e.base.name, m.Volume, err)
		}
		return e.advanceRescue(c, slot, m, rescuePhaseDetached)
	})
	if detached {
		if err := rescueStep(ctx, "detached"); err != nil {
			return err
		}
		if err != nil {
			log.Printf("ecs-ec2: wedged release of %s detached %s but could not record it (the resume scan will): %v", e.base.name, m.Volume, err)
			return nil
		}
	}
	if err == nil && stuck != "" {
		return e.rescueBlock(ctx, slot, m, stuck)
	}
	return err
}

// wedgedUmount runs the home's umount through SSM, bounded. A non-empty reason means the
// step ran out of its bound or the command failed: the command is cancelled so that it does
// not keep the lock past this pass, and the caller blocks. An error is an abort.
func (e *ecsEC2Runtime) wedgedUmount(c context.Context, cmds ssmSlotCommands, slot string) (string, error) {
	if err := rescueLeasesHeld(c); err != nil {
		return "", fmt.Errorf("%w: %w", errRescueAborted, err)
	}
	uctx, cancel := context.WithTimeout(c, wedgedUmountBudget)
	defer cancel()
	err := e.umountHome(uctx, slot)
	if err == nil {
		return "", nil
	}
	if c.Err() != nil || errors.Is(err, errHomeLockLost) {
		return "", fmt.Errorf("%w: umount: %w", errRescueAborted, err)
	}
	// Cancelled on a context of its own: the work's has ended.
	open, _ := homeGuardOf(c).open()
	ids := make([]string, 0, len(open))
	for id := range open {
		ids = append(ids, id)
	}
	cctx, ccancel := context.WithTimeout(context.WithoutCancel(c), rescueTagBudget)
	defer ccancel()
	cancelCommandIDs(cctx, cmds, slot, ids)
	return fmt.Sprintf("the umount did not complete: %v", err), nil
}

// wedgedWaitTasksStopped waits, bounded, until ECS reports every task of the service STOPPED.
// The service's running and pending counts are not enough: a task that is STOPPING or
// DEACTIVATING is in neither, and may still hold the home through its bind mount (a host umount
// does not prove the container let go). A read that fails is not an answer and keeps waiting.
// The detach follows only a true answer; there is no force.
func (e *ecsEC2Runtime) wedgedWaitTasksStopped(c context.Context, tasks serviceTasksFn) (string, error) {
	for i := 0; ; i++ {
		live := -1
		if ts, ok := tasks(c, e.base.name); ok {
			live = 0
			for _, t := range ts {
				if aws.ToString(t.LastStatus) != "STOPPED" {
					live++
				}
			}
		}
		if live == 0 {
			return "", nil
		}
		if i >= wedgedTaskPolls {
			return "a task of the service has not reached STOPPED", nil
		}
		if err := e.pollPause(c); err != nil {
			return "", fmt.Errorf("%w: %w", errRescueAborted, err)
		}
	}
}
