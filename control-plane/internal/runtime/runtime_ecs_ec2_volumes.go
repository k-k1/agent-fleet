package runtime

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// --- volumes ---

// homeVolume returns this workspace's persistent home volume, or nil. Tag lookup only:
// this is what keeps the adapter stateless (ADR 0012).
func (e *ecsEC2Runtime) homeVolume(ctx context.Context) (*ec2types.Volume, error) {
	out, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagMembership, e.base.membershipID),
			tagFilter(EC2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return nil, err
	}
	for i := range out.Volumes {
		if out.Volumes[i].State == ec2types.VolumeStateDeleting || out.Volumes[i].State == ec2types.VolumeStateDeleted {
			continue
		}
		return &out.Volumes[i], nil
	}
	return nil, nil
}

// createHomeVolume creates the user's home in the given AZ. Tags go in the SAME call
// (TagSpecifications), never as a follow-up CreateTags: an untagged volume is an
// invisible volume — State() would say `none`, the next Start would make another one,
// and the first would be billed forever with nothing pointing at it.
//
// "Create" is really three cases, tried in this order:
//
//  1. this user's home was HIBERNATED (§64.18.3) → rebuild it from that snapshot, so the
//     workspace cannot tell it ever went away;
//  2. a GOLDEN snapshot matches the running image (decision 9) → a new user skips
//     boot-install (48s) and a cold npm cache;
//  3. neither → an empty volume, which is correct, just slow.
//
// The order is the point: the user's own home always wins. Handing somebody the golden
// image when their real home merely failed to be found would be silent data loss.
func (e *ecsEC2Runtime) createHomeVolume(ctx context.Context, az string) (*ec2types.Volume, error) {
	source, err := e.restoreSource(ctx)
	if err != nil {
		return nil, err
	}
	var snapshotID string
	var carried []ec2types.Tag
	if source != nil {
		snapshotID = aws.ToString(source.SnapshotId)
		// The restored home is the one a pending Recreate or Clean home was meant for.
		carried = homeWipeTags(source.Tags)
	}
	restored := snapshotID != ""
	if restored {
		// Two different waits with two different explanations: restoring somebody's
		// hibernated home is not the same as making a new one, and the Console should
		// not call both "starting".
		e.setPhase("home: restoring")
		log.Printf("ecs-ec2: restoring %s from %s", e.base.name, snapshotID)
	} else if golden := e.goldenSnapshot(ctx); golden != "" {
		snapshotID = golden
		e.setPhase("home: creating")
		log.Printf("ecs-ec2: seeding a new home for %s from the golden snapshot %s", e.base.name, golden)
	} else {
		e.setPhase("home: creating")
	}
	var snapshot *string
	if snapshotID != "" {
		snapshot = aws.String(snapshotID)
	}
	tags := e.ownedTags(append([]ec2types.Tag{
		{Key: aws.String(EC2TagMembership), Value: aws.String(e.base.membershipID)},
		{Key: aws.String(EC2TagRole), Value: aws.String(ec2RoleHome)},
		{Key: aws.String(EC2TagWorkspace), Value: aws.String(e.base.name)},
		{Key: aws.String(EC2TagPool), Value: aws.String(e.pool.pool)},
		{Key: aws.String("Name"), Value: aws.String(e.base.name + "-home")},
	}, carried...))
	out, err := e.ec2.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String(az),
		SnapshotId:       snapshot,
		Size:             aws.Int32(e.homeGiB),
		VolumeType:       ec2types.VolumeTypeGp3,
		Encrypted:        aws.Bool(true),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeVolume,
			Tags:         tags,
		}},
	})
	if err != nil {
		return nil, err
	}
	log.Printf("ecs-ec2: created %s (%d GiB, %s) for %s", aws.ToString(out.VolumeId), e.homeGiB, az, e.base.name)
	if restored {
		// ONLY after restoring the user's OWN home. The golden snapshot is shared by every
		// future user and must never be swept up here — deleteHomeSnapshots filters on
		// af-membership + af-role=home and so cannot match it, but the intent is worth
		// stating twice for a call that deletes things.
		//
		// The home is the volume again, so the snapshot is now a stale copy that bills.
		// In the background and only once the volume is usable — and if it fails, the
		// next hibernation drops it as superseded rather than trusting it.
		volID := aws.ToString(out.VolumeId)
		e.bg(ctx, func(ctx context.Context) {
			if err := e.waitVolumeAttachable(ctx, volID); err != nil {
				log.Printf("ecs-ec2: %s restored but not usable yet; keeping %s: %v", volID, snapshotID, err)
				return
			}
			if err := e.deleteHomeSnapshots(ctx); err != nil {
				log.Printf("ecs-ec2: could not drop the restored-from snapshot of %s: %v", e.base.name, err)
			}
		})
	}
	// With the tags: placeHome reads the pending wipe off this value rather than
	// describing a volume that was created a moment ago.
	return &ec2types.Volume{
		VolumeId:         out.VolumeId,
		AvailabilityZone: out.AvailabilityZone,
		State:            out.State,
		Tags:             tags,
	}, nil
}

