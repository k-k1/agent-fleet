package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
)

// eraseHarness is a stopped member whose home has everything an administrator's Clean home
// has to tell apart: the volume on a slot, a hibernation snapshot of it, a backup copy,
// the pool's golden, the EFS keep and Claude access points, and the SSM secrets.
func eraseHarness(t *testing.T, slotRunning bool) *ec2Harness {
	t.Helper()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-slot", "ap-northeast-1a", "m7i.large", slotRunning, false)
	h.ec2.attach("vol-1", "i-slot", time.Now())
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	snap := func(id, role, membership string) {
		h.ec2.snapshots[id] = &ec2types.Snapshot{
			SnapshotId: aws.String(id), VolumeId: aws.String("vol-1"),
			State: ec2types.SnapshotStateCompleted, StartTime: aws.Time(time.Now().Add(-time.Hour)),
			Tags: []ec2types.Tag{
				{Key: aws.String(EC2TagMembership), Value: aws.String(membership)},
				{Key: aws.String(EC2TagRole), Value: aws.String(role)},
				{Key: aws.String(EC2TagBackupAt), Value: aws.String(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano))},
			},
		}
	}
	snap("snap-hib", ec2RoleHome, "M-1")
	snap("snap-backup", ec2RoleBackup, "M-1")
	snap("snap-other", ec2RoleHome, "M-2")
	h.ec2.snapshots["snap-golden"] = &ec2types.Snapshot{
		SnapshotId: aws.String("snap-golden"), State: ec2types.SnapshotStateCompleted,
		Tags: []ec2types.Tag{{Key: aws.String(EC2TagRole), Value: aws.String(EC2RoleGolden)}},
	}
	ap := func(id, role, path string) efstypes.AccessPointDescription {
		return efstypes.AccessPointDescription{AccessPointId: aws.String(id),
			RootDirectory: &efstypes.RootDirectory{Path: aws.String(path)},
			Tags: []efstypes.Tag{{Key: aws.String("af-membership"), Value: aws.String("M-1")},
				{Key: aws.String("af-role"), Value: aws.String(role)}}}
	}
	h.efs.aps = []efstypes.AccessPointDescription{
		ap("fsap-keep", "keep-ec2", "/home-keep/M-1"),
		ap("fsap-claude", "claude-ec2", "/claude-config/M-1"),
	}
	return h
}

// Clean home, not Destroy: the home and every copy a later Start would rebuild it from
// go; the logins (EFS), the service, the secrets and the backups stay.
func TestECSEC2EraseHomeRemovesTheHomeAndNothingElse(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	if err := h.rt.EraseHome(ctx); err != nil {
		t.Fatalf("EraseHome: %v", err)
	}
	if _, ok := h.ec2.volumes["vol-1"]; ok {
		t.Error("the home volume survived an administrator's Clean home")
	}
	if _, ok := h.ec2.snapshots["snap-hib"]; ok {
		t.Error("the hibernation snapshot survived: the next Start restores it and hands the erased home back")
	}
	for id, why := range map[string]string{
		"snap-backup": "a backup is a copy outside the home; only DeleteHomeBackups or Destroy may take it",
		"snap-other":  "another member's hibernated home",
		"snap-golden": "the golden every new home is built from",
	} {
		if _, ok := h.ec2.snapshots[id]; !ok {
			t.Errorf("%s was deleted — %s", id, why)
		}
	}
	if len(h.efs.aps) != 2 {
		t.Errorf("the EFS access points holding the logins and the Claude state were touched: %v", h.efs.aps)
	}
	if len(h.ssm.deletes) != 0 {
		t.Errorf("the workspace's SSM secrets were deleted: %v", h.ssm.deletes)
	}
	if _, ok := h.ecs.services["af-ws-acme-alice"]; !ok || len(h.ecs.deleteCalls) != 0 {
		t.Error("the ECS service was deleted; Clean home must leave a workspace that starts again")
	}
	if got, err := h.rt.restoreSnapshot(ctx); err != nil || got != "" {
		t.Errorf("after the erase the next Start would restore %q (err %v); want a fresh home", got, err)
	}
	if st := h.rt.State(ctx); st != "none" {
		t.Errorf("State after the erase = %q, want none (the next Start creates the home)", st)
	}
}

// A home on a running slot is still mounted there (lazy release), and detaching a mounted
// filesystem is how a home gets corrupted — even one about to be deleted leaves the slot
// with a dead mount point for the next member placed on it.
func TestECSEC2EraseHomeUnmountsBeforeDetachingAndDeleting(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, true)
	if err := h.rt.EraseHome(ctx); err != nil {
		t.Fatalf("EraseHome: %v", err)
	}
	umount, detach, del := -1, -1, -1
	for i, c := range h.ec2.calls {
		switch {
		case strings.HasPrefix(c, "SSM af-umount"):
			umount = i
		case strings.HasPrefix(c, "DetachVolume vol-1"):
			detach = i
		case strings.HasPrefix(c, "DeleteVolume vol-1"):
			del = i
		}
	}
	if umount < 0 || detach < 0 || del < 0 || !(umount < detach && detach < del) {
		t.Errorf("want umount < detach < delete, got umount=%d detach=%d delete=%d in %v", umount, detach, del, h.ec2.calls)
	}
}

