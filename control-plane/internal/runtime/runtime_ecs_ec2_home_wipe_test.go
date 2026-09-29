package runtime

import (
	"context"
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