// ResizeHome grows this member's home to the size their stored disk request resolves to
// (e.homeGiB — already clamped to the tenant cap by the CP before this runtime was
// built). It is the answer to the one thing ADR 0045 decision 4 left with no answer at
// all, "please make my home bigger", whose only previous route was destroy and recreate.
//
// Only one direction acts, and that asymmetry is EBS's, not a policy: a volume grows
// online and cannot shrink by any means. A smaller number is still stored — it is the
// admin's intent and it sizes the NEXT home this member gets — and is reported back as
// HomeResizeShrink so nobody is left believing a disk got smaller.
//
// The work is split at the point where the admin stops waiting:
//
//	synchronous   ModifyVolume. One call. It is the part that costs money and the part
//	              somebody is standing in front of, so its refusal must be visible.
//	background    the filesystem. xfs_growfs cannot run until the modification reaches
//	              `optimizing` — seconds, occasionally minutes — and holding a save open
//	              for that would be the whole latency of the feature. Losing it is
//	              cheap: `af-mount` runs xfs_growfs on every mount, so a filesystem that
//	              does not catch up now catches up at the member's next start.
func (e *ecsEC2Runtime) ResizeHome(ctx context.Context) (HomeResize, error) {
	vol, err := e.homeVolume(ctx)
	if err != nil {
		return HomeResize{}, err
	}
	want := e.homeGiB
	if vol == nil {
		return HomeResize{Outcome: HomeResizeNoHome, ToGiB: want}, nil
	}
	have := aws.ToInt32(vol.Size)
	switch {
	case want == have:
		return HomeResize{Outcome: HomeResizeSame, FromGiB: have, ToGiB: want}, nil
	case want < have:
		return HomeResize{Outcome: HomeResizeShrink, FromGiB: have, ToGiB: want}, nil
	}
	volID := aws.ToString(vol.VolumeId)
	if _, err := e.ec2.ModifyVolume(ctx, &ec2.ModifyVolumeInput{
		VolumeId: aws.String(volID), Size: aws.Int32(want),
	}); err != nil {
		// Reported, not returned. The quota row was written before this ran, and the
		// two refusals that happen in practice — EBS's 6-hour cooldown between two
		// modifications of one volume, and a service quota — are fixed by waiting
		// rather than by pressing save again, so failing the save would roll back the
		// admin's intent over something the save did not get wrong.
		log.Printf("ecs-ec2: could not grow %s (%d -> %d GiB) for %s: %v", volID, have, want, e.base.name, err)
		return HomeResize{Outcome: HomeResizeFailed, FromGiB: have, ToGiB: want, Detail: err.Error()}, nil
	}
	log.Printf("ecs-ec2: growing %s %d -> %d GiB for %s", volID, have, want, e.base.name)
	// A detached home has no mountpoint to stretch a filesystem over, and needs none:
	// af-mount grows it when it is next mounted.
	if inst := attachedInstance(vol); inst != "" {
		e.bg(ctx, func(ctx context.Context) {
			if err := e.growHomeFilesystem(ctx, volID, inst); err != nil {
				log.Printf("ecs-ec2: %s grew but its filesystem did not follow on %s; the next mount will do it: %v",
					volID, inst, err)
			}
		})
	}
	return HomeResize{Outcome: HomeResizeGrowing, FromGiB: have, ToGiB: want}, nil
}

// growHomeFilesystem stretches the mounted filesystem over a volume that has just been
// grown. XFS grows in place and only while MOUNTED, which is why this takes the slot the
// home is attached to rather than the volume alone.
func (e *ecsEC2Runtime) growHomeFilesystem(ctx context.Context, volumeID, instanceID string) error {
	if err := e.waitVolumeModified(ctx, volumeID); err != nil {
		return err
	}
	return e.runOnSlot(ctx, instanceID, "xfs_growfs "+e.homeMountPoint())
}