// An erase that died halfway is finished by pressing again, and an erase of a member who
// never started has nothing to do.
func TestECSEC2EraseHomeIsIdempotent(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	if err := h.rt.EraseHome(ctx); err != nil {
		t.Fatalf("first EraseHome: %v", err)
	}
	if err := h.rt.EraseHome(ctx); err != nil {
		t.Fatalf("second EraseHome: %v", err)
	}
	if err := newEC2Harness(t).rt.EraseHome(ctx); err != nil {
		t.Fatalf("EraseHome of a workspace that never started: %v", err)
	}
}

// The unmount has to happen while nothing holds the home, and releaseSlot is what refuses
// to take it from a service that wants a task. EraseHome must therefore fail rather than
// delete when the slot cannot be released.
func TestECSEC2EraseHomeKeepsTheHomeWhenTheSlotWillNotLetGo(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, true)
	h.ssmc.fail["af-umount"] = true
	if err := h.rt.EraseHome(ctx); err == nil {
		t.Fatal("EraseHome succeeded although the home could not be unmounted")
	}
	if _, ok := h.ec2.volumes["vol-1"]; !ok {
		t.Error("the volume was deleted while still mounted on the slot")
	}
	if _, ok := h.ec2.snapshots["snap-hib"]; !ok {
		t.Error("the hibernation snapshot was deleted although the erase stopped before the volume")
	}
}

// The deliberate step Clean home leaves out: an administrator can see the backups and
// delete them, and that deletes backups only.
func TestECSEC2HomeBackupsAreCountedAndDeletedOnRequest(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	h.ec2.snapshotState = ec2types.SnapshotStatePending
	if err := h.rt.BackupHome(ctx, time.Minute); err != nil {
		t.Fatalf("BackupHome: %v", err)
	}
	b, err := h.rt.HomeBackups(ctx)
	if err != nil {
		t.Fatalf("HomeBackups: %v", err)
	}
	if b.Count != 2 {
		t.Errorf("HomeBackups counted %d, want 2 (a capture still pending will hold the home too)", b.Count)
	}
	if b.Newest.IsZero() || time.Since(b.Newest) > time.Minute {
		t.Errorf("Newest = %v, want the pending capture that was just taken", b.Newest)
	}
	n, err := h.rt.DeleteHomeBackups(ctx)
	if err != nil || n != 2 {
		t.Fatalf("DeleteHomeBackups = %d, %v; want 2, nil", n, err)
	}
	for _, id := range []string{"snap-hib", "snap-other", "snap-golden"} {
		if _, ok := h.ec2.snapshots[id]; !ok {
			t.Errorf("deleting the backups took %s, which is not a backup", id)
		}
	}
	if b, err := h.rt.HomeBackups(ctx); err != nil || b.Count != 0 || !b.Newest.IsZero() {
		t.Errorf("after deleting: %+v, %v; want none", b, err)
	}
	if _, ok := h.ec2.volumes["vol-1"]; !ok {
		t.Error("deleting the backups deleted the home")
	}
}

// The pool sweeper advances a hibernation without the lifecycle lease the erase holds, so
// a capture it started just before the volume went can be missing from the first listing
// (DescribeSnapshots is eventually consistent). Left behind, the next Start restores it —
// the erased home, back.
func TestECSEC2EraseHomeCatchesACaptureTheFirstListingMissed(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	h.ec2.snapshots["snap-late"] = &ec2types.Snapshot{
		SnapshotId: aws.String("snap-late"), VolumeId: aws.String("vol-1"),
		State: ec2types.SnapshotStatePending, StartTime: aws.Time(time.Now()),
		Tags: []ec2types.Tag{
			{Key: aws.String(EC2TagMembership), Value: aws.String("M-1")},
			{Key: aws.String(EC2TagRole), Value: aws.String(ec2RoleHome)},
		},
	}
	h.ec2.snapshotHidden["snap-late"] = 1
	if err := h.rt.EraseHome(ctx); err != nil {
		t.Fatalf("EraseHome: %v", err)
	}
	if _, ok := h.ec2.snapshots["snap-late"]; ok {
		t.Error("a hibernation capture missing from the first listing survived the erase")
	}
	if _, ok := h.ec2.snapshots["snap-backup"]; !ok {
		t.Error("the second listing took a backup")
	}
}

// The reaper takes backups without a lock, so while the home exists a copy it started as
// the first listing was read can be missing from it.
func TestECSEC2DeleteHomeBackupsCatchesACopyTheFirstListingMissed(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	h.ec2.snapshots["snap-backup-late"] = &ec2types.Snapshot{
		SnapshotId: aws.String("snap-backup-late"), VolumeId: aws.String("vol-1"),
		State: ec2types.SnapshotStatePending, StartTime: aws.Time(time.Now()),
		Tags: []ec2types.Tag{
			{Key: aws.String(EC2TagMembership), Value: aws.String("M-1")},
			{Key: aws.String(EC2TagRole), Value: aws.String(ec2RoleBackup)},
		},
	}
	h.ec2.snapshotHidden["snap-backup-late"] = 1
	n, err := h.rt.DeleteHomeBackups(ctx)
	if err != nil || n != 2 {
		t.Fatalf("DeleteHomeBackups = %d, %v; want 2 (the listed copy and the late one)", n, err)
	}
	for _, id := range []string{"snap-backup", "snap-backup-late"} {
		if _, ok := h.ec2.snapshots[id]; ok {
			t.Errorf("%s survived the deletion", id)
		}
	}
}

