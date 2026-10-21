package runtime

// Reserving a slot for replacement (#1473).
//
// The ECS agent reads a slot's user data only when the slot is launched, so a change to the
// slot launch template reaches a retained slot only by replacing it — and nothing replaces a
// slot that has a home on it, because a Stop → Start goes back to the same box. A reservation
// is the operator's way to say "this box may not run anybody again": a tag on the instance,
// acted on at its workspace's next Start (the "mark now, act at the next Start" pattern of
// ADR 0045 decision 32). Nobody's running session is touched, and the home is never deleted.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

const (
	// ec2TagSlotReplace on a SLOT reserves it for replacement; the value is when. It is on
	// the instance rather than the home because the reservation is about the box: whoever's
	// home is on it at the next Start is the one that moves.
	ec2TagSlotReplace = "af-slot-replace"
	// ec2TagReplacesHome on a SLOT names the home volume it was launched to replace a
	// reserved slot for. It is written by RunInstances itself, so a replacement whose
	// launch answered but whose Start never got further (a CP that died, a lost response)
	// can still be found and reused instead of launching another box over the cap.
	ec2TagReplacesHome = "af-replaces-home"
	// ec2TagSlotRetire on a SLOT is the sweeper's own fence while it retires a free slot below
	// $Latest: placement skips the slot like a reserved one, but it is NOT a reservation — the
	// member sees no pill, and a Start that won the race and lives on it replaces it as an
	// automatic move (capacity fallback included). Kept apart from ec2TagSlotReplace so a
	// sweeper's decision can never read as an operator's.
	ec2TagSlotRetire = "af-slot-retire"
	// EC2 stamps these on every instance launched from a launch template, with the version
	// NUMBER it resolved — never the literal "$Latest" the launch asked for.
	ec2TagLaunchTemplateID      = "aws:ec2launchtemplate:id"
	ec2TagLaunchTemplateVersion = "aws:ec2launchtemplate:version"
	// ec2PhaseSlotRenewing is the Start phase while a reserved slot is being replaced. Not
	// "slot: replacing": that one is the lost-slot recovery, a different story for the member.
	ec2PhaseSlotRenewing = "slot: renewing"
	// autoReplaceBackoff is how long a failed automatic replacement leaves the workspace on
	// its old slot before the next Start tries to launch again. A Start that cannot get a box
	// would otherwise pay the failed RunInstances (and its wait) every time.
	autoReplaceBackoff = 10 * time.Minute
)

// errAutoReplaceDeferred means an AUTOMATIC replacement could not get a new slot and touched
// nothing: placeHome carries on with the slot the home is already on and the next Start tries
// again. Never returned for a reservation, whose rule is that the Start fails instead.
var errAutoReplaceDeferred = errors.New("automatic slot replacement deferred")

// autoReplaceBlocked remembers, per workspace, the last failed automatic replacement.
var autoReplaceBlocked = &TTLCache{m: map[string]TTLEntry{}}

// SlotAutoReplace is what an automatic replacement did, for the audit log (the adapter has
// no database; Config.OnSlotAutoReplace hands it to the CP).
type SlotAutoReplace struct {
	TenantID, WorkspaceID, Workspace string
	OldSlot, NewSlot                 string
	OldVersion, Latest               string
}

// Refusals of a reservation, kept apart from AWS failures for the HTTP layer (404/409 vs 500).
var (
	ErrSlotQuarantined = errors.New("slot is quarantined; it runs nobody and is removed by terminating it")
	ErrSlotNotOutdated = errors.New("slot is not on an older launch template version")
)

// slotRetiring reports whether the sweeper and the replacement logic treat inst as on its way
// out: an operator reserved it, or automatic replacement is on and it is below the launch
// template's $Latest. A slot whose version cannot be judged is never retiring.
func (p ec2PoolConfig) slotRetiring(inst ec2types.Instance, lt ec2LaunchTemplate) bool {
	return slotExcluded(inst) || p.slotOutdated(inst, lt)
}

// slotRetireTagged reports whether the sweeper fenced this slot off (ec2TagSlotRetire).
func slotRetireTagged(inst ec2types.Instance) bool {
	return ec2TagValue(inst.Tags, ec2TagSlotRetire) != ""
}

