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
	// EC2 stamps these on every instance launched from a launch template, with the version
	// NUMBER it resolved — never the literal "$Latest" the launch asked for.
	ec2TagLaunchTemplateID      = "aws:ec2launchtemplate:id"
	ec2TagLaunchTemplateVersion = "aws:ec2launchtemplate:version"
	// ec2PhaseSlotRenewing is the Start phase while a reserved slot is being replaced. Not
	// "slot: replacing": that one is the lost-slot recovery, a different story for the member.
	ec2PhaseSlotRenewing = "slot: renewing"
)

// Refusals of a reservation, kept apart from AWS failures for the HTTP layer (404/409 vs 500).
var (
	ErrSlotQuarantined = errors.New("slot is quarantined; it runs nobody and is removed by terminating it")
	ErrSlotNotOutdated = errors.New("slot is not on an older launch template version")
)

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
			if !slotReserved(inst) {
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
func (e *ecsEC2Runtime) replaceReservedSlot(ctx context.Context, vol *ec2types.Volume, oldID string) (ec2Placement, error) {
	volID := aws.ToString(vol.VolumeId)
	az := aws.ToString(vol.AvailabilityZone)
	log.Printf("ecs-ec2: slot %s under %s is reserved for replacement; moving the home to a new %s",
		oldID, e.base.name, e.instanceType)
	e.setPhase(ec2PhaseSlotRenewing)
	newID, wake := "", false
	if prev := ec2TagValue(vol.Tags, EC2TagClaim); prev != "" && prev != oldID {
		if running, ok := e.adoptableReplacement(ctx, prev, az, volID); ok {
			log.Printf("ecs-ec2: reusing %s, the replacement an earlier start of %s launched", prev, e.base.name)
			newID, wake = prev, !running
		}
	}
	if newID == "" {
		// An EBS volume never leaves its AZ, so the new slot has to be in the home's.
		id, err := e.runSlot(ctx, az, e.pool.maxSlots+1)
		if err != nil {
			return ec2Placement{}, fmt.Errorf("slot %s is reserved for replacement and no new slot could be launched "+
				"(the reservation stays; the old slot is not reused): %w", oldID, err)
		}
		newID = id
	}
	if err := e.claim(ctx, volID, newID); err != nil {
		e.retireUnusedReplacement(ctx, newID, volID)
		return ec2Placement{}, fmt.Errorf("claim %s for the replacement slot %s: %w", volID, newID, err)
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
	return ec2Placement{volumeID: volID, instanceID: newID, az: az, deferred: true, claimed: true, wake: wake, wipe: homeWipeOf(vol)}, nil
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

// retireUnusedReplacement undoes step 1 and 2 of replaceReservedSlot after a later step
// failed: terminate the new box — it holds nothing but this home's claim — and then drop the
// claim, in that order so the box is never unprotected while it exists. If the terminate
// fails the box is reserved instead, so nobody is placed on it and the sweeper retires it.
func (e *ecsEC2Runtime) retireUnusedReplacement(ctx context.Context, newID, volID string) {
	defer e.unclaim(ctx, volID)
	if holder, err := e.slotHolder(ctx, newID, volID); err != nil || holder != "" {
		log.Printf("ecs-ec2: leaving the replacement slot %s alone (held by %q, %v)", newID, holder, err)
		return
	}
	if err := e.terminateSlot(ctx, newID, "replacement for "+e.base.name+" not used"); err == nil {
		return
	}
	if _, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{newID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagSlotReplace), Value: aws.String(e.now().UTC().Format(time.RFC3339))}},
	}); err != nil {
		log.Printf("ecs-ec2: could not reserve the unused replacement slot %s either: %v", newID, err)
	}
}

// adoptableReplacement reports whether id — the slot this home's last claim named — is a
// replacement an earlier start launched and never used: a slot of this pool and class in
// the home's AZ, not reserved, and holding nobody's home or claim but this one's.
func (e *ecsEC2Runtime) adoptableReplacement(ctx context.Context, id, az, volID string) (running, ok bool) {
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return false, false
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			if ec2TagValue(inst.Tags, EC2TagPool) != e.pool.pool || ec2TagValue(inst.Tags, EC2TagRole) != ec2RoleSlot ||
				slotReserved(inst) || string(inst.InstanceType) != e.instanceType || inst.State == nil ||
				inst.Placement == nil || aws.ToString(inst.Placement.AvailabilityZone) != az {
				return false, false
			}
			switch inst.State.Name {
			case ec2types.InstanceStateNamePending, ec2types.InstanceStateNameRunning, ec2types.InstanceStateNameStopped:
			default:
				return false, false
			}
			if holder, err := e.slotHolder(ctx, id, volID); err != nil || holder != "" {
				return false, false
			}
			return inst.State.Name != ec2types.InstanceStateNameStopped, true
		}
	}
	return false, false
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
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return true
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			return slotReserved(inst)
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
	in := &ec2.DescribeLaunchTemplatesInput{}
	spec := launchTemplateSpec(f.pool.launchTemplate)
	if spec.LaunchTemplateId != nil {
		in.LaunchTemplateIds = []string{aws.ToString(spec.LaunchTemplateId)}
	} else {
		in.LaunchTemplateNames = []string{aws.ToString(spec.LaunchTemplateName)}
	}
	out, err := f.ec2.DescribeLaunchTemplates(ctx, in)
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	if len(out.LaunchTemplates) == 0 {
		return ec2LaunchTemplate{}, fmt.Errorf("launch template %s not found", f.pool.launchTemplate)
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