// The count goes into the audit log and back to the administrator, so it must be what
// this call deleted — not a copy another deletion removed first.
func TestECSEC2DeleteHomeBackupsCountsOnlyWhatItDeleted(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	h.ec2.snapshotGone["snap-backup"] = true
	n, err := h.rt.DeleteHomeBackups(ctx)
	if err != nil || n != 0 {
		t.Fatalf("DeleteHomeBackups = %d, %v; want 0 (the only copy was already gone)", n, err)
	}
}

func keepMark(t *testing.T, h *ec2Harness) string {
	t.Helper()
	for _, ap := range h.efs.aps {
		if aws.ToString(ap.AccessPointId) == "fsap-keep" {
			return tagValue(ap.Tags, efsTagErasedVolumes)
		}
	}
	return ""
}

func homeSnapshotOf(id, volume string, start time.Time, state ec2types.SnapshotState) *ec2types.Snapshot {
	return &ec2types.Snapshot{
		SnapshotId: aws.String(id), VolumeId: aws.String(volume), State: state, StartTime: aws.Time(start),
		Tags: []ec2types.Tag{
			{Key: aws.String(EC2TagMembership), Value: aws.String("M-1")},
			{Key: aws.String(EC2TagRole), Value: aws.String(ec2RoleHome)},
		},
	}
}

// No wait makes an eventually consistent listing complete, so the erase records which
// volume it deleted, and the restore path refuses any snapshot of that volume: a capture
// both listings missed is never handed back as the member's home.
func TestECSEC2ACaptureTheEraseNeverSawIsNeverRestored(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	h.ec2.snapshots["snap-unseen"] = homeSnapshotOf("snap-unseen", "vol-1", time.Now(), ec2types.SnapshotStateCompleted)
	h.ec2.snapshotHidden["snap-unseen"] = 3 // every listing the erase makes misses it
	if err := h.rt.EraseHome(ctx); err != nil {
		t.Fatalf("EraseHome: %v", err)
	}
	if mark := keepMark(t, h); !strings.Contains(mark, "vol-1") {
		t.Fatalf("the keep access point does not record the erased volume (%q)", mark)
	}
	if _, ok := h.ec2.snapshots["snap-unseen"]; !ok {
		t.Fatal("setup: the unseen capture was deleted, so this test proves nothing")
	}
	got, err := h.rt.restoreSnapshot(ctx)
	if err != nil || got != "" {
		t.Fatalf("restoreSnapshot = %q, %v; the erased home must never come back", got, err)
	}
	if _, ok := h.ec2.snapshots["snap-unseen"]; ok {
		t.Error("the copy of the erased home was left billing after the restore path saw it")
	}
}

// Identity, not time: a snapshot of the erased volume is refused however late it claims
// to have started, and the home created after the erase is restored as usual.
func TestECSEC2OnlyCopiesOfTheErasedVolumeAreRefused(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	if err := h.rt.EraseHome(ctx); err != nil {
		t.Fatalf("EraseHome: %v", err)
	}
	h.ec2.snapshots["snap-old-late"] = homeSnapshotOf("snap-old-late", "vol-1", time.Now().Add(time.Hour), ec2types.SnapshotStateCompleted)
	h.ec2.snapshots["snap-new"] = homeSnapshotOf("snap-new", "vol-2", time.Now().Add(time.Minute), ec2types.SnapshotStateCompleted)
	if got, err := h.rt.restoreSnapshot(ctx); err != nil || got != "snap-new" {
		t.Fatalf("restoreSnapshot = %q, %v; want the hibernation of the new home", got, err)
	}
	if _, ok := h.ec2.snapshots["snap-old-late"]; ok {
		t.Error("a copy of the erased volume survived the restore path")
	}
}

// An unreadable record is not "nothing erased": the Start fails rather than guessing.
func TestECSEC2AnUnreadableEraseRecordFailsTheRestore(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	h.efs.aps[0].Tags = append(h.efs.aps[0].Tags, efstypes.Tag{Key: aws.String(efsTagErasedVolumes), Value: aws.String("yesterday")})
	if _, err := h.rt.restoreSnapshot(ctx); err == nil {
		t.Fatal("restoreSnapshot ignored an unreadable erase record")
	}
}

// The offboarding order deletes the backups after the home is gone. A copy the reaper
// started just before Clean home deleted the volume can still be missing from the first
// listing then, so the second one runs whether or not a volume exists.
func TestECSEC2DeleteHomeBackupsRelistsAfterTheHomeIsGone(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	if err := h.rt.EraseHome(ctx); err != nil {
		t.Fatalf("EraseHome: %v", err)
	}
	h.ec2.snapshots["snap-backup-late"] = &ec2types.Snapshot{
		SnapshotId: aws.String("snap-backup-late"), VolumeId: aws.String("vol-1"),
		State: ec2types.SnapshotStatePending, StartTime: aws.Time(time.Now()),
		Tags: []ec2types.Tag{
			{Key: aws.String(EC2TagMembership), Value: aws.String("M-1")},
			{Key: aws.String(EC2TagRole), Value: aws.String(ec2RoleBackup)},
		},
	}
	h.ec2.snapshotHidden["snap-backup-late"] = 1
	n, err := h.rt.DeleteHomeBackups(ctx)
	if err != nil || n != 2 {
		t.Fatalf("DeleteHomeBackups = %d, %v; want 2 (the listed backup and the late one)", n, err)
	}
	if _, ok := h.ec2.snapshots["snap-backup-late"]; ok {
		t.Error("a backup missing from the first listing survived because no volume was left")
	}
}