// slotExcluded is what placement must not hand to anybody new: reserved by an operator, or
// fenced by the sweeper.
func slotExcluded(inst ec2types.Instance) bool {
	return slotReserved(inst) || slotRetireTagged(inst)
}

// slotOutdated is the automatic half of slotRetiring.
func (p ec2PoolConfig) slotOutdated(inst ec2types.Instance, lt ec2LaunchTemplate) bool {
	if p.noAutoReplace {
		return false
	}
	_, outdated, known := slotTemplateOutdated(inst.Tags, lt)
	return known && outdated
}

// slotReserved reports whether an instance carries a replacement reservation.
func slotReserved(inst ec2types.Instance) bool {
	return ec2TagValue(inst.Tags, ec2TagSlotReplace) != ""
}

// withoutReservedSlots drops reserved slots from a DescribeInstances answer.
func withoutReservedSlots(out *ec2.DescribeInstancesOutput) *ec2.DescribeInstancesOutput {
	kept := &ec2.DescribeInstancesOutput{}
	for _, r := range out.Reservations {
		var insts []ec2types.Instance
		for _, inst := range r.Instances {
			if !slotExcluded(inst) {
				insts = append(insts, inst)
			}
		}
		if len(insts) > 0 {
			r.Instances = insts
			kept.Reservations = append(kept.Reservations, r)
		}
	}
	return kept
}

