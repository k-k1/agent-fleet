package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
)

// --- release / destroy ---

// releaseSlot is the teardown half of a Stop, and the same routine the sweeper runs on
// anything it finds drifting: wait for the task to be gone, unmount, detach, hand the
// slot back to the pool. Idempotent — every step is a no-op when it has already
// happened, because the sweeper WILL run it again.
func (e *ecsEC2Runtime) releaseSlot(ctx context.Context) error {
	gen, err := e.startGenNow(ctx)
	if err != nil {
		return fmt.Errorf("read the start count of %s: %w", e.base.name, err)
	}
	return e.releaseSlotSince(ctx, gen)
}

// releaseSlotSince is releaseSlot anchored to a Start count taken by the CALLER — see
// startGen for why the anchor cannot be taken here.
func (e *ecsEC2Runtime) releaseSlotSince(ctx context.Context, gen int64) error {
	vol, err := e.homeVolume(ctx)
	if err != nil {
		return err
	}
	if vol == nil {
		return nil
	}
	instanceID := attachedInstance(vol)
	if instanceID == "" {
		return nil
	}
	if err := e.waitTasksGone(ctx); err != nil {
		return err
	}
	if moved, err := e.startedSince(ctx, gen); err != nil {
		return err
	} else if moved {
		return fmt.Errorf("%s was started again while releasing its slot; leaving it attached", e.base.name)
	}
	// umount first, ALWAYS — while the slot is running. A detach of a mounted filesystem
	// is how a home gets corrupted (ADR 0045 decision 8, price 3), so a failure here must stop
	// the detach.
	//
	// A STOPPED slot is the exception, and it has to be: SSM cannot reach it, and there
	// is nothing to unmount — the instance stop is an ordinary shutdown, which unmounts
	// filesystems on the way down. Waiting for an umount that can never run would leave
	// dormant slots unreclaimable.
	//
	// From the umount to the detach no mount of this home may run, or the detach pulls a
	// filesystem the umount never saw (homeMountLocks). A launch waiting on the lock, on
	// this replica or another, finds the home gone once it gets it and fails without
	// touching the slot. Everything below runs on lctx: a lease lost to another CP stops
	// it before the detach.
	lctx, unlock, err := e.lockHome(ctx, homeLockMount)
	if err != nil {
		return err
	}
	defer unlock()
	outer := ctx
	ctx = lctx
	running, err := e.instanceRunning(ctx, instanceID)
	if err != nil {
		return err
	}
	if running {
		if err := e.umountHome(ctx, instanceID); err != nil {
			return fmt.Errorf("umount %s on %s: %w", e.homeMountPoint(), instanceID, err)
		}
	}
	volumeID := aws.ToString(vol.VolumeId)
	moved, err := e.startedSince(ctx, gen)
	if err != nil {
		// The home is unmounted and stays attached: a Start mounts it, a later release
		// detaches it.
		return homeGuardOf(ctx).outcome(err)
	}
	if moved {
		// Re-mount rather than detach: the workspace is coming up and needs its home.
		log.Printf("ecs-ec2: %s restarted mid-release; re-mounting instead of detaching", e.base.name)
		return e.mountHomeLocked(ctx, ec2Placement{volumeID: volumeID, instanceID: instanceID})
	}
	// Asked here, not left to the context: its end is a timer's callback, which a goroutine
	// resumed past the deadline can outrun, and a detach after the lease is gone is the one
	// step that must not happen.
	if err := homeGuardOf(ctx).check(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := e.ec2.DetachVolume(ctx, &ec2.DetachVolumeInput{
		VolumeId:   aws.String(volumeID),
		InstanceId: aws.String(instanceID),
	}); err != nil {
		return fmt.Errorf("detach %s: %w", volumeID, err)
	}
	// The home is off the slot: what follows is bookkeeping, owed even if the lease is
	// lost now.
	ctx = outer
	// Do not call the slot free until it really is: the attachment point outlives the
	// DetachVolume response (see attachHomeWithRetry), and a Start that lands in that
	// window would grow the pool instead of swapping onto this slot.
	if err := e.waitDeviceFree(ctx, instanceID); err != nil {
		log.Printf("ecs-ec2: %s detached from %s but the device is still held: %v", volumeID, instanceID, err)
	}
	e.clearIdle(ctx, volumeID)
	// The box is back in the pool, so it stops being this person's cost from here on —
	// and starts its OWN dormancy clock, which is what eventually stops it. Before this
	// existed a released slot simply left every path that could ever stop it (§64.31).
	e.untagSlotOwner(ctx, instanceID)
	e.markSlotFree(ctx, instanceID)
	log.Printf("ecs-ec2: released slot %s from %s", instanceID, e.base.name)
	return nil
}

// waitDeviceFree polls until the slot no longer lists the home device. The instance view
// is the conservative one here — it keeps reporting the device while the volume side
// already says `available` — which makes it the right thing to wait on.
func (e *ecsEC2Runtime) waitDeviceFree(ctx context.Context, instanceID string) error {
	started := e.now()
	for i := 0; i < 30; i++ {
		out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
		if err != nil {
			return err
		}
		held := false
		for _, r := range out.Reservations {
			for _, inst := range r.Instances {
				for _, m := range inst.BlockDeviceMappings {
					if aws.ToString(m.DeviceName) == ec2HomeDevice {
						held = true
					}
				}
			}
		}
		if !held {
			if d := e.now().Sub(started); d > time.Second {
				log.Printf("ecs-ec2: slot %s gave back %s after %.0fs", instanceID, ec2HomeDevice, d.Seconds())
			}
			return nil
		}
		if err := e.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("slot %s still holds %s", instanceID, ec2HomeDevice)
}

// waitDetached blocks until the volume has really left the slot. DetachVolume ANSWERS
// while the volume is still `detaching`, so anything that acts on the volume next — a
// DeleteVolume, an attach elsewhere — has to wait for this or it fails with VolumeInUse
// (measured).
func (e *ecsEC2Runtime) waitDetached(ctx context.Context, volumeID string) error {
	for {
		out, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{volumeID}})
		if err != nil {
			return err
		}
		if len(out.Volumes) == 0 || attachedInstance(&out.Volumes[0]) == "" {
			return nil
		}
		if err := e.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// waitTasksGone waits until the service actually has no task left. Detaching while the
// container still holds the mount is the failure mode this exists to prevent.
func (e *ecsEC2Runtime) waitTasksGone(ctx context.Context) error {
	for {
		s, ok, err := e.base.describeService(ctx)
		if err != nil {
			return err
		}
		if !ok || (s.RunningCount == 0 && s.PendingCount == 0) {
			return nil
		}
		if s.DesiredCount > 0 {
			return fmt.Errorf("service %s is at desired %d; not releasing its slot", e.base.name, s.DesiredCount)
		}
		if err := e.sleep(ctx, 3*time.Second); err != nil {
			return err
		}
	}
}

// Destroy releases the slot, deletes the user's home volume, and then hands the shared
// (Fargate-side) resources to the base adapter. The slot itself is NOT terminated: it
// never belonged to this user.
//
// ⚠️ P0 left the second half out — it deleted the EBS volume but kept the ECS service,
// the two EFS access points and the two SSM parameters alive forever (ADR 0045 decision 13,
// docs/log/64 §64.18.1). The order matters: the slot has to come back to the pool and the
// volume has to be detached before the service goes away, because releaseSlot reads the
// service to prove nothing is running on it.
//
// Ordering rationale for the volume vs its snapshot: destroy is irreversible on purpose,
// so any hibernation snapshot (§64.18.2) goes too — otherwise a "deleted" user keeps
// billing at snapshot rates and their home is restorable by the next person who gets
// their membership id.
func (e *ecsEC2Runtime) Destroy(ctx context.Context) ([]string, error) {
	if err := e.Stop(ctx); err != nil {
		return nil, err
	}
	if err := e.releaseSlot(ctx); err != nil {
		return nil, err
	}
	if err := e.deleteHomeVolume(ctx); err != nil {
		return nil, err
	}
	if err := e.deleteHomeSnapshots(ctx); err != nil {
		return nil, err
	}
	// Backups are a different role and so are invisible to every other cleanup here.
	// Without this they would outlive the person and bill forever — the exact thing
	// Destroy exists to stop (docs/log/64 §64.18.1).
	if _, err := e.deleteBackups(ctx); err != nil {
		return nil, err
	}
	return e.base.Destroy(ctx)
}

// EraseHome is an administrator's Clean home on the slot pool: the home volume and its
// hibernation snapshots are deleted, and the next start builds a fresh home the way a new
// member's is built (from the golden snapshot, or empty).
//
// Deleting the volume rather than emptying it is what lets this happen now. Emptying needs
// the filesystem mounted, and a stopped workspace's home is usually on a slot that has
// gone to sleep (SSM cannot reach it), detached, or already a hibernation snapshot.
// DeleteVolume works in every one of those states, and takes the same time whatever the
// home holds.
//
// It is still Clean home and not Destroy. The seven homeKeep entries live on EFS
// (ADR 0045 decision 3-6), and so does the Claude state, so the member's logins,
// connections and identity survive. They survive as of the workspace's last start: a keep
// file that a tool replaced since then, instead of writing through its link, sits on the
// volume until the entrypoint moves it back at the next boot, and goes with the volume.
//
// The hibernation snapshots have to go as well, because createHomeVolume restores the
// member's own snapshot before anything else; one left behind would hand the erased home
// back at the next start. Backups stay: they are copies outside the home, and
// DeleteHomeBackups is the separate step that removes them.
func (e *ecsEC2Runtime) EraseHome(ctx context.Context) error {
	if err := e.Stop(ctx); err != nil {
		return err
	}
	// releaseSlot waits for the task to be gone and refuses while the service wants one,
	// so a workspace somebody started in the meantime keeps its home.
	if err := e.releaseSlot(ctx); err != nil {
		return err
	}
	// Which volumes this erase makes history of: the live one, and the ones the listed
	// hibernation snapshots were taken from (a hibernated home has no live volume).
	vol, err := e.homeVolume(ctx)
	if err != nil {
		return err
	}
	snaps, err := e.homeSnapshots(ctx)
	if err != nil {
		return err
	}
	var candidates []string
	if vol != nil {
		candidates = append(candidates, aws.ToString(vol.VolumeId))
	}
	for _, sn := range snaps {
		if v := aws.ToString(sn.VolumeId); v != "" {
			candidates = append(candidates, v)
		}
	}
	apID, rec, err := e.ensureKeepAccessPoint(ctx)
	if err != nil {
		return fmt.Errorf("keep access point of %s: %w", e.base.name, err)
	}
	// First, before anything is destroyed: these volumes are being erased. While any erase
	// is pending no restore runs (restoreSnapshot), so from here on a failure can never
	// hand the home back; and if this cannot be written, nothing is destroyed.
	added := rec.markPending(candidates)
	if len(added) > 0 {
		if err := e.writeEraseRecord(ctx, apID, rec); err != nil {
			return err
		}
	}
	if err := e.deleteHomeVolume(ctx); err != nil {
		// A DeleteVolume that answered an error may still have taken effect. A home that
		// is really still there was not erased, so what this call marked is taken back —
		// otherwise its legitimate hibernation copies would stay blocked.
		if still, lerr := e.homeVolume(ctx); lerr != nil || still != nil {
			rec.unmarkPending(added)
			if werr := e.writeEraseRecord(ctx, apID, rec); werr != nil {
				return fmt.Errorf("%w (and the pending erase could not be taken back, so this home cannot be restored from a hibernation until its owner starts it or Clean home runs again: %v)", err, werr)
			}
			return err
		}
	}
	// The live volume is gone — a member has one — so every pending erase is now done:
	// this call's, and any that an earlier attempt left.
	rec.confirmPending()
	recordErr := e.writeEraseRecord(ctx, apID, rec)
	// No capture of the volume can start once it is gone. One that started just before —
	// the pool sweeper advances a hibernation without the lifecycle lease this erase
	// holds — can be missing from an eventually consistent listing, so look twice. The
	// listings are cleanup; the record is what keeps such a copy from ever coming back.
	if err := e.deleteHomeSnapshots(ctx); err != nil {
		return errors.Join(recordErr, err)
	}
	if err := e.sleep(ctx, snapshotListingSettle); err != nil {
		return errors.Join(recordErr, err)
	}
	return errors.Join(recordErr, e.deleteHomeSnapshots(ctx))
}

// eraseRecord is efsTagErasedVolumes read back. Each word is either a volume id — a home
// that was erased — or "pending:<volume id>", an erase that has started and not finished.
// A pending word is written before the erase destroys anything and becomes a plain id once
// the volume is gone, so a failure at any point leaves either nothing destroyed or a
// record that blocks the restore.
type eraseRecord struct {
	erased  []string
	pending []string
}

const erasePendingPrefix = "pending:"

func parseEraseRecord(v string) (eraseRecord, error) {
	var r eraseRecord
	for _, w := range strings.Fields(v) {
		id, pending := strings.CutPrefix(w, erasePendingPrefix)
		if !strings.HasPrefix(id, "vol-") {
			return eraseRecord{}, fmt.Errorf("unreadable word %q", w)
		}
		if pending {
			r.pending = appendNew(r.pending, id)
		} else {
			r.erased = appendNew(r.erased, id)
		}
	}
	return r, nil
}

func (r eraseRecord) isErased(v string) bool { return slices.Contains(r.erased, v) }

func (r eraseRecord) String() string {
	words := append([]string(nil), r.erased...)
	for _, v := range r.pending {
		words = append(words, erasePendingPrefix+v)
	}
	return strings.Join(words, " ")
}

// markPending marks ids pending and returns the ones it added.
func (r *eraseRecord) markPending(ids []string) []string {
	var added []string
	for _, v := range ids {
		if v != "" && !slices.Contains(r.pending, v) {
			r.pending = append(r.pending, v)
			added = append(added, v)
		}
	}
	return added
}

func (r *eraseRecord) unmarkPending(ids []string) {
	r.pending = slices.DeleteFunc(r.pending, func(v string) bool { return slices.Contains(ids, v) })
}

func (r *eraseRecord) confirmPending() {
	for _, v := range r.pending {
		r.erased = appendNew(r.erased, v)
	}
	r.pending = nil
}

func appendNew(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

// efsTagValueMax is the longest value an EFS tag may hold.
const efsTagValueMax = 256

// writeEraseRecord stores rec on the keep access point. No id is ever dropped to make
// room: that no copy of an erased volume is left cannot be told from an eventually
// consistent listing, and a forgotten id makes such a copy restorable. A record that does
// not fit fails the write instead — about ten volumes, and the pending mark is written
// before anything is destroyed, so a full record refuses the erase. A transient refusal
// is retried within the caller's budget.
func (e *ecsEC2Runtime) writeEraseRecord(ctx context.Context, apID string, rec eraseRecord) error {
	if n := len(rec.String()); n > efsTagValueMax {
		return fmt.Errorf("the record of erased homes of %s is full (%d volumes need %d of %d characters) and never forgets one; Destroy removes this home together with the record",
			e.base.name, len(rec.erased)+len(rec.pending), n, efsTagValueMax)
	}
	delay := 2 * time.Second
	for attempt := 1; ; attempt++ {
		_, err := e.base.efs.TagResource(ctx, &efs.TagResourceInput{
			ResourceId: aws.String(apID),
			Tags:       []efstypes.Tag{{Key: aws.String(efsTagErasedVolumes), Value: aws.String(rec.String())}},
		})
		if err == nil {
			return nil
		}
		if attempt >= 6 {
			return fmt.Errorf("write the erase record of %s: %w", e.base.name, err)
		}
		if serr := e.sleep(ctx, delay); serr != nil {
			return fmt.Errorf("write the erase record of %s: %w", e.base.name, err)
		}
		delay *= 2
	}
}

// eraseRecord reads the record; empty when no home of this member was ever erased.
func (e *ecsEC2Runtime) eraseRecord(ctx context.Context) (eraseRecord, error) {
	ap, err := e.keepAccessPoint(ctx)
	if err != nil || ap == nil {
		return eraseRecord{}, err
	}
	return e.parseKeepRecord(ap)
}

func (e *ecsEC2Runtime) parseKeepRecord(ap *efstypes.AccessPointDescription) (eraseRecord, error) {
	rec, err := parseEraseRecord(tagValue(ap.Tags, efsTagErasedVolumes))
	if err != nil {
		// Unreadable is not "nothing erased": restoring an erased home is the one outcome
		// this record exists to prevent, so the Start fails instead.
		return eraseRecord{}, fmt.Errorf("%s on the keep access point of %s: %w", efsTagErasedVolumes, e.base.name, err)
	}
	return rec, nil
}

// ensureKeepAccessPoint is ensureAccessPoint for the keep role, also returning the erase
// record its tags hold — one listing for both, since every Start asks.
func (e *ecsEC2Runtime) ensureKeepAccessPoint(ctx context.Context) (string, eraseRecord, error) {
	ap, err := e.keepAccessPoint(ctx)
	if err != nil {
		return "", eraseRecord{}, err
	}
	if ap == nil {
		id, err := e.ensureAccessPoint(ctx, "keep-ec2", "/home-keep/"+e.base.membershipID)
		return id, eraseRecord{}, err
	}
	rec, err := e.parseKeepRecord(ap)
	return aws.ToString(ap.AccessPointId), rec, err
}

// dropStalePendingErase takes back every pending mark when this member's live home is one
// of them. Start runs under the lifecycle lease, so no erase is running now: the erase
// that marked the live volume failed before deleting it and could not take its marks back.
// The other pending marks — older volumes a leftover copy came from — are from such an
// erase too: an erase destroys only the live volume, and while a mark is pending no new
// home can be created (restoreSnapshot), so a live home that is itself marked means no
// pending erase got as far as destroying anything. Left in place, any of them would refuse
// this very home's hibernation copy once it hibernates. Best-effort — marks left behind
// only mean the restore path keeps refusing until this runs again.
func (e *ecsEC2Runtime) dropStalePendingErase(ctx context.Context, apID string, rec eraseRecord) {
	vol, err := e.homeVolume(ctx)
	if err != nil || vol == nil || !slices.Contains(rec.pending, aws.ToString(vol.VolumeId)) {
		return
	}
	stale := strings.Join(rec.pending, ", ")
	rec.pending = nil
	if err := e.writeEraseRecord(ctx, apID, rec); err != nil {
		log.Printf("ecs-ec2: %s still carries a pending erase (%s) although %s is its live home: %v", e.base.name, stale, aws.ToString(vol.VolumeId), err)
		return
	}
	log.Printf("ecs-ec2: took back a pending erase (%s); %s is still the live home of %s", stale, aws.ToString(vol.VolumeId), e.base.name)
}

// keepAccessPoint finds this member's keep access point (the role prepare creates), or nil.
func (e *ecsEC2Runtime) keepAccessPoint(ctx context.Context) (*efstypes.AccessPointDescription, error) {
	aps, err := e.base.accessPoints(ctx)
	if err != nil {
		return nil, err
	}
	for i, ap := range aps {
		if tagValue(ap.Tags, "af-membership") == e.base.membershipID && tagValue(ap.Tags, "af-role") == "keep-ec2" {
			return &aps[i], nil
		}
	}
	return nil, nil
}

// snapshotListingSettle is how long a deletion waits before listing snapshots a second
// time. DescribeSnapshots is eventually consistent, and the snapshots these deletions race
// are started by paths that take no lock against them (the pool sweeper's hibernation, the
// reaper's backups); a copy created a moment before the first listing may be absent from
// it. Measured on the sandbox account: a snapshot still pending is listed and deletable,
// so the wait is for visibility only.
const snapshotListingSettle = 10 * time.Second

// deleteHomeVolume detaches (if needed) and deletes this workspace's home volume.
// Absent volume = already done.
func (e *ecsEC2Runtime) deleteHomeVolume(ctx context.Context) error {
	vol, err := e.homeVolume(ctx)
	if err != nil || vol == nil {
		return err
	}
	volumeID := aws.ToString(vol.VolumeId)
	if err := e.waitDetached(ctx, volumeID); err != nil {
		return err
	}
	// DescribeVolumes can already report the volume detached while DeleteVolume still
	// answers VolumeInUse (measured — the two views converge separately). Retry rather
	// than leave a provisioned volume behind, which is the one leftover here that bills
	// forever.
	for i := 0; ; i++ {
		if _, err = e.ec2.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(volumeID)}); err == nil {
			return nil
		}
		if i >= 15 || !strings.Contains(err.Error(), "VolumeInUse") {
			return err
		}
		if serr := e.sleep(ctx, 4*time.Second); serr != nil {
			return serr
		}
	}
}

// homeSnapshots returns this workspace's hibernation snapshots (§64.18.2), newest first
// is not guaranteed — callers that need one pick by state. Owner self-scoped so a
// public snapshot with a colliding tag can never be picked up.
func (e *ecsEC2Runtime) homeSnapshots(ctx context.Context) ([]ec2types.Snapshot, error) {
	out, err := e.ec2.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"},
		Filters: []ec2types.Filter{
			tagFilter(EC2TagMembership, e.base.membershipID),
			tagFilter(EC2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return nil, err
	}
	return out.Snapshots, nil
}

// hibernate is one STEP of putting a long-unused home to sleep (ADR 0045 decision 4, docs/log/64
// §64.18.2), not the whole thing. A snapshot of a 45 GiB home takes 30–40 minutes, and
// the sweeper cannot sit on that, so the operation is resumable: each sweep advances it
// by one step and the state lives in AWS, not in the CP (ADR 0012).
//
//	no snapshot          → release the slot, then start one
//	snapshot pending     → nothing to do; the next sweep looks again
//	snapshot completed   → delete the volume (the home is now the snapshot)
//
// The slot is released BEFORE the snapshot on purpose: releaseSlot unmounts and detaches,
// so what gets captured is a quiesced filesystem rather than a crash-consistent image of
// a live mount. It also returns the slot to the pool at the earliest moment, which is
// half of why hibernation is worth doing.
// BeginHibernate is the reaper's entry point into the series above (tier 3 — see
// hibernatingRuntime in reaper.go). It only ever STARTS one: the timing decision belongs
// to the reaper, which can see the tenant's home_hibernate_after, and every step after
// the first belongs to the pool sweeper, which can resume it after a CP restart.
//
// Two guards, both of which are the difference between "cheap" and "lost work":
//
//   - already marked ⇒ do nothing. Otherwise the reaper and the sweeper would both be
//     advancing the same hibernation, and two CreateSnapshot calls in the same window
//     leave a second, orphaned capture billing forever.
//   - a live service ⇒ do nothing. The reaper decided from the database that nobody has
//     opened this workspace in weeks; AWS is the authority on whether it is running right
//     now, and it is the one that would lose the mount.
//
// Hibernating pins this adapter to the hibernation entry point as an interface value.
// The real interface is the reaper's (reaper.go: hibernatingRuntime) and cannot be
// imported here, so the CP asserts against THIS in runtime_seam.go — chaining the two
// declarations, because a drift between them would not break anything loudly: the
// reaper's `rt.(hibernatingRuntime)` would simply stop matching, and homes would quietly
// never hibernate again.
var Hibernating interface {
	BeginHibernate(ctx context.Context) error
} = (*ecsEC2Runtime)(nil)

func (e *ecsEC2Runtime) BeginHibernate(ctx context.Context) error {
	vol, err := e.homeVolume(ctx)
	if err != nil || vol == nil {
		return err // no home here: already a snapshot, or never created
	}
	if at := ec2TagValue(vol.Tags, EC2TagHibernating); at != "" {
		return nil // under way; the pool sweeper carries it the rest of the way
	}
	s, ok, err := e.base.describeService(ctx)
	if err != nil {
		return err
	}
	if ok && (s.DesiredCount > 0 || s.RunningCount > 0 || s.PendingCount > 0) {
		return nil
	}
	log.Printf("ecs-ec2 hibernate: %s has not been opened for the tenant's retention window; putting the home away", e.base.name)
	return e.hibernate(ctx)
}

func (e *ecsEC2Runtime) hibernate(ctx context.Context) error {
	vol, err := e.homeVolume(ctx)
	if err != nil || vol == nil {
		return err
	}
	volumeID := aws.ToString(vol.VolumeId)
	mark, fresh, err := e.hibernationMark(ctx, vol)
	if err != nil {
		return err
	}
	snaps, err := e.homeSnapshots(ctx)
	if err != nil {
		return err
	}
	for _, s := range snaps {
		// Only a snapshot of THIS volume, started after THIS hibernation's mark, is the
		// capture we are waiting for. Everything else is a leftover — from a restore
		// whose cleanup did not finish, or from a dormancy the owner interrupted by
		// coming back — and taking one of those for the capture deletes a volume holding
		// work that was never captured.
		if aws.ToString(s.VolumeId) != volumeID || s.StartTime == nil || s.StartTime.Before(mark) {
			log.Printf("ecs-ec2 hibernate: dropping the superseded snapshot %s of %s",
				aws.ToString(s.SnapshotId), e.base.name)
			if _, err := e.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: s.SnapshotId}); err != nil && !isAWSNotFound(err) {
				return err
			}
			continue
		}
		switch s.State {
		case ec2types.SnapshotStateCompleted:
			// A Clean home that failed with this volume still here, and could not take its
			// marks back, leaves them pending — and while any is, restoreSnapshot restores
			// nothing, so deleting the volume would leave the home nowhere. It stays until
			// the owner's Start takes the marks back or Clean home finishes. This step runs
			// from the sweeper, without the lifecycle lease, which is why it asks here.
			rec, err := e.eraseRecord(ctx)
			if err != nil {
				return err
			}
			if len(rec.pending) > 0 {
				log.Printf("ecs-ec2 hibernate: %s has an unfinished Clean home (%s); keeping %s until it is resolved",
					e.base.name, strings.Join(rec.pending, ", "), volumeID)
				return nil
			}
			// A Recreate or Clean home marked after this capture started is only on the
			// volume. Once the volume is gone the snapshot is the home, so the marks have
			// to be on it first (WipeHome explains why this read is late enough). Only
			// added, so a stronger mark already on the snapshot is never narrowed.
			for _, w := range []HomeWipe{HomeWipeRepos, HomeWipeClean} {
				if ec2TagValue(vol.Tags, homeWipeKey(w)) == "" {
					continue
				}
				if err := e.markHomeWipe(ctx, aws.ToString(s.SnapshotId), s.Tags, w); err != nil {
					return err
				}
			}
			log.Printf("ecs-ec2 hibernate: %s captured in %s; deleting the volume",
				e.base.name, aws.ToString(s.SnapshotId))
			return e.deleteHomeVolume(ctx)
		case ec2types.SnapshotStateError:
			// A failed snapshot would otherwise pin the home in this state forever:
			// never "completed", so the volume never goes, and never absent, so no new
			// snapshot is ever started.
			log.Printf("ecs-ec2 hibernate: snapshot %s of %s failed; discarding it and retrying",
				aws.ToString(s.SnapshotId), e.base.name)
			if _, err := e.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: s.SnapshotId}); err != nil && !isAWSNotFound(err) {
				return err
			}
			return nil
		default:
			return nil // pending: let it finish
		}
	}
	if err := e.releaseSlot(ctx); err != nil {
		// Take the mark back off if THIS call put it there. releaseSlot unmounts over SSM,
		// so it fails for as long as the slot is unreachable — an AZ having a bad day is
		// the case that matters — and a mark with nothing behind it is not free:
		//
		//   - the volume looks "hibernating" in the pool view while it is still attached
		//     and perfectly fine;
		//   - every sweep logs the same failure with no way to tell it apart from progress;
		//   - and the mark outlives the outage, so the FIRST snapshot taken afterwards is
		//     judged against a timestamp from before it.
		//
		// A mark that was already there is left alone: it belongs to a hibernation that is
		// genuinely under way, and the snapshot it validates may already exist.
		if fresh {
			e.unmarkHibernating(ctx, volumeID)
		}
		return fmt.Errorf("release the slot before snapshotting: %w", err)
	}
	out, err := e.ec2.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{
		VolumeId:    aws.String(volumeID),
		Description: aws.String("agent-fleet hibernated home for " + e.base.name),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSnapshot,
			Tags: e.ownedTags(append([]ec2types.Tag{
				{Key: aws.String(EC2TagMembership), Value: aws.String(e.base.membershipID)},
				{Key: aws.String(EC2TagRole), Value: aws.String(ec2RoleHome)},
				{Key: aws.String(EC2TagWorkspace), Value: aws.String(e.base.name)},
				{Key: aws.String(EC2TagPool), Value: aws.String(e.pool.pool)},
				{Key: aws.String("Name"), Value: aws.String(e.base.name + "-home")},
			}, homeWipeTags(vol.Tags)...)),
		}},
	})
	if err != nil {
		return fmt.Errorf("snapshot %s: %w", volumeID, err)
	}
	log.Printf("ecs-ec2 hibernate: %s → %s (the volume goes once it completes)",
		e.base.name, aws.ToString(out.SnapshotId))
	return nil
}

