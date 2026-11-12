package runtime

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// The Start side of rung L1 (ADR 0121 decision 2; the handover is the dated note of
// #3965). A member's Start whose home sits on a dark slot cannot be served by waiting on that
// box, and it must not count itself either: the rescue's preparation step bumps the Start
// count to g_r and ends any Start, so a Start that bumped first would be aborted by the
// rescue it triggered, and one that bumped after would overtake it.
//
// So the Start yields. Under the claim lock, in the step that would have counted it, it
// reads the world and takes one of three roads (admitStart):
//
//   - nothing dark, no rescue: it counts itself exactly as before (beginStart);
//   - a rescue owns the home (marker on the box, count equal to its g_r): it does not count,
//     shows the blocked-phase line and waits;
//   - the box is dark (stamp of this launch epoch, past darkAfter): it triggers the rescue
//     (rescuePrepare, the same step the sweeper takes, which moves the count itself), shows
//     the line, and carries the rescue on in the background.
//
// The waiter that follows (awaitRescue) places the home once the rescue is gone and the
// home is off the box, and only if the count is still g_r: a Stop, a Destroy or a newer Start
// moved it, and the work is theirs. Rescue mode only; any other action is today's Start.

const (
	// darkAdmitTries bounds how often a Start decides again after the rescue stepped aside
	// (somebody acted between its decision and its locked step). After that it counts itself.
	darkAdmitTries = 3

	// darkRescuePhaseHead starts the reason of both blocked-phase lines, which is how
	// State() recognizes them once the home is off the box.
	darkRescuePhaseHead = "the slot this workspace's home is on stopped responding"
)

// Bounds on the waiter, in polls. Variables only so a test can shorten them. The production
// product is the background budget (AF_ECS_EC2_WAIT_SEC, 10 minutes by default); the
// waiter also ends with its context.
var (
	darkStartPolls     = 120
	darkStartPollEvery = 5 * time.Second
)

// darkStartHook is a test's seam between a Start's decision to rescue and the rescue's own
// locked step: whatever lands there is what rescuePrepare must meet. Nil in production.
var darkStartHook func()

// afterRescueCountHook runs in startAfterRescue between the count read and the bump. Nil in production.
var afterRescueCountHook func(ctx context.Context)

type admitKind int

const (
	admitStart  admitKind = iota // counted: go on with the Start (gen)
	admitYield                   // a rescue owns the home: wait for it (slot, marker)
	admitRescue                  // the box is dark: trigger the rescue (decision)
)

type startAdmission struct {
	kind     admitKind
	gen      int64
	slot     string
	marker   rescueMarker
	decision darkDecision
}

// admitStart is beginStart for a Start that may have to yield. With rescue off or `try`
// exhausted it is exactly beginStart.
func (e *ecsEC2Runtime) admitStart(ctx context.Context, mayRescue bool) (startAdmission, error) {
	if e.pool.darkAction != darkActionRescue || e.pool.darkAfter <= 0 {
		gen, err := e.beginStart(ctx)
		return startAdmission{kind: admitStart, gen: gen}, err
	}
	lctx, unlock, err := e.lockHome(ctx, homeLockClaim)
	if err != nil {
		return startAdmission{}, err
	}
	defer unlock()
	g, err := e.startGenNow(lctx)
	if err != nil {
		return startAdmission{}, fmt.Errorf("count the start of %s: %w", e.base.name, err)
	}
	// A read that fails is not an answer: the Start goes on as it always did, and the
	// rescue's own locked step decides on what it reads itself.
	if adm, ok := e.darkAdmission(lctx, g, mayRescue); ok {
		return adm, nil
	}
	gen, err := e.bumpStartCount(lctx)
	if err != nil {
		return startAdmission{}, fmt.Errorf("count the start of %s: %w", e.base.name, err)
	}
	return startAdmission{kind: admitStart, gen: gen}, nil
}