// replaceReservedSlot is placeHome's branch for a home whose slot is reserved: move the home
// onto a new slot of this workspace's class, launched from the template's $Latest, and retire
// the old box.
//
// ⚠️ The ORDER is the safety argument:
//
//  1. Get the new slot FIRST. Capacity and quota are the likely failures, and at this point
//     nothing has been touched: the Start fails with the reason, the home stays on the
//     reserved slot and the reservation stays. Falling back to the reserved slot instead
//     would defeat the reservation, whose usual reason is security.
//  2. Claim the home for the new slot AT ONCE. The claim is what makes occupiedInstances
//     count the new box as taken, so no other Start attaches to it while this one waits
//     for the old home's release — without it another member's home could land there and
//     this one would be left with no slot at all. It also records the new box on the home,
//     which is how a retry after a CP crash finds it again (adoptableReplacement).
//  3. Release the home from the old slot with releaseSlot — umount before detach, refused
//     while a task runs, fenced on the Start generation — and confirm it really is off.
//     If that fails, the new box is terminated (it holds nothing but this claim) and the
//     claim dropped, so the pool is back to where it was, under its cap, and the next Start
//     simply tries again.
//  4. Terminate the old box, but only after re-reading that nothing holds it. A failure here
//     costs money, not data: the box is free and still reserved, so nobody is placed on it
//     and the sweeper retires it.
//
// The pool cap is checked with the reserved box not counted, since it is on its way out:
// otherwise a full pool could never replace anything.
//
// automatic is the same move for a slot that is merely below the template's $Latest (nobody
// reserved it). Steps 2–4 are identical; only step 1 differs: a new slot that cannot be had
// returns errAutoReplaceDeferred with nothing touched, so a member's Start is not failed
// for a hardening nobody asked them about. The explicit-reservation rule above is unchanged.
func (e *ecsEC2Runtime) replaceReservedSlot(ctx context.Context, vol *ec2types.Volume, oldID string, automatic bool) (ec2Placement, error) {
	volID := aws.ToString(vol.VolumeId)
	az := aws.ToString(vol.AvailabilityZone)
	if automatic {
		if autoReplaceBlocked.get(e.base.name, autoReplaceBackoff, func() string { return "" }) != "" {
			return ec2Placement{}, errAutoReplaceDeferred
		}
		log.Printf("ecs-ec2: slot %s under %s is below the launch template's $Latest; moving the home to a new %s",
			oldID, e.base.name, e.instanceType)
		// The member gets the ordinary "getting a machine ready" line: "retired by an
		// administrator" would be false, and the WS bar pill (SlotReplacePending) stays off.
		e.setPhase("slot: creating")
	} else {
		log.Printf("ecs-ec2: slot %s under %s is reserved for replacement; moving the home to a new %s",
			oldID, e.base.name, e.instanceType)
		e.setPhase(ec2PhaseSlotRenewing)
	}
	newID, wake := "", false
	// An earlier replacement is only worth reusing if it is still what a launch now would
	// give: the reservation's usual reason is a template change, and a box from the version
	// before it would defeat that. lt is read here, authoritatively, every time.
	lt, ltErr := describeLaunchTemplate(ctx, e.ec2, e.pool.launchTemplate)
	if ltErr != nil {
		log.Printf("ecs-ec2: reading the slot launch template before reusing a replacement: %v", ltErr)
	}
	adoptEarlier := func() {
		for _, prev := range e.earlierReplacements(ctx, vol, oldID) {
			running, verdict := e.adoptableReplacement(ctx, prev, az, volID, lt)
			switch verdict {
			case replacementAdopt:
				if newID == "" {
					log.Printf("ecs-ec2: reusing %s, the replacement an earlier start of %s launched", prev, e.base.name)
					newID, wake = prev, !running
				}
			case replacementRetire:
				// Launched from an older template and never used: give its place under the cap
				// back so the launch below can have it.
				_ = e.terminateSlot(ctx, prev, "unused replacement for "+e.base.name+" from an older launch template")
			}
		}
	}
	adoptEarlier()
	if newID == "" {
		// An EBS volume never leaves its AZ, so the new slot has to be in the home's.
		id, err := e.runSlot(ctx, az, e.pool.maxSlots+1, volID)
		if err != nil {
			if automatic {
				// A launch whose answer was lost may still have produced a box (tagged
				// af-replaces-home at launch). Falling back to the old slot would leave it with
				// no Start to collect it, so use it now instead of orphaning it.
				adoptEarlier()
			}
			switch {
			case newID != "":
			case automatic:
				e.deferAutoReplace(oldID, err)
				return ec2Placement{}, errAutoReplaceDeferred
			default:
				return ec2Placement{}, fmt.Errorf("slot %s is reserved for replacement and no new slot could be launched "+
					"(the reservation stays; the old slot is not reused): %w", oldID, err)
			}
		} else {
			newID = id
		}
	}
	if err := e.claim(ctx, volID, newID); err != nil {
		e.retireUnusedReplacement(ctx, newID, volID)
		if automatic {
			e.deferAutoReplace(oldID, err)
			return ec2Placement{}, errAutoReplaceDeferred
		}
		return ec2Placement{}, fmt.Errorf("claim %s for the replacement slot %s: %w", volID, newID, err)
	}
	// The new slot may have been fenced (the sweeper retiring an orphan) or reserved between
	// its adoption and the claim above. The claim now protects it from the sweeper's occupancy
	// re-read, but a fence that landed first is a decision to terminate it: stop BEFORE the
	// home leaves the old slot. Unreadable counts as fenced.
	if e.slotNowReserved(ctx, newID) {
		e.retireUnusedReplacement(ctx, newID, volID)
		if automatic {
			e.deferAutoReplace(oldID, errors.New("the replacement slot was fenced during the move"))
			return ec2Placement{}, errAutoReplaceDeferred
		}
		return ec2Placement{}, fmt.Errorf("the replacement slot %s was reserved during the move (the reservation stays): try again", newID)
	}
	if err := e.moveHomeOff(ctx, oldID); err != nil {
		e.retireUnusedReplacement(ctx, newID, volID)
		return ec2Placement{}, fmt.Errorf("move the home off the reserved slot %s (the reservation stays): %w", oldID, err)
	}
	if holder, err := e.slotHolder(ctx, oldID, ""); err != nil || holder != "" {
		log.Printf("ecs-ec2: not terminating the reserved slot %s yet (held by %q, %v); the sweeper retires it once free",
			oldID, holder, err)
	} else {
		_ = e.terminateSlot(ctx, oldID, "reserved for replacement, replaced by "+newID)
	}
	e.clearDormancy(ctx, volID)
	slotReplaceSeen.set(e.base.name, "")
	if automatic {
		e.recordAutoReplace(ctx, oldID, newID)
	}
	return ec2Placement{volumeID: volID, instanceID: newID, az: az, deferred: true, claimed: true, wake: wake,
		wipe: homeWipeOf(vol), replacement: true}, nil
}

