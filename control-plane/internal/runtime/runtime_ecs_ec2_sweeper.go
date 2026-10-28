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
)

// --- drift sweeper (docs/log/64 §64.15.6) ---

// sweepLoop re-derives the world from tags every sweepEvery and finishes whatever a
// crashed CP left half-done. Without it, "hold no state" would mean "lose the teardown
// when the process dies": a volume stuck on a slot no one is using, a claim that pins a
// workspace at `starting`, a ghost container instance the scheduler keeps trying to
// place onto.
func (f *ecsEC2Factory) sweepLoop(ctx context.Context) {
	t := time.NewTicker(f.pool.sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := f.sweep(ctx); err != nil {
				log.Printf("ecs-ec2 sweep: %v", err)
			}
		}
	}
}

func (f *ecsEC2Factory) sweep(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, f.pool.waitBudget)
	defer cancel()
	vols, err := f.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, f.pool.pool),
			tagFilter(EC2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return fmt.Errorf("describe home volumes: %w", err)
	}
	for i := range vols.Volumes {
		f.sweepVolume(ctx, &vols.Volumes[i])
	}
	// Repair the billing tags BEFORE the volume sweep's own releases are re-read: the
	// truth is "who is attached right now", and that is exactly what `vols` holds.
	f.sweepSlotOwnerTags(ctx, vols.Volumes)
	f.sweepFreeSlots(ctx, vols.Volumes)
	return f.sweepGhostInstances(ctx)
}

// sweepSlotOwnerTags takes af-membership/af-tenant off any slot that is not actually
// holding that person's home. It is the repair half of tagSlotOwner: a CP that dies
// between "detach" and "delete the tag" would otherwise keep charging the last user for
// a box that is back in the free pool, and nothing else would ever notice — the pool
// logic does not read these tags, so a stale one is invisible except on the invoice.
//
// The attached volumes ARE the answer to who owns what (the same source freeSlots and
// releaseSlot already trust), so this compares against them rather than asking ECS.
// It repairs BOTH directions, because they are different failures:
//   - a slot tagged for somebody whose home is not on it → clear (a crash between the
//     detach and the untag; the box is back in the pool and its hours are shared cost);
//   - a slot holding a home but not tagged for it → stamp (a crash between the attach
//     and the stamp, or a box that predates this code) — copied FROM THE VOLUME, which
//     is the only place that knows the owner without a DB read.
func (f *ecsEC2Factory) sweepSlotOwnerTags(ctx context.Context, homes []ec2types.Volume) {
	type ownerTags struct{ membership, tenant string }
	owner := map[string]ownerTags{} // instance id -> who is actually on it
	for i := range homes {
		if inst := attachedInstance(&homes[i]); inst != "" {
			owner[inst] = ownerTags{
				membership: ec2TagValue(homes[i].Tags, EC2TagMembership),
				tenant:     ec2TagValue(homes[i].Tags, EC2TagTenant),
			}
		}
	}
	out, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, f.pool.pool),
			// 🔴 The ROLE, which the other five EC2-side walks have always paired with af-pool
			// and this one did not (ADR 0077 review R3). Without it this sweep walks every
			// instance in the pool — including an engine box, which carries af-pool too — and
			// writes or strips af-membership / af-tenant on it, billing somebody's hours to a
			// GPU they never had. Quarantined boxes stay in: they can still hold a home's
			// owner tags, and the repair is exactly what this exists for.
			{Name: aws.String("tag:" + EC2TagRole), Values: []string{ec2RoleSlot, ec2RoleQuarantined}},
			// A terminated box still answers DescribeInstances for a while and cannot be
			// tagged; excluding it keeps the log free of failures nobody can act on.
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		log.Printf("ecs-ec2 sweep: reading slot owner tags: %v", err)
		return
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			id := aws.ToString(inst.InstanceId)
			stamped := ec2TagValue(inst.Tags, EC2TagMembership)
			want := owner[id]
			switch {
			case stamped == want.membership:
				continue // already right (including "nobody on it, no tag")
			case want.membership == "":
				log.Printf("ecs-ec2 sweep: slot %s is billed to %q but holds no home; clearing", id, stamped)
				if _, err := f.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
					Resources: []string{id},
					Tags:      []ec2types.Tag{{Key: aws.String(EC2TagMembership)}, {Key: aws.String(EC2TagTenant)}},
				}); err != nil {
					log.Printf("ecs-ec2 sweep: clearing the owner of slot %s failed: %v", id, err)
				}
			default:
				log.Printf("ecs-ec2 sweep: slot %s holds %q but is billed to %q; restamping", id, want.membership, stamped)
				tags := []ec2types.Tag{{Key: aws.String(EC2TagMembership), Value: aws.String(want.membership)}}
				if want.tenant != "" {
					tags = append(tags, ec2types.Tag{Key: aws.String(EC2TagTenant), Value: aws.String(want.tenant)})
				}
				if _, err := f.ec2.CreateTags(ctx, &ec2.CreateTagsInput{Resources: []string{id}, Tags: tags}); err != nil {
					log.Printf("ecs-ec2 sweep: restamping the owner of slot %s failed: %v", id, err)
				}
				// The new owner's tenant is unknown but the previous one's may still be
				// on the box; leaving it would bill this person's hours to a tenant they
				// are not in, which is worse than no tenant at all.
				if want.tenant == "" && ec2TagValue(inst.Tags, EC2TagTenant) != "" {
					if _, err := f.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
						Resources: []string{id},
						Tags:      []ec2types.Tag{{Key: aws.String(EC2TagTenant)}},
					}); err != nil {
						log.Printf("ecs-ec2 sweep: clearing a stale tenant tag on %s failed: %v", id, err)
					}
				}
			}
		}
	}
}