// waitVolumeModified polls until a ModifyVolume has gone far enough for the GUEST to see
// the new size.
//
// `optimizing` is that point and `completed` is not the bar: optimizing is EBS's
// background re-layout of the volume, which on a large one runs for hours, while the
// capacity is already there and xfs_growfs already works. Waiting for completed would
// turn a 10-second operation into an all-afternoon one for no gain.
func (e *ecsEC2Runtime) waitVolumeModified(ctx context.Context, volumeID string) error {
	for i := 0; ; i++ {
		out, err := e.ec2.DescribeVolumesModifications(ctx, &ec2.DescribeVolumesModificationsInput{
			VolumeIds: []string{volumeID},
		})
		if err == nil && len(out.VolumesModifications) > 0 {
			m := out.VolumesModifications[0]
			switch m.ModificationState {
			case ec2types.VolumeModificationStateOptimizing, ec2types.VolumeModificationStateCompleted:
				return nil
			case ec2types.VolumeModificationStateFailed:
				return fmt.Errorf("volume %s modification failed: %s", volumeID, aws.ToString(m.StatusMessage))
			}
		}
		if i >= 60 {
			return fmt.Errorf("volume %s is still modifying", volumeID)
		}
		if err := e.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// waitVolumeAttachable polls until the volume leaves `creating`. Measured at ~3s for a
// fresh volume (docs/log/64 §64.7), which is why this can sit in the synchronous path.
func (e *ecsEC2Runtime) waitVolumeAttachable(ctx context.Context, volumeID string) error {
	for i := 0; ; i++ {
		out, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{volumeID}})
		if err != nil {
			return err
		}
		if len(out.Volumes) == 0 {
			return fmt.Errorf("volume %s disappeared", volumeID)
		}
		switch out.Volumes[0].State {
		case ec2types.VolumeStateAvailable, ec2types.VolumeStateInUse:
			return nil
		case ec2types.VolumeStateError, ec2types.VolumeStateDeleting, ec2types.VolumeStateDeleted:
			return fmt.Errorf("volume %s is %s", volumeID, out.Volumes[0].State)
		}
		if i >= 30 {
			return fmt.Errorf("volume %s still %s", volumeID, out.Volumes[0].State)
		}
		if err := e.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// attachHomeWithRetry keeps trying while the only problem is that the slot's attachment
// point has not been given back yet.
//
// Measured on real AWS: DetachVolume answers, the volume reads `available`, and an
// attach to the same slot is STILL refused ~7s later with "Attachment point /dev/sdf is
// already in use". Without this retry the next Start reads that as "no free slot" and
// grows the pool — paying for an instance and ~90s to avoid a wait of a few seconds,
// which is precisely the cost the pool exists to avoid. A genuine race with another
// workspace (the device really is taken now) simply exhausts the window and moves on to
// the next candidate.
func (e *ecsEC2Runtime) attachHomeWithRetry(ctx context.Context, volumeID, instanceID string) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = e.attachHome(ctx, volumeID, instanceID); err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), "already in use") {
			return err
		}
		if serr := e.sleep(ctx, 2*time.Second); serr != nil {
			return serr
		}
	}
	return err
}

func (e *ecsEC2Runtime) attachHome(ctx context.Context, volumeID, instanceID string) error {
	_, err := e.ec2.AttachVolume(ctx, &ec2.AttachVolumeInput{
		Device:     aws.String(ec2HomeDevice),
		InstanceId: aws.String(instanceID),
		VolumeId:   aws.String(volumeID),
	})
	return err
}

// claim / unclaim mark the one window where a workspace is starting but has nothing
// attached yet (see EC2TagClaim).
func (e *ecsEC2Runtime) claim(ctx context.Context, volumeID, instanceID string) error {
	_, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{volumeID},
		Tags: []ec2types.Tag{
			{Key: aws.String(EC2TagClaim), Value: aws.String(instanceID)},
			{Key: aws.String(ec2TagClaimAt), Value: aws.String(e.now().UTC().Format(time.RFC3339))},
		},
	})
	return err
}

func (e *ecsEC2Runtime) unclaim(ctx context.Context, volumeID string) {
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{volumeID},
		Tags:      []ec2types.Tag{{Key: aws.String(EC2TagClaim)}, {Key: aws.String(ec2TagClaimAt)}},
	}); err != nil {
		log.Printf("ecs-ec2: clearing the claim on %s failed (sweeper will): %v", volumeID, err)
	}
}

