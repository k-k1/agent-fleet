package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// --- Runtime port ---

func (e *ecsEC2Runtime) Token() string    { return e.base.Token() }
func (e *ecsEC2Runtime) Name() string     { return e.base.Name() }
func (e *ecsEC2Runtime) Endpoint() string { return e.base.Endpoint() }

// State maps the THREE-part truth — home volume, slot, service — onto the port's four
// words (ADR 0045 decision 3-5). The service's desired/running alone cannot tell a booting
// slot from a stopped workspace, and calling a starting workspace `stopped` invites the
// caller to Start it a second time.
//
//	no volume                                  → none
//	live claim (attached or not)               → starting — a Start is converging
//	volume attached, task RUNNING              → running
//	volume attached, service desired 1         → starting
//	volume attached, service desired 0 or gone → stopped
//	volume available, no claim                 → stopped
//
// The claim tag — not the attachment — is what says "starting". An earlier revision
// reported "volume attached but no service yet" as `starting`, and that was a trap:
// Start returns early on `starting`, so a workspace whose launch died between the
// attach and the CreateService could never be started again by anyone. Measured on real
// AWS. Now the same tag that covers the pre-attach window covers the whole launch, and
// it EXPIRES, so a dead launch always falls back to `stopped`.
func (e *ecsEC2Runtime) State(ctx context.Context) string {
	vol, err := e.homeVolume(ctx)
	if err != nil {
		// A cancelled context is the caller leaving (a Console poll aborted because the
		// tab closed or the next poll superseded it), not a fault. Logging it printed an
		// error line per abandoned poll on a real deployment — noise that makes the log
		// harder to read exactly when somebody is reading it for a real failure.
		if ctx.Err() == nil {
			log.Printf("ecs-ec2 state: describe home volume for %s: %v", e.base.name, err)
		}
		return "none"
	}
	if vol == nil {
		return "none"
	}
	if e.claimLive(vol) {
		return "starting"
	}
	if attachedInstance(vol) == "" {
		return "stopped"
	}
	// Taken BEFORE the service is read: the diagnosis below can wait on several AWS calls,
	// and what it finds describes this Start count, not a later one (notePlacementBlocked).
	gen, genErr := e.startGenNow(ctx)
	s, ok, err := e.base.describeService(ctx)
	if err != nil {
		log.Printf("ecs-ec2 state: describe service %s: %v", e.base.name, err)
		return "starting" // attached but unknown: never report a live slot as gone
	}
	if !ok {
		// Attached with no service and no claim: an abandoned attachment. Say stopped so
		// Start can pick it up again (it reuses the attachment) and the sweeper can
		// release it.
		return "stopped"
	}
	switch {
	// serviceRolledOut (runtime_ecs.go): launch() re-registers a task definition
	// and forces a new deployment on every Start (2074/2106), including the
	// "reattach to the same slot" case — so a RUNNING task can still share the
	// service with an old task that Service Connect hasn't stopped routing to
	// yet. Reporting "running" before that settles let a client's OAuth
	// start/complete land on two different Agent processes and lose the
	// in-memory flow_id (confirmed 2026-08-19 on the dev deployment).
	case s.DesiredCount >= 1 && s.RunningCount >= 1 && serviceRolledOut(s):
		e.clearBlockedPhase()
		return "running"
	case s.DesiredCount >= 1:
		e.notePlacementBlocked(ctx, s, gen, genErr == nil)
		return "starting"
	default:
		return "stopped"
	}
}