func (f *ecsEC2Factory) sweepVolume(ctx context.Context, vol *ec2types.Volume) {
	rt := f.runtimeForVolume(vol)
	if rt == nil {
		return
	}
	volumeID := aws.ToString(vol.VolumeId)
	if ec2TagValue(vol.Tags, EC2TagClaim) != "" && !rt.claimLive(vol) {
		log.Printf("ecs-ec2 sweep: clearing an expired claim on %s", volumeID)
		rt.unclaim(ctx, volumeID)
	}
	att := attachment(vol)
	if att == nil {
		// A hibernation already under way: the slot went back to the pool at its first
		// step (which is also what cleared af-idle-since, so the dormancy test below
		// cannot be what resumes it). Keep advancing it — nothing else will, and stopping
		// here leaves BOTH a snapshot and a volume billing. Deliberately not gated on
		// hibernateAfter: turning the feature off must not strand a home half-way.
		if ec2TagValue(vol.Tags, EC2TagHibernating) != "" {
			if err := rt.hibernate(ctx); err != nil {
				log.Printf("ecs-ec2 sweep: hibernating %s failed: %v", rt.base.name, err)
			}
		}
		return
	}
	// A Start is in flight on this home. Everything below reads "no live service" as
	// "dormant", and for those few seconds that is exactly wrong (docs/log/64 §64.31.6):
	// placeHome clears the dormancy marks FIRST and upsertService raises desiredCount
	// LAST, with the mount — an SSM round trip, 10–30s — in between. A sweep landing in
	// that window stamps af-idle-since back onto a workspace that is coming UP.
	//
	// It never caused a premature stop (re-stamping moves the clock forward, not back),
	// but the mark then outlives the launch: a RUNNING workspace wears a dormancy mark
	// until its next Stop, which makes the pool screen report it as idle and makes
	// evictLongestIdle pick it as a victim — and releaseSlot refusing a live service
	// turns that into a failed Start at the cap rather than a move to the next victim.
	//
	// The free-slot walk has honoured live claims from the start; this is the same rule
	// on the other half of the same sweep. Deliberately AFTER the hibernation branch
	// above: a claim must never be able to strand a capture half-way. An EXPIRED claim
	// falls through (claimLive parses af-claim-at rather than trusting the tag's
	// presence), so a launch that died cannot pin this walk either.
	if rt.claimLive(vol) {
		return
	}
	instanceID := aws.ToString(att.InstanceId)
	s, ok, err := rt.base.describeService(ctx)
	if err != nil {
		return
	}
	if ok && (s.DesiredCount > 0 || s.RunningCount > 0 || s.PendingCount > 0) {
		return // live workspace: leave it alone
	}
	if !ok {
		// Attached but the workspace has no service at all — drift (a launch that never
		// got that far, or a workspace that was removed). This is the only case where
		// the sweeper still takes the home off its slot; a merely STOPPED workspace
		// keeps its slot on purpose (lazy release).
		//
		// The grace is on the ATTACH time and belongs to THIS branch only: a Start
		// attaches before it creates the service, so without it a sweep landing in that
		// window would tear down a launch in progress. The idle-stop below must NOT be
		// gated this way — a long-lived attachment is exactly what it is looking for.
		if att.AttachTime != nil && time.Since(*att.AttachTime) < f.pool.releaseGrace {
			return
		}
		log.Printf("ecs-ec2 sweep: %s is attached to %s but has no service; releasing", volumeID, instanceID)
		if err := rt.releaseSlot(ctx); err != nil {
			log.Printf("ecs-ec2 sweep: releasing %s failed: %v", volumeID, err)
		}
		return
	}
	// Dormant workspace. Stamp it if the Stop lost the mark, then put the slot to sleep
	// once it has been dormant long enough: a stopped slot keeps the image cache and the
	// attachment (so the owner comes back in ~90s instead of 135s) and costs only its
	// root volume instead of a running instance.
	idle, marked := idleSince(vol, time.Now())
	if !marked {
		rt.markIdle(ctx, volumeID)
		return
	}
	// Hibernation is the third and last step of the same series (reaper stops the
	// workspace → the slot sleeps → the home becomes a snapshot), but it is NOT started
	// here: how long a tenant's homes may sit before they are put away is a database
	// answer and this loop has no database (ADR 0012). The reaper stamps af-hibernating
	// and starts the capture; from then on the branch above (att == nil) advances it.
	// A hibernation whose first step has run therefore never reaches this point.

	// Past slotTerminateAfter the box goes away entirely. This is checked BEFORE the sleep
	// test, not after: the two are stages of one clock, and by the time this fires the box
	// is normally already stopped — but a deployment with slotSleepAfter=0 must still be
	// able to reclaim roots, and there the box is running and skips straight to here.
	if f.pool.slotTerminateAfter > 0 && idle >= f.pool.slotTerminateAfter {
		// The home comes OFF first, through releaseSlot, rather than by letting
		// TerminateInstances drop it (the home is DeleteOnTermination=false, so it would
		// survive either way). Both reasons are about what the OWNER sees if they Start
		// during the ~60s a terminate takes:
		//
		//   - releaseSlot is anchored to the Start generation, so a Start racing this
		//     aborts the release — and re-mounts — instead of losing its home mid-launch.
		//     Nothing about TerminateInstances can be taken back.
		//   - placeHome derives placement from the ATTACHMENT. A volume still attached to a
		//     shutting-down instance reads as branch ①: it would claim that box, find its
		//     instance type still matching, and then fail to wake it.
		//
		// releaseSlot also skips the umount on an already-stopped box (SSM cannot reach
		// one) and refuses while any task remains, which is exactly the guard this branch
		// would otherwise have to repeat. On failure we leave the box alone: a slot that
		// could not be released is not one to terminate.
		if err := rt.releaseSlot(ctx); err != nil {
			log.Printf("ecs-ec2 sweep: releasing %s before terminating slot %s: %v", volumeID, instanceID, err)
			return
		}
		rt.terminateSlot(ctx, instanceID, fmt.Sprintf("%s dormant %.0fm", rt.base.name, idle.Minutes()))
		return
	}
	if f.pool.slotSleepAfter <= 0 || idle < f.pool.slotSleepAfter {
		return // 0 = never sleep (see slotSleepAfter)
	}
	running, err := rt.instanceRunning(ctx, instanceID)
	if err != nil || !running {
		return
	}
	// Never stop a slot that still carries a task ENI. ECS detaches those a little after
	// the task is gone, and an instance stopped in that window comes back MULTI-ENI —
	// which is how it silently loses its auto-assigned public IPv4 and, on a deployment
	// without NAT, its egress: the agent then never reconnects (ADR 0045 decision 3-3, and
	// reproduced through this very code path in the live test). Skipping just means the
	// next sweep stops it.
	if held, err := f.taskENIsAttached(ctx, instanceID); err != nil || held {
		if held {
			log.Printf("ecs-ec2 sweep: slot %s still has a task ENI; leaving it running for now", instanceID)
		}
		return
	}
	log.Printf("ecs-ec2 sweep: %s has been dormant %.0fm; stopping slot %s (home stays attached)",
		rt.base.name, idle.Minutes(), instanceID)
	if _, err := f.ec2.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{instanceID}}); err != nil {
		log.Printf("ecs-ec2 sweep: stopping %s failed: %v", instanceID, err)
	}
}