// ownedTags appends `af-tenant` when this workspace knows its tenant slug. Nothing in
// the pool reads it — it exists so the invoice can be grouped by tenant (ADR 0048 decision 3).
// An unknown slug appends nothing rather than an empty value: an empty cost allocation
// tag is a real group in the bill, and "tenant = (blank)" reads like a bug.
func (e *ecsEC2Runtime) ownedTags(tags []ec2types.Tag) []ec2types.Tag {
	if e.base.tenantSlug == "" {
		return tags
	}
	return append(tags, ec2types.Tag{Key: aws.String(EC2TagTenant), Value: aws.String(e.base.tenantSlug)})
}

// tagSlotOwner / untagSlotOwner put this workspace's owner tags on the INSTANCE, and
// take them off again when the slot goes back to the pool.
//
// Why the instance and not just the home volume (docs/log/67 §67.4): measured on the live
// deployment, 91% of the cost that can be attributed to a person at all is the slot's
// instance-hours, and the instance carried af-role/af-pool/af-slot-size but NO
// af-membership. That is correct as pool logic — a slot belongs to nobody until a home
// is attached to it — but it means the cost allocation tag had nothing to attach to.
//
// ⚠️ Neither call may fail a Start or a Stop. These tags are read by the bill and by
// nothing else; the sweeper repairs whatever a crash leaves behind (sweepSlotOwnerTags).
// A pool that stops working because a billing tag could not be written would be a far
// worse bug than a mis-attributed hour.
//
// ⚠️ Untag on release is what keeps a WARM POOL slot out of somebody's bill. Without it
// the last user of a box keeps paying for it while it sits idle waiting for the next
// person — and "idle pool" is exactly the shared cost the operator needs to see as
// shared (ADR 0048 decision 4).
func (e *ecsEC2Runtime) tagSlotOwner(ctx context.Context, instanceID string) {
	if instanceID == "" {
		return
	}
	tags := e.ownedTags([]ec2types.Tag{
		{Key: aws.String(EC2TagMembership), Value: aws.String(e.base.membershipID)},
	})
	if _, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{instanceID},
		Tags:      tags,
	}); err != nil {
		log.Printf("ecs-ec2: stamping the owner of slot %s failed (billing only): %v", instanceID, err)
	}
}

func (e *ecsEC2Runtime) untagSlotOwner(ctx context.Context, instanceID string) {
	if instanceID == "" {
		return
	}
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{instanceID},
		Tags: []ec2types.Tag{
			{Key: aws.String(EC2TagMembership)},
			{Key: aws.String(EC2TagTenant)},
		},
	}); err != nil {
		log.Printf("ecs-ec2: clearing the owner of slot %s failed (sweeper will): %v", instanceID, err)
	}
}

// markSlotFree / clearSlotFree move a SLOT in and out of the free set's dormancy clock,
// exactly as markIdle / clearIdle do for a home. They are the instance-side pair because
// a free slot has no volume to carry the mark (see ec2TagSlotIdleSince).
//
// ⚠️ Best-effort like the owner tags, and for the same reason: neither a Start nor a Stop
// may fail because a bookkeeping tag could not be written. The sweeper re-stamps a missing
// mark on its next pass, which costs one sweep of extra running time and nothing else.
func (e *ecsEC2Runtime) markSlotFree(ctx context.Context, instanceID string) {
	if instanceID == "" {
		return
	}
	if _, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{instanceID},
		Tags: []ec2types.Tag{{
			Key: aws.String(ec2TagSlotIdleSince), Value: aws.String(e.now().UTC().Format(time.RFC3339)),
		}},
	}); err != nil {
		log.Printf("ecs-ec2: marking slot %s free failed (the sweeper will stamp it): %v", instanceID, err)
	}
}

// clearSlotFree is called when a workspace takes the slot. Without it a box that is used
// for less than one sweep interval keeps the mark from BEFORE that tenancy, and the next
// release would look older than it is — the slot would sleep with no grace at all.
func (e *ecsEC2Runtime) clearSlotFree(ctx context.Context, instanceID string) {
	if instanceID == "" {
		return
	}
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{instanceID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagSlotIdleSince)}},
	}); err != nil {
		log.Printf("ecs-ec2: clearing the free mark on slot %s failed (sweeper will): %v", instanceID, err)
	}
}

// claimLive reports whether the volume carries a claim that has not expired. The TTL
// matters: a claim that outlives its failed launch pins the workspace at `starting`
// forever, and Start returns early on `starting`, so the user could never recover.
func (e *ecsEC2Runtime) claimLive(vol *ec2types.Volume) bool {
	if ec2TagValue(vol.Tags, EC2TagClaim) == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339, ec2TagValue(vol.Tags, ec2TagClaimAt))
	if err != nil {
		return false
	}
	return e.now().Sub(at) < e.pool.claimTTL
}