// Whether the home still exists decides what deleting the backups means (the schedule goes
// on copying a home that exists), so the answer comes from AWS, not from a roster snapshot.
func TestECSEC2HomeBackupsSaysWhetherTheHomeExists(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	if b, err := h.rt.HomeBackups(ctx); err != nil || !b.HomeExists {
		t.Fatalf("with a home volume: %+v, %v; want HomeExists", b, err)
	}
	if err := h.rt.EraseHome(ctx); err != nil {
		t.Fatalf("EraseHome: %v", err)
	}
	if b, err := h.rt.HomeBackups(ctx); err != nil || b.HomeExists {
		t.Fatalf("after the erase: %+v, %v; want no home", b, err)
	}
}

// An erase that fails before the volume is gone records nothing. The home is still the
// member's, and so are its hibernation copies: the one a hibernation already under way
// completes after the failure is the only copy there will be once the sweeper deletes the
// volume, and refusing it would lose the home.
func TestECSEC2AFailedEraseRecordsNothing(t *testing.T) {
	ctx := context.Background()
	for name, fail := range map[string]func(h *ec2Harness){
		"the slot will not let go":       func(h *ec2Harness) { h.ssmc.fail["af-umount"] = true },
		"the volume will not be deleted": func(h *ec2Harness) { h.ec2.deleteVolumeErr = errors.New("RequestLimitExceeded") },
	} {
		t.Run(name, func(t *testing.T) {
			h := eraseHarness(t, name == "the slot will not let go")
			delete(h.ec2.snapshots, "snap-hib")
			h.ec2.snapshots["snap-pending"] = homeSnapshotOf("snap-pending", "vol-1", time.Now().Add(-time.Hour), ec2types.SnapshotStatePending)
			fail(h)
			if err := h.rt.EraseHome(ctx); err == nil {
				t.Fatal("EraseHome succeeded although the volume is still there")
			}
			if mark := keepMark(t, h); mark != "" {
				t.Fatalf("a failed erase recorded %q", mark)
			}
			// The hibernation finishes after the failure: the capture completes and the
			// volume goes, and the home is now that copy.
			h.ec2.snapshots["snap-pending"].State = ec2types.SnapshotStateCompleted
			delete(h.ec2.volumes, "vol-1")
			if got, err := h.rt.restoreSnapshot(ctx); err != nil || got != "snap-pending" {
				t.Fatalf("restoreSnapshot = %q, %v; the home the erase failed to delete must come back", got, err)
			}
		})
	}
}

// The pending mark goes first. If it cannot be written, nothing is destroyed: a home that
// is still there is still the member's.
func TestECSEC2AnEraseThatCannotMarkDestroysNothing(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	h.efs.tagErr = errors.New("ThrottlingException: rate exceeded")
	if err := h.rt.EraseHome(ctx); err == nil {
		t.Fatal("EraseHome succeeded without being able to mark the erase")
	}
	if _, ok := h.ec2.volumes["vol-1"]; !ok {
		t.Error("the volume was deleted without the pending mark in place")
	}
	if _, ok := h.ec2.snapshots["snap-hib"]; !ok {
		t.Error("the hibernation snapshot was deleted without the pending mark in place")
	}
}

// The volume went but the record could not be confirmed: the pending mark stays, so no
// restore runs — a copy the listings missed can never come back — until Clean home runs
// again and finishes the record.
func TestECSEC2AnEraseThatCannotConfirmBlocksTheRestoreUntilItIsRerun(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	h.ec2.snapshots["snap-unseen"] = homeSnapshotOf("snap-unseen", "vol-1", time.Now(), ec2types.SnapshotStateCompleted)
	h.ec2.snapshotHidden["snap-unseen"] = 3
	h.efs.tagErrOn = func(call int) error {
		if call >= 2 { // the pending mark is written; every confirm fails
			return errors.New("ServiceUnavailable")
		}
		return nil
	}
	if err := h.rt.EraseHome(ctx); err == nil {
		t.Fatal("EraseHome succeeded although the record could not be confirmed")
	}
	if _, ok := h.ec2.volumes["vol-1"]; ok {
		t.Fatal("setup: the volume should be gone")
	}
	if mark := keepMark(t, h); mark != "pending:vol-1" {
		t.Fatalf("record = %q, want the pending mark to stay", mark)
	}
	if got, err := h.rt.restoreSnapshot(ctx); err == nil {
		t.Fatalf("restoreSnapshot = %q with an erase unfinished; it must refuse", got)
	}
	h.efs.tagErrOn = nil
	if err := h.rt.EraseHome(ctx); err != nil {
		t.Fatalf("EraseHome, run again: %v", err)
	}
	if mark := keepMark(t, h); mark != "vol-1" {
		t.Fatalf("record after the rerun = %q, want vol-1 erased", mark)
	}
	if got, err := h.rt.restoreSnapshot(ctx); err != nil || got != "" {
		t.Fatalf("restoreSnapshot after the rerun = %q, %v; the erased home must not come back", got, err)
	}
}

