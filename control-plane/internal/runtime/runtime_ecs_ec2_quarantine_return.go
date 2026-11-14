package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Refusals ReturnQuarantinedSlot adds to the ones TerminateQuarantinedSlot exports.
var (
	// ErrSlotQuarantineToken: the token the caller sent is not an af-quarantine value.
	ErrSlotQuarantineToken = errors.New("not a quarantine token")
	// ErrSlotQuarantineChanged: the slot carries quarantine records, but none with exactly the
	// token the operator saw. It was re-quarantined or already returned since the screen loaded.
	ErrSlotQuarantineChanged = errors.New("the quarantine on this slot is not the one you saw; reload the pool view")
	// ErrSlotRoleQuarantined: af-role=quarantined is Terminate's kind of quarantine. It keeps
	// the evidence and is never returned to the pool.
	ErrSlotRoleQuarantined = errors.New("slot is quarantined by role (af-role=quarantined); it is terminated, not returned")
	// ErrSlotPlacing: a Start holds a live claim on the home that sits on the slot.
	ErrSlotPlacing = errors.New("a start is placing the home on this slot; try again in a moment")
	// ErrSlotBusy: the home's claim lock, or the instance's lease, was not had in time.
	ErrSlotBusy = errors.New("another operation holds the slot's lock; try again")
)

// SlotReturn is what ReturnQuarantinedSlot reports, and what the audit row carries.
type SlotReturn struct {
	InstanceID string `json:"instance_id"`
	// Workspace is the occupant whose home was on the slot when it was returned, "" = none.
	Workspace string `json:"workspace,omitempty"`
	// Reason is the quarantine reason, kept because the record is deleted.
	Reason string `json:"quarantine_reason"`
	// CountBumped: the home's Start count was moved on, which voids a late write of the
	// same token and stops a rescue that still holds it from writing it again.
	CountBumped bool `json:"count_bumped"`
	// OtherRecords is how many other af-quarantine-* records the slot still carries. Void ones
	// are cleared by the sweeper; one in force (a slot with no home) keeps the slot held.
	OtherRecords int `json:"other_records"`
}

// ReturnQuarantinedSlot returns a rung-L2 slot to the pool: it deletes the af-quarantine-*
// record whose value is exactly token, the one the pool view showed. The role is `slot`
// throughout; role-quarantined boxes are Terminate's and are refused.
//
// The delete is fenced the way every other writer of these records is:
//
//   - A home of the token's volume is on the slot: the claim lock of that home's workspace is
//     taken, so no Start counts and no rescue writes meanwhile. A live claim refuses
//     (ErrSlotPlacing). When the record is in force (its count is the home's current Start
//     count) the count is bumped AFTER the delete: a rescue that still holds the old token, or
//     a CreateTags already on its way, then reads as void instead of re-holding the slot. The
//     bump is after, not before, so the carry of a bump finds no record to copy forward.
//   - No home of that token is on the slot: the per-instance lease is taken, the one the
//     finalizers of af-wedge-at use. There is no count to move; a late write of the token holds
//     an idle slot, which the guide's "delete it again" covers.
//
// The delete matches the value, so a record a re-quarantine wrote under a new token in between
// survives and the call answers ErrSlotQuarantineChanged. The af-wedge-at-* history stays: the
// next dark episode inside the window quarantines the slot again.
//
// Only tag deletes on a slot of this pool, the same call clearVoidQuarantine makes.
func (f *ecsEC2Factory) ReturnQuarantinedSlot(ctx context.Context, instanceID, token string) (SlotReturn, error) {
	res := SlotReturn{InstanceID: instanceID}
	want, ok := parseQuarantineTag(token)
	if !ok {
		return res, ErrSlotQuarantineToken
	}
	key := quarantineKey(want.Volume, want.Gen)
	probe := f.probeRuntime()
	inst, err := probe.slotInstance(ctx, instanceID)
	if err != nil {
		return res, err
	}
	if err := checkReturnable(inst, key, token); err != nil {
		return res, err
	}
	res.Reason = want.Reason

	holder, err := probe.slotHolder(ctx, instanceID, "")
	if err != nil {
		return res, fmt.Errorf("find the home on %s: %w", instanceID, err)
	}
	if holder != "" && holder == want.Volume {
		vols, err := f.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{holder}})
		if err != nil {
			return res, fmt.Errorf("read the home %s: %w", holder, err)
		}
		if len(vols.Volumes) > 0 {
			if rt := f.rescueRuntime(&vols.Volumes[0]); rt != nil {
				res.Workspace = ec2TagValue(vols.Volumes[0].Tags, EC2TagWorkspace)
				return res, rt.returnUnderClaim(ctx, instanceID, want, key, token, &res)
			}
		}
		return res, fmt.Errorf("no runtime for the home %s", holder)
	}
	return res, probe.returnUnderInstanceLease(ctx, instanceID, want, key, token, &res)
}