// sweepFreeSlots is the sleep test for slots that hold NO home — the half sweepVolume
// structurally cannot do, because it walks homes and a released slot has none
// (docs/log/64 §64.31, ADR 0045 decision 22).
//
// ★ The bug it fixes: Ec2SlotSleepSec promises "a slot with no running task is STOPPED,
// ~$9.6/month instead of ~$95". That was only ever true for a slot with a home still
// attached. The moment releaseSlot ran — an eviction, a size/class change, a Destroy, the
// golden baker finishing with its seed and its probe — the box left every path that could
// stop it and ran until an operator noticed. On the live deployment three of them had been
// up for over a day with zero tasks.
//
// The shape mirrors sweepVolume deliberately: stamp on first sighting, act only on the
// NEXT pass. That is what makes it safe across CP restarts and across the two replicas a
// rolling deploy always overlaps — both read the same tag and reach the same verdict, and
// StopInstances is idempotent, so a double call is a no-op rather than a race.
//
// No warm spare is kept, and that is not an omission (§64.17.2, "no pre-warming").
// A free RUNNING slot buys the next arrival 43s instead of 110s, and costs ~$95/month to
// hold. A free STOPPED one buys almost nothing: waking it and then attaching and mounting
// somebody's home is 123–143s against the 135s of building a box from scratch, because the
// 32s of cold pull it saves is spent again on the boot and the mount SSM round trip. So a
// free slot is stopped after slotSleepAfter and TERMINATED after slotTerminateAfter, and
// the second timer is the one that gives its root volume back (docs/log/64 §64.32).
//
// This walk sees BOTH states for that reason. It used to list only `running` boxes, which
// was consistent while stopping was the last thing that could happen to one — but it means
// a slot leaves this walk forever the moment it is stopped, so the terminate stage would
// never see the boxes it exists for.
func (f *ecsEC2Factory) sweepFreeSlots(ctx context.Context, homes []ec2types.Volume) {
	// No early return when both timers are off: a free slot reserved for replacement is
	// retired whatever they say, because placement skips it (slotsOfMyType) and nothing
	// else would ever give its place under the cap back.
	timersOff := f.pool.slotSleepAfter <= 0 && f.pool.slotTerminateAfter <= 0
	probe := f.probeRuntime()
	busy := map[string]bool{}
	for i := range homes {
		if inst := attachedInstance(&homes[i]); inst != "" {
			busy[inst] = true
		}
		// A slot some other workspace is LAUNCHING onto holds no attachment yet; only the
		// claim says so. Missing it would stop a box mid-Start.
		if inst := ec2TagValue(homes[i].Tags, EC2TagClaim); inst != "" && probe.claimLive(&homes[i]) {
			busy[inst] = true
		}
	}
	// A free slot below $Latest goes the way of a reserved one (#1934). Unreadable template =
	// no slot is called outdated.
	var lt ec2LaunchTemplate
	if !f.pool.noAutoReplace {
		var ltErr error
		if lt, ltErr = f.launchTemplateLatest(ctx); ltErr != nil {
			log.Printf("ecs-ec2 sweep: slot launch template unreadable; outdated free slots are left alone: %v", ltErr)
			lt = ec2LaunchTemplate{}
		}
	}
	out, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, f.pool.pool),
			// af-role=slot excludes quarantined boxes on purpose: quarantineSlot already
			// stopped those, and re-stopping them would only add noise to the log.
			tagFilter(EC2TagRole, ec2RoleSlot),
			{Name: aws.String("instance-state-name"), Values: []string{"running", "stopped"}},
		},
	})
	if err != nil {
		log.Printf("ecs-ec2 sweep: listing free slots: %v", err)
		return
	}
	now := time.Now()
	type candidate struct {
		id        string
		idle      time.Duration
		terminate bool
		reserved  bool
		outdated  bool // below $Latest only; fenced by the sweeper itself before it acts
		orphan    bool // a replacement launched for a home that never claimed it
	}
	var due []candidate
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			id := aws.ToString(inst.InstanceId)
			stamp := ec2TagValue(inst.Tags, ec2TagSlotIdleSince)
			if busy[id] {
				// Occupied. Repair the other direction too — a mark left on a taken slot
				// would otherwise stop it the moment its home is released next time, with
				// no grace at all.
				if stamp != "" {
					probe.clearSlotFree(ctx, id)
				}
				continue
			}
			if f.pool.slotRetiring(inst, lt) {
				// No grace: nobody should be placed on it, so waiting buys nothing. The fences
				// below (fresh occupancy, ECS tasks, task ENIs) still apply.
				due = append(due, candidate{id: id, terminate: true, reserved: slotReserved(inst), outdated: !slotReserved(inst) && !slotRetireTagged(inst)})
				continue
			}
			// A replacement (af-replaces-home) that no home claimed or holds, long after its
			// launch: the Start that asked for it never got the answer (a lost RunInstances
			// response, a CP that died) and nothing will come back for it. Retired whatever
			// the timers say, or it bills and takes a place under the cap for good. Younger
			// than that it may be a Start still in flight, which claimed it already (busy).
			if ec2TagValue(inst.Tags, ec2TagReplacesHome) != "" && inst.LaunchTime != nil &&
				now.Sub(*inst.LaunchTime) > f.orphanReplacementAfter() {
				due = append(due, candidate{id: id, terminate: true, orphan: true})
				continue
			}
			if timersOff {
				continue // see slotSleepAfter / slotTerminateAfter
			}
			at, err := time.Parse(time.RFC3339, stamp)
			if stamp == "" || err != nil {
				// First sighting (or a mark nobody can read): stamp it and let the NEXT
				// sweep decide. releaseSlot normally does this; this is the repair path for
				// a CP that died between the detach and the tag, and for the slots that
				// predate this code — which is exactly what the live deployment had.
				probe.markSlotFree(ctx, id)
				continue
			}
			idle := now.Sub(at)
			running := inst.State != nil && inst.State.Name == ec2types.InstanceStateNameRunning
			switch {
			case f.pool.slotTerminateAfter > 0 && idle >= f.pool.slotTerminateAfter:
				due = append(due, candidate{id: id, idle: idle, terminate: true})
			case running && f.pool.slotSleepAfter > 0 && idle >= f.pool.slotSleepAfter:
				due = append(due, candidate{id: id, idle: idle})
			}
		}
	}
	if len(due) == 0 {
		return
	}
	// A slot that is only below $Latest (nobody reserved it) is reserved HERE, before the
	// occupancy re-read below: a placement that attaches to it checks the reservation before and
	// after the attach (slotNowReserved), so either it sees this tag or the re-read sees it.
	// Without the tag the sweeper and a Start meet with nothing between them, and a home attached
	// after the re-read lives on an instance that is about to be terminated.
	kept := due[:0]
	for _, c := range due {
		if c.outdated || c.orphan {
			if _, err := f.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
				Resources: []string{c.id},
				Tags:      []ec2types.Tag{{Key: aws.String(ec2TagSlotRetire), Value: aws.String(time.Now().UTC().Format(time.RFC3339))}},
			}); err != nil {
				log.Printf("ecs-ec2 sweep: could not fence the outdated free slot %s (%v); leaving it", c.id, err)
				continue
			}
		}
		kept = append(kept, c)
	}
	due = kept
	if len(due) == 0 {
		return
	}
	// Re-read occupancy immediately before acting. `homes` was read at the top of the
	// sweep and a Start takes ~seconds to attach; without this the window between the two
	// is the whole sweep, and stopping a box a launch has just landed on strands that
	// workspace until the release grace expires.
	fresh, err := probe.occupiedInstances(ctx)
	if err != nil {
		log.Printf("ecs-ec2 sweep: re-reading slot occupancy before sleeping free slots: %v", err)
		return // unknown occupancy is never a reason to stop something
	}
	// The ECS side of the same question. A task can be RUNNING on a slot whose home is
	// not in `homes` at all (the baker's probe, or drift), and the instance's own view of
	// its ENIs lags — so both are asked, and either one answering "busy" is enough.
	tasks, err := probe.slotTaskCounts(ctx)
	if err != nil {
		log.Printf("ecs-ec2 sweep: reading task counts before sleeping free slots: %v", err)
		return
	}
	for _, c := range due {
		if fresh[c.id] {
			continue // taken between the two reads
		}
		if tasks[c.id] > 0 {
			log.Printf("ecs-ec2 sweep: free slot %s still runs %d task(s); leaving it", c.id, tasks[c.id])
			continue
		}
		// Never stop a slot that still carries a task ENI — the same guard the occupied
		// path uses, and for the same reason: an instance stopped inside that window comes
		// back MULTI-ENI and silently loses its auto-assigned public IPv4 and its egress
		// (ADR 0045 decision 3-3, reproduced on real hardware). Skipping just means the next
		// sweep stops it.
		if held, err := f.taskENIsAttached(ctx, c.id); err != nil || held {
			if held {
				log.Printf("ecs-ec2 sweep: free slot %s still has a task ENI; leaving it running for now", c.id)
			}
			continue
		}
		if c.terminate {
			// Nothing to release: this walk is BY DEFINITION the slots holding no home, and
			// the three checks above have just re-confirmed it against volumes, ECS tasks
			// and the instance's own ENIs.
			why := fmt.Sprintf("free for %.0fm", c.idle.Minutes())
			if c.reserved {
				why = "free and reserved for replacement"
			} else if c.orphan {
				why = "replacement that no home claimed"
			} else if c.outdated {
				why = "free and below the launch template's $Latest"
			}
			probe.terminateSlot(ctx, c.id, why)
			continue
		}
		log.Printf("ecs-ec2 sweep: slot %s has held no home for %.0fm; stopping it", c.id, c.idle.Minutes())
		if _, err := f.ec2.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{c.id}}); err != nil {
			log.Printf("ecs-ec2 sweep: stopping the free slot %s failed: %v", c.id, err)
		}
	}
}