// A failed erase whose marks could not be taken back leaves them pending on a home that is
// still alive: the live volume's, and that of an older volume a leftover copy came from.
// The owner's next Start — which holds the lifecycle lease, so no erase is running — takes
// them all back. Left behind, the older volume's mark would refuse this home's own copy
// once it hibernates.
func TestECSEC2AStartOnTheLiveHomeTakesBackAStalePendingErase(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	h.efs.aps[0].Tags = append(h.efs.aps[0].Tags, efstypes.Tag{Key: aws.String(efsTagErasedVolumes), Value: aws.String("vol-7")})
	h.ec2.snapshots["snap-old"] = homeSnapshotOf("snap-old", "vol-9", time.Now().Add(-48*time.Hour), ec2types.SnapshotStateCompleted)
	h.ec2.deleteVolumeErr = errors.New("RequestLimitExceeded")
	h.efs.tagErrOn = func(call int) error {
		if call >= 2 { // the marks are written; taking them back fails
			return errors.New("ServiceUnavailable")
		}
		return nil
	}
	if err := h.rt.EraseHome(ctx); err == nil {
		t.Fatal("EraseHome succeeded although the volume is still there")
	}
	if mark := keepMark(t, h); mark != "vol-7 pending:vol-1 pending:vol-9" {
		t.Fatalf("setup: record = %q, want both marks left pending", mark)
	}
	h.efs.tagErrOn = nil
	h.ec2.deleteVolumeErr = nil
	if _, err := h.rt.prepare(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if mark := keepMark(t, h); mark != "vol-7" {
		t.Fatalf("record = %q; want every pending mark taken back and the erased volume kept", mark)
	}
	// The home hibernates later; its own copy is what the next Start restores.
	delete(h.ec2.volumes, "vol-1")
	if got, err := h.rt.restoreSnapshot(ctx); err != nil || got != "snap-hib" {
		t.Fatalf("restoreSnapshot = %q, %v; the home no erase reached must come back", got, err)
	}
}

// No erased volume is forgotten to make room: a listing cannot prove its copies are gone,
// even one that shows none. A record with no room for another erase refuses it before
// anything is destroyed, and keeps every id it has.
func TestECSEC2AFullEraseRecordRefusesTheErase(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	var ids []string
	for i := 0; i < 11; i++ {
		ids = append(ids, fmt.Sprintf("vol-%017d", i)) // no copy of any of them is listed
	}
	full := strings.Join(append(ids, "vol-12345678"), " ") // 254 characters: valid, and no room left
	h.efs.aps[0].Tags = append(h.efs.aps[0].Tags, efstypes.Tag{Key: aws.String(efsTagErasedVolumes), Value: aws.String(full)})
	if err := h.rt.EraseHome(ctx); err == nil {
		t.Fatal("EraseHome succeeded although the record had no room for the erase")
	}
	if _, ok := h.ec2.volumes["vol-1"]; !ok {
		t.Error("the volume was deleted although the erase could not be recorded")
	}
	if _, ok := h.ec2.snapshots["snap-hib"]; !ok {
		t.Error("the hibernation snapshot was deleted although the erase could not be recorded")
	}
	if mark := keepMark(t, h); mark != full {
		t.Errorf("record = %q; no erased volume may be forgotten to make room", mark)
	}
}

// A failed Clean home that could not take its marks back leaves them pending on a live
// volume, and the sweeper advances a hibernation without the lifecycle lease. Deleting the
// volume then would leave the home nowhere — no copy is restored while a mark is pending —
// so the volume stays until the owner's Start takes the marks back.
func TestECSEC2HibernationKeepsAVolumeWhileAnEraseIsPending(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	h.ec2.deleteVolumeErr = errors.New("RequestLimitExceeded")
	h.efs.tagErrOn = func(call int) error {
		if call >= 2 { // the mark is written; taking it back fails
			return errors.New("ServiceUnavailable")
		}
		return nil
	}
	if err := h.rt.EraseHome(ctx); err == nil {
		t.Fatal("EraseHome succeeded although the volume is still there")
	}
	h.efs.tagErrOn, h.ec2.deleteVolumeErr = nil, nil
	// The hibernation the reaper begins: the capture (completed at once by the fake), then
	// the step that would delete the volume.
	for step := 1; step <= 2; step++ {
		if err := h.rt.hibernate(ctx); err != nil {
			t.Fatalf("hibernate step %d: %v", step, err)
		}
	}
	if _, ok := h.ec2.volumes["vol-1"]; !ok {
		t.Fatal("the hibernation deleted a volume with an erase pending; no copy of it can be restored")
	}
	// The owner starts: the marks go, and the hibernation can finish.
	if _, err := h.rt.prepare(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := h.rt.hibernate(ctx); err != nil {
		t.Fatalf("hibernate after the Start: %v", err)
	}
	if _, ok := h.ec2.volumes["vol-1"]; ok {
		t.Fatal("the hibernation did not finish once the marks were taken back")
	}
	if got, err := h.rt.restoreSnapshot(ctx); err != nil || got == "" {
		t.Fatalf("restoreSnapshot = %q, %v; the home no erase reached must come back", got, err)
	}
}

// One call lists at most 100 access points and a file system carries two per member.
// A member whose access points are on a later page must still be found: the mark is
// written on theirs, no duplicate is created, and Destroy removes both.
func TestECSEC2AccessPointsAreFoundPastTheFirstPage(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	h.efs.pageSize = 100
	var others []efstypes.AccessPointDescription
	for i := 0; i < 150; i++ {
		id := fmt.Sprintf("fsap-other-%d", i)
		others = append(others, efstypes.AccessPointDescription{AccessPointId: aws.String(id),
			Tags: []efstypes.Tag{{Key: aws.String("af-membership"), Value: aws.String(fmt.Sprintf("M-x%d", i))},
				{Key: aws.String("af-role"), Value: aws.String("keep-ec2")}}})
	}
	h.efs.aps = append(others, h.efs.aps...) // the member's two now sit on the second page
	if err := h.rt.EraseHome(ctx); err != nil {
		t.Fatalf("EraseHome: %v", err)
	}
	if len(h.efs.createCalls) != 0 {
		t.Errorf("a second keep access point was created (%d) because the first page did not show the member's", len(h.efs.createCalls))
	}
	if keepMark(t, h) == "" {
		t.Error("the mark was not written on the member's keep access point on the second page")
	}
	if _, err := h.rt.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	for _, ap := range h.efs.aps {
		if tagValue(ap.Tags, "af-membership") == "M-1" {
			t.Errorf("Destroy left %s behind on the second page", aws.ToString(ap.AccessPointId))
		}
	}
	if len(h.efs.aps) != 150 {
		t.Errorf("Destroy touched other members' access points: %d left, want 150", len(h.efs.aps))
	}
}

// --- a member's Recreate and Clean home (runtime_ecs_ec2_home_wipe.go) ---

// memberWipeHarness is a stopped member whose home is still on a hot, registered slot:
// the one placement a plain Start finishes inline, so any deferral is the wipe's doing.
func memberWipeHarness(t *testing.T) *ec2Harness {
	t.Helper()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now())
	h.ci.registered["i-hot"] = true
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	return h
}

// scaledUp says whether the service was asked for a task — the moment the home is handed
// to the workspace.
func scaledUp(h *ec2Harness) bool {
	for _, c := range h.ecs.updateCalls {
		if aws.ToInt32(c.DesiredCount) >= 1 {
			return true
		}
	}
	return false
}

// callIndex is the position of the first call with prefix in the shared EC2/SSM log.
func callIndex(h *ec2Harness, prefix string) int {
	for i, c := range h.ec2.calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

// The request only marks; the Start after it goes to the background even on the slot
// that would otherwise finish inline, and there the removal sits between the mount and the
// task, with the mark gone before the task.
func TestECSEC2MemberWipeRunsBetweenTheMountAndTheTask(t *testing.T) {
	ctx := context.Background()
	h := memberWipeHarness(t)
	if err := h.rt.WipeHome(ctx, HomeWipeRepos); err != nil {
		t.Fatalf("WipeHome: %v", err)
	}
	if len(h.ssmc.commands) != 0 {
		t.Fatalf("WipeHome reached the slot (%v); a sleeping slot would hold the request past the ingress timeout", h.ssmc.commands)
	}
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagHomeWipe); got != string(HomeWipeRepos) {
		t.Fatalf("mark = %q, want repos", got)
	}
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.deferred) != 1 || scaledUp(h) {
		t.Fatalf("a Start with a pending wipe must hand off before the task: deferred=%d scaledUp=%v", len(h.deferred), scaledUp(h))
	}
	if st := h.rt.State(ctx); st != "starting" {
		t.Errorf("State during the wipe = %q, want starting", st)
	}
	h.runDeferred(ctx)
	mount, wipe := callIndex(h, "SSM af-mount"), callIndex(h, "SSM mountpoint -q")
	if mount < 0 || wipe < 0 || mount > wipe {
		t.Fatalf("want the mount before the wipe, got mount=%d wipe=%d in %v", mount, wipe, h.ec2.calls)
	}
	if cmd := h.ec2.calls[wipe]; !strings.Contains(cmd, "/af-home/M-1/dev/repos") || strings.Contains(cmd, "find ") {
		t.Errorf("a Recreate must remove ~/repos and nothing else: %s", cmd)
	}
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagHomeWipe); got != "" {
		t.Errorf("the mark survived a completed wipe (%q); the next Start would remove the member's new work", got)
	}
	if !scaledUp(h) {
		t.Error("the workspace was not started after the wipe")
	}
}