// hibernationMark returns the moment this hibernation began, stamping it if this is the
// first step, and whether THIS call is what stamped it. Written BEFORE the slot is
// released so a CP that dies in between resumes instead of starting over — and so the
// mark always predates the snapshot it validates.
//
// The "fresh" answer is what lets the caller take it back when the very next step fails:
// see hibernate.
func (e *ecsEC2Runtime) hibernationMark(ctx context.Context, vol *ec2types.Volume) (time.Time, bool, error) {
	if v := ec2TagValue(vol.Tags, EC2TagHibernating); v != "" {
		at, err := time.Parse(time.RFC3339, v)
		if err == nil {
			return at, false, nil
		}
		// An unparseable mark is worse than none: every snapshot would compare against a
		// zero time and be accepted. Re-stamp.
		log.Printf("ecs-ec2 hibernate: %s has an unreadable %s tag (%q); re-stamping",
			e.base.name, EC2TagHibernating, v)
	}
	// RFC3339Nano, not RFC3339: second granularity would round the mark DOWN, placing it
	// up to a second before it was actually written — long enough to accept a snapshot
	// from the dormancy this one replaces.
	at := e.now().UTC()
	if _, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{aws.ToString(vol.VolumeId)},
		Tags:      []ec2types.Tag{{Key: aws.String(EC2TagHibernating), Value: aws.String(at.Format(time.RFC3339Nano))}},
	}); err != nil {
		return time.Time{}, false, fmt.Errorf("mark %s as hibernating: %w", aws.ToString(vol.VolumeId), err)
	}
	return at, true, nil
}