// notePlacementBlocked surfaces "the scheduler cannot place this task" as a phase, so a
// start that is never going to converge says WHY instead of spinning.
//
// ⚠️ This is the one place State() writes something, which is worth the exception.
// `starting` here means only "desired >= 1 and nothing is running yet", and the adapter
// sets it no timeout — a task ECS refuses to place holds that until the CP's start
// deadline stops it (start_deadline.go; docs/log/70 §70.14.6: a task definition declaring
// ARM64 while pinned to an x86_64 slot). ECS says exactly why in
// the service events; the CP was throwing that away and reporting a bare `starting`,
// so the only way to find out was `aws ecs describe-services` by hand. Nothing else in
// the read path can reach the events — BootPhase() takes no context and cannot call
// AWS — so it is written from here or not at all.
//
// The ECS events come from the DescribeServices the caller already made. Looking for a
// previous task stuck in STOPPING (stuckStoppingReason) costs more calls, so it only runs
// when the events have nothing to say, and at most every stuckStoppingEvery.
//
// gen is the Start count read before that DescribeServices. Everything this decides is a
// snapshot, and a Stop or newer Start can land while the lookups are in flight: writing or
// clearing a phase then would undo the Stop's clear or erase the newer Start's reason. The
// check and the write share homeLockClaim with beginStart, and are not applied
// to the lookups themselves, so a Stop never waits on AWS here. Gen 0 is a real count (no
// Start yet on this process), not "unchecked". An unreadable count (genOK false) writes
// nothing.
func (e *ecsEC2Runtime) notePlacementBlocked(ctx context.Context, s ecstypes.Service, gen int64, genOK bool) {
	why := ecsPlacementBlocked(s, time.Now())
	if why == "" {
		// ECS says nothing about a start waiting on the previous task's stop.
		var known bool
		if why, known = e.stuckStoppingReason(ctx, s); !known {
			return // a failed lookup says nothing either way: leave the phase as it is
		}
	}
	if !genOK {
		return
	}
	if why == "" {
		// Only the blocked phase goes: a task placed after the wall was named leaves State()
		// at `starting` until the rollout completes, and keeping "Waiting will not help" up
		// for that stretch is the bug this answer exists to prevent. Any other phase is an
		// ordinary Start writing its progress ("slot: creating", "home: attaching"), and
		// this poll must not wipe that.
		if !strings.HasPrefix(e.BootPhase(), blockedPhasePrefix) {
			return
		}
		e.ifGenCurrent(ctx, gen, e.clearBlockedPhase)
		return
	}
	phase := blockedPhasePrefix + why
	if e.BootPhase() == phase {
		return // already said, and this runs every 4s
	}
	if e.ifGenCurrent(ctx, gen, func() { e.setPhase(phase) }) {
		log.Printf("ecs-ec2: %s cannot be placed and will stay `starting` until this is fixed or the start deadline stops it: %s", e.base.name, why)
	}
}

// ifGenCurrent runs f only while the Start count still reads gen, under the lock that
// moves it. It does not treat gen 0 specially (unlike ifOwnedStrict); a count that cannot
// be read or a lock that cannot be had skips f.
func (e *ecsEC2Runtime) ifGenCurrent(ctx context.Context, gen int64, f func()) bool {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	lctx, unlock, err := e.lockHome(dctx, homeLockClaim)
	if err != nil {
		return false
	}
	defer unlock()
	if moved, err := e.startedSince(lctx, gen); err != nil || moved {
		return false
	}
	// The count may have been read under a lease that has run out since (a paused process, a
	// slow answer): another replica's Stop can then have completed while the count still
	// reads as ours. The clock decides, not the timer callback (homeLockGuard).
	if homeGuardOf(lctx).check() != nil || lctx.Err() != nil {
		return false
	}
	f()
	return true
}

// clearBlockedPhase removes a blocked phase once nothing blocks the task any more. Scoped
// to the prefix on purpose: any other phase belongs to a Start that is still in flight, and
// clearing that from a poll would blank the starting dialog mid-boot.
//
// CompareAndDelete, not read-then-setPhase(""): this runs on `starting` polls, while a
// Start may be storing its own progress, and an unconditional delete between the read
// and the write would erase that progress instead.
func (e *ecsEC2Runtime) clearBlockedPhase() {
	v, ok := startPhase.Load(e.base.name)
	if !ok {
		return
	}
	if s, _ := v.(string); strings.HasPrefix(s, blockedPhasePrefix) {
		startPhase.CompareAndDelete(e.base.name, v)
	}
}

// blockedPhasePrefix is the contract with the Console: WsStartingDialog maps this
// prefix to a localized headline and prints the rest — the raw ECS sentence — beneath
// it. The raw text is the valuable half; it names the constraint that failed.
const blockedPhasePrefix = "blocked: "