// deferAutoReplace backs the workspace off the replacement for autoReplaceBackoff.
func (e *ecsEC2Runtime) deferAutoReplace(oldID string, cause error) {
	log.Printf("ecs-ec2: could not replace the outdated slot %s for %s (%v); staying on it and retrying after %s",
		oldID, e.base.name, cause, autoReplaceBackoff)
	autoReplaceBlocked.set(e.base.name, "1")
}

// recordAutoReplace hands the finished move to the CP's audit log (best effort: the move is
// done whether or not the record lands).
func (e *ecsEC2Runtime) recordAutoReplace(ctx context.Context, oldID, newID string) {
	if e.onAutoReplace == nil {
		return
	}
	ev := SlotAutoReplace{TenantID: e.tenantID, WorkspaceID: e.workspaceID, Workspace: e.base.name, OldSlot: oldID, NewSlot: newID}
	if out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{oldID}}); err == nil {
		for _, r := range out.Reservations {
			for _, i := range r.Instances {
				ev.OldVersion = ec2TagValue(i.Tags, ec2TagLaunchTemplateVersion)
			}
		}
	}
	if lt, err := describeLaunchTemplate(ctx, e.ec2, e.pool.launchTemplate); err == nil {
		ev.Latest = strconv.FormatInt(lt.latest, 10)
	}
	e.onAutoReplace(context.WithoutCancel(ctx), ev)
}

// moveHomeOff releases this workspace's home from oldID and confirms it is detached.
// releaseSlot can return nil with the home still on the box (a Start that raced it re-mounts
// instead of detaching), and claiming a new slot for a home that never left the old one would
// strand the Start.
func (e *ecsEC2Runtime) moveHomeOff(ctx context.Context, oldID string) error {
	if err := e.releaseSlot(ctx); err != nil {
		return err
	}
	vol, err := e.homeVolume(ctx)
	if err != nil {
		return err
	}
	if vol != nil && attachedInstance(vol) == oldID {
		return fmt.Errorf("the home is still attached to %s after the release", oldID)
	}
	return nil
}

// retireUnusedReplacement undoes steps 1 and 2 of replaceReservedSlot after a later step
// failed: terminate the new box — it holds nothing but this home's claim — or, if that
// fails, reserve it so nobody is placed on it and the sweeper retires it.
//
// ⚠️ The claim is dropped ONLY once one of those is confirmed. Until then it is what keeps
// other Starts off the box and what points this home's next Start at it; dropping it on an
// unknown outcome (an unreadable occupancy, both writes failing) left a box nothing would
// ever collect, holding the one place under the cap the retry needs. Kept, the workspace
// reads `starting` until the claim expires (claimTTL), and the next Start then adopts the
// box (adoptableReplacement). The cleanup runs on a context the request cannot cancel: a
// client that hung up must not turn a recoverable failure into a stranded box.
func (e *ecsEC2Runtime) retireUnusedReplacement(ctx context.Context, newID, volID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	holder, err := e.slotHolder(ctx, newID, volID)
	switch {
	case err != nil:
		log.Printf("ecs-ec2: cannot tell whether the replacement slot %s is free (%v); keeping %s's claim on it", newID, err, volID)
		return
	case holder != "":
		// Somebody else's home is on it, so it is theirs now and not ours to retire.
		log.Printf("ecs-ec2: the replacement slot %s holds %s; leaving it", newID, holder)
		e.unclaim(ctx, volID)
		return
	}
	if err := e.terminateSlot(ctx, newID, "replacement for "+e.base.name+" not used"); err == nil {
		e.unclaim(ctx, volID)
		return
	}
	if _, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{newID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagSlotReplace), Value: aws.String(e.now().UTC().Format(time.RFC3339))}},
	}); err != nil {
		log.Printf("ecs-ec2: could not retire or reserve the unused replacement slot %s (%v); keeping %s's claim on it", newID, err, volID)
		return
	}
	e.unclaim(ctx, volID)
}

// earlierReplacements lists the slots an earlier attempt to replace oldID for this home may
// have left: the one its claim names, and every live slot launched for it (ec2TagReplacesHome)
// — the second covers a launch whose claim was never written. adoptableReplacement decides.
func (e *ecsEC2Runtime) earlierReplacements(ctx context.Context, vol *ec2types.Volume, oldID string) []string {
	volID := aws.ToString(vol.VolumeId)
	var ids []string
	seen := map[string]bool{oldID: true, "": true}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	add(ec2TagValue(vol.Tags, EC2TagClaim))
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, e.pool.pool),
			tagFilter(ec2TagReplacesHome, volID),
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopped"}},
		},
	})
	if err != nil {
		log.Printf("ecs-ec2: looking for an earlier replacement for %s: %v", volID, err)
		return ids
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			add(aws.ToString(inst.InstanceId))
		}
	}
	return ids
}