// A wipe that did not happen must not be followed by a task that sees the home it was
// supposed to remove, and it must not be lost: the next Start does it.
func TestECSEC2FailedMemberWipeLeavesTheWorkspaceStoppedAndMarked(t *testing.T) {
	ctx := context.Background()
	h := memberWipeHarness(t)
	h.ssmc.fail["rm -rf"] = true
	if err := h.rt.WipeHome(ctx, HomeWipeRepos); err != nil {
		t.Fatalf("WipeHome: %v", err)
	}
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.runDeferred(ctx)
	if scaledUp(h) {
		t.Fatal("the task started although the wipe failed")
	}
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagHomeWipe); got != string(HomeWipeRepos) {
		t.Errorf("mark after a failed wipe = %q, want it kept for the next Start", got)
	}
	if st := h.rt.State(ctx); st != "stopped" {
		t.Errorf("State after a failed wipe = %q, want stopped (not starting until the claim expires)", st)
	}

	delete(h.ssmc.fail, "rm -rf")
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	h.runDeferred(ctx)
	if !scaledUp(h) || ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagHomeWipe) != "" {
		t.Errorf("the next Start did not finish the wipe: scaledUp=%v tags=%v", scaledUp(h), h.ec2.volumes["vol-1"].Tags)
	}
}