// ecsPlacementBlocked returns the ECS event explaining why the CURRENT deployment cannot
// be placed, or "" if there is no such event.
//
// Scoped to events newer than the PRIMARY deployment: a service that was wedged, got
// fixed and redeployed still carries the old complaint in its event list, and reporting
// that would turn a normal cold start into a fake diagnosis.
//
// ⚠️ ECS de-duplicates its own events, so the message appears once and then goes quiet
// for a long while. That is exactly why this is matched against the deployment rather
// than "was there an event recently": the wedge is permanent but the event is not
// repeated (measured — the run that prompted this had a single event and then silence).
//
// ⚠️ And an event has to STAND for placementBlockedGrace before it counts, because the
// same sentence is emitted by a wait the CP created itself — see that constant.
//
// ⚠️ A task placed after the event makes the event moot, however old it is. State() keeps
// answering `starting` until the rollout COMPLETES, which can be minutes after the task
// started (measured: 122 s), so the grace alone does not stop a placed task from being
// called a wall. Two checks, because each misses a case the other covers: the PRIMARY
// deployment's runningCount does not depend on ECS's wording, and a newer "has started"
// event still answers when that task has since died and runningCount is back at 0. A
// "has started" event is the PRIMARY's own: ec2SingleTaskDeployment (maximum 100%,
// minimum 0%) drains the outgoing deployment and never starts a task under it.
//
// Compared by CreatedAt rather than by position: the API lists newest first in practice
// but does not promise it, and a misread order would hide a real wall behind a start.
func ecsPlacementBlocked(s ecstypes.Service, now time.Time) string {
	var since time.Time
	for _, d := range s.Deployments {
		if aws.ToString(d.Status) != "PRIMARY" {
			continue
		}
		if d.RunningCount >= 1 {
			return "" // a workspace service wants one task, and it is placed
		}
		if d.CreatedAt != nil {
			since = *d.CreatedAt
		}
	}
	for _, ev := range s.Events {
		if ev.CreatedAt != nil && ev.CreatedAt.After(since) && strings.Contains(aws.ToString(ev.Message), "has started ") {
			since = *ev.CreatedAt // every complaint up to here predates a task that was placed
		}
	}
	for _, ev := range s.Events {
		if ev.CreatedAt == nil || ev.CreatedAt.Before(since) {
			continue
		}
		if msg := aws.ToString(ev.Message); strings.Contains(msg, "unable to place a task") {
			// Not `return ""` on a young one: a wedge that ECS re-emitted leaves a fresh
			// copy beside the aged original. Skipping it reports the original; a bare
			// return would hide the wall every time ECS repeated itself.
			if now.Sub(aws.ToTime(ev.CreatedAt)) < placementBlockedGrace {
				continue
			}
			return strings.TrimSpace(msg)
		}
	}
	return ""
}

// placementBlockedGrace is how long "unable to place a task" must stand before it is
// reported as a wall.
//
// 🔥 Without it the message fires during a wait the CP ITSELF created. Growing the pool
// takes an EC2 launch plus an ECS agent registration, and the service is already asking
// for its task while that happens; ECS then names as "closest matching" one of the
// STOPPED slots (Ec2SlotTerminateAfterSec=0 leaves them registered with the agent
// disconnected — 6 of 9 on the deployment where this was found), which reads like a
// diagnosis and is only an artefact. Measured on acrt 2026-09-07, a member moving to an
// arm slot class: pool grown 04:11:43 → "unable to place … doesn't have the agent
// connected" 04:12:08 → task started 04:12:38. The Console had already told them
// "Cannot start. Waiting will not help" — 30 seconds before it started.
//
// 120s is that 55-second window with margin. Erring long is the cheap direction: a real
// wedge is permanent, so the only cost is naming it later, while erring short is the bug
// above. Once the task is placed the event is moot however old it gets; ecsPlacementBlocked
// checks that itself rather than relying on State() having reached `running` first.
const placementBlockedGrace = 120 * time.Second