type replacementVerdict int

const (
	replacementSkip   replacementVerdict = iota // not ours to touch, or cannot be judged
	replacementAdopt                            // reuse it
	replacementRetire                           // ours and unused, but from an older template
)

// adoptableReplacement judges id — a slot an earlier attempt for this home may have
// launched. It is reused only if it is a slot of this pool and class in the home's AZ, not
// reserved, holding nobody's home or claim but this one's, AND launched from the template's
// current $Latest (slotTemplateOutdated against lt, read by the caller). A slot that passes
// everything but the template is retired instead; one whose version cannot be judged is
// left alone — it is neither reused nor destroyed on a guess.
func (e *ecsEC2Runtime) adoptableReplacement(ctx context.Context, id, az, volID string, lt ec2LaunchTemplate) (running bool, verdict replacementVerdict) {
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return false, replacementSkip
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			if ec2TagValue(inst.Tags, EC2TagPool) != e.pool.pool || ec2TagValue(inst.Tags, EC2TagRole) != ec2RoleSlot ||
				slotExcluded(inst) || string(inst.InstanceType) != e.instanceType || inst.State == nil ||
				inst.Placement == nil || aws.ToString(inst.Placement.AvailabilityZone) != az {
				return false, replacementSkip
			}
			switch inst.State.Name {
			case ec2types.InstanceStateNamePending, ec2types.InstanceStateNameRunning, ec2types.InstanceStateNameStopped:
			default:
				return false, replacementSkip
			}
			if holder, err := e.slotHolder(ctx, id, volID); err != nil || holder != "" {
				return false, replacementSkip
			}
			_, outdated, known := slotTemplateOutdated(inst.Tags, lt)
			switch {
			case !known:
				return false, replacementSkip
			case outdated:
				return false, replacementRetire
			}
			return inst.State.Name != ec2types.InstanceStateNameStopped, replacementAdopt
		}
	}
	return false, replacementSkip
}

// pendingReplacementsForOthers names the slots in out that were launched to replace a
// reserved slot for ANOTHER workspace's home and are still waiting for it: the home exists
// and is still attached to a reserved slot elsewhere. Those are spoken for even when the
// home's claim was never written or has expired — otherwise another member takes the box
// and the owner's retry finds the pool full for good.
//
// The link expires by itself: once that home has moved (onto this slot or anywhere else),
// is detached, or is gone, the slot is an ordinary one again, so a stale tag can never hold
// a box out of the pool. Unreadable answers count as pending: the cost is a slot skipped.
func (e *ecsEC2Runtime) pendingReplacementsForOthers(ctx context.Context, out *ec2.DescribeInstancesOutput) map[string]bool {
	pending := map[string]bool{}
	byHome := map[string][]string{}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			if v := ec2TagValue(inst.Tags, ec2TagReplacesHome); v != "" {
				byHome[v] = append(byHome[v], aws.ToString(inst.InstanceId))
			}
		}
	}
	if len(byHome) == 0 {
		return pending
	}
	all := func() map[string]bool {
		for _, ids := range byHome {
			for _, id := range ids {
				pending[id] = true
			}
		}
		return pending
	}
	homes := make([]string, 0, len(byHome))
	for v := range byHome {
		homes = append(homes, v)
	}
	vols, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, e.pool.pool),
			{Name: aws.String("volume-id"), Values: homes},
		},
	})
	if err != nil {
		return all()
	}
	// home volume id → the slot it is still on, for homes of other workspaces only.
	on := map[string]string{}
	for i := range vols.Volumes {
		v := &vols.Volumes[i]
		if ec2TagValue(v.Tags, EC2TagWorkspace) == e.base.name {
			continue
		}
		if inst := attachedInstance(v); inst != "" {
			on[aws.ToString(v.VolumeId)] = inst
		}
	}
	if len(on) == 0 {
		return pending
	}
	olds := make([]string, 0, len(on))
	for _, inst := range on {
		olds = append(olds, inst)
	}
	insts, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: olds})
	if err != nil {
		return all()
	}
	// Reserved, or below $Latest with automatic replacement on: either way the owner's next
	// Start moves, so a replacement launched for it is spoken for. Unlike the rest of the
	// automatic logic, a template that cannot be read counts as "retiring" here: the cost of
	// guessing wrong is a slot skipped, the alternative a replacement handed to someone else.
	var lt ec2LaunchTemplate
	ltUnknown := false
	if !e.pool.noAutoReplace {
		var ltErr error
		if lt, ltErr = describeLaunchTemplate(ctx, e.ec2, e.pool.launchTemplate); ltErr != nil {
			lt, ltUnknown = ec2LaunchTemplate{}, true
		}
	}
	reserved := map[string]bool{}
	for _, r := range insts.Reservations {
		for _, inst := range r.Instances {
			if ltUnknown || e.pool.slotRetiring(inst, lt) {
				reserved[aws.ToString(inst.InstanceId)] = true
			}
		}
	}
	for v, ids := range byHome {
		old, ok := on[v]
		if !ok || !reserved[old] {
			continue
		}
		for _, id := range ids {
			if id != old {
				pending[id] = true
			}
		}
	}
	return pending
}