// terminateSlot ends a dormant box for good: out of the cluster first, then out of EC2.
// It is the only caller of TerminateInstances, and the last step of the dormancy series
// (see slotTerminateAfter).
//
// ⚠️ The ECS half is not optional and not automatic. A terminated instance stays in the
// cluster as ACTIVE with agentConnected=false (ADR 0045 decision 3-2, and observed again on
// the live deployment when the three boxes were terminated by hand) — and a ghost that
// looks ACTIVE still satisfies placement constraints, so ECS can pick it for a task that
// then never starts. sweepGhostInstances is the repair path for boxes that vanished some
// other way; when WE are the one removing it, saying so up front is cheaper and leaves no
// window. Deregistering first also means a failed TerminateInstances degrades into the
// case sweepGhostInstances already handles, rather than into a live box ECS won't use.
//
// The caller owns the safety argument (no tasks, no home, not claimed); this function
// only carries it out. It has two callers with opposite needs — the sweeper, which can
// only log, and TerminateQuarantinedSlot, which answers an operator waiting on an HTTP
// request — so it both logs and returns the failure.
func (e *ecsEC2Runtime) terminateSlot(ctx context.Context, instanceID, why string) error {
	e.deregisterSlot(ctx, instanceID)
	if _, err := e.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	}); err != nil {
		log.Printf("ecs-ec2: terminating slot %s (%s) failed: %v", instanceID, why, err)
		return err
	}
	log.Printf("ecs-ec2: terminated slot %s (%s); its root volume goes with it", instanceID, why)
	return nil
}