// unmarkHibernating removes a hibernation mark this CP just wrote and could not act on.
// Best effort: leaving it is untidy, not dangerous, and the caller is already returning
// the real error.
func (e *ecsEC2Runtime) unmarkHibernating(ctx context.Context, volumeID string) {
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{volumeID},
		Tags:      []ec2types.Tag{{Key: aws.String(EC2TagHibernating)}},
	}); err != nil {
		log.Printf("ecs-ec2 hibernate: could not take the mark back off %s: %v", volumeID, err)
	}
}

// restoreSnapshot returns the completed hibernation snapshot to build this home from, or
// "" when there is none. A pending one is deliberately NOT used: CreateVolume from an
// incomplete snapshot is rejected, and answering "" here means the user gets a fresh home
// while their real one is still being captured — data loss dressed up as a fast start.
// Returning an error instead makes the Start fail, which is the honest outcome.
func (e *ecsEC2Runtime) restoreSnapshot(ctx context.Context) (string, error) {
	s, err := e.restoreSource(ctx)
	if err != nil || s == nil {
		return "", err
	}
	return aws.ToString(s.SnapshotId), nil
}

// restoreSource is restoreSnapshot with the whole snapshot, whose tags the restored volume
// inherits the pending wipe from. nil when there is nothing to restore.
func (e *ecsEC2Runtime) restoreSource(ctx context.Context) (*ec2types.Snapshot, error) {
	all, err := e.homeSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := e.eraseRecord(ctx)
	if err != nil {
		return nil, err
	}
	// An erase that has not finished may already have deleted the volume; whatever copy
	// is listed now could be the home it was erasing. Refuse rather than guess.
	if len(rec.pending) > 0 {
		return nil, fmt.Errorf("an erase of the home of %s did not finish (%s); run Clean home again to finish it",
			e.base.name, strings.Join(rec.pending, ", "))
	}
	// A snapshot of an erased volume is a copy of an erased home — one the erase could not
	// see yet (EraseHome). It is never restored, and it goes, so it stops billing and
	// cannot be mistaken for anything later.
	var snaps []ec2types.Snapshot
	for _, s := range all {
		if rec.isErased(aws.ToString(s.VolumeId)) {
			log.Printf("ecs-ec2: %s is a copy of %s, a home of %s that was erased; deleting it instead of restoring it",
				aws.ToString(s.SnapshotId), aws.ToString(s.VolumeId), e.base.name)
			if _, err := e.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: s.SnapshotId}); err != nil && !isAWSNotFound(err) {
				log.Printf("ecs-ec2: could not delete %s: %v", aws.ToString(s.SnapshotId), err)
			}
			continue
		}
		snaps = append(snaps, s)
	}
	// NEWEST completed, not first: a restore whose snapshot cleanup failed leaves an
	// older one behind, and picking that would silently hand the user a home from two
	// hibernations ago.
	var newest *ec2types.Snapshot
	for i := range snaps {
		if snaps[i].State != ec2types.SnapshotStateCompleted {
			continue
		}
		if newest == nil || snapshotStartedAfter(snaps[i], *newest) {
			newest = &snaps[i]
		}
	}
	if newest != nil {
		return newest, nil
	}
	for _, s := range snaps {
		if s.State == ec2types.SnapshotStatePending {
			return nil, fmt.Errorf("home of %s is being hibernated (%s); try again once it completes",
				e.base.name, aws.ToString(s.SnapshotId))
		}
	}
	return nil, nil
}