// darkAdmission decides, under the claim lock with the count g read, whether this Start
// yields. false means: count it.
func (e *ecsEC2Runtime) darkAdmission(lctx context.Context, g int64, mayRescue bool) (startAdmission, bool) {
	if _, ok := e.ssmc.(ssmSlotCommands); !ok {
		return startAdmission{}, false
	}
	vol, err := e.homeVolume(lctx)
	if err != nil || vol == nil {
		return startAdmission{}, false
	}
	slot := attachedInstance(vol)
	if slot == "" || e.claimLive(vol) {
		return startAdmission{}, false
	}
	inst, err := e.slotInstance(lctx, slot)
	if err != nil || inst == nil || inst.State == nil || inst.LaunchTime == nil ||
		ec2TagValue(inst.Tags, EC2TagRole) != ec2RoleSlot {
		return startAdmission{}, false
	}
	volumeID := aws.ToString(vol.VolumeId)
	launch := inst.LaunchTime.UTC().Truncate(time.Second)
	if raw := ec2TagValue(inst.Tags, ec2TagRescue); raw != "" {
		// A marker that is not this home's, does not parse, or no longer matches (count moved,
		// box started again) is not a rescue that owns the home: the Start counts itself as
		// it did before, which overtakes it (ADR 0121 decision 3). `preparing` is no owner
		// either: a count equal to its g_r proves nothing there (L1 review round 1).
		m, ok := parseRescueMarker(raw)
		// An operator's force-stop is not one a Start waits for: the member's Start counts itself
		// and overtakes it, as it overtakes any operator action.
		if ok && !m.Operator && m.Volume == volumeID && m.Gen == g && m.Epoch.Equal(launch) &&
			m.Phase.rank() >= rescuePhaseBegun.rank() && m.Phase != rescuePhaseDetached {
			return startAdmission{kind: admitYield, slot: slot, marker: m}, true
		}
		return startAdmission{}, false
	}
	if !mayRescue || inst.State.Name != ec2types.InstanceStateNameRunning {
		return startAdmission{}, false
	}
	// The stamp must be of this launch epoch and past darkAfter. The sweep's other legs
	// (silent agents, the Agent probe) were true when it stamped; rescuePrepare asks again
	// for what it can (the service down, the box running in this epoch, no live claim).
	stamp, ok := parseDarkStamp(ec2TagValue(inst.Tags, ec2TagDarkSince))
	if !ok || !stamp.launch.Equal(launch) || e.now().Sub(stamp.since) < e.pool.darkAfter {
		return startAdmission{}, false
	}
	return startAdmission{kind: admitRescue, slot: slot, decision: darkDecision{slot: slot, vol: vol, launch: launch, gd: g}}, true
}

// yieldToRescue ends the Start's request: it shows the blocked-phase line and hands the rest
// to the background. cmds and ids are set when this Start began the rescue (the preparation
// step has run, so the cancel and the steps are its to carry on); otherwise the rescue is the
// sweeper's and the Start only waits.
func (e *ecsEC2Runtime) yieldToRescue(ctx context.Context, slot string, m rescueMarker, cmds ssmSlotCommands, ids []string) {
	log.Printf("ecs-ec2 start: %s is on the dark slot %s; yielding to the rescue (marker %s)", e.base.name, slot, m)
	e.noteRescuePhase(ctx, m)
	e.bg(ctx, func(c context.Context) {
		if cmds != nil {
			e.rescueContinue(c, cmds, slot, m, ids)
		}
		e.awaitRescue(c, slot, m)
	})
}

// noteRescuePhase writes the blocked-phase line while the rescue still owns the count.
func (e *ecsEC2Runtime) noteRescuePhase(ctx context.Context, m rescueMarker) {
	phase := blockedPhasePrefix + darkRescuePhaseHead + "; it is being recovered, and the workspace starts on another slot when that finishes"
	if !m.BlockedSince.IsZero() {
		phase = blockedPhasePrefix + darkRescuePhaseHead + " and recovering it has stopped at " + string(m.Phase) +
			"; it is retried automatically, and an operator may have to stop the box"
	}
	if e.BootPhase() == phase {
		return
	}
	e.ifGenCurrent(ctx, m.Gen, func() { e.setPhase(phase) })
}

func isDarkRescuePhase(p string) bool {
	return strings.HasPrefix(p, blockedPhasePrefix+darkRescuePhaseHead)
}