// Refusals TerminateQuarantinedSlot hands back to its caller. Exported because the
// distinction they carry — the operator asked for something that cannot be done, as
// opposed to AWS failing — has to survive the trip to the HTTP layer in another package,
// where 404 / 409 and 500 are different answers.
var (
	ErrSlotNotFound       = errors.New("no such slot in this pool")
	ErrSlotNotQuarantined = errors.New("slot is not quarantined")
	ErrSlotInUse          = errors.New("slot still holds a home")
)

// TerminateQuarantinedSlot ends one quarantined box because an operator said so, and is
// the only way a deployment has to be rid of one. quarantineSlot stops rather than
// terminates on purpose (ADR 0045 decision 20 — the evidence is kept), and both sweeper
// walks filter on af-role=slot, so nothing else in the product ever collects an
// af-role=quarantined box: its root volume (SlotRootVolumeGiB, 100 GiB by default) bills
// for as long as the deployment lives, and one more box joins it on every mount failure.
//
// Everything it decides is re-derived from AWS (ADR 0012), and the guards ARE the safety
// argument terminateSlot says its caller owns:
//
//   - the box carries THIS pool's af-pool tag. An id from another deployment (or an
//     engine box, or somebody's EC2 instance) reads as "no such slot" and nothing happens;
//   - its af-role is `quarantined`. af-role=slot is refused rather than handled: a working
//     box leaves through the dormancy series, and an instance id typed into an admin API
//     must not be able to take a live slot — with a running workspace on it — with it;
//   - no home is attached to it, and no live claim points at it. That is the only guard
//     whose absence would cost data rather than money.
//
// Tasks are deliberately NOT a guard. The box abandonLostSlot left running still holds the
// ENI of a task whose workspace has already been moved off it, and that is precisely the
// box an operator comes here to remove; terminateSlot deregisters the container instance
// first, so ECS gives that task up instead of keeping a ghost that satisfies placement.
//
// The quarantine reason is returned rather than just logged: it is the evidence decision 23
// refused to let an automatic sweep destroy, and the caller writes it into the audit log so
// it outlives the box.
func (f *ecsEC2Factory) TerminateQuarantinedSlot(ctx context.Context, instanceID string) (string, error) {
	insts, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		// Filtered rather than looked up by InstanceIds: an unknown id then comes back as
		// an empty answer instead of an AWS API error, and the af-pool filter makes "not
		// ours" and "not there" the same reply — which is the reply this should give.
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, f.pool.pool),
			{Name: aws.String("instance-id"), Values: []string{instanceID}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		return "", err
	}
	var inst *ec2types.Instance
	for _, r := range insts.Reservations {
		for i := range r.Instances {
			if aws.ToString(r.Instances[i].InstanceId) == instanceID {
				inst = &r.Instances[i]
			}
		}
	}
	if inst == nil {
		return "", ErrSlotNotFound
	}
	if ec2TagValue(inst.Tags, EC2TagRole) != ec2RoleQuarantined {
		return "", fmt.Errorf("%w (af-role=%s)", ErrSlotNotQuarantined, ec2TagValue(inst.Tags, EC2TagRole))
	}
	reason := ec2TagValue(inst.Tags, ec2TagQuarantineReason)

	// The homes, read the way every other decision in this file reads them: the volume's
	// attachment is who owns what, and a claim is a placement in flight that has not
	// attached yet. Quarantine detaches the home it failed to mount, so finding one here
	// means that detach did not happen — the one case where terminating costs a person
	// their files rather than the deployment a few dollars.
	vols, err := f.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, f.pool.pool),
			tagFilter(EC2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return "", err
	}
	probe := f.probeRuntime()
	for i := range vols.Volumes {
		v := &vols.Volumes[i]
		id := aws.ToString(v.VolumeId)
		if attachedInstance(v) == instanceID {
			return "", fmt.Errorf("%w: %s is still attached to it", ErrSlotInUse, id)
		}
		if ec2TagValue(v.Tags, EC2TagClaim) == instanceID && probe.claimLive(v) {
			return "", fmt.Errorf("%w: %s is being placed on it", ErrSlotInUse, id)
		}
	}
	if err := probe.terminateSlot(ctx, instanceID, "quarantined, terminated by an operator"); err != nil {
		return reason, err
	}
	return reason, nil
}