// withoutSlots drops the named instances from a DescribeInstances answer.
func withoutSlots(out *ec2.DescribeInstancesOutput, drop map[string]bool) *ec2.DescribeInstancesOutput {
	if len(drop) == 0 {
		return out
	}
	kept := &ec2.DescribeInstancesOutput{}
	for _, r := range out.Reservations {
		var insts []ec2types.Instance
		for _, inst := range r.Instances {
			if !drop[aws.ToString(inst.InstanceId)] {
				insts = append(insts, inst)
			}
		}
		if len(insts) > 0 {
			r.Instances = insts
			kept.Reservations = append(kept.Reservations, r)
		}
	}
	return kept
}

// clearReplacesHome drops a replacement's af-replaces-home link once the home is running on
// it. Best-effort: a stale link expires by itself (pendingReplacementsForOthers).
func (e *ecsEC2Runtime) clearReplacesHome(ctx context.Context, instanceID string) {
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{instanceID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagReplacesHome)}},
	}); err != nil {
		log.Printf("ecs-ec2: clearing the replacement link on %s failed (it expires by itself): %v", instanceID, err)
	}
}

// slotNowReserved re-reads a placement candidate's reservation immediately before and after
// the attach. Candidate lists are read earlier, so a reservation can land in between.
//
// Why two reads close the race: ReserveSlotReplacement writes its tag and only THEN reads who
// holds the slot. A placement whose second read missed the tag attached and claimed before
// the tag was written, so the reservation's own read sees that home, names it in the audit
// log, and it moves at its next Start. Either the placement sees the reservation, or the
// reservation sees the placement. An unreadable answer counts as reserved: the cost is one
// candidate skipped, the alternative a member placed on a box an operator wants gone.
func (e *ecsEC2Runtime) slotNowReserved(ctx context.Context, id string) bool {
	return e.slotTagged(ctx, id, slotExcluded)
}

// slotNowReservedByOperator is slotNowReserved for an operator's reservation alone — the
// sweeper's fence (ec2TagSlotRetire) does not forbid the automatic fallback.
func (e *ecsEC2Runtime) slotNowReservedByOperator(ctx context.Context, id string) bool {
	return e.slotTagged(ctx, id, slotReserved)
}

func (e *ecsEC2Runtime) slotTagged(ctx context.Context, id string, tagged func(ec2types.Instance) bool) bool {
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return true
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			return tagged(inst)
		}
	}
	return true
}

// backOffReservedSlot takes a home off a slot it was just attached to — before anything was
// mounted — because the slot turned out to be reserved.
func (e *ecsEC2Runtime) backOffReservedSlot(ctx context.Context, volID, instID string) error {
	log.Printf("ecs-ec2 start: slot %s was reserved for replacement while %s was being placed on it; stepping off", instID, volID)
	if _, err := e.ec2.DetachVolume(ctx, &ec2.DetachVolumeInput{VolumeId: aws.String(volID), InstanceId: aws.String(instID)}); err != nil {
		e.unclaim(ctx, volID)
		return fmt.Errorf("detach %s from the reserved slot %s: %w", volID, instID, err)
	}
	err := e.waitDetached(ctx, volID)
	e.unclaim(ctx, volID)
	return err
}