// Start brings the workspace up on a slot. Everything that can be slow is pushed off
// the caller's thread: Start runs inside an HTTP request behind a 60s-idle ALB
// (docs/log/62 §62.5), so only the hot path — attach 3s + mount 4s + a few API calls —
// may run synchronously. Booting a slot (19s) or creating one (8s to running, then
// ~20s to register) hands off to a background goroutine and State() says `starting`.
func (e *ecsEC2Runtime) Start(ctx context.Context) error {
	switch e.State(ctx) {
	case "running":
		return nil
	case "starting":
		// Already converging. Re-entering would attach a second slot / restart the
		// service deployment from zero, exactly as on Fargate.
		return nil
	}
	// A Destroy's home task may still be removing /claude-config/<id> and /home-keep/<id>
	// (its CP restarted, or its budget ran out, and the row stayed). Starting now would
	// mount them again and lose the new logins with them. Asked before anything is created:
	// the base's gate reads the SSM marker and ECS, so it holds across a restart.
	if err := e.base.HomeWipeBlocked(ctx); err != nil {
		return err
	}
	// Mark that a Start has begun, so a teardown still draining from the Stop that the
	// recreate / clean-home handlers issued a moment ago aborts instead of pulling this
	// workspace's home out from under it.
	gen, err := e.beginStart(ctx)
	if err != nil {
		return err
	}
	e.setPhase("preparing")
	prep, err := e.prepare(ctx)
	if err != nil {
		e.setPhase("")
		return err
	}
	place, err := e.placeHome(ctx)
	if err != nil {
		e.setPhase("")
		return err
	}
	place.gen = gen
	// Removing a home's contents takes as long as the home is big, and this thread is
	// the request's.
	if place.wipe != "" {
		place.deferred = true
	}
	if place.deferred && place.claimErr != nil {
		// Nothing has been launched; the attachment stays, so a retry takes the cheap path.
		e.setPhase("")
		return fmt.Errorf("mark the start of %s as converging: %w", e.base.name, place.claimErr)
	}
	if place.deferred {
		// The slot is not attachable yet (pending instance) or not registered with ECS
		// yet (just started, waking, or gone dark). Finish in the background; the claim
		// tag / the attachment is what keeps State() at `starting` until it lands.
		e.bg(ctx, func(c context.Context) { e.finishStart(c, place, prep) })
		return nil
	}
	if err := e.launch(ctx, place, prep); err != nil {
		if errors.Is(err, errSlotLost) {
			// The slot answered placeHome and was gone by the time launch looked again.
			// abandonLostSlot has freed the home; finding it another box is minutes of
			// work, which is exactly what may not happen on this thread — so hand off and
			// let State() keep saying `starting` off the claim.
			e.setPhase("slot: replacing")
			e.bg(ctx, func(c context.Context) {
				defer e.setPhase("")
				if e.startSuperseded(c, place) {
					return
				}
				next, perr := e.placeHome(c)
				next.gen = place.gen
				if perr == nil {
					perr = next.claimErr
				}
				if perr != nil {
					log.Printf("ecs-ec2 start: re-placing %s after losing slot %s: %v", e.base.name, place.instanceID, perr)
					e.unclaim(c, place.volumeID)
					return
				}
				e.converge(c, next, prep, 0)
			})
			return nil
		}
		e.setPhase("")
		return err
	}
	e.setPhase("")
	return nil
}

// Stop scales the service to zero and LEAVES THE HOME ATTACHED — "lazy release".
//
// The first implementation unmounted and detached right away, which handed the slot
// back in ~13s but made the common case pay for it: the same person coming back after
// lunch had to find a slot, attach, mkfs-check and mount again. Keeping the attachment
// makes that return the cheapest path there is (measured 13.2s: no attach, no mount,
// just the service going back to desiredCount 1), and it gives the affinity for free —
// **the attachment IS "this user's slot"**, so nothing has to be remembered anywhere.
//
// Two things keep it from turning into hoarding, both in the sweeper:
//
//   - the slot is STOPPED after AF_ECS_EC2_SLOT_SLEEP_SEC (default 15m). A stopped slot
//     costs only its root volume (~$9.6/month vs ~$95 running) and keeps the image
//     cache, so the return is ~90s instead of 135s.
//   - when someone else needs a slot and the pool is at its cap, the longest-idle
//     occupant is evicted (placeHome → evict).
//
// Nothing is unmounted here, so nothing can be corrupted here. The teardown order
// (umount before detach) still lives in releaseSlot, which is now reached only by
// eviction, Destroy and drift repair.
func (e *ecsEC2Runtime) Stop(ctx context.Context) error {
	_, err := e.stopCounted(ctx)
	return err
}