// The mark goes before the task starts. A task started with the mark still on would have
// its work removed by the member's next ordinary Start.
func TestECSEC2MemberWipeDoesNotStartTheTaskWhileTheMarkRemains(t *testing.T) {
	ctx := context.Background()
	h := memberWipeHarness(t)
	h.ec2.deleteTagsErr = map[string]error{ec2TagHomeWipe: errors.New("throttled")}
	if err := h.rt.WipeHome(ctx, HomeWipeRepos); err != nil {
		t.Fatalf("WipeHome: %v", err)
	}
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.runDeferred(ctx)
	if scaledUp(h) {
		t.Error("the task started while the mark was still on the home")
	}
}

// A Recreate after a pending Clean home must not narrow it; a Clean home after a pending
// Recreate widens it.
func TestECSEC2MemberWipeMarkOnlyWidens(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		first, second, want HomeWipe
	}{
		{HomeWipeClean, HomeWipeRepos, HomeWipeClean},
		{HomeWipeRepos, HomeWipeClean, HomeWipeClean},
		{HomeWipeRepos, HomeWipeRepos, HomeWipeRepos},
	} {
		h := memberWipeHarness(t)
		for _, w := range []HomeWipe{c.first, c.second} {
			if err := h.rt.WipeHome(ctx, w); err != nil {
				t.Fatalf("WipeHome(%s): %v", w, err)
			}
		}
		if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagHomeWipe); got != string(c.want) {
			t.Errorf("%s then %s: mark = %q, want %s", c.first, c.second, got, c.want)
		}
	}
}

// A hibernated home has no volume: the mark goes on the snapshot, the restore carries it
// onto the new volume, and the Start that restored it performs it.
func TestECSEC2MemberWipeOfAHibernatedHomeHappensAfterTheRestore(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.snapshots["snap-hib"] = &ec2types.Snapshot{
		SnapshotId: aws.String("snap-hib"), VolumeId: aws.String("vol-gone"),
		State: ec2types.SnapshotStateCompleted, StartTime: aws.Time(time.Now().Add(-24 * time.Hour)),
		Tags: []ec2types.Tag{
			{Key: aws.String(EC2TagMembership), Value: aws.String("M-1")},
			{Key: aws.String(EC2TagRole), Value: aws.String(ec2RoleHome)},
		},
	}
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	if err := h.rt.WipeHome(ctx, HomeWipeClean); err != nil {
		t.Fatalf("WipeHome: %v", err)
	}
	if got := ec2TagValue(h.ec2.snapshots["snap-hib"].Tags, ec2TagHomeWipe); got != string(HomeWipeClean) {
		t.Fatalf("mark on the hibernation snapshot = %q, want clean", got)
	}
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if scaledUp(h) {
		t.Fatal("the restored home was handed to a task before the wipe")
	}
	var restored string
	for id, v := range h.ec2.volumes {
		if aws.ToString(v.SnapshotId) == "snap-hib" {
			restored = id
		}
	}
	if restored == "" {
		t.Fatal("the home was not restored from its snapshot")
	}
	h.runDeferred(ctx)
	wipe := callIndex(h, "SSM mountpoint -q")
	if wipe < 0 || !strings.Contains(h.ec2.calls[wipe], "find ") {
		t.Fatalf("the restored home was not cleaned: %v", h.ec2.calls)
	}
	if got := ec2TagValue(h.ec2.volumes[restored].Tags, ec2TagHomeWipe); got != "" {
		t.Errorf("mark on the restored volume after the wipe = %q", got)
	}
	if !scaledUp(h) {
		t.Error("the workspace was not started after the wipe")
	}
}

// The capture copies the mark, and a mark that reached only the volume after the capture
// started is put on the snapshot before the volume goes — otherwise the restore hands back
// the home the member asked to have cleared.
func TestECSEC2HibernationCarriesAPendingMemberWipe(t *testing.T) {
	ctx := context.Background()

	h := hibernateHarness(t, 60*24*time.Hour)
	h.ec2.setTag("vol-1", ec2TagHomeWipe, string(HomeWipeRepos))
	if err := h.rt.hibernate(ctx); err != nil {
		t.Fatalf("hibernate: %v", err)
	}
	for id, s := range h.ec2.snapshots {
		if got := ec2TagValue(s.Tags, ec2TagHomeWipe); got != string(HomeWipeRepos) {
			t.Errorf("capture %s of a marked home carries %q, want repos", id, got)
		}
	}

	h = hibernateHarness(t, 60*24*time.Hour)
	h.ec2.snapshotState = ec2types.SnapshotStatePending
	if err := h.rt.hibernate(ctx); err != nil {
		t.Fatalf("hibernate step 1: %v", err)
	}
	// The mark lands on the volume only, as when WipeHome listed the snapshots before the
	// capture existed.
	h.ec2.setTag("vol-1", ec2TagHomeWipe, string(HomeWipeClean))
	for _, s := range h.ec2.snapshots {
		s.State = ec2types.SnapshotStateCompleted
	}
	if err := h.rt.hibernate(ctx); err != nil {
		t.Fatalf("hibernate step 2: %v", err)
	}
	if _, ok := h.ec2.volumes["vol-1"]; ok {
		t.Fatal("the volume was kept; this test wants the step that deletes it")
	}
	for id, s := range h.ec2.snapshots {
		if got := ec2TagValue(s.Tags, ec2TagHomeWipe); got != string(HomeWipeClean) {
			t.Errorf("snapshot %s lost the mark the volume carried when it was deleted: %q", id, got)
		}
	}
}