// deregisterSlot takes one box out of the ECS cluster by its EC2 id. Force, because the
// point of the call is that nothing may be placed here again — and by the time we ask,
// the box is about to stop existing, so a graceful drain has nothing to drain.
//
// A box the cluster does not know about is not an error: a slot that never finished
// registering is exactly the kind we terminate.
func (e *ecsEC2Runtime) deregisterSlot(ctx context.Context, instanceID string) {
	if err := e.deregisterContainerInstance(ctx, instanceID); err != nil {
		// Not fatal: sweepGhostInstances re-tries this once the EC2 side is gone.
		log.Printf("ecs-ec2 sweep: deregistering %s failed: %v", instanceID, err)
	}
}

// deregisterContainerInstance is deregisterSlot with the failure handed back. Nil also
// means "the cluster has no container instance for this box".
func (e *ecsEC2Runtime) deregisterContainerInstance(ctx context.Context, instanceID string) error {
	arns, err := e.listContainerInstanceARNs(ctx)
	if err != nil || len(arns) == 0 {
		return err
	}
	for _, chunk := range chunkStrings(arns, 100) {
		out, err := e.ci.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
			Cluster:            aws.String(e.base.cfg.cluster),
			ContainerInstances: chunk,
		})
		if err != nil {
			return fmt.Errorf("look up the container instance of %s: %w", instanceID, err)
		}
		for _, ci := range out.ContainerInstances {
			if !isPoolContainerInstance(ci) || aws.ToString(ci.Ec2InstanceId) != instanceID {
				continue
			}
			if _, err := e.ci.DeregisterContainerInstance(ctx, &ecs.DeregisterContainerInstanceInput{
				Cluster:           aws.String(e.base.cfg.cluster),
				ContainerInstance: ci.ContainerInstanceArn,
				Force:             aws.Bool(true),
			}); err != nil {
				return fmt.Errorf("deregister the container instance of %s: %w", instanceID, err)
			}
			return nil
		}
	}
	return nil
}

