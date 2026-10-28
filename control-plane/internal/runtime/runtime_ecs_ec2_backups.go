package runtime

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// --- periodic backups (ADR 0045 decision 17) ---

// backupSnapshots lists this membership's backup copies, newest first. Role-filtered, so
// a hibernation capture or the pool's golden can never be counted, pruned, or mistaken
// for one.
func (e *ecsEC2Runtime) backupSnapshots(ctx context.Context) ([]ec2types.Snapshot, error) {
	out, err := e.ec2.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"},
		Filters: []ec2types.Filter{
			tagFilter(EC2TagMembership, e.base.membershipID),
			tagFilter(EC2TagRole, ec2RoleBackup),
		},
	})
	if err != nil {
		return nil, err
	}
	snaps := out.Snapshots
	sort.SliceStable(snaps, func(i, j int) bool {
		return backupStamp(snaps[j]).Before(backupStamp(snaps[i]))
	})
	return snaps, nil
}

// backupStamp is when the CP asked for a backup — af-backup-at, falling back to what EBS
// reports. The tag is preferred because the schedule is a statement about this deployment
// ("when did we last ask"), not about how long AWS took to answer.
func backupStamp(s ec2types.Snapshot) time.Time {
	if v := ec2TagValue(s.Tags, EC2TagBackupAt); v != "" {
		if at, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return at
		}
	}
	if s.StartTime != nil {
		return *s.StartTime
	}
	return time.Time{}
}

// BackupHome keeps a copy of this home OUTSIDE its Availability Zone, because an EBS
// volume lives in exactly one and cannot be evacuated: losing that AZ is otherwise the
// same as losing the work. Snapshots are regional, so they are the only place a home can
// be that its own AZ is not (docs/log/64 §64.21, ADR 0045 decision 17).
//
// Three things this deliberately does NOT do:
//
//   - It does not unmount anything. The volume is captured where it is, mounted and in
//     use, so a backup is CRASH-CONSISTENT — the same guarantee as snapshotting a running
//     instance, and the same one a power cut gives. Quiescing would mean taking a working
//     person's home away on a timer, which is a worse product than a backup that may need
//     a filesystem check. (This is the opposite call from bake-golden.sh — §64.18.5 — for
//     the opposite reason: a golden is everyone's STARTING state and gets to be clean.)
//   - It does not restore. A backup is older than the home by construction, and handing
//     somebody a silently older home is the failure mode this file keeps designing around.
//     Restoring is an operator decision, with the runbook in §64.21.
//   - It keeps no state in the CP. Whether a backup is due is read from the newest one's
//     tag (ADR 0012), so a restart, a second replica and a redeploy all agree.
func (e *ecsEC2Runtime) BackupHome(ctx context.Context, every time.Duration) error {
	if every <= 0 {
		return nil
	}
	vol, err := e.homeVolume(ctx)
	if err != nil || vol == nil {
		// No volume: either this workspace has never started, or the home is already a
		// hibernation snapshot — which is itself a regional copy, so there is nothing a
		// backup would add.
		return err
	}
	if ec2TagValue(vol.Tags, EC2TagHibernating) != "" {
		// A hibernation is capturing this volume and will DELETE it. A second capture of
		// the same blocks would pay twice for one moment in time.
		return nil
	}
	snaps, err := e.backupSnapshots(ctx)
	if err != nil {
		return err
	}
	for _, s := range snaps {
		if s.State == ec2types.SnapshotStatePending {
			return nil // one at a time; a 45 GiB home takes 30–40 minutes
		}
	}
	if len(snaps) > 0 && e.now().Sub(backupStamp(snaps[0])) < every {
		return e.pruneBackups(ctx, snaps)
	}
	at := e.now().UTC()
	out, err := e.ec2.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{
		VolumeId:    vol.VolumeId,
		Description: aws.String("agent-fleet backup of " + e.base.name),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSnapshot,
			Tags: e.ownedTags([]ec2types.Tag{
				{Key: aws.String(EC2TagMembership), Value: aws.String(e.base.membershipID)},
				{Key: aws.String(EC2TagRole), Value: aws.String(ec2RoleBackup)},
				{Key: aws.String(EC2TagWorkspace), Value: aws.String(e.base.name)},
				{Key: aws.String(EC2TagPool), Value: aws.String(e.pool.pool)},
				{Key: aws.String(EC2TagBackupAt), Value: aws.String(at.Format(time.RFC3339Nano))},
				{Key: aws.String("Name"), Value: aws.String(e.base.name + "-backup")},
			}),
		}},
	})
	if err != nil {
		return fmt.Errorf("back up %s: %w", aws.ToString(vol.VolumeId), err)
	}
	log.Printf("ecs-ec2 backup: %s → %s", e.base.name, aws.ToString(out.SnapshotId))
	// Re-read rather than pruning the list we had a moment ago: the copy just created is
	// part of the retention count, and pruning the stale list would keep backupKeep+1
	// forever — one more than the operator agreed to pay for, every time.
	fresh, err := e.backupSnapshots(ctx)
	if err != nil {
		return err
	}
	return e.pruneBackups(ctx, fresh)
}