// The command itself, run by sh against a real directory. `mountpoint` is stubbed on PATH:
// the one thing a test cannot provide is a mounted volume.
func TestHomeWipeCommandOnADirectory(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	setup := func(t *testing.T, mounted bool) (string, []string) {
		t.Helper()
		root := t.TempDir()
		mp := filepath.Join(root, "af-home", "M-1")
		home := filepath.Join(mp, "dev")
		keepDir := filepath.Join(root, "keep")
		for _, d := range []string{home + "/repos/app/.git", home + "/.local/bin", home + "/.cache", keepDir + "/.config"} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, f := range []string{home + "/repos/app/main.go", home + "/.bashrc", home + "/.local/bin/claude"} {
			if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// A keep entry as the entrypoint leaves it (a link into EFS), and one a tool
		// replaced with a plain file since the last boot.
		if err := os.Symlink(keepDir+"/.config", home+"/.config"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(home+"/.gitconfig", []byte("[user]"), 0o644); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(root, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		code := "1"
		if mounted {
			code = "0"
		}
		if err := os.WriteFile(bin+"/mountpoint", []byte("#!/bin/sh\nexit "+code+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return mp, append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	}
	run := func(t *testing.T, mp string, env []string, what HomeWipe) error {
		t.Helper()
		cmd, err := homeWipeCommand(mp, what)
		if err != nil {
			t.Fatal(err)
		}
		c := exec.Command("sh", "-c", cmd)
		c.Env = env
		out, err := c.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, out)
		}
		return nil
	}
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }

	t.Run("recreate", func(t *testing.T) {
		mp, env := setup(t, true)
		if err := run(t, mp, env, HomeWipeRepos); err != nil {
			t.Fatal(err)
		}
		if exists(mp + "/dev/repos") {
			t.Error("~/repos survived a Recreate")
		}
		for _, p := range []string{"/dev/.local/bin/claude", "/dev/.bashrc", "/dev/.config", "/dev/.gitconfig"} {
			if !exists(mp + p) {
				t.Errorf("a Recreate removed %s", p)
			}
		}
	})
	t.Run("clean", func(t *testing.T) {
		mp, env := setup(t, true)
		if err := run(t, mp, env, HomeWipeClean); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"/dev/repos", "/dev/.local", "/dev/.cache", "/dev/.bashrc"} {
			if exists(mp + p) {
				t.Errorf("Clean home left %s", p)
			}
		}
		for _, p := range []string{"/dev/.config", "/dev/.gitconfig"} {
			if !exists(mp + p) {
				t.Errorf("Clean home removed the keep entry %s", p)
			}
		}
		if !exists(filepath.Join(filepath.Dir(filepath.Dir(mp)), "keep", ".config")) {
			t.Error("Clean home followed the keep link into EFS")
		}
	})
	t.Run("not mounted", func(t *testing.T) {
		mp, env := setup(t, false)
		if err := run(t, mp, env, HomeWipeClean); err == nil {
			t.Error("a wipe of an unmounted home reported success")
		}
		if !exists(mp + "/dev/repos/app/main.go") {
			t.Error("a wipe of an unmounted home removed files")
		}
	})
	t.Run("no home yet", func(t *testing.T) {
		mp, env := setup(t, true)
		if err := os.RemoveAll(mp + "/dev"); err != nil {
			t.Fatal(err)
		}
		if err := run(t, mp, env, HomeWipeRepos); err != nil {
			t.Errorf("a home no task has booted has nothing to remove: %v", err)
		}
	})
}

// Stop only lowered the desired count. The old task holds the home as its bind mount
// until it exits, and removing files under it would pull them out of a running workspace.
func TestECSEC2MemberWipeWaitsForTheOldTaskToExit(t *testing.T) {
	ctx := context.Background()
	h := memberWipeHarness(t)
	h.ecs.drainingPolls = 50 // outlasts every other DescribeServices before the mount
	draining := -1
	h.ssmc.onSend = func(cmd string) {
		if strings.HasPrefix(cmd, "mountpoint -q") {
			draining = h.ecs.drainingPolls
		}
	}
	if err := h.rt.WipeHome(ctx, HomeWipeRepos); err != nil {
		t.Fatalf("WipeHome: %v", err)
	}
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.runDeferred(ctx)
	if draining != 0 {
		t.Errorf("the wipe was sent while the old task was still running (%d polls of it left)", draining)
	}
}