// slotTaskCounts maps EC2 instance id → tasks ECS believes are on it (running + pending).
// A slot the cluster does not know about is simply absent, which reads as 0 — correct,
// since nothing can be placed on it.
func (e *ecsEC2Runtime) slotTaskCounts(ctx context.Context) (map[string]int, error) {
	arns, err := e.listContainerInstanceARNs(ctx)
	if err != nil || len(arns) == 0 {
		return map[string]int{}, err
	}
	counts := map[string]int{}
	for _, chunk := range chunkStrings(arns, 100) {
		out, err := e.ci.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
			Cluster:            aws.String(e.base.cfg.cluster),
			ContainerInstances: chunk,
		})
		if err != nil {
			return nil, err
		}
		for _, ci := range out.ContainerInstances {
			if !isPoolContainerInstance(ci) {
				continue
			}
			counts[aws.ToString(ci.Ec2InstanceId)] = int(ci.RunningTasksCount + ci.PendingTasksCount)
		}
	}
	return counts, nil
}

// probeRuntime is a throwaway ecsEC2Runtime used purely as the library for pool-wide tag
// queries. It is bound to no workspace, so only the pool-wide calls are valid on it.
func (f *ecsEC2Factory) probeRuntime() *ecsEC2Runtime {
	rt := f.newProbeRuntime()
	if f.bg != nil {
		rt.bg = f.bg
	}
	if f.sleep != nil {
		rt.sleep = f.sleep
	}
	return rt
}