// checkReturnable is the refusals that depend only on what the box carries.
func checkReturnable(inst *ec2types.Instance, key, token string) error {
	if inst == nil {
		return ErrSlotNotFound
	}
	switch ec2TagValue(inst.Tags, EC2TagRole) {
	case ec2RoleSlot:
	case ec2RoleQuarantined:
		return ErrSlotRoleQuarantined
	default:
		return ErrSlotNotFound // an engine box or something else in the pool tag
	}
	if ec2TagValue(inst.Tags, key) == token {
		return nil
	}
	if len(quarantineRecordsOf(inst.Tags)) == 0 {
		return fmt.Errorf("%w (no af-quarantine record on it)", ErrSlotNotQuarantined)
	}
	return ErrSlotQuarantineChanged
}

// returnUnderClaim is the claim-lock path: the home named by the token is on the slot.
func (e *ecsEC2Runtime) returnUnderClaim(ctx context.Context, instanceID string, want quarantineTag, key, token string, res *SlotReturn) error {
	lctx, unlock, err := e.lockBounded(ctx, homeLockClaim, e.base.name, rescueClaimBudget)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSlotBusy, err)
	}
	defer unlock()
	// Everything is read again under the lock: the world the first read described may have moved.
	g, err := e.startGenNow(lctx)
	if err != nil {
		return fmt.Errorf("read the start count of %s: %w", e.base.name, err)
	}
	vol, err := e.homeVolume(lctx)
	if err != nil {
		return fmt.Errorf("read the home of %s: %w", e.base.name, err)
	}
	onBox := vol != nil && aws.ToString(vol.VolumeId) == want.Volume && attachedInstance(vol) == instanceID
	if onBox && e.claimLive(vol) {
		return ErrSlotPlacing
	}
	inForce := onBox && g == want.Gen
	if res.OtherRecords, err = e.deleteQuarantineRecord(lctx, instanceID, key, token); err != nil {
		return err
	}
	log.Printf("ecs-ec2: RETURNED slot %s to the pool: deleted %s (in force=%v)", instanceID, key, inForce)
	if !inForce {
		return nil
	}
	if _, err := e.bumpStartCount(lctx); err != nil {
		// The slot is already returned; the bump only voids a late write of the same token.
		log.Printf("ecs-ec2: returned slot %s but could not move the start count of %s on: %v; if the quarantine reappears, return it again", instanceID, e.base.name, err)
		return nil
	}
	res.CountBumped = true
	return nil
}

// returnUnderInstanceLease is the path for a slot with no home of the token's volume on it.
func (e *ecsEC2Runtime) returnUnderInstanceLease(ctx context.Context, instanceID string, want quarantineTag, key, token string, res *SlotReturn) error {
	lctx, unlock, err := e.lockBounded(ctx, homeLockWedge, instanceID, rescueWedgeBudget)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSlotBusy, err)
	}
	defer unlock()
	// A home of the token's volume that arrived since needs its claim lock, not this lease: refuse.
	if holder, err := e.slotHolder(lctx, instanceID, ""); err != nil {
		return fmt.Errorf("find the home on %s: %w", instanceID, err)
	} else if holder != "" && holder == want.Volume {
		return fmt.Errorf("%w: its home arrived on the slot", ErrSlotBusy)
	}
	if res.OtherRecords, err = e.deleteQuarantineRecord(lctx, instanceID, key, token); err != nil {
		return err
	}
	log.Printf("ecs-ec2: RETURNED slot %s to the pool: deleted %s (no home of that token on it)", instanceID, key)
	return nil
}

// deleteQuarantineRecord re-reads the slot under the caller's lock and deletes the one record,
// with its value, so only that exact token goes. It returns how many other records remain.
func (e *ecsEC2Runtime) deleteQuarantineRecord(ctx context.Context, instanceID, key, token string) (int, error) {
	inst, err := e.slotInstance(ctx, instanceID)
	if err != nil {
		return 0, err
	}
	if err := checkReturnable(inst, key, token); err != nil {
		return 0, err
	}
	if err := homeGuardOf(ctx).check(); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrSlotBusy, err)
	}
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{instanceID},
		Tags:      []ec2types.Tag{{Key: aws.String(key), Value: aws.String(token)}},
	}); err != nil {
		return 0, fmt.Errorf("delete %s on %s: %w", key, instanceID, err)
	}
	return len(quarantineRecordsOf(inst.Tags)) - 1, nil
}