// stopCounted is Stop that also hands back the Start count its abort set. A caller that
// goes on to act on the slot (the admin force-stop) fences that work on the count: any
// Start accepted after this Stop moves it.
func (e *ecsEC2Runtime) stopCounted(ctx context.Context) (int64, error) {
	// First: a Start still converging in the background must find itself overtaken before
	// desiredCount is touched, or it raises the count again after the scale-to-zero below.
	gen, err := e.beginStart(ctx)
	if err != nil {
		return 0, err
	}
	return gen, e.stopAfterAbort(ctx)
}

func (e *ecsEC2Runtime) stopAfterAbort(ctx context.Context) error {
	if _, ok, err := e.base.describeService(ctx); err != nil {
		return err
	} else if ok {
		if _, err := e.base.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{
			Cluster:      aws.String(e.base.cfg.cluster),
			Service:      aws.String(e.base.name),
			DesiredCount: aws.Int32(0),
		}); err != nil {
			return err
		}
	}
	// A blocked phase outlives the start it described otherwise: State() only clears it on
	// running/starting, and the Console keeps its starting dialog open while any phase is
	// reported — so a stopped workspace would sit behind "cannot be placed" for good.
	e.clearBlockedPhase()
	// A Start overtaken above no longer owns the claim or the phase; clearing them here is
	// what makes State() read stopped now rather than when the claim expires.
	e.setPhase("")
	e.dropClaimOnStop(ctx)
	// Record when the dormancy started. If this write is lost (CP restart mid-Stop) the
	// sweeper stamps it the first time it sees an idle attachment, so the worst case is
	// that the slot sleeps one sweep later.
	vol, err := e.homeVolume(ctx)
	if err != nil || vol == nil || attachedInstance(vol) == "" {
		return nil
	}
	e.markIdle(ctx, aws.ToString(vol.VolumeId))
	return nil
}

// markIdle / clearIdle move the home in and out of the dormant set.
func (e *ecsEC2Runtime) markIdle(ctx context.Context, volumeID string) {
	if _, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{volumeID},
		Tags:      []ec2types.Tag{{Key: aws.String(EC2TagIdleSince), Value: aws.String(e.now().UTC().Format(time.RFC3339))}},
	}); err != nil {
		log.Printf("ecs-ec2: marking %s idle failed (the sweeper will stamp it): %v", volumeID, err)
	}
}

func (e *ecsEC2Runtime) clearIdle(ctx context.Context, volumeID string) {
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{volumeID},
		Tags:      []ec2types.Tag{{Key: aws.String(EC2TagIdleSince)}},
	}); err != nil {
		log.Printf("ecs-ec2: clearing the idle mark on %s failed: %v", volumeID, err)
	}
}

// clearDormancy is what a START calls: the owner is back, so both the dormancy clock and
// any in-flight hibernation are off. It is deliberately NOT the same thing as clearIdle —
// hibernation's own first step is releaseSlot, which clears the idle mark, and if that
// also dropped the hibernation mark the operation would forget itself one line after
// starting.
//
// A snapshot already running is left to finish rather than cancelled: the next
// hibernation drops it as superseded (it started before that one's mark), and
// DeleteSnapshot racing a completing capture buys nothing.
func (e *ecsEC2Runtime) clearDormancy(ctx context.Context, volumeID string) {
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{volumeID},
		Tags:      []ec2types.Tag{{Key: aws.String(EC2TagIdleSince)}, {Key: aws.String(EC2TagHibernating)}},
	}); err != nil {
		log.Printf("ecs-ec2: clearing the dormancy marks on %s failed: %v", volumeID, err)
	}
}

// idleSince reports how long a home has been dormant, and whether it is dormant at all.
func idleSince(vol *ec2types.Volume, now time.Time) (time.Duration, bool) {
	v := ec2TagValue(vol.Tags, EC2TagIdleSince)
	if v == "" {
		return 0, false
	}
	at, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return 0, false
	}
	return now.Sub(at), true
}