func (f *ecsEC2Factory) newProbeRuntime() *ecsEC2Runtime {
	return &ecsEC2Runtime{
		base: &ecsRuntime{cfg: f.base.cfg, ecs: f.base.ecs}, ci: f.ci, ec2: f.ec2, pool: f.pool,
		bg: backgroundWithin(f.pool.waitBudget), now: time.Now, sleep: sleepCtx,
		// A sibling the sweeps derive from it (siblingFor) releases homes as well.
		leases: f.leases,
	}
}

// runtimeForVolume rebuilds just enough of an ecsEC2Runtime to act on a volume found by
// tag — the sweeper starts from AWS, not from the database, so it can clean up after a
// workspace the CP has not looked at since it restarted.
func (f *ecsEC2Factory) runtimeForVolume(vol *ec2types.Volume) *ecsEC2Runtime {
	membership := ec2TagValue(vol.Tags, EC2TagMembership)
	name := ec2TagValue(vol.Tags, EC2TagWorkspace)
	if membership == "" || name == "" {
		return nil
	}
	rt, ok := f.New(Workspace{ContainerName: name, MembershipID: membership}, SecretKeys{}, nil).(*ecsEC2Runtime)
	if !ok {
		return nil
	}
	return rt
}

// sweepGhostInstances deregisters container instances whose EC2 instance is gone or
// long disconnected. ECS does NOT do this for stopped instances or disconnected agents
// even when they are terminated (ADR 0045 decision 3-2), and the ghosts satisfy placement
// constraints — the scheduler keeps aiming tasks at a box that is not there.
func (f *ecsEC2Factory) sweepGhostInstances(ctx context.Context) error {
	rt := f.probeRuntime()
	arns, err := rt.listContainerInstanceARNs(ctx)
	if err != nil || len(arns) == 0 {
		return err
	}
	for _, chunk := range chunkStrings(arns, 100) {
		out, err := f.ci.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
			Cluster:            aws.String(f.base.cfg.cluster),
			ContainerInstances: chunk,
		})
		if err != nil {
			return err
		}
		for _, ci := range out.ContainerInstances {
			// An engine box's agent disconnects while MI drains it, and its EC2 instance is
			// gone a few minutes later — exactly the shape this sweeper deregisters. Doing
			// that to a box ECS is still tidying up is not the CP's call.
			if !isPoolContainerInstance(ci) {
				continue
			}
			if ci.AgentConnected || aws.ToString(ci.Status) != "ACTIVE" {
				continue
			}
			if ci.RegisteredAt != nil && time.Since(*ci.RegisteredAt) < f.pool.ghostAfter {
				continue
			}
			id := aws.ToString(ci.Ec2InstanceId)
			alive, err := f.instanceAlive(ctx, id)
			if err != nil || alive {
				continue
			}
			log.Printf("ecs-ec2 sweep: deregistering ghost container instance %s (ec2 %s is gone)",
				aws.ToString(ci.ContainerInstanceArn), id)
			if _, err := f.ci.DeregisterContainerInstance(ctx, &ecs.DeregisterContainerInstanceInput{
				Cluster:           aws.String(f.base.cfg.cluster),
				ContainerInstance: ci.ContainerInstanceArn,
				Force:             aws.Bool(true),
			}); err != nil {
				log.Printf("ecs-ec2 sweep: deregistering %s failed: %v", id, err)
			}
		}
	}
	return nil
}

// taskENIsAttached reports whether ECS still has a task network interface on the slot.
// Task ENIs are described with the attachment ARN of the task that owns them, which is
// what distinguishes them from the instance's own primary interface.
func (f *ecsEC2Factory) taskENIsAttached(ctx context.Context, instanceID string) (bool, error) {
	out, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		return false, err
	}
	return instancesHoldTaskENI(out), nil
}

func (f *ecsEC2Factory) instanceAlive(ctx context.Context, instanceID string) (bool, error) {
	out, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		return true, err // unknown: never deregister on a failed lookup
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			if inst.State != nil && inst.State.Name != ec2types.InstanceStateNameTerminated {
				return true, nil
			}
		}
	}
	return false, nil
}