// pruneBackups keeps the newest backupKeep COMPLETED copies and deletes the rest.
//
// Completed only, on purpose: a capture in flight has already been paid for and deleting
// it buys nothing. It also means the count never drops below the retention while a new
// backup is still running — the oldest goes when its replacement is actually usable.
func (e *ecsEC2Runtime) pruneBackups(ctx context.Context, snaps []ec2types.Snapshot) error {
	keep := e.pool.backupKeep
	if keep < 1 {
		keep = 1
	}
	seen := 0
	for _, s := range snaps {
		if s.State != ec2types.SnapshotStateCompleted {
			continue
		}
		seen++
		if seen <= keep {
			continue
		}
		log.Printf("ecs-ec2 backup: dropping %s of %s (keeping %d)",
			aws.ToString(s.SnapshotId), e.base.name, keep)
		if _, err := e.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: s.SnapshotId}); err != nil && !isAWSNotFound(err) {
			return fmt.Errorf("prune backup %s: %w", aws.ToString(s.SnapshotId), err)
		}
	}
	return nil
}

// deleteBackups removes every backup of this membership and returns how many it removed.
// Only two things call it: Destroy, and an administrator deleting the backups on purpose
// (DeleteHomeBackups). A backup outliving its home is the entire point, so no cleanup,
// sweep or Clean home may take one as a side effect.
func (e *ecsEC2Runtime) deleteBackups(ctx context.Context) (int, error) {
	snaps, err := e.backupSnapshots(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range snaps {
		_, err := e.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: s.SnapshotId})
		switch {
		case err == nil:
			n++
		case isAWSNotFound(err):
			// Gone already — another deletion got there first. Not ours to count.
		default:
			return n, fmt.Errorf("delete backup %s: %w", aws.ToString(s.SnapshotId), err)
		}
	}
	return n, nil
}

// HomeBackups counts this member's backup copies for the administrator deciding whether
// to delete them.
func (e *ecsEC2Runtime) HomeBackups(ctx context.Context) (HomeBackups, error) {
	snaps, err := e.backupSnapshots(ctx) // newest first
	if err != nil {
		return HomeBackups{}, err
	}
	vol, err := e.homeVolume(ctx)
	if err != nil {
		return HomeBackups{}, err
	}
	out := HomeBackups{Count: len(snaps), HomeExists: vol != nil}
	if len(snaps) > 0 {
		out.Newest = backupStamp(snaps[0])
	}
	return out, nil
}

// DeleteHomeBackups is the step Clean home leaves out on purpose (see EraseHome). A copy
// still being captured is deleted too: it would otherwise complete a moment later and
// hold the very home the administrator meant to be rid of.
//
// The reaper takes backups without a lock, so one it started as the first listing was
// read — or just before a Clean home deleted the volume it copies — can be missing from
// it; the second listing after snapshotListingSettle catches that in all but an extreme
// case. A backup is never restored automatically, so one that surfaces even later is not
// a silent return of the home: it shows in the member's count and can be deleted again. A
// copy the schedule takes after this returns is a new backup of the home as it now is —
// which is why an offboarding cleans the home first.
func (e *ecsEC2Runtime) DeleteHomeBackups(ctx context.Context) (int, error) {
	n, err := e.deleteBackups(ctx)
	if err != nil {
		return n, err
	}
	if err := e.sleep(ctx, snapshotListingSettle); err != nil {
		return n, err
	}
	m, err := e.deleteBackups(ctx)
	return n + m, err
}