// slotHolder names the home attached to the instance, or placed on it under a live claim, or
// "" when it holds none. except is a home whose own claim on the box does not count. It is
// the guard that makes terminating a box cost money rather than somebody's files.
func (e *ecsEC2Runtime) slotHolder(ctx context.Context, instanceID, except string) (string, error) {
	vols, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, e.pool.pool),
			tagFilter(EC2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return "", err
	}
	for i := range vols.Volumes {
		v := &vols.Volumes[i]
		id := aws.ToString(v.VolumeId)
		if attachedInstance(v) == instanceID {
			return id, nil
		}
		if id != except && ec2TagValue(v.Tags, EC2TagClaim) == instanceID && e.claimLive(v) {
			return id, nil
		}
	}
	return "", nil
}

// --- the member's side ---

// slotReplaceSeen memoizes SlotReplacePending per workspace name. /api/workspace is polled
// every few seconds per open Console, and the answer costs two AWS reads; a reservation is
// acted on at the next Start, so a minute of lag in announcing it changes nothing.
var slotReplaceSeen = &TTLCache{m: map[string]TTLEntry{}}

const slotReplaceSeenTTL = time.Minute

// SlotReplacePending reports whether this workspace's next Start moves it to a new slot,
// because the slot its home is on is reserved for replacement. When in doubt, false: the
// answer only adds a line to the member's WS bar.
func (e *ecsEC2Runtime) SlotReplacePending(ctx context.Context) bool {
	return slotReplaceSeen.get(e.base.name, slotReplaceSeenTTL, func() string {
		vol, err := e.homeVolume(ctx)
		if err != nil || vol == nil {
			return ""
		}
		inst := attachedInstance(vol)
		if inst == "" {
			return ""
		}
		out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{inst}})
		if err != nil {
			return ""
		}
		for _, r := range out.Reservations {
			for _, i := range r.Instances {
				if slotReserved(i) {
					return "1"
				}
			}
		}
		return ""
	}) != ""
}

// --- the operator's side ---

// ec2LaunchTemplate is what the slots are compared against: the template the CP launches
// from, and its $Latest — the version runSlot asks for (launchTemplateSpec).
type ec2LaunchTemplate struct {
	id     string
	latest int64
}

func (f *ecsEC2Factory) launchTemplateLatest(ctx context.Context) (ec2LaunchTemplate, error) {
	return describeLaunchTemplate(ctx, f.ec2, f.pool.launchTemplate)
}

// describeLaunchTemplate reads the slot launch template ref (an id or a name) and its $Latest.
func describeLaunchTemplate(ctx context.Context, api ec2API, ref string) (ec2LaunchTemplate, error) {
	in := &ec2.DescribeLaunchTemplatesInput{}
	spec := launchTemplateSpec(ref)
	if spec.LaunchTemplateId != nil {
		in.LaunchTemplateIds = []string{aws.ToString(spec.LaunchTemplateId)}
	} else {
		in.LaunchTemplateNames = []string{aws.ToString(spec.LaunchTemplateName)}
	}
	out, err := api.DescribeLaunchTemplates(ctx, in)
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	if len(out.LaunchTemplates) == 0 {
		return ec2LaunchTemplate{}, fmt.Errorf("launch template %s not found", ref)
	}
	lt := out.LaunchTemplates[0]
	return ec2LaunchTemplate{id: aws.ToString(lt.LaunchTemplateId), latest: aws.ToInt64(lt.LatestVersionNumber)}, nil
}

