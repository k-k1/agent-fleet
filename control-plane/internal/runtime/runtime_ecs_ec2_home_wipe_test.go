package runtime

import (
	"context"
	"errors"
	"fmt"
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

// The record is written after the volume is gone, and a record that cannot be written
// fails the erase — the administrator retries — while the listings still clean up what
// they can see.
func TestECSEC2ARecordThatCannotBeWrittenFailsTheErase(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	h.efs.tagErr = errors.New("ThrottlingException: rate exceeded")
	if err := h.rt.EraseHome(ctx); err == nil {
		t.Fatal("EraseHome succeeded without recording the erased volume")
	}
	if _, ok := h.ec2.volumes["vol-1"]; ok {
		t.Error("the volume was kept although the erase had already got past it")
	}
	if _, ok := h.ec2.snapshots["snap-hib"]; ok {
		t.Error("the listed hibernation snapshot was left although the record failed")
	}
}

// The record stays within a tag value: the most recent erased volumes are kept.
func TestECSEC2TheEraseRecordKeepsTheMostRecentVolumes(t *testing.T) {
	ctx := context.Background()
	h := eraseHarness(t, false)
	for i := 0; i < maxErasedVolumes+3; i++ {
		if err := h.rt.recordErasedVolumes(ctx, []string{fmt.Sprintf("vol-%02d", i)}); err != nil {
			t.Fatalf("recordErasedVolumes: %v", err)
		}
	}
	got := strings.Fields(keepMark(t, h))
	if len(got) != maxErasedVolumes || got[0] != "vol-03" || got[len(got)-1] != fmt.Sprintf("vol-%02d", maxErasedVolumes+2) {
		t.Errorf("record = %v, want the last %d", got, maxErasedVolumes)
	}
	if n := len(strings.Join(got, " ")); n > 256 {
		t.Errorf("record is %d characters; an EFS tag value holds 256", n)
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