// clearDarkRescuePhase drops the line when the home is off the box: nothing is being waited
// for any more, and a phase left behind keeps the Console's dialog open on a stopped workspace.
func (e *ecsEC2Runtime) clearDarkRescuePhase() {
	v, ok := startPhase.Load(e.base.name)
	if !ok {
		return
	}
	if s, _ := v.(string); isDarkRescuePhase(s) {
		startPhase.CompareAndDelete(e.base.name, v)
	}
}

// awaitRescue waits until the rescue is over and then places the home, if the count is still
// the rescue's. It never acts on a count it cannot read, and it never raises one.
func (e *ecsEC2Runtime) awaitRescue(ctx context.Context, slot string, m rescueMarker) {
	for i := 0; i < darkStartPolls; i++ {
		if i > 0 {
			if err := e.sleep(ctx, darkStartPollEvery); err != nil {
				return
			}
		}
		g, err := e.startGenNow(ctx)
		if err != nil {
			continue
		}
		if g != m.Gen {
			return // a Stop, a Destroy or a newer Start owns the home now
		}
		vol, err := e.homeVolume(ctx)
		if err != nil {
			continue
		}
		if vol == nil || aws.ToString(vol.VolumeId) != m.Volume {
			return
		}
		cur, found, err := e.rescueOnBox(ctx, slot, m)
		if err != nil {
			continue
		}
		if attachedInstance(vol) == "" {
			if found {
				continue // detached; the finalizer and the pool bookkeeping are not done yet
			}
			e.startAfterRescue(ctx, m.Gen)
			return
		}
		if !found {
			// The rescue is gone and the home is still on the box with the count unmoved: it
			// stopped without taking the home off (its preparation was dropped). Nothing is
			// being waited for; the member's next Start decides again.
			e.ifGenCurrent(ctx, m.Gen, e.clearDarkRescuePhase)
			return
		}
		e.noteRescuePhase(ctx, cur)
	}
}

// rescueOnBox reads the box's marker for this rescue: found is false when the box is gone or
// carries no marker for the home.
func (e *ecsEC2Runtime) rescueOnBox(ctx context.Context, slot string, m rescueMarker) (rescueMarker, bool, error) {
	inst, err := e.slotInstance(ctx, slot)
	if err != nil {
		return rescueMarker{}, false, err
	}
	if inst == nil {
		return rescueMarker{}, false, nil
	}
	cur, ok := parseRescueMarker(ec2TagValue(inst.Tags, ec2TagRescue))
	if !ok || cur.Volume != m.Volume {
		return rescueMarker{}, false, nil
	}
	return cur, true, nil
}

// startAfterRescue is the Start that waited: it counts itself only if the count is still g_r
// (so nothing happened since the rescue took the home), then goes on as any Start.
func (e *ecsEC2Runtime) startAfterRescue(ctx context.Context, gr int64) {
	fail := func(gen int64, err error) {
		log.Printf("ecs-ec2 start: %s after its slot was recovered: %v", e.base.name, err)
		e.ifGenCurrent(ctx, gen, func() {
			e.setPhase(blockedPhasePrefix + "could not start after the slot was recovered: " + clipMiddle(err.Error(), 300))
		})
	}
	if err := e.base.HomeWipeBlocked(ctx); err != nil {
		fail(gr, err)
		return
	}
	lctx, unlock, err := e.lockHome(ctx, homeLockClaim)
	if err != nil {
		fail(gr, err)
		return
	}
	g, err := e.startGenNow(lctx)
	if err != nil || g != gr {
		unlock()
		if err != nil {
			fail(gr, err)
		}
		return
	}
	if afterRescueCountHook != nil {
		afterRescueCountHook(lctx)
	}
	// The count was read under a lease that may have run out since (a paused process, a slow
	// answer): another replica's Stop or Start can then have completed while it still reads
	// g_r, and a bump now would overtake that Start. The clock decides, not the timer callback.
	if homeGuardOf(lctx).check() != nil || lctx.Err() != nil {
		unlock()
		return
	}
	gen, err := e.bumpStartCount(lctx)
	unlock()
	if err != nil {
		fail(gr, err)
		return
	}
	if err := e.startFrom(ctx, gen); err != nil {
		fail(gen, err)
	}
}