// slotTemplateOutdated compares a slot's launch template stamp with the template's $Latest.
// known=false when either side cannot be read — a missing template id, a version that is not
// a positive integer — and such a slot is never called outdated, so the bulk reservation
// cannot sweep up boxes nobody can account for. Both halves of the stamp are validated before
// either is compared.
//
//   - a slot launched from a DIFFERENT template (the pool stack replaced it) is outdated:
//     a version number means nothing across templates;
//   - otherwise outdated means its number is below $Latest. $Default plays no part: the CP
//     launches with $Latest, so that is what a new slot gets.
func slotTemplateOutdated(tags []ec2types.Tag, lt ec2LaunchTemplate) (version string, outdated, known bool) {
	version = ec2TagValue(tags, ec2TagLaunchTemplateVersion)
	id := ec2TagValue(tags, ec2TagLaunchTemplateID)
	if lt.id == "" || lt.latest <= 0 || id == "" {
		return version, false, false
	}
	v, err := strconv.ParseInt(version, 10, 64)
	if err != nil || v <= 0 {
		return version, false, false
	}
	if id != lt.id {
		return version, true, true
	}
	return version, v < lt.latest, true
}

// SlotReservation is what a reservation acted on, for the reply and the audit log.
type SlotReservation struct {
	InstanceID      string `json:"instance_id"`
	Workspace       string `json:"workspace"` // the occupant whose next Start moves; "" = free
	TemplateVersion string `json:"template_version"`
	TemplateLatest  string `json:"template_latest"`
	Reserved        bool   `json:"reserved"`
}

// ReserveSlotReplacement sets (reserve=true) or clears a slot's replacement reservation.
// onlyOutdated is the bulk path's re-check: the slot must still be below $Latest when the
// write happens, whatever the screen showed when the operator confirmed.
//
// Everything is re-read from AWS (ADR 0012), and the guards mirror TerminateQuarantinedSlot:
// the box must carry this pool's af-pool tag (anything else reads as "no such slot"), and a
// quarantined box is refused — it already runs nobody, and terminating it is how it goes.
func (f *ecsEC2Factory) ReserveSlotReplacement(ctx context.Context, instanceID string, reserve, onlyOutdated bool) (SlotReservation, error) {
	res := SlotReservation{InstanceID: instanceID}
	insts, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, f.pool.pool),
			{Name: aws.String("instance-id"), Values: []string{instanceID}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		return res, err
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
		return res, ErrSlotNotFound
	}
	switch ec2TagValue(inst.Tags, EC2TagRole) {
	case ec2RoleSlot:
	case ec2RoleQuarantined:
		return res, ErrSlotQuarantined
	default:
		return res, ErrSlotNotFound
	}
	lt, ltErr := f.launchTemplateLatest(ctx)
	if ltErr != nil {
		log.Printf("ecs-ec2: reading the slot launch template for a reservation: %v", ltErr)
	} else {
		res.TemplateLatest = strconv.FormatInt(lt.latest, 10)
	}
	var outdated bool
	res.TemplateVersion, outdated, _ = slotTemplateOutdated(inst.Tags, lt)
	if reserve && onlyOutdated && !outdated {
		if ltErr != nil {
			return res, fmt.Errorf("%w (the launch template could not be read: %v)", ErrSlotNotOutdated, ltErr)
		}
		return res, ErrSlotNotOutdated
	}
	if reserve {
		_, err = f.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
			Resources: []string{instanceID},
			Tags:      []ec2types.Tag{{Key: aws.String(ec2TagSlotReplace), Value: aws.String(time.Now().UTC().Format(time.RFC3339))}},
		})
	} else {
		_, err = f.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
			Resources: []string{instanceID},
			Tags:      []ec2types.Tag{{Key: aws.String(ec2TagSlotReplace)}},
		})
	}
	if err != nil {
		return res, err
	}
	// Read the occupant AFTER the write, never before: a placement that attached before the
	// tag existed is then seen here and named in the audit log, which is the other half of
	// slotNowReserved's argument.
	probe := f.probeRuntime()
	if holder, err := probe.slotHolder(ctx, instanceID, ""); err == nil && holder != "" {
		vols, err := f.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{holder}})
		if err == nil && len(vols.Volumes) > 0 {
			res.Workspace = ec2TagValue(vols.Volumes[0].Tags, EC2TagWorkspace)
		}
	}
	res.Reserved = reserve
	// The member's badge reads through a one-minute memo; a reservation just made should not
	// wait it out.
	slotReplaceSeen.reset()
	log.Printf("ecs-ec2: slot %s (template version %s, $Latest %s, occupant %q) reserved for replacement: %v",
		instanceID, res.TemplateVersion, res.TemplateLatest, res.Workspace, reserve)
	return res, nil
}