// goldenSnapshot returns the pool's pre-baked home for the image this deployment runs, or
// "" to build an empty one. Best-effort by design: a new user with no golden gets a slow
// first start, which is a worse experience but not a wrong one, so nothing here is ever
// promoted to an error that blocks a Start.
//
// ★ The image stamp is checked, not assumed. Re-baking is a manual step tied to a release
// (decision 9), and the failure mode of forgetting it is invisible: only NEW users get it,
// they get old CLIs, and everything looks fine. A stale golden is therefore refused —
// loudly — rather than used.
func (e *ecsEC2Runtime) goldenSnapshot(ctx context.Context) string {
	// Everybody reads EC2RoleGolden. Only the baker's probe reads a candidate, and it
	// says so on itself — an unproven golden is not something a lookup should be able
	// to reach by accident (§64.28.3).
	role := e.seedRole
	if role == "" {
		role = EC2RoleGolden
	}
	out, err := e.ec2.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"},
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, e.pool.pool),
			tagFilter(EC2TagRole, role),
		},
	})
	if err != nil {
		log.Printf("ecs-ec2: looking up the golden snapshot failed; building an empty home: %v", err)
		return ""
	}
	want := e.base.cfg.workspaceImage
	// The comparison is by CONTENT where the content is knowable (goldenIdentity):
	// a golden re-stamped for a new tag that resolves to the same bytes is still ours,
	// and one stamped with our tag that no longer resolves to the same bytes is not.
	id := goldenIdentityFor(ctx, e.base.ecr, want)
	var stale []string
	var newest *ec2types.Snapshot
	for i := range out.Snapshots {
		s := out.Snapshots[i]
		if s.State != ec2types.SnapshotStateCompleted {
			continue
		}
		if !id.matches(s.Tags) {
			stale = append(stale, fmt.Sprintf("%s(%s)", aws.ToString(s.SnapshotId), ec2TagValue(s.Tags, ec2TagImage)))
			continue
		}
		// ⚠️ A golden of ANOTHER architecture is not a candidate for this home. The
		// pool bakes one per declared arch and they all carry the same image stamp, so
		// image alone stops discriminating the moment a second arch is declared — and
		// what is left is "newest wins", which is a coin toss. This is NOT a stale
		// golden and must not be reported as one: the other arch's golden is correct,
		// it just is not ours.
		//
		// Measured on the real deployment (docs/log/70 §70.14.5): with x86_64 and arm64
		// baking at the same time, the x86_64 probe was seeded from the arm64
		// candidate. Nothing broke — §70.5's self-heal wipes the wrong-arch bits and
		// re-runs boot-install — which is exactly why this had to be found in a log
		// rather than by a failure: it silently throws away the whole point of the
		// golden, and it makes the probe prove the wrong snapshot.
		if snapshotArch(s) != archOrX86(e.arch) {
			continue
		}
		if newest == nil || snapshotStartedAfter(s, *newest) {
			newest = &out.Snapshots[i]
		}
	}
	if newest != nil {
		return aws.ToString(newest.SnapshotId)
	}
	// A stale CANDIDATE is the baker's own bookkeeping, not an operator problem — it
	// only means the image moved while a bake was in flight, and the next tick starts a
	// fresh one. Only a stale published golden is worth the standing warning.
	if len(stale) > 0 && role == EC2RoleGolden {
		log.Printf("ecs-ec2: the golden snapshot %s was baked from another image; this deployment runs %s. "+
			"New homes are being built EMPTY (slow first start) until it is re-baked — ADR 0045 決定 9.",
			strings.Join(stale, ", "), want)
	}
	return ""
}

func snapshotStartedAfter(a, b ec2types.Snapshot) bool {
	if a.StartTime == nil {
		return false
	}
	return b.StartTime == nil || a.StartTime.After(*b.StartTime)
}

// deleteHomeSnapshots removes every hibernation snapshot of this home. Called by Destroy
// and, after a restore, by createHomeVolume — both are moments when the snapshot is
// definitively superseded. Hibernation itself must never call it: it would delete the
// snapshot it just took.
func (e *ecsEC2Runtime) deleteHomeSnapshots(ctx context.Context) error {
	snaps, err := e.homeSnapshots(ctx)
	if err != nil {
		return err
	}
	for _, s := range snaps {
		if _, err := e.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{
			SnapshotId: s.SnapshotId,
		}); err != nil && !isAWSNotFound(err) {
			return fmt.Errorf("delete snapshot %s: %w", aws.ToString(s.SnapshotId), err)
		}
	}
	return nil
}
